package main

// desktop/relay.go 自建中继客户端（跨网手机远程，仿飞书长连接思路）：
// 桌面**主动出站**连接用户自建的中继服务器（NAT 友好、无需公网 IP / 端口映射），
// 服务器做哑管道，手机在任意网络通过同一个中继与本机对话——
// 项目/会话/运行状态/聊天与局域网远程完全同源（复用 RunManager + 事件 fanout）。
// 协议见 docs/relay/protocol.md；服务器部署见 docs/relay/deploy.md。
// 配置存 models.json 顶层 "relay"（config.Get/SetRelayConfig）。

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	qrcode "github.com/skip2/go-qrcode"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/config"
	"localai/internal/media"
	"localai/internal/msg"
	"localai/internal/sessions"
	"localai/internal/tools"
)

type relayClient struct {
	mu        sync.Mutex
	running   bool
	connected bool // 是否已与服务器建立活动 WS（区别于「已启动但重连中」）
	lastErr   string
	cancel    context.CancelFunc
	server    string
	token     string
}

var relayC = &relayClient{}

// Connect 连接中继（保存配置 + 启动出站连接）；重复连接先断开。
func (rc *relayClient) Connect(a *App, server, token string) map[string]any {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	if server == "" || token == "" {
		return map[string]any{"ok": false, "msg": "服务器地址与 device token 不能为空"}
	}
	// 归一化 https/http → wss/ws
	server = strings.Replace(strings.Replace(server, "https://", "wss://", 1), "http://", "ws://", 1)
	server = strings.TrimRight(server, "/")
	rc.Disconnect(a)
	config.SetRelayConfig(server, token)

	ctx, cancel := context.WithCancel(context.Background())
	rc.mu.Lock()
	rc.running, rc.cancel, rc.server, rc.token = true, cancel, server, token
	rc.mu.Unlock()
	go rc.loop(a, ctx, server, token)
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "relay:status", map[string]any{"running": true})
	}
	return map[string]any{"ok": true, "msg": "中继已启动（出站连接）"}
}

// Disconnect 断开中继连接。
func (rc *relayClient) Disconnect(a *App) {
	rc.mu.Lock()
	running, cancel := rc.running, rc.cancel
	rc.running, rc.cancel = false, nil
	rc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if running && a != nil && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "relay:status", map[string]any{"running": false})
	}
}

// Status 中继运行状态（含手机页地址与二维码——连接成功后扫码即打开该网站）。
func (rc *relayClient) Status() map[string]any {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return map[string]any{
		"running": rc.running, "connected": rc.connected, "server": rc.server,
		"phone_url": relayPhoneURL(), "qr": relayQRData(), "error": rc.lastErr,
	}
}

// setConnected 更新活动连接状态（连上/掉线）。
func (rc *relayClient) setConnected(on bool) {
	rc.mu.Lock()
	rc.connected = on
	rc.mu.Unlock()
}

// setErr 记录最近一次连接错误（面板展示排查用）。
func (rc *relayClient) setErr(e string) {
	rc.mu.Lock()
	rc.lastErr = e
	rc.mu.Unlock()
}

// RelayPhoneURL 中继手机页地址：由保存的服务器地址推导 https://host/s/?d=token。
func relayPhoneURL() string {
	c := config.GetRelayConfig()
	host := strings.TrimRight(strings.TrimSpace(msg.S(c, "server_url")), "/")
	host = strings.Replace(strings.Replace(host, "wss://", "https://", 1), "ws://", "http://", 1)
	token := strings.TrimSpace(msg.S(c, "device_token"))
	if host == "" || token == "" {
		return ""
	}
	return host + "/s/?d=" + token
}

