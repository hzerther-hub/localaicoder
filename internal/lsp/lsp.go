// Package lsp 轻量 LSP 客户端（JSON-RPC over stdio）——多语言智能提示。
// 对译 Python lsp.py；读取器用 goroutine + channel 替代 select()
//（select() 在 Windows 不能用于管道，Go 方案天然跨平台）。
//
// 提供三块能力：
//  1. 编辑器「真语义」补全 / 签名提示（按文件扩展名选语言服务器）；
//  2. 诊断收集：编辑器底栏 ✗/⚠ 计数 + lsp_diagnostics 工具；
//  3. 启动预热：探测工作区主要语言，提前拉起服务器。
//
// 支持语言（服务器存在才启用，缺失自动跳过）：python/js/ts/vue/html/css/
// json/go/rust/c/cpp/java/cs/ruby/kotlin/swift/php/dart/bash/erlang/yaml，
// 另补充 lua/zig/haskell（参考 DeepSeek-Reasonix 的 DefaultSpecs）。
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

// servers 语言 -> 服务器命令（首元素为可执行名，PATH 上缺失则该语言不可用）。
var servers = map[string][]string{
	"python": {"pyright-langserver", "--stdio"},
	"js":     {"typescript-language-server", "--stdio"},
	"ts":     {"typescript-language-server", "--stdio"},
	"vue":    {"typescript-language-server", "--stdio"},
	"html":   {"vscode-html-language-server", "--stdio"},
	"css":    {"vscode-css-language-server", "--stdio"},
	"json":   {"vscode-json-languageserver", "--stdio"},
	"go":     {"gopls"},
	"rust":   {"rust-analyzer"},
	"c":      {"clangd"},
	"cpp":    {"clangd"},
	"java":   {"jdtls"},
	"cs":     {"OmniSharp", "-lsp"},
	"ruby":   {"solargraph", "stdio"},
	"kotlin": {"kotlin-language-server"},
	"swift":  {"sourcekit-lsp"},
	"dart":   {"dart", "language-server"},
	"php":    {"intelephense", "--stdio"},
	"bash":   {"bash-language-server", "start"},
	"sh":     {"bash-language-server", "start"},
	"erlang": {"erlang_ls"},
	"yaml":   {"yaml-language-server", "--stdio"},
	// 参考 DeepSeek-Reasonix 补充
	"lua":     {"lua-language-server"},
	"zig":     {"zls"},
	"haskell": {"haskell-language-server-wrapper", "--lsp"},
}

var serversMu sync.RWMutex

// RegisterServer 注册/覆盖一个语言的服务器命令（运行时扩展用）。
func RegisterServer(lang string, cmd ...string) {
	serversMu.Lock()
	servers[lang] = cmd
	serversMu.Unlock()
}

// autoInstallPkgs 语言 → (安装方式, 包名)。npm 系装到应用自包含目录
//（CONFIG_DIR/lsp，Reasonix 式：不依赖系统全局安装），go 用 GOBIN 同理。
var autoInstallPkgs = map[string][2]string{
	"python": {"npm", "pyright"},
	"js":     {"npm", "typescript typescript-language-server"},
	"ts":     {"npm", "typescript typescript-language-server"},
	"vue":    {"npm", "typescript typescript-language-server"},
	"html":   {"npm", "vscode-langservers-extracted"},
	"css":    {"npm", "vscode-langservers-extracted"},
	"json":   {"npm", "vscode-langservers-extracted"},
	"go":     {"go", "golang.org/x/tools/gopls@latest"},
	"ruby":   {"gem", "solargraph"},
	"bash":   {"npm", "bash-language-server"},
	"sh":     {"npm", "bash-language-server"},
	"yaml":   {"npm", "yaml-language-server"},
	"php":    {"npm", "intelephense"},
}

// InstallSpec 一条结构化安装指令（参数数组+工作目录，避免 shell 引号/路径转换问题）。
type InstallSpec struct {
	Argv    []string
	WorkDir string // 非空则设置 c.Dir；npm/go 用应用 lsp 目录
}

// InstallSpecFor 返回语言的安装规范；ok=false 表示不支持自动安装。
func InstallSpecFor(lang string) (InstallSpec, bool) {
	pk, ok := autoInstallPkgs[lang]
	if !ok {
		return InstallSpec{}, false
	}
	dir := ServersDir()
	switch pk[0] {
	case "npm":
		return InstallSpec{Argv: []string{"npm", "install", "--prefix", dir, pk[1]}, WorkDir: dir}, true
	case "go":
		return InstallSpec{Argv: []string{"go", "install", "golang.org/x/tools/gopls@latest"}, WorkDir: dir}, true
	}
	return InstallSpec{Argv: []string{pk[0], "install", pk[1]}, WorkDir: dir}, true
}

