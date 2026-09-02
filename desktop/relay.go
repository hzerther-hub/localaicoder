package main

// desktop/relay.go 自建中继客户端（跨网手机远程，仿飞书长连接思路）：
// 桌面**主动出站**连接用户自建的中继服务器（NAT 友好、无需公网 IP / 端口映射），
// 服务器做哑管道，手机在任意网络通过同一个中继与本机对话——
// 项目/会话/运行状态/聊天与局域网远程完全同源（复用 RunManager + 事件 fanout）。
// 协议见 docs/relay/protocol.md；服务器部署见 docs/relay/deploy.md。
// 配置存 models.json 顶层 "relay"（config.Get/SetRelayConfig）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	fsEnabled bool // WEB 端文件浏览/编辑总开关（内存镜像，持久化在 relay.fs_enabled）
	fsSafe    bool // 安全目录模式：手机只能访问当前项目目录（relay.fs_safe）
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

	// hello 握手（服务器/手机页可显示工作区+模型+文件开关）
	a.mu.Lock()
	modelKey := a.modelKey
	mode := a.mode
	a.mu.Unlock()
	rc.syncFs()
	_ = writeRelay(conn, map[string]any{
		"type": "hello", "workspace": tools.GetWorkspace(), "model": modelKey, "mode": mode,
		"fs_enabled": rc.fsOK(), "fs_safe": rc.fsSafeOn(),
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
		case "fs_list", "fs_read", "fs_write", "fs_open_desktop", "fs_rename", "fs_delete":
			rc.handleFs(a, conn, wmu, in)
		case "dir_list":
			rc.handleDirList(conn, wmu, in)
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

// ---------------- WEB 端文件浏览/编辑（relay.fs_enabled 开关门控，默认关闭） ----------------

const (
	fsReadMax  = 1 << 20   // fs_read 读取上限（超出截断）
	fsWriteMax = 2 << 20   // fs_write 内容上限
	fsEditMax  = 200 << 10 // 手机端允许编辑的大小上限（>此值只读，防止手机浏览器卡顿）
)

// fsOK 手机端文件操作是否放行（内存开关，连接时从配置同步、面板切换时更新）。
func (rc *relayClient) fsOK() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.fsEnabled
}

// setFsEnabled 更新总开关内存镜像。
func (rc *relayClient) setFsEnabled(on bool) {
	rc.mu.Lock()
	rc.fsEnabled = on
	rc.mu.Unlock()
}

// fsSafeOn 安全目录模式是否开启（开启后手机只能在当前项目目录内活动）。
func (rc *relayClient) fsSafeOn() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.fsSafe
}

// setFsSafe 更新安全目录模式内存镜像。
func (rc *relayClient) setFsSafe(on bool) {
	rc.mu.Lock()
	rc.fsSafe = on
	rc.mu.Unlock()
}

// syncFs 从持久化配置同步两个开关（每次重连后调用）。
// fs_safe 键缺省视为开启：安全目录模式是防护栏，宁保守不放开。
func (rc *relayClient) syncFs() {
	c := config.GetRelayConfig()
	safe, ok := c["fs_safe"].(bool)
	if !ok {
		safe = true
	}
	rc.mu.Lock()
	rc.fsEnabled = msg.B(c, "fs_enabled")
	rc.fsSafe = safe
	rc.mu.Unlock()
}

// fsGuard 对手机发来的路径做安全目录守卫：
// 安全模式开启时把空路径视为当前项目，并拒绝项目目录之外的一切访问；返回校验后的路径。
func (rc *relayClient) fsGuard(path string) (string, error) {
	if !rc.fsSafeOn() {
		return path, nil
	}
	ws := tools.GetWorkspace()
	if strings.TrimSpace(path) == "" {
		return ws, nil // 安全模式下"列盘符"直接落到当前项目
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	wsAbs, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}
	wsAbs = strings.TrimRight(wsAbs, `\/`)
	within := strings.EqualFold(abs, wsAbs) ||
		strings.HasPrefix(strings.ToLower(abs), strings.ToLower(wsAbs)+string(filepath.Separator))
	if !within {
		return "", fmt.Errorf("安全目录模式：仅允许访问当前项目（%s），可在电脑端关闭该限制", wsAbs)
	}
	return abs, nil
}

