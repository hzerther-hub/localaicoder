package main

// desktop/telegram.go Telegram 机器人（长轮询，无需公网回调/webhook）：
// 循环 getUpdates（50s 长轮询），收到文本消息 → 本机 agent 执行（schedMu 串行、
// 切到启动时目录、远端起写工具一律拒绝）→ sendMessage 回复。
// 凭据存 models.json 顶层 "telegram"（config.Get/SetTelegramConfig）。
// 纯 stdlib（默认 http.Transport 走 ProxyFromEnvironment，国内可配 HTTPS_PROXY）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/tools"
)

type telegramBot struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	token   string
	allow   []string
	chats   map[int64]*feishuChat // chat_id → 独立会话历史
	httpc   *http.Client
}

var telegram = &telegramBot{chats: map[int64]*feishuChat{}, httpc: &http.Client{Timeout: 40 * time.Second}}

// Connect 连接 Telegram（保存配置 + 启动长轮询）。
func (t *telegramBot) Connect(a *App, token, allowlist string) map[string]any {
	token = strings.TrimSpace(token)
	if token == "" {
		return map[string]any{"ok": false, "msg": "Bot Token 不能为空（找 @BotFather 申请）"}
	}
	t.Disconnect(a)
	config.SetTelegramConfig(token, allowlist)

	var allow []string
	for _, s := range strings.FieldsFunc(allowlist, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '；' || r == ' '
	}) {
		if s = strings.TrimSpace(s); s != "" {
			allow = append(allow, s)
		}
	}
	a.mu.Lock()
	modelKey := a.modelKey
	a.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.running, t.cancel, t.token, t.allow = true, cancel, token, allow
	t.mu.Unlock()
	go t.loop(a, ctx, token, allow, modelKey, tools.GetWorkspace())
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "channel:status", map[string]any{"channel": "telegram", "running": true})
	}
	return map[string]any{"ok": true, "msg": "Telegram 机器人已连接（长轮询）"}
}

// Disconnect 断开。
func (t *telegramBot) Disconnect(a *App) {
	t.mu.Lock()
	running, cancel := t.running, t.cancel
	t.running, t.cancel = false, nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if running && a != nil && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "channel:status", map[string]any{"channel": "telegram", "running": false})
	}
}

// Status 连接状态。
func (t *telegramBot) Status() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return map[string]any{"running": t.running}
}

// loop 长轮询 getUpdates。
func (t *telegramBot) loop(a *App, ctx context.Context, token string, allow []string, modelKey, workspace string) {
	var offset int64
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := t.getUpdates(ctx, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second): // 网络异常退避
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			t.handleUpdate(a, u, token, allow, modelKey, workspace)
		}
	}
}

// getUpdates 一次长轮询。
func (t *telegramBot) getUpdates(ctx context.Context, token string, offset int64) ([]tgUpdate, error) {
	form := url.Values{}
	form.Set("timeout", "50")
	form.Set("allowed_updates", `["message"]`)
	if offset > 0 {
		form.Set("offset", strconv.FormatInt(offset, 10))
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+token+"/getUpdates", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Ok     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.Ok {
		return nil, fmt.Errorf("telegram api error")
	}
	return out.Result, nil
}

// handleUpdate 处理一条消息：白名单 → 取文本 → 每会话串行跑 agent → 回复。
func (t *telegramBot) handleUpdate(a *App, u tgUpdate, token string, allow []string, modelKey, workspace string) {
	if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
		return
	}
	m := u.Message
	chatID := m.Chat.ID
	sender := strconv.FormatInt(m.From.ID, 10)
	if m.From.Username != "" {
		sender = "@" + m.From.Username
	}
	// 白名单：配置了才生效（支持数字 id 或 @username，chat_id 或 sender 任一命中即可）
	if len(allow) > 0 && !containsStr(allow, sender) && !containsStr(allow, strconv.FormatInt(chatID, 10)) {
		return // 未授权：静默忽略
	}

	chat := t.chat(chatID)
	chat.mu.Lock()
	hist := chat.history
	go func() {
		defer chat.mu.Unlock()
		schedMu.Lock()
		defer schedMu.Unlock()
		model := config.FindModel(modelKey)
		if model == nil {
			t.reply(token, chatID, "本机当前模型不可用")
			return
		}
		prev := tools.GetWorkspace()
		tools.SetWorkspace(workspace)
		defer tools.SetWorkspace(prev)

		a2 := &agent.Agent{
			Mode:   agent.ModeAlways,
			Model:  model,
			OnStop: func() bool { return false },
			// 远端安全：写工具执行前一律拒绝
			OnApproval: func(name string, args map[string]any, summary string) bool {
				t.reply(token, chatID, "已拒绝远程写操作："+name)
				return false
			},
		}
		final, runErr := a2.Run(m.Text, hist, nil)
		chat.history = a2.Messages
		reply := final
		if runErr != nil {
			reply = "⚠️ " + runErr.Error()
		} else if strings.TrimSpace(reply) == "" {
			reply = "（本轮无文本输出）"
		}
		t.reply(token, chatID, reply)
	}()
}

// reply 发送文本回复。
func (t *telegramBot) reply(token string, chatID int64, text string) {
	if len(text) > 3800 {
		text = text[:3800] + "\n…（已截断）"
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	resp, err := t.httpc.Post("https://api.telegram.org/bot"+token+"/sendMessage", "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// chat 取（或建）会话状态。
func (t *telegramBot) chat(chatID int64) *feishuChat {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.chats[chatID]
	if c == nil {
		c = &feishuChat{}
		t.chats[chatID] = c
	}
	return c
}

// tgUpdate / tgMessage Telegram 更新结构。
type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Text string  `json:"text"`
	Chat *tgChat `json:"chat"`
	From tgFrom  `json:"from"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgFrom struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// ---------------- 桌面绑定 ----------------

// TelegramConnect 连接 Telegram 机器人。
func (a *App) TelegramConnect(token, allowlist string) map[string]any {
	return telegram.Connect(a, token, allowlist)
}

// TelegramDisconnect 断开 Telegram 机器人。
func (a *App) TelegramDisconnect() map[string]any {
	telegram.Disconnect(a)
	return map[string]any{"ok": true}
}

// TelegramStatus Telegram 连接状态。
func (a *App) TelegramStatus() map[string]any { return telegram.Status() }

// GetTelegramConfig 读取已保存的 Telegram 配置。
func (a *App) GetTelegramConfig() map[string]any { return config.GetTelegramConfig() }
