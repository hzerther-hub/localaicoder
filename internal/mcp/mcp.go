// Package mcp MCP 客户端（stdio 子进程 + streamable HTTP）与服务器管理器
//（对译 Python mcp.py；协议 2024-11-05）。
package mcp

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

const (
	protocolVersion = "2024-11-05"
	initTimeout     = 20 * time.Second
	callTimeout     = 120 * time.Second
)

// MCPError 传输/协议错误。
type MCPError struct{ Msg string }

func (e *MCPError) Error() string { return e.Msg }

// safeName 非法字符归一（工具名只留字母数字-_）。
var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func safeName(s string) string { return unsafeRe.ReplaceAllString(s, "_") }

// ---------------- 客户端 ----------------

type clientBase struct {
	name        string
	tools       []map[string]any
	sendMu      sync.Mutex
	onDisconnect func(error) // 进程退出时回调
}

// Start initialize 握手 → tools/list；返回工具定义列表。
func (c *clientBase) Start(t transport) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"clientInfo": map[string]any{"name": "local-ai-studio", "version": "1.0"},
		"protocolVersion": protocolVersion,
		"capabilities":   map[string]any{},
	}
	res, err := t.request("initialize", params, initTimeout)
	if err != nil {
		return err
	}
	_ = res
	if err := t.notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	res, err = t.request("tools/list", map[string]any{}, initTimeout)
	if err != nil {
		return err
	}
	c.tools = nil
	for _, tv := range msg.L(res, "tools") {
		if t2, ok := tv.(map[string]any); ok {
			c.tools = append(c.tools, t2)
		}
	}
	return nil
}

type transport interface {
	request(method string, params map[string]any, timeout time.Duration) (map[string]any, error)
	notify(method string, params map[string]any) error
	close() error
}

// CallTool 调用工具；归一化 content parts（文本拼接；图片落盘返回路径）。
func (c *clientBase) CallTool(t transport, tool string, args map[string]any) (string, []string) {
	res, err := t.request("tools/call", map[string]any{
		"name": tool, "arguments": args,
	}, callTimeout)
	if err != nil {
		return "错误：MCP 调用失败：" + err.Error(), nil
	}
	if isError := msg.B(res, "isError"); isError {
		// 工具层报错时带回真实错误文本（否则只剩笼统一句，UI 和模型都无法诊断）
		var parts []string
		for _, cv := range msg.L(res, "content") {
			if p, ok := cv.(map[string]any); ok && msg.S(p, "type") == "text" {
				if s := strings.TrimSpace(msg.S(p, "text")); s != "" {
					parts = append(parts, s)
				}
			}
		}
		detail := strings.Join(parts, "; ")
		if detail == "" {
			return "错误：MCP 工具返回错误", nil
		}
		if len(detail) > 500 {
			detail = detail[:500] + "…"
		}
		return "错误：MCP 工具返回错误：" + detail, nil
	}
	var textParts []string
	var media []string
	for _, cv := range msg.L(res, "content") {
		part, ok := cv.(map[string]any)
		if !ok {
			continue
		}
		switch msg.S(part, "type") {
		case "text":
			textParts = append(textParts, msg.S(part, "text"))
		case "image":
			if p := saveImageBase64(msg.S(part, "data"), msg.S(part, "mimeType")); p != "" {
				media = append(media, p)
			}
		case "resource":
			if uri := msg.S(part, "uri"); strings.HasPrefix(uri, "data:image") {
				if p := saveImageURI(uri); p != "" {
					media = append(media, p)
				}
			} else if t2 := msg.S(part, "text"); t2 != "" {
				textParts = append(textParts, t2)
			}
		}
	}
	return strings.Join(textParts, "\n"), media
}

func saveImageBase64(data, mime string) string {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ""
	}
	ext := ".png"
	if e := mimeExt(mime); e != "" {
		ext = e
	}
	return saveMedia(raw, ext)
}

func saveImageURI(uri string) string {
	// data:image/png;base64,xxxx
	i := strings.Index(uri, ";base64,")
	if i < 0 {
		return ""
	}
	mime := strings.TrimPrefix(uri[:i], "data:")
	raw, err := base64.StdEncoding.DecodeString(uri[i+len(";base64,"):])
	if err != nil {
		return ""
	}
	ext := ".png"
	if e := mimeExt(mime); e != "" {
		ext = e
	}
	return saveMedia(raw, ext)
}

func mimeExt(mime string) string {
	switch strings.ToLower(strings.Split(mime, ";")[0]) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/png":
		return ".png"
	}
	return ""
}