// InstallCmd 返回显示用的命令文本。
func InstallCmd(lang string) string {
	spec, ok := InstallSpecFor(lang)
	if !ok {
		return ""
	}
	return strings.Join(spec.Argv, " ")
}

var langID = map[string]string{
	"python": "python", "js": "javascript", "ts": "typescript", "vue": "vue",
	"html": "html", "css": "css", "json": "json", "go": "go", "rust": "rust",
	"c": "c", "cpp": "cpp", "java": "java", "cs": "csharp", "ruby": "ruby",
	"kotlin": "kotlin", "swift": "swift", "dart": "dart", "php": "php",
	"sh": "shellscript", "bash": "shellscript", "erlang": "erlang",
	"yaml": "yaml", "lua": "lua", "zig": "zig", "haskell": "haskell",
}

// extLang 后缀 -> 语言（多语言探测 + 单文件识别共用）。
var extLang = map[string]string{
	".py": "python", ".pyw": "python", ".pyi": "python",
	".js": "js", ".mjs": "js", ".cjs": "js", ".jsx": "js",
	".ts": "ts", ".tsx": "ts", ".mts": "ts",
	".vue": "vue", ".html": "html", ".htm": "html",
	".css": "css", ".scss": "css", ".less": "css",
	".json": "json", ".jsonc": "json",
	".go": "go", ".rs": "rust",
	".c": "c", ".h": "cpp", ".cpp": "cpp", ".cc": "cpp", ".cxx": "cpp",
	".hpp": "cpp", ".hh": "cpp",
	".java": "java", ".cs": "cs", ".rb": "ruby",
	".kt": "kotlin", ".kts": "kotlin", ".swift": "swift",
	".dart": "dart", ".php": "php",
	".sh": "sh", ".bash": "sh", ".erl": "erlang", ".hrl": "erlang",
	".escript": "erlang",
	".yaml":    "yaml", ".yml": "yaml",
	".lua": "lua", ".zig": "zig", ".hs": "haskell",
}

// probeSkipDirs 工作区语言探测时跳过的目录（生成物/依赖）。
var probeSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	"__pycache__": true, "venv": true, ".venv": true, "env": true,
	".tox": true, ".mypy_cache": true, ".pytest_cache": true, "dist": true,
	"build": true, "target": true, ".idea": true, ".vscode": true,
	".next": true, ".nuxt": true, "vendor": true, "bower_components": true,
	"media": true, ".cache": true, "coverage": true, "out": true,
	"bin": true, "obj": true,
}

// sevMark 诊断严重级别 -> 标记（LSP: 1=Error 2=Warning 3=Info 4=Hint）。
var sevMark = map[int]string{1: "✗", 2: "⚠", 3: "ℹ", 4: "·"}

// AvailableFor 该语言是否有可执行的 LSP 服务器（自包含目录优先，其次系统 PATH）。
func AvailableFor(lang string) bool {
	serversMu.RLock()
	cmd := servers[lang]
	serversMu.RUnlock()
	return len(cmd) > 0 && lookupServerPath(cmd[0]) != ""
}

// LanguageOf 文件路径 -> 语言标识（无对应语言返回空串）。
func LanguageOf(path string) string {
	return extLang[strings.ToLower(filepath.Ext(path))]
}

// LangIDOf 内部语言键 -> LSP languageId。
func LangIDOf(lang string) string {
	if id, ok := langID[lang]; ok {
		return id
	}
	return "plaintext"
}

// ServerName 返回语言对应的 LSP 服务器可执行名（用于界面提示）。
func ServerName(lang string) string {
	serversMu.RLock()
	defer serversMu.RUnlock()
	if cmd := servers[lang]; len(cmd) > 0 {
		return cmd[0]
	}
	return ""
}