// relayQRData 手机页二维码 dataURL。
func relayQRData() string {
	u := relayPhoneURL()
	if u == "" {
		return ""
	}
	png, err := qrcode.Encode(u, qrcode.Medium, 360)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// GenToken 生成 64 位随机 device token（用户粘到服务器白名单）。
func GenToken() string { return randID() + randID() + randID() + randID() }

// loop 出站拨号 + 指数退避重连。
func (rc *relayClient) loop(a *App, ctx context.Context, server, token string) {
	// wss/wss 直连，不走 HTTPS_PROXY/HTTP_PROXY（本机代理常无法正确升级 WebSocket，
	// 导致“use of closed network connection”丢连接）。中继是自建服务器，直连最可靠。
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Proxy:            func(*http.Request) (*url.URL, error) { return nil, nil },
		NetDialContext:   (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		u := server + "/client?d=" + url.QueryEscape(token)
		conn, _, err := dialer.DialContext(ctx, u, nil)
		if err != nil {
			rc.setErr(err.Error())
			rc.setConnected(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		rc.setErr("")
		rc.setConnected(true)
		rc.pump(a, ctx, conn, server, token)
		rc.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		// 断线：等退避后重连
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// pump 单次连接的主循环：hello + 事件上行（fanout）+ 指令下行（send/state/messages）。
func (rc *relayClient) pump(a *App, ctx context.Context, conn *websocket.Conn, server, token string) {
	defer conn.Close()
	rc.mu.Lock()
	rc.server, rc.token = server, token
	rc.mu.Unlock()

	// hello 握手（服务器/手机页可显示工作区+模型）
	a.mu.Lock()
	modelKey := a.modelKey
	mode := a.mode
	a.mu.Unlock()
	_ = writeRelay(conn, map[string]any{
		"type": "hello", "workspace": tools.GetWorkspace(), "model": modelKey, "mode": mode,
	})

	ch := a.runner.Subscribe()
	defer a.runner.Unsubscribe(ch)

	wmu := &sync.Mutex{} // gorilla 连接同一时刻只允许一个写者
	done := make(chan struct{})
	defer close(done)

	// 心跳：每 25s 发 ping，防 NAT/中间设备静默掐断空闲 TCP；读超时 75s 探测死连接
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				conn.Close()
				return
			case <-ticker.C:
				wmu.Lock()
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				wmu.Unlock()
			}
		}
	}()

	// 上行：把 agent 事件流推给服务器（广播到手机）
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				conn.Close()
				return
			case ev := <-ch:
				_ = writeRelayLocked(conn, wmu, ev)
			}
		}
	}()

	// 保活与死连接探测：客户端每 25s 发 ping，服务器回 pong 触发 PongHandler
	// 延长读超时 —— 空闲连接就不会被“读超时”误杀（修复频繁断开）。
	// 只有当服务器真死（90s 无任何帧/保活）才触发重连。
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})

	// 下行：处理手机经服务器转来的指令
	for {
		var in map[string]any
		if err := conn.ReadJSON(&in); err != nil {
			rc.setErr(shortConnErr(err))
			return
		}
		switch msg.S(in, "type") {
		case "send":
			rc.handleSend(a, in)
		case "state":
			_ = writeRelayLocked(conn, wmu, relayState(a, in["rid"]))
		case "messages":
			_ = writeRelayLocked(conn, wmu, relayMessages(msg.S(in, "id"), in["rid"]))
		case "models":
			_ = writeRelayLocked(conn, wmu, relayModels(a, in["rid"]))
		case "model":
			if k := msg.S(in, "key"); k != "" {
				a.SetCurrentModel(k)
			}
		case "effort":
			if k := msg.S(in, "key"); k != "" {
				a.SetReasoningEffort(k, msg.S(in, "effort"))
			}
		case "mode":
			if v := strings.TrimSpace(msg.S(in, "value")); v != "" {
				a.SetPermissionMode(v)
			}
		case "delete_session":
			if id := msg.S(in, "id"); id != "" {
				a.DeleteSession(id)
			}
		case "rename_session":
			if id := msg.S(in, "id"); id != "" {
				a.RenameSession(id, msg.S(in, "title"))
			}
		case "open_session":
			if id := msg.S(in, "id"); id != "" {
				a.openSessionFromPhone(id)
			}
		case "workspace":
			// 手机端切项目（同步方向=手机→PC）：切工作区并打开该项目最近的会话；
			// create=true 时目录不存在则先创建（手机端"新建目录"）
			if dir := msg.S(in, "dir"); dir != "" {
				a.switchWorkspaceFromPhone(dir, msg.B(in, "create"))
			}
		}
	}
}

// openSessionFromPhone 手机端打开某会话 → 桌面切到同一会话（双向跟踪）。
func (a *App) openSessionFromPhone(id string) {
	if a == nil || id == "" {
		return
	}
	_ = a.LoadSession(id) // 切工作区 + 设 sessionID + 播种历史 + fanout session:opened(手机)
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "session:opened", id) // 桌面 UI 跟随切换
	}
}

