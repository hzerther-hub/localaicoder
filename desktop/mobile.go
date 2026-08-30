package main

// desktop/mobile.go 移动端远程控制中继：
// 局域网起一个带 token 的 HTTP 服务，手机扫码打开轻量控制台——
// 项目列表 / 会话记录 / 运行中标记 / 聊天。发送直接走 RunManager（与桌面同源同步：
// 手机发的消息桌面实时可见，桌面发起的手机也能看）；权限跟随本机模式。
// 二维码/URL 由绑定 MobileStart 返回；状态变化经 "mobile:status" 事件推给前端。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/config"
	"localai/internal/media"
	"localai/internal/msg"
	"localai/internal/sessions"
	"localai/internal/tools"
)

type mobileRelay struct {
	mu        sync.Mutex
	srv       *http.Server
	ln        net.Listener
	url       string // 形如 http://192.168.1.5:51770/?t=<token>（二维码内容）
	token     string
	running   bool
	connected bool // 已有手机客户端接入
}

var mobile = &mobileRelay{}

// lanIP 取第一个非回环 IPv4（尽力而为；取不到回退 127.0.0.1）。
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipn, ok := addr.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// setConnected 更新接入状态并通知桌面 UI。
func (m *mobileRelay) setConnected(a *App, on bool) {
	m.mu.Lock()
	changed := m.connected != on
	m.connected = on
	m.mu.Unlock()
	if changed && a != nil && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "mobile:status", map[string]any{"connected": on})
	}
}

// Start 启动中继服务（重复启动返回现有状态）。返回 {ok,url,qr}。
func (m *mobileRelay) Start(a *App) map[string]any {
	m.mu.Lock()
	if m.running {
		url, qr := m.url, m.qrData()
		m.mu.Unlock()
		return map[string]any{"ok": true, "url": url, "qr": qr}
	}
	token := randID() + randID()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		m.mu.Unlock()
		return map[string]any{"ok": false, "msg": "监听端口失败: " + err.Error()}
	}
	m.ln, m.token = ln, token
	m.url = fmt.Sprintf("http://%s:%d/?t=%s", lanIP(), ln.Addr().(*net.TCPAddr).Port, token)
	m.running = true
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.page)
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) { m.stream(a, w, r) })
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) { m.apiState(a, w, r) })
	mux.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) { m.apiMessages(a, w, r) })
	mux.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) { m.apiSend(a, w, r) })
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) { m.apiModels(a, w, r) })
	mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) { m.apiSetModel(a, w, r) })
	mux.HandleFunc("/api/effort", func(w http.ResponseWriter, r *http.Request) { m.apiSetEffort(a, w, r) })
	mux.HandleFunc("/api/mode", func(w http.ResponseWriter, r *http.Request) { m.apiMode(a, w, r) })
	m.srv = &http.Server{Handler: m.auth(mux), ReadHeaderTimeout: 5 * time.Second}
	srv, ctx := m.srv, a.ctx
	m.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			m.Stop(a)
		}
	}()
	if ctx != nil {
		wailsruntime.EventsEmit(ctx, "mobile:status", map[string]any{"running": true})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"ok": true, "url": m.url, "qr": m.qrData()}
}

// Stop 停止服务。
func (m *mobileRelay) Stop(a *App) map[string]any {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return map[string]any{"ok": true}
	}
	srv, ln := m.srv, m.ln
	m.running, m.connected = false, false
	m.srv, m.ln = nil, nil
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
	if ln != nil {
		_ = ln.Close()
	}
	if a != nil && a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "mobile:status", map[string]any{"running": false, "connected": false})
	}
	return map[string]any{"ok": true}
}

// Status 当前状态（桌面绑定轮询）。
func (m *mobileRelay) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"running": m.running, "connected": m.connected, "url": m.url, "qr": m.qrData()}
}