var mediaMu sync.Mutex

func saveMedia(raw []byte, ext string) string {
	dir := config.MediaDir()
	_ = os.MkdirAll(dir, 0o755)
	h := hashName(raw)
	p := filepath.Join(dir, h+ext)
	mediaMu.Lock()
	defer mediaMu.Unlock()
	if _, err := os.Stat(p); err != nil {
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			return ""
		}
	}
	return p
}

// ---------------- stdio 传输 ----------------

type StdioClient struct {
	clientBase
	proc    *exec.Cmd
	stdin   io.WriteCloser
	errFile *os.File // MCP 进程 stderr 日志
	mu      sync.Mutex
	pending map[int64]chan map[string]any
	nextID  int64
}

// NewStdioClient 创建 stdio 子进程客户端。
func NewStdioClient(name string, command string, args []string, env []string, cwd string) (*StdioClient, error) {
	c := &StdioClient{
		clientBase: clientBase{name: name},
		pending:    map[int64]chan map[string]any{},
	}
	c.proc = exec.Command(command, args...)
	if len(env) > 0 {
		c.proc.Env = append(os.Environ(), env...)
	}
	if cwd != "" {
		c.proc.Dir = cwd
	}
	stdin, err := c.proc.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := c.proc.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// 捕获 MCP 进程的 stderr 到日志文件（方便诊断）
	logDir := config.Dir()
	if logDir == "" {
		logDir = filepath.Join(os.TempDir(), "localai-mcp-logs")
	}
	var errFile *os.File
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("[mcp] 创建日志目录失败：%v", err)
	} else {
		var err2 error
		errFile, err2 = os.OpenFile(filepath.Join(logDir, "mcp_"+name+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err2 == nil {
			c.proc.Stderr = errFile
			c.errFile = errFile
			log.Printf("[mcp] 日志写入 %s", errFile.Name())
		} else {
			log.Printf("[mcp] 创建日志文件失败：%v", err2)
		}
	}
	if err := c.proc.Start(); err != nil {
		return nil, err
	}
	c.stdin = stdin
	go c.readLoop(stdout)
	go c.watchProcessExit() // 监控进程退出
	return c, nil
}

// readLoop 读器 goroutine：按行读 JSON，按 id 分发响应。
func (c *StdioClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 32<<20), 32<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		id := int64(msg.F(m, "id"))
		if id == 0 {
			continue // 通知：stdio 客户端不处理服务器推送
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- m
		}
	}
}

// watchProcessExit 监控 MCP 进程退出，进程退出后关闭连接并通知上层。
func (c *StdioClient) watchProcessExit() {
	err := c.proc.Wait()
	if c.errFile != nil {
		c.errFile.Close()
		c.errFile = nil
	}
	log.Printf("[mcp] %s 进程退出：code=%v err=%v", c.name, c.proc.ProcessState, err)
	// 通知上层连接断开
	if c.onDisconnect != nil {
		c.onDisconnect(fmt.Errorf("MCP 进程 %s 已退出", c.name))
	}
}

func (c *StdioClient) write(obj map[string]any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	b, _ := json.Marshal(obj)
	_, err := c.stdin.Write(append(b, '\n'))
	return err
}

func (c *StdioClient) request(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, &MCPError{Msg: "连接失败: " + err.Error()}
	}
	select {
	case m := <-ch:
		if e := msg.M(m, "error"); e != nil {
			return nil, &MCPError{Msg: fmt.Sprintf("JSON-RPC 错误: %s (code=%d)",
				msg.S(e, "message"), msg.I(e, "code"))}
		}
		if res, ok := m["result"].(map[string]any); ok {
			return res, nil
		}
		return map[string]any{}, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, &MCPError{Msg: "请求超时: " + method}
	}
}

func (c *StdioClient) notify(method string, params map[string]any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *StdioClient) close() error {
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
	}
	return nil
}

// Start 握手 + 工具发现。
func (c *StdioClient) Start() error { return c.clientBase.Start(c) }

// CallTool 调用工具。
func (c *StdioClient) CallTool(tool string, args map[string]any) (string, []string) {
	return c.clientBase.CallTool(c, tool, args)
}

// Close 关闭。
func (c *StdioClient) Close() { _ = c.close() }

// ---------------- streamable HTTP 传输 ----------------

type HTTPClient struct {
	clientBase
	url       string
	headers   map[string]string
	sessionID string
}