// fsFrame 构造带 type/rid 的响应帧。
func fsFrame(reqType string, rid any, kv ...any) map[string]any {
	out := map[string]any{"type": reqType, "rid": rid}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			out[k] = kv[i+1]
		}
	}
	return out
}

// handleFs 手机端文件浏览/编辑指令入口。
func (rc *relayClient) handleFs(a *App, conn *websocket.Conn, wmu *sync.Mutex, in map[string]any) {
	rid, t := in["rid"], msg.S(in, "type")
	if !rc.fsOK() {
		_ = writeRelayLocked(conn, wmu, fsFrame(t, rid, "error", "未开启 WEB 文件访问（电脑端「移动端远程控制」面板中打开开关）"))
		return
	}
	path, err := rc.fsGuard(msg.S(in, "path"))
	if err != nil {
		_ = writeRelayLocked(conn, wmu, fsFrame(t, rid, "error", err.Error()))
		return
	}
	switch t {
	case "fs_list":
		_ = writeRelayLocked(conn, wmu, fsList(path, rid, rc.fsSafeOn()))
	case "fs_read":
		_ = writeRelayLocked(conn, wmu, fsRead(path, rid))
	case "fs_write":
		_ = writeRelayLocked(conn, wmu, fsWrite(path, msg.S(in, "content"), rid))
	case "fs_open_desktop":
		if p := path; p != "" && a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "editor:open", p) // 桌面编辑器同步打开（CodeMirror 按类型着色）
		}
		_ = writeRelayLocked(conn, wmu, fsFrame("fs_open_desktop", rid, "ok", true))
	case "fs_rename":
		_ = writeRelayLocked(conn, wmu, fsRename(path, msg.S(in, "name"), rid))
	case "fs_delete":
		_ = writeRelayLocked(conn, wmu, fsDelete(path, rid))
	}
}

// fsRename 重命名（同目录内改名；安全模式下新旧路径都必须在项目内）。
func fsRename(path, newName string, rid any) map[string]any {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(newName) == "" {
		return fsFrame("fs_rename", rid, "error", "路径或新名称为空")
	}
	base := filepath.Base(filepath.Clean(newName)) // 只取末段，防路径穿越
	if base == "." || base == string(filepath.Separator) {
		return fsFrame("fs_rename", rid, "error", "新名称不合法")
	}
	newPath := filepath.Join(filepath.Dir(path), base)
	if _, err := os.Stat(newPath); err == nil {
		return fsFrame("fs_rename", rid, "error", "同名文件/目录已存在")
	}
	if err := os.Rename(path, newPath); err != nil {
		return fsFrame("fs_rename", rid, "error", err.Error())
	}
	return fsFrame("fs_rename", rid, "ok", true, "path", newPath)
}

// fsDelete 删除文件或空目录（非空目录拒绝，防止手机误删整棵目录树）。
func fsDelete(path string, rid any) map[string]any {
	if strings.TrimSpace(path) == "" {
		return fsFrame("fs_delete", rid, "error", "path 为空")
	}
	st, err := os.Stat(path)
	if err != nil {
		return fsFrame("fs_delete", rid, "error", err.Error())
	}
	if st.IsDir() {
		if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
			return fsFrame("fs_delete", rid, "error", "目录非空，请在电脑端删除（防止误删整棵目录）")
		}
	}
	if err := os.Remove(path); err != nil {
		return fsFrame("fs_delete", rid, "error", err.Error())
	}
	return fsFrame("fs_delete", rid, "ok", true, "path", path)
}

// ---------------- Cursor 式文件引用（refs 随 send 消息附带，内容注入给模型） ----------------

const fsRefMax = 128 << 10 // 单个引用读取上限