// switchWorkspaceFromPhone 手机端切换项目：切工作区，并自动打开该项目最近的会话（如有）。
// create=true 且目录不存在时先创建（手机端"新建目录"）。
// SetWorkspace/LoadSession 内部会广播 sessions:changed / session:opened，手机端状态随之对齐。
func (a *App) switchWorkspaceFromPhone(dir string, create bool) {
	if a == nil || dir == "" {
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if !create {
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	a.SetWorkspace(dir)
	for _, s := range sessions.ListSessions(1, dir, "") {
		a.LoadSession(s.ID)
		break
	}
}

// broadcastSessions 会话/项目结构变化 → 广播给手机端刷新会话列表，并通知桌面前端刷新侧栏。
func broadcastSessions(a *App) {
	if a == nil {
		return
	}
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "sessions:changed"}) // 手机端
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "sessions:changed", nil) // 桌面前端
	}
}

// shortConnErr 提炼连接错误关键词（面板展示更友好）。
func shortConnErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "TLS") || strings.Contains(s, "certificate") || strings.Contains(s, "x509") {
		return "TLS/证书错误：" + s
	}
	if strings.Contains(s, "i/o timeout") {
		return "连接超时（服务器可能不在线或防火墙拦了 443）"
	}
	if strings.Contains(s, "403") || strings.Contains(s, "forbidden") {
		return "服务器拒绝：token 不在白名单"
	}
	if strings.Contains(s, "connection refused") || strings.Contains(s, "no such host") {
		return "无法连接到服务器：" + s
	}
	return s
}

// handleSend 手机经中继发消息：与局域网远程同一条执行路径。
func (rc *relayClient) handleSend(a *App, in map[string]any) {
	session, text := msg.S(in, "session"), msg.S(in, "text")
	if session == "" || strings.TrimSpace(text) == "" {
		return
	}
	a.mu.Lock()
	model := config.FindModel(a.modelKey)
	mode := a.mode
	a.mu.Unlock()
	if model == nil {
		return
	}
	var ws string
	if s := sessions.Load(session); s != nil {
		ws = s.Workspace
	}
	if ws == "" {
		ws = tools.GetWorkspace()
	}
	ensureSessionWorkspace(a, ws)
	a.runner.SeedHistory(session)
	// 显示文本（广播）带 📎 附件名一次；传给 agent 的**原文**不带，避免 agent 消息内容里
	// 同时出现"📎 附件名"与附件分析里的文件名 → 历史帧重复显示。
	displayText := text
	if label := phoneAttLabel(in); label != "" {
		displayText += label
	}
	emitUserMessage(a, session, displayText, imgsOfAtts(in))
	if err := a.runner.Send(session, *model, text, savePhoneUploads(in), mode); err != nil {
		relayNotice(a, "会话正在运行中，请稍候")
	}
}

// savePhoneUploads 保存手机上传的附件（dataURL）到工作区 media/，返回路径列表供 agent 使用。
func savePhoneUploads(in map[string]any) []any {
	ws := tools.GetWorkspace()
	dir := filepath.Join(ws, "media")
	_ = os.MkdirAll(dir, 0o755)
	var out []any
	for _, x := range msg.L(in, "atts") {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		name := msg.S(m, "name")
		data := msg.S(m, "data")
		if name == "" || data == "" {
			continue
		}
		p, err := writeUpload(dir, name, data)
		if err == nil {
			out = append(out, p)
		}
	}
	return out
}

