package main

// desktop/feishu.go 飞书机器人（长连接双向）：
// wss 长连接收 im.message.receive_v1（无需公网回调地址），每个会话/单聊一个
// 独立 agent 会话（schedMu 串行、执行时切到启动时的工作目录、远端写工具一律拒绝），
// 跑完把最终回复发回飞书。凭据存 models.json 顶层 "feishu"（config.Get/SetFeishuConfig）。
// 依赖官方 oapi-sdk-go/v3（仅桌面模块，内核保持零冗余依赖）。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/tools"
)

type feishuBot struct {
	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	api       *lark.Client
	modelKey  string
	workspace string
	allow     []string
	chats     map[string]*feishuChat // chat_id → 独立会话（单聊/每群各一份历史）
	name      string                 // 渠道名（feishu / lark；channel:status 事件用）
	domain    string                 // 开放平台域名（飞书/Lark 不同）
	cfgSave   func(appID, appSecret, allowlist string)
}

type feishuChat struct {
	mu      sync.Mutex
	history []msg.Msg
}

// newFeishuBot 两个实例共用一个实现：国内飞书 & 国际 Lark（域名不同）。
func newFeishuBot(name, domain string, cfgSave func(string, string, string)) *feishuBot {
	return &feishuBot{chats: map[string]*feishuChat{}, name: name, domain: domain, cfgSave: cfgSave}
}

var feishu = newFeishuBot("feishu", lark.FeishuBaseUrl, config.SetFeishuConfig)
var larkB = newFeishuBot("lark", lark.LarkBaseUrl, config.SetLarkConfig)

// Connect 连接飞书/Lark（保存配置 + 启动长连接；重复连接先断旧）。
func (f *feishuBot) Connect(a *App, appID, appSecret, allowlist string) map[string]any {
	appID, appSecret = strings.TrimSpace(appID), strings.TrimSpace(appSecret)
	if appID == "" || appSecret == "" {
		return map[string]any{"ok": false, "msg": "App ID / App Secret 不能为空"}
	}
	f.Disconnect(a)
	f.cfgSave(appID, appSecret, allowlist)

	var allow []string
	for _, s := range strings.FieldsFunc(allowlist, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '；' || r == ' '
	}) {
		if s = strings.TrimSpace(s); s != "" {
			allow = append(allow, s)
		}
	}
	a.mu.Lock()
	f.modelKey = a.modelKey
	a.mu.Unlock()
	f.workspace = tools.GetWorkspace()
	f.allow = allow
	f.api = lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(f.domain))

	ed := larkdispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(f.onMessage)
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.cancel = cancel
	f.running = true
	f.mu.Unlock()

	ws := larkws.NewClient(appID, appSecret, larkws.WithEventHandler(ed), larkws.WithDomain(f.domain))
	go func() {
		// Start 阻塞运行（SDK 内置断线重连）；异常退出时回退状态并通知 UI
		if err := ws.Start(ctx); err != nil && ctx.Err() == nil {
			f.mu.Lock()
			f.running = false
			f.mu.Unlock()
			if a.ctx != nil {
				wailsruntime.EventsEmit(a.ctx, "channel:status",
					map[string]any{"channel": f.name, "running": false, "error": err.Error()})
			}
		}
	}()
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "channel:status", map[string]any{"channel": f.name, "running": true})
	}
	return map[string]any{"ok": true, "msg": f.name + " 机器人已连接（长连接模式）"}
}

// Disconnect 断开长连接。
func (f *feishuBot) Disconnect(a *App) {
	f.mu.Lock()
	running, cancel := f.running, f.cancel
	f.running, f.cancel = false, nil
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if running && a != nil && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "channel:status", map[string]any{"channel": f.name, "running": false})
	}
}

// Status 连接状态。
func (f *feishuBot) Status() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"running": f.running, "chats": len(f.chats)}
}