// expandRefs 把手机消息携带的 refs（文件引用）展开成上下文块。
// 安全规则：总开关关闭时不读取；每个路径都过安全目录守卫；最多 6 个引用、单个 128KB。
func (rc *relayClient) expandRefs(in map[string]any) string {
	list := msg.L(in, "refs")
	if len(list) == 0 || !rc.fsOK() {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, x := range list {
		if n >= 6 {
			break
		}
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		path := msg.S(m, "path")
		if path == "" {
			continue
		}
		guarded, err := rc.fsGuard(path)
		if err != nil {
			continue // 安全目录模式：项目外引用静默跳过
		}
		content := fsRefContent(guarded, int64(msg.F(m, "start")), int64(msg.F(m, "end")))
		if content == "" {
			continue
		}
		n++
		ext := strings.TrimPrefix(filepath.Ext(guarded), ".")
		fmt.Fprintf(&b, "\n\n[用户引用文件 %s]", guarded)
		if s, e := int64(msg.F(m, "start")), int64(msg.F(m, "end")); s > 0 {
			fmt.Fprintf(&b, " 第%d-%d行", s, e)
		}
		fmt.Fprintf(&b, "\n%s%s\n%s\n%s", fence4, ext, content, fence4)
	}
	return b.String()
}

const fence4 = "````"

// fsRefContent 读取文件全部或行区间内容（fsRefMax 截断）。
func fsRefContent(path string, start, end int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, fsRefMax)
	n, rerr := io.ReadFull(f, buf)
	if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
		return ""
	}
	data := strings.ReplaceAll(string(buf[:n]), "\r\n", "\n")
	lines := strings.Split(data, "\n")
	if start > 0 {
		lo := start - 1
		if lo < 0 {
			lo = 0
		}
		if end < start {
			end = start
		}
		if end > int64(len(lines)) {
			end = int64(len(lines))
		}
		if lo >= int64(len(lines)) {
			return ""
		}
		data = strings.Join(lines[lo:end], "\n")
	}
	if len(data) > fsRefMax {
		data = data[:fsRefMax] + "\n…（截断）"
	}
	return data
}

// fsList 列目录；path 为空时列盘符（Windows）或根目录（POSIX）。
// 路径边界由 fsGuard 决定：安全目录模式下空路径已是当前项目、越界路径已被拒绝。
func fsList(path string, rid any, safe bool) map[string]any {
	if strings.TrimSpace(path) == "" {
		return fsDrives(rid, safe)
	}
	// 盘符形式（如 D:）归一为根目录（D:\），否则 ReadDir 返回的是该盘当前目录
	if runtime.GOOS == "windows" && len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fsFrame("fs_list", rid, "error", err.Error())
	}
	dirs, files := []map[string]any{}, []map[string]any{}
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		if e.IsDir() {
			dirs = append(dirs, map[string]any{"name": e.Name(), "path": full})
			continue
		}
		size := int64(0)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		files = append(files, map[string]any{"name": e.Name(), "path": full, "size": size})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"].(string) < dirs[j]["name"].(string) })
	sort.Slice(files, func(i, j int) bool { return files[i]["name"].(string) < files[j]["name"].(string) })
	return fsFrame("fs_list", rid, "path", path, "dirs", dirs, "files", files, "safe", safe)
}

// fsDrives 列根：Windows 探测逻辑盘符，POSIX 列 /。
func fsDrives(rid any, safe bool) map[string]any {
	if runtime.GOOS != "windows" {
		return fsList(string(filepath.Separator), rid, safe)
	}
	dirs := []map[string]any{}
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err != nil {
			continue
		}
		dirs = append(dirs, map[string]any{"name": root, "path": root})
	}
	return fsFrame("fs_list", rid, "path", "", "dirs", dirs, "files", []map[string]any{})
}