// ServersDir 应用自包含 LSP 服务器目录（CONFIG_DIR/lsp）——
// 与 DeepSeek-Reasonix 同思路：语言服务器随应用安装，不依赖系统 PATH。
func ServersDir() string { return filepath.Join(config.Dir(), "lsp") }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// lookupServerPath 解析服务器可执行文件：①应用 lsp 目录（npm --prefix 装的
// node_modules/.bin shim）②lsp/bin（GOBIN）③系统 PATH。返回空串=未找到。
func lookupServerPath(name string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		if fileExists(name) {
			return name
		}
		return ""
	}
	bin := filepath.Join(ServersDir(), "node_modules", ".bin")
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".cmd", ".exe", ""} {
			cand := filepath.Join(bin, name+ext)
			if fileExists(cand) {
				return cand
			}
		}
	} else if cand := filepath.Join(bin, name); fileExists(cand) {
		return cand
	}
	if cand := filepath.Join(ServersDir(), "bin", name+binExe()); fileExists(cand) {
		return cand
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func binExe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// ProbeWorkspace 探测工作区开发语言：按源码文件数降序返回语言键列表。
// 只统计有可用语言服务器的语言；扫描上限 maxFiles 个文件防大仓库卡顿。
func ProbeWorkspace(ws string, maxFiles int) []string {
	if ws == "" {
		return nil
	}
	if st, err := os.Stat(ws); err != nil || !st.IsDir() {
		return nil
	}
	counts := map[string]int{}
	n := 0
	_ = filepath.Walk(ws, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if probeSkipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		if n > maxFiles {
			return filepath.SkipAll
		}
		if lang := extLang[strings.ToLower(filepath.Ext(info.Name()))]; lang != "" {
			counts[lang]++
		}
		return nil
	})
	var langs []string
	for l := range counts {
		if AvailableFor(l) {
			langs = append(langs, l)
		}
	}
	sort.Slice(langs, func(a, b int) bool { return counts[langs[a]] > counts[langs[b]] })
	return langs
}

// Warm 启动预热一个语言服务器（initialize 即可，不绑定文件）。
// 打开文件首次补全不再等服务器冷启动（pyright/node 类启动最慢）。
func Warm(lang, workspace string) bool {
	if !AvailableFor(lang) {
		return false
	}
	serversMu.RLock()
	cmd := append([]string(nil), servers[lang]...)
	serversMu.RUnlock()
	c, err := newClient(cmd, workspace, "", lang)
	if err != nil {
		return false
	}
	if err := c.Start(); err != nil {
		c.Close()
		return false
	}
	c.Close()
	return true
}

// ---------------- 客户端 ----------------

// Client 一个 LSP 服务器会话。
type Client struct {
	path        string // 绑定的文件（空 = 预热模式）
	workspace   string
	uri         string
	langid      string
	cmd         []string
	proc        *exec.Cmd
	stdin       io.WriteCloser
	msgs        chan map[string]any
	mu          sync.Mutex
	started     bool
	id          int
	diagnostics []any // 最新一次 publishDiagnostics 结果
}

func newClient(cmd []string, workspace, path, lang string) (*Client, error) {
	c := &Client{cmd: cmd, workspace: workspace, path: path}
	if path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		c.path = abs
		if workspace == "" {
			c.workspace = filepath.Dir(abs)
		}
		c.uri = "file://" + abs
		c.langid = LangIDOf(lang)
		if lang == "" {
			c.langid = LangIDOf(LanguageOf(path))
		}
	} else {
		if workspace == "" {
			wd, _ := os.Getwd()
			c.workspace = wd
		}
		abs, _ := filepath.Abs(c.workspace)
		c.uri = "file://" + abs
		c.langid = LangIDOf(lang)
	}
	if c.workspace == "" {
		c.workspace, _ = os.Getwd()
	}
	return c, nil
}

// NewClientForFile 为文件创建客户端；语言不可用/服务器缺失返回错误。
func NewClientForFile(path string) (*Client, error) {
	lang := LanguageOf(path)
	if lang == "" {
		return nil, fmt.Errorf("无对应语言: %s", path)
	}
	serversMu.RLock()
	cmd := servers[lang]
	serversMu.RUnlock()
	if len(cmd) == 0 || lookupServerPath(cmd[0]) == "" {
		return nil, fmt.Errorf("语言 %s 未安装 LSP 服务器 %s", lang, cmd[0])
	}
	return newClient(append([]string(nil), cmd...), filepath.Dir(path), path, lang)
}