// writeUpload 解析 dataURL→base64→写入文件（防目录穿越，文件名加时间戳）。
func writeUpload(dir, name, data string) (string, error) {
	raw := data
	if i := strings.Index(raw, ","); i >= 0 {
		raw = raw[i+1:]
	}
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	base := filepath.Base(name) // 防路径穿越
	if base == "." || base == "/" || base == "" {
		base = "upload.bin"
	}
	fn := fmt.Sprintf("%d-%s", time.Now().UnixNano(), base)
	p := filepath.Join(dir, fn)
	if err := os.WriteFile(p, dec, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// imgsOfPaths 从桌面端路径附件提取图片缩略图（SendMessage 广播带图，手机端实时显示）。
func imgsOfPaths(attachments []any) []string {
	var out []string
	for _, at := range attachments {
		if p, ok := at.(string); ok && media.Classify(p) == "image" {
			if d, err := media.ImageToDataURL(p); err == nil {
				out = append(out, media.ThumbDataURL(d, 480))
			}
		}
	}
	return out
}

// phoneAttLabel 手机端附件名标签（"📎 name1  name2"），非图片附件也能显示文件名。
func phoneAttLabel(in map[string]any) string {
	var names []string
	for _, x := range msg.L(in, "atts") {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		if n := msg.S(m, "name"); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\n\n\U0001F4CE " + strings.Join(names, "  ")
}

// imgsOfAtts 提取手机发送附件里的图片 dataURL 缩略图（user_message 广播带图，手机端实时渲染）。
func imgsOfAtts(in map[string]any) []string {
	var out []string
	for _, x := range msg.L(in, "atts") {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		d := msg.S(m, "data")
		if strings.HasPrefix(d, "data:image") {
			out = append(out, media.ThumbDataURL(d, 480))
		}
	}
	return out
}

// relayNotice 向手机页广播提示。
func relayNotice(a *App, text string) {
	ev := msg.Event{"type": "error", "delta": text}
	a.runner.fanout(ev)
}

// modelListPayload 模型列表（手机顶栏切换用），移动端与中继共用。
func modelListPayload(a *App) map[string]any {
	out := make([]map[string]any, 0, len(a.ListModels()))
	for _, mm := range a.ListModels() {
		out = append(out, map[string]any{
			"key": mm.Key, "model_id": mm.ModelID, "display_name": mm.DisplayName,
			"is_current": mm.IsCurrent, "reasoning": mm.Reasoning, "vision": mm.Vision,
			"reasoning_effort": mm.ReasoningEffort, "reasoning_choices": mm.ReasoningChoices,
		})
	}
	return map[string]any{"models": out}
}

// relayModels 中继模型列表帧。
func relayModels(a *App, rid any) map[string]any {
	ml := modelListPayload(a)
	ml["type"] = "models"
	ml["rid"] = rid
	return ml
}

// relayState 全量状态帧。
func relayState(a *App, rid any) map[string]any {
	runs := map[string]bool{}
	for _, rr := range a.runner.ListRuns() {
		if id, ok := rr["sessionId"].(string); ok {
			runs[id] = true
		}
	}
	out := make([]map[string]any, 0)
	trash := map[string]bool{}
	for _, t := range config.GetProjectTrash() {
		trash[t] = true
	}
	for _, s := range sessions.ListSessions(200, "", "") {
		if trash[s.Workspace] {
			continue // 已删除（垃圾箱）项目：手机端一并隐藏
		}
		out = append(out, map[string]any{
			"id": s.ID, "title": s.Title, "updated": s.Updated,
			"workspace": s.Workspace, "running": runs[s.ID],
		})
	}
	a.mu.Lock()
	mode := a.mode
	cur := a.modelKey
	sid := a.sessionID
	a.mu.Unlock()
	return map[string]any{"type": "state", "rid": rid,
		"workspace": tools.GetWorkspace(), "mode": mode, "current": cur,
		"current_session": sid, "sessions": out,
		"branch": tools.GitBranch(tools.GetWorkspace()), "compact": a.GetCompactInfo()}
}

// relayMessages 会话消息帧。
func relayMessages(id string, rid any) map[string]any {
	s := sessions.Load(id)
	if s == nil {
		return map[string]any{"type": "messages", "rid": rid, "id": id, "messages": []map[string]any{}}
	}
	msgs := make([]map[string]any, 0, len(s.Messages))
	for _, raw := range s.Messages {
		mm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := msg.Role(mm)
		if role != "user" && role != "assistant" {
			continue
		}
		text, imgs := extractMsgContent(mm)
		mmap := map[string]any{"role": role, "text": text}
		if len(imgs) > 0 {
			mmap["images"] = imgs
		}
		if text == "" && len(imgs) == 0 {
			continue
		}
		msgs = append(msgs, mmap)
	}
	return map[string]any{"type": "messages", "rid": rid, "id": id, "messages": msgs}
}

// writeRelay 单次写入（无锁调用；仅在启动握手用）。
func writeRelay(conn *websocket.Conn, v any) error {
	return conn.WriteJSON(v)
}

// writeRelayLocked 加锁写入（事件上行与指令下行并发）。
func writeRelayLocked(conn *websocket.Conn, mu *sync.Mutex, v any) error {
	mu.Lock()
	defer mu.Unlock()
	return conn.WriteJSON(v)
}

// ---------------- 桌面绑定 ----------------

// RelayConnect 连接自建中继（server 可为 https/wss 形式）。
func (a *App) RelayConnect(server, token string) map[string]any {
	return relayC.Connect(a, server, token)
}

// RelayDisconnect 断开中继。
func (a *App) RelayDisconnect() map[string]any {
	relayC.Disconnect(a)
	return map[string]any{"ok": true}
}

// RelayStatus 中继状态。
func (a *App) RelayStatus() map[string]any { return relayC.Status() }

// RelayGenToken 生成 device token。
func (a *App) RelayGenToken() string { return GenToken() }

// GetRelayConfig 读取中继配置。
func (a *App) GetRelayConfig() map[string]any { return config.GetRelayConfig() }