// qrData 生成二维码 PNG 的 dataURL（调用方需持有 m.mu）。
func (m *mobileRelay) qrData() string {
	if m.url == "" {
		return ""
	}
	png, err := qrcode.Encode(m.url, qrcode.Medium, 360)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// auth token 校验中间件：所有路径必须带 ?t=<token>。
func (m *mobileRelay) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		token := m.token
		m.mu.Unlock()
		if token == "" || r.URL.Query().Get("t") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// page 手机端页面（单文件内嵌：项目/会话/聊天/运行标记）。
func (m *mobileRelay) page(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	token := m.token
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, strings.Replace(mobilePageHTML, "%TOKEN%", token, 1))
}

// writeJSON JSON 响应小工具。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// apiState 全量状态：会话列表（含项目、运行标记）+ 当前工作区 + 权限模式。
func (m *mobileRelay) apiState(a *App, w http.ResponseWriter, r *http.Request) {
	runs := map[string]bool{}
	for _, rr := range a.runner.ListRuns() {
		if id, ok := rr["sessionId"].(string); ok {
			runs[id] = true
		}
	}
	list := sessions.ListSessions(200, "", "")
	out := make([]map[string]any, 0, len(list))
	for _, s := range list {
		out = append(out, map[string]any{
			"id": s.ID, "title": s.Title, "updated": s.Updated,
			"workspace": s.Workspace, "running": runs[s.ID],
		})
	}
	a.mu.Lock()
	mode := a.mode
	sid := a.sessionID
	a.mu.Unlock()
	writeJSON(w, map[string]any{
		"workspace": tools.GetWorkspace(), "mode": mode, "current_session": sid, "sessions": out,
	})
}

// apiMessages 读取会话历史（user/assistant 文本，供手机端渲染对话记录）。
func (m *mobileRelay) apiMessages(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s := sessions.Load(id)
	if s == nil {
		writeJSON(w, map[string]any{"ok": false, "msg": "会话不存在"})
		return
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
	writeJSON(w, map[string]any{"ok": true, "id": id, "title": s.Title,
		"workspace": s.Workspace, "messages": msgs})
}

// extractMsgContent 取消息文本 + 图片 dataURL（手机端渲染图片用）。
func extractMsgContent(mm msg.Msg) (string, []string) {
	if c := msg.S(mm, "content"); c != "" {
		return cutAttDump(c), nil
	}
	var text strings.Builder
	var imgs []string
	for _, p := range msg.L(mm, "content") {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		switch msg.S(pm, "type") {
		case "text":
			appendContentText(&text, msg.S(pm, "text"))
		case "image_url":
			if u := msg.S(msg.M(pm, "image_url"), "url"); u != "" {
				imgs = append(imgs, media.ThumbDataURL(u, 480)) // 缩略图 base64（手机端解码显示）
			}
		}
	}
	return text.String(), imgs
}

// appendContentText 追加文本；附件内容转储（"📎 附件文件 … 内容：…"）只保留「📎 文件名」，丢弃正文。
func appendContentText(b *strings.Builder, t string) {
	if t == "" {
		return
	}
	if name := attDumpName(t); name != "" {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\U0001F4CE " + name)
		return
	}
	if isAttContentDump(t) {
		return // 其它附件说明（PDF/压缩包）也不作正文展示
	}
	b.WriteString(t)
}

// attDumpName 从 "📎 附件文件 <name> 内容：…" 提取文件名；不是这种格式返回空。
func attDumpName(t string) string {
	const prefix = "\U0001F4CE 附件文件 "
	if !strings.HasPrefix(t, prefix) {
		return ""
	}
	rest := t[len(prefix):]
	name := rest
	for _, sep := range []string{" 内容", "内容"} {
		if i := strings.Index(rest, sep); i >= 0 {
			name = rest[:i]
			break
		}
	}
	return strings.TrimSpace(name)
}

// cutAttDump 截断字符串 content 里"附件文件内容转储"之后的正文（只留用户输入部分）。
func cutAttDump(s string) string {
	markers := []string{"\U0001F4CE 附件文件", "[PDF 附件", "[压缩包附件", "\U0001F4CE 压缩包"}
	for _, m := range markers {
		if i := strings.Index(s, m); i >= 0 {
			return strings.TrimRight(s[:i], "\n ")
		}
	}
	return s
}

// isAttContentDump 是否 agent 附件分析产生的"附件内容转储"（消息里不该展示原文件内容，避免冗余/重复）。
func isAttContentDump(s string) bool {
	prefixes := []string{"\U0001F4CE 附件文件", "[PDF 附件", "[压缩包附件", "\U0001F4CE 压缩包"}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// flattenText 取消息文本（content 为字符串或多模态 parts 数组）。
func flattenText(mm msg.Msg) string {
	if c := msg.S(mm, "content"); c != "" {
		return c
	}
	var b strings.Builder
	for _, p := range msg.L(mm, "content") {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		if msg.S(pm, "type") == "text" {
			b.WriteString(msg.S(pm, "text"))
		}
	}
	return b.String()
}

// ensureSessionWorkspace 切到会话所属工作目录（全局状态；桌面 UI 经事件同步）。
func ensureSessionWorkspace(a *App, ws string) {
	if ws == "" {
		return
	}
	if st, err := os.Stat(ws); err != nil || !st.IsDir() {
		return
	}
	if tools.GetWorkspace() == ws {
		return
	}
	tools.SetWorkspace(ws)
	config.SaveLastWorkspace(ws)
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "workspace:changed", ws)
	}
}