// Start 拉起服务器进程并完成 initialize 握手。
func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	exe := lookupServerPath(c.cmd[0])
	var proc *exec.Cmd
	if strings.HasSuffix(strings.ToLower(exe), ".cmd") || strings.HasSuffix(strings.ToLower(exe), ".bat") {
		args := append([]string{"/c", exe}, c.cmd[1:]...)
		proc = exec.Command("cmd", args...)
	} else {
		proc = exec.Command(exe, c.cmd[1:]...)
	}
	stdin, err := proc.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return err
	}
	proc.Stderr = nil
	if err := proc.Start(); err != nil {
		return err
	}
	c.proc = proc
	c.stdin = stdin
	c.msgs = make(chan map[string]any, 64)
	go c.readLoop(stdout)

	absWS, _ := filepath.Abs(c.workspace)
	init := map[string]any{
		"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{
			"processId":    os.Getpid(),
			"rootUri":      "file://" + absWS,
			"capabilities": map[string]any{},
		},
	}
	c.id = 1
	c.send(init)
	c.read(5 * time.Second) // 等 initialize 响应（冷启动可慢）
	c.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if c.path != "" {
		c.didOpenLocked(readFileOrEmpty(c.path))
	}
	c.started = true
	return nil
}

// readLoop 读取器 goroutine：解析 Content-Length 帧，通知随读随存。
func (c *Client) readLoop(r io.Reader) {
	defer close(c.msgs)
	reader := bufio.NewReaderSize(r, 1<<20)
	for {
		headers := map[string]string{}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if i := strings.Index(line, ":"); i > 0 {
				headers[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
			}
		}
		n := 0
		fmt.Sscanf(headers["content-length"], "%d", &n)
		if n <= 0 {
			continue
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			c.takeNotice(m)
			c.msgs <- m
		}
	}
}

// takeNotice 处理服务器推送的通知（诊断等）。
func (c *Client) takeNotice(m map[string]any) {
	if msg.S(m, "method") != "textDocument/publishDiagnostics" {
		return
	}
	params, _ := m["params"].(map[string]any)
	if params == nil {
		return
	}
	uri := msg.S(params, "uri")
	if c.path == "" || uri == c.uri {
		if items := msg.L(params, "items"); items != nil {
			c.diagnostics = items
		} else {
			c.diagnostics = nil
		}
	}
}

func (c *Client) send(obj map[string]any) {
	if c.stdin == nil {
		return
	}
	payload, _ := json.Marshal(obj)
	fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(payload))
	_, _ = c.stdin.Write(payload)
	_, _ = c.stdin.Write([]byte("\n"))
}

// read 读一条消息；超时/EOF 返回 nil。
func (c *Client) read(timeout time.Duration) map[string]any {
	if c.msgs == nil {
		return nil
	}
	select {
	case m, ok := <-c.msgs:
		if !ok {
			return nil
		}
		return m
	case <-time.After(timeout):
		return nil
	}
}

func readFileOrEmpty(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// DidOpen 打开文件（LSP 必须先 didOpen 才能补全）。
func (c *Client) DidOpen(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.didOpenLocked(text)
}

func (c *Client) didOpenLocked(text string) {
	c.send(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri": c.uri, "languageId": c.langid, "version": 1, "text": text,
			},
		},
	})
	c.read(600 * time.Millisecond) // 吞掉发布诊断等通知
}

// DidChange 全量同步当前内容。
func (c *Client) DidChange(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": c.uri, "version": 2},
			"contentChanges": []any{map[string]any{"text": text}},
		},
	})
	c.read(400 * time.Millisecond)
}

// ensureStarted 惰性启动（Start 自带锁，先查后启避免重入死锁）。
func (c *Client) ensureStarted() error {
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if started {
		return nil
	}
	return c.Start()
}

func (c *Client) request(method string, params map[string]any, timeout time.Duration) any {
	if err := c.ensureStarted(); err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	rid := c.id
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "id": rid, "params": params})
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m := c.read(timeout)
		if m == nil {
			break
		}
		if msg.F(m, "id") == float64(rid) {
			return m["result"]
		}
	}
	return nil
}

// CompletionItem 补全项。
type CompletionItem struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// Complete 语义补全；返回归一化的 [{label, detail}]。
func (c *Client) Complete(line, char int) []CompletionItem {
	result := c.request("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": c.uri},
		"position":     map[string]any{"line": line, "character": char},
	}, 2500*time.Millisecond)
	var items []any
	switch r := result.(type) {
	case map[string]any: // CompletionList
		items = msg.L(r, "items")
	case []any: // 直接返回数组
		items = r
	}
	var out []CompletionItem
	for _, iv := range items {
		it, ok := iv.(map[string]any)
		if !ok {
			continue
		}
		detail := msg.S(it, "detail")
		if detail == "" {
			if d, ok := it["documentation"].(string); ok {
				detail = strings.Join(strings.Fields(d), " ")
			}
		}
		out = append(out, CompletionItem{
			Label: msg.S(it, "label"), Detail: strings.TrimSpace(detail),
		})
	}
	return out
}