// NewHTTPClient 创建 streamable HTTP 客户端。
func NewHTTPClient(name, url string, headers map[string]string) *HTTPClient {
	return &HTTPClient{clientBase: clientBase{name: name}, url: url, headers: headers}
}

func (c *HTTPClient) post(body map[string]any, timeout time.Duration) ([]map[string]any, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, &MCPError{Msg: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &MCPError{Msg: "连接失败: " + err.Error()}
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &MCPError{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b))}
	}
	ct := resp.Header.Get("Content-Type")
	var messages []map[string]any
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 32<<20), 32<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &m) == nil {
				messages = append(messages, m)
			}
		}
	} else {
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err == nil {
			messages = append(messages, m)
		}
	}
	return messages, nil
}

func (c *HTTPClient) request(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	id := time.Now().UnixNano()
	msgs, err := c.post(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}, timeout)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if int64(msg.F(m, "id")) == id {
			if e := msg.M(m, "error"); e != nil {
				return nil, &MCPError{Msg: fmt.Sprintf("JSON-RPC 错误: %s (code=%d)",
					msg.S(e, "message"), msg.I(e, "code"))}
			}
			if res, ok := m["result"].(map[string]any); ok {
				return res, nil
			}
			return map[string]any{}, nil
		}
	}
	return nil, &MCPError{Msg: "响应中没有匹配的 id: " + method}
}

func (c *HTTPClient) notify(method string, params map[string]any) error {
	_, err := c.post(map[string]any{
		"jsonrpc": "2.0", "method": method, "params": params,
	}, 30*time.Second)
	return err
}

func (c *HTTPClient) close() error {
	if c.sessionID == "" {
		return nil
	}
	req, err := http.NewRequest("DELETE", c.url, nil)
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", c.sessionID)
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Start 握手 + 工具发现。
func (c *HTTPClient) Start() error { return c.clientBase.Start(c) }

// CallTool 调用工具。
func (c *HTTPClient) CallTool(tool string, args map[string]any) (string, []string) {
	return c.clientBase.CallTool(c, tool, args)
}

// Close 释放会话。
func (c *HTTPClient) Close() { _ = c.close() }

// ---------------- 管理器 ----------------

type mcpClient interface {
	Start() error
	CallTool(tool string, args map[string]any) (string, []string)
	Close()
}

// Manager 管理全部已连接的 MCP 服务器。
type Manager struct {
	mu         sync.RWMutex
	clients    map[string]mcpClient // server name
	toolMap    map[string]mcpTool   // "mcp_<server>_<tool>" → 目标
	readonly   map[string]bool      // server name
	connected  bool
}

type mcpTool struct {
	server string
	tool   string
}

// Connected 是否已连接。
func (m *Manager) Connected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

// IsServerConnected 检查指定服务器是否已连接。
func (m *Manager) IsServerConnected(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.clients[name]
	return ok
}

// DisconnectServer 断开指定服务器。
func (m *Manager) DisconnectServer(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[name]; ok {
		c.Close()
		delete(m.clients, name)
		// 清理该服务器的 toolMap 条目
		for k, v := range m.toolMap {
			if v.server == name {
				delete(m.toolMap, k)
			}
		}
	}
}

// ToolMap 工具名映射（agent 用 len 判断）。
func (m *Manager) ToolMapLen() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.toolMap)
}