// emitUserMessage 用户消息同步事件：桌面聊天区与手机页都以它渲染用户气泡。
func emitUserMessage(a *App, sessionID, text string, imgs []string) {
	ev := msg.Event{"type": "user_message", "sessionId": sessionID, "text": text}
	if len(imgs) > 0 {
		ev["images"] = imgs
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "agent:event", ev)
	}
	a.runner.fanout(ev)
}

// apiSend 手机发送消息：走 RunManager 与桌面完全同源（历史/审批/事件全一致）。
func (m *mobileRelay) apiSend(a *App, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Session string `json:"session"`
		Text    string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if strings.TrimSpace(in.Text) == "" || strings.TrimSpace(in.Session) == "" {
		http.Error(w, "empty", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	model := config.FindModel(a.modelKey)
	mode := a.mode
	a.mu.Unlock()
	if model == nil {
		writeJSON(w, map[string]any{"ok": false, "msg": "当前模型不可用"})
		return
	}
	var ws string
	if s := sessions.Load(in.Session); s != nil {
		ws = s.Workspace
	}
	if ws == "" { // 会话还没落盘（新会话）：用当前工作区补全
		ws = tools.GetWorkspace()
	}
	ensureSessionWorkspace(a, ws)
	a.runner.SeedHistory(in.Session) // 续聊旧会话：内存历史为空时从 SQLite 播种
	emitUserMessage(a, in.Session, in.Text, nil)
	if err := a.runner.Send(in.Session, *model, in.Text, nil, mode); err != nil {
		writeJSON(w, map[string]any{"ok": false, "msg": "会话正在运行中，请稍候"})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiModels 模型列表（手机顶栏切换用）。
func (m *mobileRelay) apiModels(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, modelListPayload(a))
}

// apiSetModel 切换当前模型。
func (m *mobileRelay) apiSetModel(a *App, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Key != "" {
		a.SetCurrentModel(in.Key)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiSetEffort 设置思考等级。
func (m *mobileRelay) apiSetEffort(a *App, w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key    string `json:"key"`
		Effort string `json:"effort"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Key != "" {
		a.SetReasoningEffort(in.Key, in.Effort)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiMode 读取(GET) / 切换(POST) 权限模式。
func (m *mobileRelay) apiMode(a *App, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var in struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Mode != "" {
			a.SetPermissionMode(in.Mode)
		}
		writeJSON(w, map[string]any{"ok": true, "mode": a.GetPermissionMode()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "mode": a.GetPermissionMode()})
}
func (m *mobileRelay) stream(a *App, w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	sid := r.URL.Query().Get("sid")
	ch := a.runner.Subscribe()
	defer func() {
		a.runner.Unsubscribe(ch)
		m.setConnected(a, false)
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok2 := <-ch:
			if !ok2 {
				return
			}
			if sid != "" {
				if s := msg.S(ev, "sessionId"); s != "" && s != sid {
					continue
				}
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// ---------------- 桌面绑定 ----------------

// MobileStart 启动手机远程服务，返回 {ok,url,qr}。
func (a *App) MobileStart() map[string]any { return mobile.Start(a) }

// MobileStop 停止手机远程服务。
func (a *App) MobileStop() map[string]any { return mobile.Stop(a) }

// MobileStatus 手机远程状态 {running,connected,url,qr}。
func (a *App) MobileStatus() map[string]any { return mobile.Status() }

// mobilePageHTML 手机端页面（深色单页）：项目切换 / 会话抽屉（运行标记）/
// 对话记录 / 流式聊天。用户气泡统一由 user_message 事件渲染（双端同步）。
const mobilePageHTML = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1">
<title>Local AI Studio · 远程</title>
<style>
*{box-sizing:border-box}
 body{margin:0;background:#0d0e12;color:#e8e8ef;font:15px/1.55 -apple-system,"PingFang SC","Microsoft YaHei",system-ui,sans-serif;display:flex;flex-direction:column;height:100dvh;-webkit-font-smoothing:antialiased}
 header{display:flex;align-items:center;gap:8px;padding:9px 12px;background:rgba(22,23,31,.92);backdrop-filter:blur(10px);border-bottom:1px solid #23242f;position:sticky;top:0;z-index:6}
 select{background:#14151d;border:1px solid #2a2b38;border-radius:10px;color:#e2e2ec;font-size:13.5px;padding:9px 10px;-webkit-appearance:none;appearance:none}
 select:focus{outline:none;border-color:#6c5ce7}
 #btnS{flex:none;background:#1a1b24;border:1px solid #2a2b38;border-radius:10px;color:#c9c9d8;font-size:13px;padding:9px 12px}
 #proj{flex:1;min-width:0}
 #run{color:#3ddc97;font-size:12px;flex:none;font-variant-numeric:tabular-nums}
 .hrow{display:flex;gap:8px;padding:8px 12px;border-bottom:1px solid #1c1d28;background:#131419;position:sticky;top:51px;z-index:5}
 .ctl{flex:1;min-width:0;display:flex;flex-direction:column;gap:2px}
 .ctl span{font-size:10.5px;color:#7b7c8a;padding-left:2px}
 .ctl select{width:100%;padding:6px 8px;font-size:12.5px}
 .ctl:nth-child(2),.ctl:nth-child(3){flex:none;width:82px}
 #mode{font-size:11px;color:#7b7c8a;text-align:center;padding:6px;background:#0f1014}
 #log{flex:1;overflow-y:auto;padding:16px 14px 8px;display:flex;flex-direction:column;gap:10px}
 .user{align-self:flex-end;max-width:84%;background:linear-gradient(135deg,#7c5ce8,#9a6cf0);color:#fff;padding:10px 14px;border-radius:16px 16px 4px 16px;white-space:pre-wrap;word-break:break-word}
 .ai{align-self:flex-start;max-width:84%;background:#1a1b25;color:#e8e8ef;border:1px solid #2a2b38;padding:10px 14px;border-radius:16px 16px 16px 4px;white-space:pre-wrap;word-break:break-word}
 .tool{align-self:flex-start;font-size:12px;color:#8b8b98;background:transparent;padding:0 4px}
 .err{align-self:stretch;font-size:12.5px;color:#ff8ba0;background:rgba(255,107,129,.08);border:1px solid rgba(255,107,129,.22);padding:8px 12px;border-radius:12px}
 .empty{color:#6a6b78;text-align:center;margin-top:44px;font-size:13.5px}
  #qc{display:flex;flex-wrap:wrap;gap:9px 20px;padding:11px 14px;border-top:1px solid #1c1d28;background:#131419}
 #qc span{color:#a6abbf;font-size:13.5px;cursor:pointer;line-height:1.7;white-space:nowrap;letter-spacing:.2px}
 #qc span:active{color:#fff;text-decoration:underline}
 form{display:flex;gap:8px;padding:10px 12px;border-top:1px solid #1c1d28;background:#131419}
 form input{flex:1;background:#14151d;border:1px solid #2a2b38;border-radius:12px;padding:11px 14px;color:#e8e8ef;font-size:15px}
 form input:focus{outline:none;border-color:#6c5ce7}
 form button{background:linear-gradient(135deg,#6c5ce7,#8f6cf0);border:0;border-radius:12px;color:#fff;padding:0 20px;font-size:15px;font-weight:600}
 #drawer{position:fixed;inset:0;background:rgba(0,0,0,.5);display:none;z-index:9}
 #drawer.open{display:block}
 #drawer .panel{position:absolute;left:0;top:0;bottom:0;width:82%;max-width:340px;background:#15161d;overflow-y:auto;padding:12px;box-shadow:2px 0 24px rgba(0,0,0,.5)}
 .gr{font-size:11px;color:#7b7c8a;padding:10px 6px 6px;letter-spacing:.6px;text-transform:uppercase}
 .s{padding:11px 12px;border-radius:12px;margin-bottom:4px;color:#d6d6e2;cursor:pointer;display:flex;gap:8px;align-items:center;background:#1a1b25}
 .s:active{background:#23242f}
 .s.on{background:linear-gradient(135deg,rgba(108,92,231,.2),rgba(143,108,240,.15));color:#cdc2ff;border:1px solid rgba(108,92,231,.35)}
 .s .run{color:#3ddc97;flex:none}
 .s .t{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:14px}
 .s .ago{font-size:11px;color:#7b7c8a;flex:none}

 #slash{display:none;position:fixed;left:12px;right:12px;bottom:62px;background:#17181f;border:1px solid #2a2b38;border-radius:14px;overflow:hidden;box-shadow:0 8px 30px rgba(0,0,0,.5);z-index:8}
 #slash.open{display:block}
 #slash .si{display:flex;align-items:center;gap:10px;padding:11px 14px;cursor:pointer;border-bottom:1px solid #1e1f29}
 #slash .si:last-child{border-bottom:0}
 #slash .si:active{background:#23242f}
 #slash .si .k{flex:none;color:#8f7cf0;font-family:ui-monospace,SFMono-Regular,monospace;font-size:13px;font-weight:600}
 #slash .si .d{flex:1;font-size:13px;color:#b9b9c8;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}

</style></head><body>
<header>
 <button id="btnS">☰ 会话</button>
 <select id="proj"></select>
 <span id="run"></span>
</header>
<div class="hrow">
 <div class="ctl"><span>模型</span><select id="model"></select></div>
 <div class="ctl"><span>推理</span><select id="effort"></select></div>
 <div class="ctl"><span>权限</span><select id="modeSel"><option value="always">总是允许</option><option value="ask">询问</option><option value="readonly">只读</option></select></div>
</div>
<div id="log"><div class="empty">从右上选择一个会话开始</div></div>
<div id="qc"></div>
<div id="slash"></div>
<form id="f"><input id="i" placeholder="发消息…" autocomplete="off"><button>发送</button></form>
<script>
const T='%TOKEN%';
const RELAY=false;
const $=id=>document.getElementById(id);
const log=$('log');
let ALL=[],wsCur='',sid='',runs=new Set(),es=null,cur=null;
const esc=s=>{const d=document.createElement('div');d.textContent=s;return d.innerHTML;};
const ago=ts=>{const d=Date.now()/1000-ts;if(d<60)return '刚刚';if(d<3600)return Math.floor(d/60)+'分';if(d<86400)return Math.floor(d/3600)+'时';return Math.floor(d/86400)+'天';};
async function api(p,opt){const r=await fetch(p+(p.includes('?')?'&':'?')+'t='+T,opt);if(!r.ok)throw new Error(r.status);return r.json();}
async function loadState(){
 try{const s=await api('/api/state');ALL=s.sessions||[];
  try{$('modeSel').value=s.mode}catch(e){}
  $('mode').textContent='工作区 '+s.workspace+' · 权限 '+s.mode+(runs.size?' · '+runs.size+' 个会话运行中':'');
  const map={};ALL.forEach(x=>{const k=x.workspace||'';(map[k]=map[k]||{n:0,u:0}).n++;map[k].u=Math.max(map[k].u,x.updated)});
  const sel=$('proj');const keep=wsCur||s.workspace;
  sel.innerHTML=Object.keys(map).sort((a,b)=>map[b].u-map[a].u).map(w=>'<option value="'+esc(w)+'">'+esc(w.split('/').filter(Boolean).pop()||w)+' ('+map[w].n+')</option>').join('');
  wsCur=map[keep]?keep:(sel.options[0]?sel.options[0].value:'');sel.value=wsCur;
  runs=new Set(ALL.filter(x=>x.running).map(x=>x.id));
  renderSess();badge();
 }catch(e){}
}
function renderSess(){
 const rows=ALL.filter(x=>(x.workspace||'')===wsCur).sort((a,b)=>b.updated-a.updated);
 let h='<div class="gr">项目会话（'+rows.length+'）</div>';
 rows.forEach(x=>{h+='<div class="s'+(x.id===sid?' on':'')+'" data-id="'+x.id+'">'
  +(x.running?'<span class="run">▶</span>':'')+'<span class="t">'+esc(x.title)+'</span><span class="ago">'+ago(x.updated)+'</span></div>';});
 $('sessList').innerHTML=h||'<div class="gr">该项目暂无会话</div>';
}
function badge(){$('run').textContent=runs.size?'▶ '+runs.size:'';}
async function openS(id){
 sid=id;$('drawer').classList.remove('open');log.innerHTML='';cur=null;renderSess();
 try{
  const m=await api('/api/messages?id='+sid);
  if(!m.messages||!m.messages.length){log.innerHTML='<div class="empty">（空会话，直接发第一条消息）</div>';}
  else m.messages.forEach(x=>add(x.role==='user'?'user':'ai',x.text));
 }catch(e){add('err','读取历史失败');}
 connect();
}
function add(cls,t){const d=document.createElement('div');d.className=cls;d.textContent=t;log.appendChild(d);log.scrollTop=log.scrollHeight;return d;}
function connect(){
 if(es)es.close();if(!sid)return;
 es=new EventSource('/stream?t='+T+'&sid='+sid);
 es.onmessage=e=>{let m;try{m=JSON.parse(e.data)}catch(_){return}
  if(m.sessionId&&sid&&m.sessionId!==sid)return;
  if(m.type==='user_message'){add('user',m.text);cur=null;}
  else if(m.type==='text'){if(!cur)cur=add('ai','');cur.textContent+=m.delta;log.scrollTop=log.scrollHeight;}
  else if(m.type==='tool'){add('tool','🔧 '+m.delta);}
  else if(m.type==='error'){add('err',m.delta);cur=null;}
  else if(m.type==='run:started'){runs.add(m.sessionId);markSess(m.sessionId,true);badge();}
  else if(m.type==='run:finished'){runs.delete(m.sessionId);markSess(m.sessionId,false);badge();cur=null;}
 };
}
function markSess(id,on){ALL.forEach(x=>{if(x.id===id)x.running=on});renderSess();}
$('sessList').onclick=e=>{const r=e.target.closest('.s');if(r)openS(r.dataset.id);};
$('btnS').onclick=()=>{$('drawer').classList.toggle('open');if($('drawer').classList.contains('open'))loadState();};
$('drawer').onclick=e=>{if(e.target.id==='drawer')$('drawer').classList.remove('open')};
$('proj').onchange=()=>{wsCur=$('proj').value;renderSess();};
$('f').onsubmit=async ev=>{ev.preventDefault();
 const i=$('i');const t=i.value.trim();if(!t||!sid)return;
 i.value='';
 try{const r=await api('/api/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({session:sid,text:t})});
  if(!r.ok)add('err',r.msg||'发送失败');
 }catch(e){add('err','发送失败');}
};

async function loadModels(){
 let d; try{ d = RELAY ? await req({type:'models'}) : await api('/api/models'); }catch(e){ d=null; }
 const sel=$('model');
 if(!d||!d.models||!d.models.length){ sel.innerHTML='<option value="">'+(RELAY?'正在连接桌面…':'暂无模型')+'</option>'; fillEffort(null); return; }
 sel.innerHTML=d.models.map(x=>'<option value="'+esc(x.key)+'"'+(x.is_current?' selected':'')+'>'+esc(x.model_id||x.display_name)+'</option>').join('');
 fillEffort(d.models.find(x=>x.is_current)||null);
}
function fillEffort(mi){
 const ef=$('effort');
 const choices=(mi&&mi.reasoning&&mi.reasoning_choices)?mi.reasoning_choices:[];
 ef.innerHTML=choices.length
   ? choices.map(c=>'<option value="'+esc(c)+'"'+((mi.reasoning_effort||'')===c?' selected':'')+'>'+esc(c===''?'默认':c)+'</option>').join('')
   : '<option value="">'+(mi&&mi.reasoning?'默认':'—')+'</option>';
 if(mi&&mi.reasoning_effort)ef.value=mi.reasoning_effort;
}
$('model').onchange=async()=>{const k=$('model').value;
 try{ RELAY ? await req({type:'model',key:k}) : await api('/api/model',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key:k})}); }catch(e){}
 loadModels(); };  // 只切模型，不清理当前聊天记录
$('effort').onchange=async()=>{const k=$('model').value,e=$('effort').value;
 try{ RELAY ? await req({type:'effort',key:k,effort:e}) : await api('/api/effort',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key:k,effort:e})}); }catch(e){}
};
$('modeSel').onchange=async()=>{const v=$('modeSel').value;
 try{ RELAY ? await req({type:'mode',value:v}) : await api('/api/mode',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({mode:v})}); }catch(e){}
};
const QCS=[['📂 列目录','列出当前项目的目录结构'],['🔍 找 BUG','分析这个项目，找出可能的 BUG 并给出修复'],['🧪 写测试','为最近的代码改动补充单元测试'],['📝 总结改动','总结最近的 git 提交和改动要点'],['🧹 重构建议','分析当前代码，给出重构建议'],['❓ 这是什么','解释当前项目的作用和整体结构']];
$('qc').innerHTML=QCS.map(x=>'<span data-p="'+esc(x[1])+'">'+x[0]+'</span>').join('');
$('qc').onclick=e=>{const b=e.target.closest('[data-p]');if(!b)return;const i=$('i');i.value=(i.value?i.value+'\n':'')+b.dataset.p;i.focus();};
loadModels();


// '/' 斜杠命令下拉（点击填入输入框）
const CMDS=[['/列目录','列出当前项目的目录结构'],['/找bug','分析这个项目，找出可能的 BUG 并给出修复'],
 ['/写测试','为最近的代码改动补充单元测试'],['/总结','总结最近的 git 提交和改动要点'],
 ['/重构','分析当前代码，给出重构建议'],['/解释','解释当前项目的作用和整体结构']];
function slashShow(q){
 const box=$('slash');
 const list=CMDS.filter(c=>!q||c[0].slice(1).toLowerCase().includes(q.toLowerCase())).slice(0,8);
 if(!list.length){box.classList.remove('open');return;}
 box.innerHTML=list.map(c=>'<div class="si" data-t="'+esc(c[1])+'"><span class="k">'+c[0]+'</span><span class="d">'+esc(c[1])+'</span></div>').join('');
 box.classList.add('open');
}
$('i').addEventListener('input',()=>{const v=$('i').value;if(v.startsWith('/'))slashShow(v.slice(1));else $('slash').classList.remove('open');});
$('slash').addEventListener('click',e=>{const si=e.target.closest('.si');if(!si)return;const i=$('i');i.value=si.dataset.t;i.focus();$('slash').classList.remove('open');});
$('i').addEventListener('keydown',e=>{if(e.key==='Escape')$('slash').classList.remove('open');});

loadState();setInterval(loadState,8000);
</script></body></html>`