// Signature 签名提示；返回首个签名 label，无则空。
func (c *Client) Signature(line, char int) string {
	result := c.request("textDocument/signatureHelp", map[string]any{
		"textDocument": map[string]any{"uri": c.uri},
		"position":     map[string]any{"line": line, "character": char},
	}, 2*time.Second)
	rmap, _ := result.(map[string]any)
	if sigs := msg.L(rmap, "signatures"); len(sigs) > 0 {
		if s, ok := sigs[0].(map[string]any); ok {
			return msg.S(s, "label")
		}
	}
	return ""
}

// Diag 同步最新内容并拉取诊断。返回 [{line, mark, msg}]（行号从 1 起）。
func (c *Client) Diag(text string, wait time.Duration) []DiagItem {
	if err := c.ensureStarted(); err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didChange",
		"params": map[string]any{
			"textDocument":   map[string]any{"uri": c.uri, "version": 2},
			"contentChanges": []any{map[string]any{"text": text}},
		},
	})
	c.diagnostics = nil
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if c.read(200*time.Millisecond) == nil && c.diagnostics != nil {
			break // 已收到且通道静默 → 收工
		}
	}
	return FormatDiags(c.diagnostics)
}

// Close 关闭会话（shutdown + kill）。
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "method": "shutdown", "params": nil})
	c.read(500 * time.Millisecond)
	if c.proc != nil && c.proc.Process != nil {
		_ = c.proc.Process.Kill()
	}
	c.started = false
}

// DiagItem 紧凑诊断项。
type DiagItem struct {
	Line int    `json:"line"`
	Mark string `json:"mark"`
	Msg  string `json:"msg"`
}

// FormatDiags LSP 诊断项 -> 紧凑列表（纯函数，便于测试）。
func FormatDiags(items []any) []DiagItem {
	var out []DiagItem
	for _, iv := range items {
		it, ok := iv.(map[string]any)
		if !ok || msg.S(it, "message") == "" {
			continue
		}
		line := 1
		if r := msg.M(it, "range"); r != nil {
			if pos := msg.M(r, "start"); pos != nil {
				line = msg.I(pos, "line") + 1
			}
		}
		sev := msg.I(it, "severity")
		if sev == 0 {
			sev = 3
		}
		mark := sevMark[sev]
		if mark == "" {
			mark = "ℹ"
		}
		m := strings.TrimSpace(msg.S(it, "message"))
		if i := strings.IndexByte(m, '\n'); i >= 0 {
			m = m[:i]
		}
		if len(m) > 200 {
			m = m[:200]
		}
		out = append(out, DiagItem{Line: line, Mark: mark, Msg: m})
	}
	return out
}

// DiagnosticsForFile 一次性检查文件（工具 lsp_diagnostics 用：
// 创建客户端 → didOpen+diag → 关闭）。
func DiagnosticsForFile(path, content string, waitSec float64) ([]DiagItem, error) {
	c, err := NewClientForFile(path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.Diag(content, time.Duration(waitSec*float64(time.Second))), nil
}

// ---------------- 编辑器会话管理（按文件缓存客户端） ----------------

var (
	editorMu   sync.Mutex
	editorClis = map[string]*Client{} // path -> client
)

// EditorClient 取/建某文件的持久客户端（编辑器补全/诊断复用，避免每次冷启动）。
func EditorClient(path string) (*Client, error) {
	editorMu.Lock()
	defer editorMu.Unlock()
	if c, ok := editorClis[path]; ok {
		return c, nil
	}
	c, err := NewClientForFile(path)
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	editorClis[path] = c
	return c, nil
}

// CloseEditor 关闭某文件的客户端。
func CloseEditor(path string) {
	editorMu.Lock()
	c := editorClis[path]
	delete(editorClis, path)
	editorMu.Unlock()
	if c != nil {
		c.Close()
	}
}

// CloseAllEditors 关闭全部编辑器客户端（应用退出时）。
func CloseAllEditors() {
	editorMu.Lock()
	clis := make([]*Client, 0, len(editorClis))
	for _, c := range editorClis {
		clis = append(clis, c)
	}
	editorClis = map[string]*Client{}
	editorMu.Unlock()
	for _, c := range clis {
		c.Close()
	}
}