// Connect 读 mcp.json 并启动所有 enabled 服务器；onLog 可选日志回调。
func (m *Manager) Connect(onLog func(string)) {
	m.mu.Lock()
	m.clients = map[string]mcpClient{}
	m.toolMap = map[string]mcpTool{}
	m.readonly = map[string]bool{}
	m.connected = false
	m.mu.Unlock()

	data := config.LoadMCPServers()
	servers, _ := data["servers"].(map[string]any)
	for name, sv := range servers {
		cfg, ok := sv.(map[string]any)
		if !ok || !msg.B(cfg, "enabled") {
			continue
		}
		var c mcpClient
		var err error
		if u := msg.S(cfg, "url"); u != "" {
			hdrs := map[string]string{}
			for k, v := range msg.M(cfg, "headers") {
				if s, ok := v.(string); ok {
					hdrs[k] = s
				}
			}
			c = NewHTTPClient(name, u, hdrs)
		} else if cmd := msg.S(cfg, "command"); cmd != "" {
			var args []string
			for _, a := range msg.L(cfg, "args") {
				if s, ok := a.(string); ok {
					args = append(args, s)
				}
			}
			var env []string
			for k, v := range msg.M(cfg, "env") {
				if s, ok := v.(string); ok {
					env = append(env, k+"="+s)
				}
			}
			c, err = NewStdioClient(name, cmd, args, env, msg.S(cfg, "cwd"))
			if err == nil {
				if sc, ok := c.(*StdioClient); ok && onLog != nil {
					sc.onDisconnect = func(err error) {
						m.mu.Lock()
						delete(m.clients, name)
						delete(m.toolMap, "mcp_"+safeName(name)+"_")
						m.connected = len(m.clients) > 0
						m.mu.Unlock()
						onLog(fmt.Sprintf("MCP %s 已断开：%v", name, err))
					}
				}
			}
		} else {
			continue
		}
		if onLog != nil {
			onLog(fmt.Sprintf("连接 MCP 服务器 %s ...", name))
		}
		if err == nil {
			err = c.Start()
		}
		if err != nil {
			if onLog != nil {
				onLog(fmt.Sprintf("MCP %s 连接失败: %v", name, err))
			}
			c.Close()
			continue
		}
		m.mu.Lock()
		m.clients[name] = c
		m.readonly[name] = msg.B(cfg, "readonly")
		m.mu.Unlock()
		if onLog != nil {
			onLog(fmt.Sprintf("MCP %s 已连接", name))
		}
	}
	// 构建 tool_map
	m.mu.Lock()
	for name, c := range m.clients {
		base := c.(interface{ Tools() []map[string]any })
		for _, t := range base.Tools() {
			toolName := msg.S(t, "name")
			if toolName == "" {
				continue
			}
			m.toolMap["mcp_"+safeName(name)+"_"+safeName(toolName)] = mcpTool{name, toolName}
		}
	}
	m.connected = len(m.clients) > 0
	m.mu.Unlock()
}

// Tools 暴露原始工具定义（Connect 内部用）。
func (c *StdioClient) Tools() []map[string]any { return c.tools }

// Tools 暴露原始工具定义。
func (c *HTTPClient) Tools() []map[string]any { return c.tools }

// ToolSchemas 转成 OpenAI function schema（带 [MCP:<server>] 前缀）。
func (m *Manager) ToolSchemas() []map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []map[string]any
	for name, c := range m.clients {
		base := c.(interface{ Tools() []map[string]any })
		for _, t := range base.Tools() {
			desc := msg.S(t, "description")
			if len(desc) > 4096 {
				desc = desc[:4096]
			}
			input := t["inputSchema"]
			if input == nil {
				input = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "mcp_" + safeName(name) + "_" + safeName(msg.S(t, "name")),
					"description": "[MCP:" + name + "] " + desc,
					"parameters":  input,
				},
			})
		}
	}
	return out
}

// IsWriteTool 工具是否视为可写（服务器未标记 readonly 即可写）。
func (m *Manager) IsWriteTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.toolMap[name]
	if !ok {
		return false
	}
	return !m.readonly[t.server]
}

// FindTool 在指定 server 的工具里按候选真实工具名挑第一个存在的，返回完整调用名
// （mcp_<server>_<tool>）；都没有返回空串。不同 MCP 包对同类工具命名不同
// （如 chrome-devtools 的 take_screenshot 与 playwright 的 browser_take_screenshot）。
func (m *Manager) FindTool(server string, candidates ...string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, cand := range candidates {
		for k, t := range m.toolMap {
			if t.server == server && t.tool == cand {
				return k
			}
		}
	}
	return ""
}

// Call 路由调用；错误作为文本返回（循环不中断）。
func (m *Manager) Call(name string, args map[string]any) (string, []string) {
	m.mu.RLock()
	t, ok := m.toolMap[name]
	c := m.clients[t.server]
	m.mu.RUnlock()
	if !ok || c == nil {
		return "错误：未知 MCP 工具 " + name, nil
	}
	text, media := c.CallTool(t.tool, args)
	return text, media
}

// StopAll 断开全部服务器。
func (m *Manager) StopAll() {
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]mcpClient{}
	m.toolMap = map[string]mcpTool{}
	m.connected = false
	m.mu.Unlock()
	for _, c := range clients {
		c.Close()
	}
}

// ---------------- 单例 ----------------

var (
	mgrOnce sync.Once
	mgr     *Manager
)

// GetManager 惰性单例。
func GetManager() *Manager {
	mgrOnce.Do(func() { mgr = &Manager{} })
	return mgr
}

// ResetManager 断开并重置单例（测试/重连用）。
func ResetManager() {
	if mgr != nil {
		mgr.StopAll()
	}
	mgrOnce = sync.Once{}
	mgr = nil
}