// onMessage 收到飞书消息：白名单校验 → 取文本 → 异步跑 agent → 回复。
func (f *feishuBot) onMessage(ctx context.Context, ev *larkim.P2MessageReceiveV1) error {
	if ev.Event == nil || ev.Event.Message == nil {
		return nil
	}
	m := ev.Event.Message
	chatID := deref(m.ChatId)
	content := deref(m.Content)
	if chatID == "" || deref(m.MessageType) != "text" {
		return nil
	}
	sender := ""
	if ev.Event.Sender != nil && ev.Event.Sender.SenderId != nil {
		sender = deref(ev.Event.Sender.SenderId.OpenId)
	}
	f.mu.Lock()
	allow := f.allow
	modelKey := f.modelKey
	workspace := f.workspace
	f.mu.Unlock()
	// 白名单（配置了才生效；未授权静默忽略）
	if len(allow) > 0 && !containsStr(allow, sender) {
		return nil
	}
	text := feishuText(content) // {"text":"@机器人 帮我…"} → 去掉 @ 前缀
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// 每会话串行；全局与定时任务/手机远程共用 schedMu（工作目录是全局状态）
	chat := f.chat(chatID)
	chat.mu.Lock()
	hist := chat.history
	go func() {
		defer chat.mu.Unlock()
		schedMu.Lock()
		defer schedMu.Unlock()
		model := config.FindModel(modelKey)
		if model == nil {
			f.reply(chatID, "本机当前模型不可用")
			return
		}
		prev := tools.GetWorkspace()
		tools.SetWorkspace(workspace)
		defer tools.SetWorkspace(prev)

		a := &agent.Agent{
			Mode:   agent.ModeAlways,
			Model:  model,
			OnStop: func() bool { return false },
			// 远端安全：写工具执行前一律拒绝
			OnApproval: func(name string, args map[string]any, summary string) bool {
				f.reply(chatID, "已拒绝远程写操作："+name)
				return false
			},
		}
		final, runErr := a.Run(text, hist, nil)
		chat.history = a.Messages
		reply := final
		if runErr != nil {
			reply = "⚠️ " + runErr.Error()
		} else if strings.TrimSpace(reply) == "" {
			reply = "（本轮无文本输出）"
		}
		f.reply(chatID, reply)
	}()
	return nil
}

// reply 发文本回复到指定会话（尽力而为）。
func (f *feishuBot) reply(chatID, text string) {
	f.mu.Lock()
	api := f.api
	f.mu.Unlock()
	if api == nil {
		return
	}
	if len(text) > 3600 {
		text = text[:3600] + "\n…（已截断）"
	}
	b, _ := json.Marshal(map[string]string{"text": text})
	_, _ = api.Im.Message.Create(context.Background(),
		larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("text").
				Content(string(b)).
				Build()).
			Build())
}

// chat 取（或建）会话状态。
func (f *feishuBot) chat(chatID string) *feishuChat {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.chats[chatID]
	if c == nil {
		c = &feishuChat{}
		f.chats[chatID] = c
	}
	return c
}

// feishuText 解析消息 content JSON 并去掉群聊 @ 前缀。
func feishuText(content string) string {
	var in struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(content), &in)
	text := in.Text
	if strings.HasPrefix(text, "@") {
		if i := strings.Index(text, " "); i > 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	return strings.TrimSpace(text)
}

// deref 安全取字符串指针。
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// containsStr 列表包含（忽略大小写）。
func containsStr(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// ---------------- 桌面绑定 ----------------

// FeishuConnect 连接飞书机器人（保存配置 + 启动长连接）。
func (a *App) FeishuConnect(appID, appSecret, allowlist string) map[string]any {
	return feishu.Connect(a, appID, appSecret, allowlist)
}

// FeishuDisconnect 断开飞书机器人。
func (a *App) FeishuDisconnect() map[string]any {
	feishu.Disconnect(a)
	return map[string]any{"ok": true}
}

// FeishuStatus 飞书连接状态。
func (a *App) FeishuStatus() map[string]any { return feishu.Status() }

// GetFeishuConfig 读取已保存的飞书配置（面板回显）。
func (a *App) GetFeishuConfig() map[string]any { return config.GetFeishuConfig() }

// ---------------- Lark（国际版飞书）绑定 ----------------

// LarkConnect 连接 Lark 机器人（域名走 open.larksuite.com）。
func (a *App) LarkConnect(appID, appSecret, allowlist string) map[string]any {
	return larkB.Connect(a, appID, appSecret, allowlist)
}

// LarkDisconnect 断开 Lark 机器人。
func (a *App) LarkDisconnect() map[string]any {
	larkB.Disconnect(a)
	return map[string]any{"ok": true}
}

// LarkStatus Lark 连接状态。
func (a *App) LarkStatus() map[string]any { return larkB.Status() }

// GetLarkConfig 读取已保存的 Lark 配置。
func (a *App) GetLarkConfig() map[string]any { return config.GetLarkConfig() }