// fsRead 读文本文件：含 NUL 判为二进制拒绝；超过 fsReadMax 截断。
func fsRead(path string, rid any) map[string]any {
	st, err := os.Stat(path)
	if err != nil {
		return fsFrame("fs_read", rid, "error", err.Error())
	}
	if st.IsDir() {
		return fsFrame("fs_read", rid, "error", "这是目录，请选择文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return fsFrame("fs_read", rid, "error", err.Error())
	}
	defer f.Close()
	buf := make([]byte, fsReadMax+1)
	n, rerr := io.ReadFull(f, buf)
	if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
		return fsFrame("fs_read", rid, "error", rerr.Error())
	}
	truncated := n > fsReadMax
	if truncated {
		n = fsReadMax
	}
	data := buf[:n]
	if bytes.IndexByte(data, 0) >= 0 {
		return fsFrame("fs_read", rid, "error", "二进制文件不支持在手机端查看")
	}
	return fsFrame("fs_read", rid,
		"path", path, "name", filepath.Base(path),
		"content", string(data), "truncated", truncated, "size", st.Size())
}

// fsWrite 写文本文件（开关开启即直接生效，不经写工具审批）。
func fsWrite(path, content string, rid any) map[string]any {
	if strings.TrimSpace(path) == "" {
		return fsFrame("fs_write", rid, "error", "path 为空")
	}
	if len(content) > fsWriteMax {
		return fsFrame("fs_write", rid, "error", "内容超过 2MB 上限，请用电脑端编辑")
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fsFrame("fs_write", rid, "error", err.Error())
	}
	return fsFrame("fs_write", rid, "ok", true, "path", path)
}

// handleDirList 手机端工作目录浏览（旧版手机页协议：只列子目录，用于下钻/新建会话）。
func (rc *relayClient) handleDirList(conn *websocket.Conn, wmu *sync.Mutex, in map[string]any) {
	rid := in["rid"]
	if !rc.fsOK() {
		_ = writeRelayLocked(conn, wmu, fsFrame("dir_list", rid, "error", "未开启 WEB 文件访问（电脑端「移动端远程控制」面板中打开开关）"))
		return
	}
	path, err := rc.fsGuard(msg.S(in, "path"))
	if err != nil {
		_ = writeRelayLocked(conn, wmu, fsFrame("dir_list", rid, "error", err.Error()))
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		_ = writeRelayLocked(conn, wmu, fsFrame("dir_list", rid, "error", err.Error()))
		return
	}
	dirs := []map[string]any{}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, map[string]any{"name": e.Name(), "path": filepath.Join(path, e.Name())})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"].(string) < dirs[j]["name"].(string) })
	_ = writeRelayLocked(conn, wmu, fsFrame("dir_list", rid, "path", path, "dirs", dirs))
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
	// Cursor 式引用：手机消息里带的 📎 文件引用（refs），此处读取内容注入给模型，
	// 手机输入框只见短引用不见源码。未开启文件开关时不自动读取。
	if ctx := rc.expandRefs(in); ctx != "" {
		text += "\n\n" + ctx
	}
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
		"branch": tools.GitBranch(tools.GetWorkspace()), "compact": a.GetCompactInfo(),
		"fs": relayC.fsOK(), "fs_safe": relayC.fsSafeOn()}
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

// RelaySetFsEnabled 设置"WEB 端文件浏览/编辑"开关：持久化 + 更新内存 + 广播手机端显隐文件面板。
func (a *App) RelaySetFsEnabled(on bool) map[string]any {
	config.SetRelayFsEnabled(on)
	relayC.setFsEnabled(on)
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "fs", "enabled": on, "safe": relayC.fsSafeOn()})
	}
	return map[string]any{"ok": true}
}

// RelaySetFsSafe 设置"安全目录模式"：开启后手机只能访问当前项目目录（持久化 + 广播）。
func (a *App) RelaySetFsSafe(on bool) map[string]any {
	config.SetRelayFsSafe(on)
	relayC.setFsSafe(on)
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "fs", "enabled": relayC.fsOK(), "safe": on})
	}
	return map[string]any{"ok": true}
}
