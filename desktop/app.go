// desktop App：暴露给前端（window.go.main.App）的绑定方法集合。
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/agent"
	"localai/internal/cache"
	"localai/internal/codeindex"
	"localai/internal/codera"
	"localai/internal/config"
	"localai/internal/llm"
	"localai/internal/localmodels"
	"localai/internal/lsp"
	"localai/internal/msg"
	"localai/internal/mcp"
	"localai/internal/products"
	"localai/internal/sessions"
	"localai/internal/tools"
)

// App 桌面应用状态与绑定方法。
type App struct {
	ctx        context.Context
	mu         sync.Mutex
	runner     *RunManager
	terms      *TerminalManager
	sessionID  string
	modelKey   string
	mode       string
	mcpStarted bool
}

// NewApp 创建应用实例。
func NewApp() *App {
	return &App{
		sessionID: sessions.NewID(),
		mode:      agent.ModeAlways,
		terms:     NewTerminalManager(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.terms.SetCtx(ctx)
	a.runner = NewRunManager(ctx)
	tools.SetWorkspace(config.LoadLastWorkspace())
	models, def := config.LoadModels()
	if len(models) > 0 {
		a.modelKey = def
		if a.modelKey == "" {
			a.modelKey = models[0].Key
		}
	}
}

func (a *App) domReady(ctx context.Context) {
	if !a.mcpStarted {
		a.mcpStarted = true
		go mcp.GetManager().Connect(func(line string) {
			runtime.EventsEmit(a.ctx, "mcp:log", map[string]any{"line": line})
		})
	}
	a.startLocalPoll() // 本地模型状态轮询（local:status 事件）
}

func (a *App) shutdown(ctx context.Context) {
	if a.runner != nil {
		a.runner.Stop()
	}
	mcp.ResetManager()
	lsp.CloseAllEditors()
}

// ---------------- 产品/偏好 ----------------

// ProductInfo 产品信息（品牌 + 功能开关，前端按 flag 显隐入口）。
type ProductInfo struct {
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	Features map[string]bool `json:"features"`
}

// GetProductInfo 当前产品 profile。
func (a *App) GetProductInfo() ProductInfo {
	p := products.Active()
	f := map[string]bool{}
	for _, k := range products.KnownFeatures {
		f[k] = p.Feature(k, true)
	}
	return ProductInfo{Name: p.Name, Title: p.Title, Features: f}
}

// Prefs 界面偏好。
type Prefs struct {
	Language   string `json:"language"`
	Standalone bool   `json:"standalone"`
	FontSize   int    `json:"font_size"`
	Workspace  string `json:"workspace"`
}

// GetPrefs 读偏好。
func (a *App) GetPrefs() Prefs {
	return Prefs{
		Language:   config.GetLanguage(),
		Standalone: config.GetStandalone(),
		FontSize:   config.GetFontSizeChat(),
		Workspace:  tools.GetWorkspace(),
	}
}

// SetPrefs 写偏好（language/standalone/font_size）。
func (a *App) SetPrefs(p Prefs) {
	if p.Language != "" {
		config.SetLanguage(p.Language)
	}
	config.SetStandalone(p.Standalone)
	if p.FontSize > 0 {
		config.SetFontSizeChat(p.FontSize)
	}
}

// GetWorkspace 当前工作目录。
func (a *App) GetWorkspace() string { return tools.GetWorkspace() }

// SetWorkspace 切换并记住工作目录。
func (a *App) SetWorkspace(dir string) string {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	tools.SetWorkspace(dir)
	config.SaveLastWorkspace(dir)
	return dir
}

// PickWorkspace 原生目录选择框；取消返回空串。
func (a *App) PickWorkspace() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择工作目录",
	})
	if err != nil {
		return ""
	}
	return dir
}

// GitBranch 工作区当前分支（非仓库空串）。
func (a *App) GitBranch() string { return tools.GitBranch("") }

// ---------------- 模型管理（参考 Reasonix provider 概念：能力位 + 端点探测） ----------------

// ModelInfo 模型条目（含能力与预算，供选择器/管理面板展示）。
type ModelInfo struct {
	Key              string   `json:"key"`
	ProviderName     string   `json:"provider_name"`
	ModelID          string   `json:"model_id"`
	DisplayName      string   `json:"display_name"`
	BaseURL          string   `json:"base_url"`
	Vision           bool     `json:"vision"`
	Reasoning        bool     `json:"reasoning"`
	ReasoningEffort  string   `json:"reasoning_effort"`
	ReasoningChoices []string `json:"reasoning_choices"`
	ContextWindow    int      `json:"context_window"`
	IsDefault        bool     `json:"is_default"`
	IsCurrent        bool     `json:"is_current"`
	Local            bool     `json:"local"`
}

func toModelInfo(m config.ModelConfig, def, cur string) ModelInfo {
	return ModelInfo{
		Key: m.Key, ProviderName: m.ProviderName, ModelID: m.ModelID,
		DisplayName: m.DisplayName, BaseURL: m.BaseURL,
		Vision: m.Vision, Reasoning: m.Reasoning,
		ReasoningEffort: m.ReasoningEffort, ReasoningChoices: m.ReasoningChoices,
		ContextWindow: m.ContextWindow,
		IsDefault:     m.Key == def, IsCurrent: m.Key == cur,
		Local: strings.Contains(m.BaseURL, "127.0.0.1") || strings.Contains(m.BaseURL, "localhost"),
	}
}

// ListModels 全部模型（含能力位）。
func (a *App) ListModels() []ModelInfo {
	models, def := config.LoadModels()
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, toModelInfo(m, def, a.modelKey))
	}
	return out
}

// ProviderInfo 一个供应商（含其下模型）。
type ProviderInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	BaseURL   string      `json:"base_url"`
	APIKey    string      `json:"api_key"`
	APIFormat string      `json:"api_format"` // chat_completions / responses / opencode
	Enabled   bool        `json:"enabled"`
	Models    []ModelInfo `json:"models"`
}

// ListProviders 按 provider 分组返回模型（模型管理面板「先管供应商」用）。
func (a *App) ListProviders() []ProviderInfo {
	models, def := config.LoadModels()
	raw := config.LoadModelsData()
	grouped := map[string]*ProviderInfo{}
	var order []string
	for _, m := range models {
		p := grouped[m.ProviderName+"|"+m.BaseURL]
		if p == nil {
			p = &ProviderInfo{ID: providerIDOf(m.Key), Name: m.ProviderName, BaseURL: m.BaseURL, Enabled: true}
			// 从 models.json 原始 provider 读 api_key / api_format
			for _, pv := range rawProviders(raw) {
				if str(pv["id"]) == p.ID {
					p.APIKey = str(pv["api_key"])
					p.APIFormat = str(pv["api_format"])
					if p.APIFormat == "" {
						p.APIFormat = "chat_completions"
					}
				}
			}
			grouped[m.ProviderName+"|"+m.BaseURL] = p
			order = append(order, m.ProviderName+"|"+m.BaseURL)
		}
		p.Models = append(p.Models, toModelInfo(m, def, a.modelKey))
	}
	var out []ProviderInfo
	for _, k := range order {
		out = append(out, *grouped[k])
	}
	return out
}

func rawProviders(data map[string]any) []map[string]any {
	var out []map[string]any
	for _, pv := range mustArray(data["providers"]) {
		if p, ok := pv.(map[string]any); ok {
			out = append(out, p)
		}
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func mustArray(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func providerIDOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i]
		}
	}
	return key
}

// SaveProvider 新增/更新一个供应商（provider 级配置）；返回是否成功。
func (a *App) SaveProvider(id, name, baseURL, apiKey, apiFormat string) bool {
	if id == "" || baseURL == "" {
		return false
	}
	data := config.LoadModelsData()
	providers := mustArray(data["providers"])
	found := false
	for _, pv := range providers {
		p := pv.(map[string]any)
		if str(p["id"]) == id {
			p["name"] = name
			p["base_url"] = baseURL
			p["api_key"] = apiKey
			if apiFormat != "" {
				p["api_format"] = apiFormat
			}
			found = true
		}
	}
	if !found {
		providers = append(providers, map[string]any{
			"id": id, "name": name, "base_url": baseURL, "api_key": apiKey,
			"api_format": orStr(apiFormat, "chat_completions"), "models": []any{},
		})
	}
	data["providers"] = providers
	config.SaveModelsData(data)
	return true
}

func orStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// SetModelCapability 设置某个模型的单项能力（vision 开关 / reasoning 开关 / reasoning_effort）。
// 仅更新指定字段，其余保留。返回更新后的模型 key（改 model_id 场景用）。
func (a *App) SetModelCapability(key string, vision, reasoning *bool, reasoningEffort *string) map[string]any {
	mc := config.UpdateModel(key, "", "", "", "", vision, reasoning, reasoningEffort)
	if mc == nil {
		return map[string]any{"ok": false}
	}
	return map[string]any{"ok": true, "key": mc.Key, "vision": mc.Vision, "reasoning_effort": mc.ReasoningEffort}
}

// AddProviderModel 给指定供应商追加一个模型 ID。
func (a *App) AddProviderModel(providerID, modelID string, vision bool) bool {
	if modelID == "" {
		return false
	}
	return config.AugmentProviderModels(providerID, []string{modelID}, vision) > 0
}

// SetCurrentModel 切换当前模型。
func (a *App) SetCurrentModel(key string) {
	a.mu.Lock()
	a.modelKey = key
	a.mu.Unlock()
	config.SetDefaultModel(key)
	runtime.EventsEmit(a.ctx, "model:changed", key)
}

// SetReasoningEffort 为当前模型保存推理等级。
func (a *App) SetReasoningEffort(key, effort string) {
	config.UpdateModel(key, "", "", "", "", nil, nil, &effort)
	runtime.EventsEmit(a.ctx, "model:changed", key)
}

// FetchEndpointModels 探测端点 /models 列表（添加模型时自动填充 ID）。
func (a *App) FetchEndpointModels(baseURL, apiKey string) []string {
	if apiKey == "" {
		apiKey = "local-noauth"
	}
	return llm.FetchModelIDs(baseURL, apiKey)
}

// AddModels 批量添加自定义模型；返回新增条目 key。
func (a *App) AddModels(modelIDs []string, baseURL, apiKey string, vision bool) []string {
	added := config.AddCustomModel(modelIDs, baseURL, apiKey, nil, vision, "")
	out := make([]string, 0, len(added))
	for _, m := range added {
		out = append(out, m.Key)
	}
	return out
}

// RemoveModel 删除模型。
func (a *App) RemoveModel(key string) bool { return config.RemoveModel(key) }

// UpdateModelMeta 修改模型（端点/密钥/显示名/能力）。
func (a *App) UpdateModelMeta(key, baseURL, apiKey, displayName string, vision, reasoning *bool) bool {
	return config.UpdateModel(key, baseURL, apiKey, "", displayName, vision, reasoning, nil) != nil
}

// ---------------- 会话 ----------------

// SessionMeta 列表项。
type SessionMeta = sessions.Meta

// ListSessions 最近会话（按目录过滤可选）。
func (a *App) ListSessions(workspace, query string) []SessionMeta {
	return sessions.ListSessions(100, workspace, query)
}

// LoadedSession 载入的会话（messages 为原始 OpenAI 结构，前端负责渲染）。
type LoadedSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Workspace string `json:"workspace"`
	Messages  []any  `json:"messages"`
	Notes     []any  `json:"notes"`
}

// NewSession 新建会话。
func (a *App) NewSession() string {
	ws := tools.GetWorkspace()
	a.mu.Lock()
	defer a.mu.Unlock()
	// 复用该项目已有的空「新会话」（标题=新会话 且无消息），保证一个项目最多一个空会话
	for _, m := range sessions.ListSessions(100, ws, "新会话") {
		if s := sessions.Load(m.ID); s != nil && len(s.Messages) == 0 {
			a.sessionID = m.ID
			return m.ID
		}
	}
	id := sessions.NewID()
	a.sessionID = id
	// 立即落盘一条空会话（归属当前项目），侧栏按项目分组即可显示
	_ = sessions.Save(id, []msg.Msg{}, "新会话", ws, nil)
	return id
}

// LoadSession 载入历史会话（切工作目录 + 返回消息）。
func (a *App) LoadSession(id string) *LoadedSession {
	s := sessions.Load(id)
	if s == nil {
		return nil
	}
	if s.Workspace != "" {
		if st, err := os.Stat(s.Workspace); err == nil && st.IsDir() {
			tools.SetWorkspace(s.Workspace)
			config.SaveLastWorkspace(s.Workspace)
			runtime.EventsEmit(a.ctx, "workspace:changed", s.Workspace)
		}
	}
	a.mu.Lock()
	a.sessionID = id
	a.mu.Unlock()
	return &LoadedSession{
		ID: s.ID, Title: s.Title, Workspace: s.Workspace,
		Messages: s.Messages, Notes: s.Notes,
	}
}

// DeleteSession 删除会话。
func (a *App) DeleteSession(id string) bool { return sessions.Delete(id) }

// RenameSession 改名。
func (a *App) RenameSession(id, title string) bool { return sessions.Rename(id, title) }

// CurrentSession 当前会话 ID。
func (a *App) CurrentSession() string { return a.sessionID }

// ---------------- 聊天运行 ----------------

// SendMessage 发送消息（attachments 为文件路径或 snippet 字典；返回本次运行 ID）。
func (a *App) SendMessage(text string, attachments []any) error {
	a.mu.Lock()
	model := config.FindModel(a.modelKey)
	sid := a.sessionID
	mode := a.mode
	a.mu.Unlock()
	if model == nil {
		return fmt.Errorf("当前模型不可用: %s", a.modelKey)
	}
	return a.runner.Send(sid, *model, text, attachments, mode)
}

// StopRun 停止当前运行。
func (a *App) StopRun() { a.runner.Stop() }

// RespondApproval 应答审批请求。
func (a *App) RespondApproval(id string, allow bool) { a.runner.RespondApproval(id, allow) }

// SetPermissionMode 设置权限模式（readonly/ask/always）。
func (a *App) SetPermissionMode(mode string) {
	a.mu.Lock()
	a.mode = mode
	a.mu.Unlock()
}

// GetPermissionMode 当前权限模式。
func (a *App) GetPermissionMode() string { return a.mode }

// GetUsage 最近一次运行的用量统计（含费用估算）。
func (a *App) GetUsage() map[string]any {
	if a.runner == nil {
		return map[string]any{}
	}
	return a.runner.UseStats()
}

// ---------------- 编辑器 + LSP ----------------

// ReadFileText 读文件文本。
func (a *App) ReadFileText(path string) (string, error) {
	b, err := os.ReadFile(tools.ResolvePath(path))
	return string(b), err
}

// WriteFileText 编辑器保存。
func (a *App) WriteFileText(path, content string) error {
	p := tools.ResolvePath(path)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return os.WriteFile(p, []byte(content), 0o644)
}

// DirEntry 目录条目。
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Path  string `json:"path"`
}

// ListDir 列目录（相对工作目录）。
func (a *App) ListDir(rel string) []DirEntry {
	root := tools.ResolvePath(rel)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []DirEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && e.Name() != ".git" {
			continue
		}
		out = append(out, DirEntry{
			Name: e.Name(), IsDir: e.IsDir(),
			Path: filepath.ToSlash(filepath.Join(rel, e.Name())),
		})
	}
	return out
}

// LspComplete LSP 智能补全（多语言）。
func (a *App) LspComplete(path string, text string, line, char int) []lsp.CompletionItem {
	c, err := lsp.EditorClient(tools.ResolvePath(path))
	if err != nil {
		return nil
	}
	c.DidChange(text)
	return c.Complete(line, char)
}

// LspDiag LSP 诊断（编辑器波纹线）。
func (a *App) LspDiag(path string, text string) []lsp.DiagItem {
	c, err := lsp.EditorClient(tools.ResolvePath(path))
	if err != nil {
		return nil
	}
	return c.Diag(text, 1200*time.Millisecond)
}

// ReadImageDataURL 读图片为 data URL（前端标注画布底图；绕过 file:// 加载限制）。
func (a *App) ReadImageDataURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".gif":
		mime = "image/gif"
	case ".webp":
		mime = "image/webp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

// SaveDataURL 保存前端标注画布导出的 PNG dataURL 为文件，返回路径（作附件）。
func (a *App) SaveDataURL(dataURL string) (string, error) {
	const prefix = "base64,"
	i := strings.Index(dataURL, prefix)
	if i < 0 {
		return "", fmt.Errorf("非法 dataURL")
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[i+len(prefix):])
	if err != nil {
		return "", err
	}
	dir := filepath.Join(config.MediaDir(), "shots")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, fmt.Sprintf("anno_%s.png", time.Now().Format("20060102_150405.000")))
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// DeletePath 删除文件/目录（沙箱：仅允许工作目录内的路径）。
func (a *App) DeletePath(path string) (bool, string) {
	full := tools.ResolvePath(path)
	if !tools.PathInWorkspace(full, "") {
		return false, "拒绝：路径在工作目录之外（沙箱限制）"
	}
	if full == tools.GetWorkspace() {
		return false, "拒绝：不能删除工作目录本身"
	}
	if err := os.RemoveAll(full); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// LspProbeLanguages 工作区语言探测（启动预热）。
func (a *App) LspProbeLanguages() []string { return lsp.ProbeWorkspace(tools.GetWorkspace(), 5000) }

// LspServerStatus 某文件的 LSP 服务器状态（状态栏提示：是否安装/服务器名/安装命令）。
func (a *App) LspServerStatus(path string) map[string]any {
	lang := lsp.LanguageOf(path)
	if lang == "" {
		return map[string]any{"supported": false}
	}
	server := lsp.ServerName(lang)
	return map[string]any{
		"supported":   true,
		"lang":        lang,
		"server":      server,
		"available":   lsp.AvailableFor(lang),
		"install_cmd": lsp.InstallCmd(lang),
	}
}

// LspInstall 自动安装该文件的 LSP 服务器（阻塞执行，最长 5 分钟）。
func (a *App) LspInstall(path string) map[string]any {
	lang := lsp.LanguageOf(path)
	spec, ok := lsp.InstallSpecFor(lang)
	if !ok {
		return map[string]any{"ok": false, "output": "该语言不支持自动安装，请手动安装 LSP 服务器"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// 用参数数组直接执行（无 shell 引号/路径转换问题），设置工作目录
	var c *exec.Cmd
	if goruntime.GOOS == "windows" {
		c = exec.Command("cmd", append([]string{"/c"}, spec.Argv...)...)
	} else {
		c = exec.Command(spec.Argv[0], spec.Argv[1:]...)
	}
	if spec.WorkDir != "" {
		_ = os.MkdirAll(spec.WorkDir, 0o755)
		c.Dir = spec.WorkDir
		// 确保 go install 产物落到应用自己的 bin 目录（gopls 用）
		c.Env = append(os.Environ(), "GOBIN="+filepath.Join(spec.WorkDir, "bin"))
	}
	out, err := c.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return map[string]any{"ok": false, "output": "安装超时（5 分钟），请检查网络后重试"}
	}
	okInstall := err == nil
	tail := string(out)
	if len(tail) > 800 {
		tail = tail[len(tail)-800:]
	}
	if okInstall {
		// 安装成功后重查可用性（自包含目录优先）
		return map[string]any{
			"ok":     lsp.AvailableFor(lang),
			"output": strings.TrimSpace(tail),
		}
	}
	return map[string]any{"ok": false, "output": strings.TrimSpace(tail)}
}

// LspAvailable 支持的语言 → 服务器可用性。
func (a *App) LspAvailable(langs []string) map[string]bool {
	out := map[string]bool{}
	for _, l := range langs {
		out[l] = lsp.AvailableFor(l)
	}
	return out
}

// ---------------- 知识库（RAG） ----------------

// KBConfig 面板配置。
type KBConfig struct {
	Enabled  bool     `json:"enabled"`
	Inject   bool     `json:"inject"`
	Auto     bool     `json:"auto"`
	TopK     int      `json:"top_k"`
	EmbedKey string   `json:"embedding"`
	Roots    []string `json:"roots"`
}

// GetKBConfig 读知识库配置。
func (a *App) GetKBConfig() KBConfig {
	return KBConfig{
		Enabled: config.GetKBEnabled(), Inject: config.GetKBInject(),
		Auto: config.GetKBAuto(), TopK: config.GetKBTopK(),
		EmbedKey: config.GetKBEmbedding(), Roots: config.GetKBRoots(),
	}
}

// SetKBConfig 写知识库配置。
func (a *App) SetKBConfig(c KBConfig) {
	config.SetKBEnabled(c.Enabled)
	config.SetKBInject(c.Inject)
	config.SetKBAuto(c.Auto)
	config.SetKBTopK(c.TopK)
	config.SetKBEmbedding(c.EmbedKey)
	if c.Roots != nil {
		config.SetKBRoots(c.Roots)
	}
}

// PickDirectory 原生目录选择。
func (a *App) PickDirectory() string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择目录"})
	if err != nil {
		return ""
	}
	return dir
}

// PickFiles 原生多选文件对话框（聊天附件：图片/文档/音视频）；取消返回空。
func (a *App) PickFiles() []string {
	files, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择附件（图片 / 文档 / 音视频）",
		Filters: []runtime.FileFilter{
			{DisplayName: "常用类型 (png/jpg/gif/pdf/docx/zip/mp3/mp4…)",
				Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.pdf;*.docx;*.txt;*.md;*.csv;*.log;*.json;*.xml;*.zip;*.py;*.js;*.ts;*.go;*.java;*.c;*.cpp;*.rs;*.mp3;*.wav;*.flac;*.ogg;*.m4a;*.mp4;*.mkv;*.webm;*.mov;*.avi"},
			{DisplayName: "全部文件 (*.*)", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil
	}
	return files
}

// BuildKB 构建知识库（进度经 kb:progress 事件推送）。
func (a *App) BuildKB(force bool) {
	go func() {
		roots := config.GetKBRoots()
		stats := codera.Build(roots, force, func(done, total int) {
			runtime.EventsEmit(a.ctx, "kb:progress", map[string]any{"done": done, "total": total})
		})
		runtime.EventsEmit(a.ctx, "kb:done", stats)
	}()
}

// KBStats 知识库统计。
func (a *App) KBStats() map[string]any { return codera.Stats(config.GetKBRoots()) }

// KBQuery 测试检索。
func (a *App) KBQuery(q string) []codera.Hit { return codera.Search(q, 5, nil) }

// RebuildIndex 重建工作区索引（index_search 用）。
func (a *App) RebuildIndex() map[string]any {
	s := codeindex.Build(tools.GetWorkspace(), true, nil)
	return map[string]any{
		"files_indexed": s.FilesIndexed, "updated": s.Updated,
		"seconds": s.Seconds,
	}
}

// ---------------- 本地 GPU 模型（gpulocal 式管理） ----------------

// LocalModelInfo 本地模型条目 + 运行状态。
type LocalModelInfo = localmodels.LocalModel

// ListLocalModels 本地模型列表（附当前状态）。
func (a *App) ListLocalModels() []LocalModelInfo {
	list := localmodels.List()
	states := localmodels.StatusAll()
	for i := range list {
		if s, ok := states[list[i].Key]; ok {
			list[i].State = s
		} else {
			list[i].State = localmodels.StateStopped
		}
	}
	return list
}

// LocalModelAction 启动/停止本地模型服务（action: "start" | "stop"）。
func (a *App) LocalModelAction(key, action string) (bool, string) {
	for _, m := range localmodels.List() {
		if m.Key != key {
			continue
		}
		var ok bool
		var msg string
		if action == "start" {
			ok, msg = localmodels.Start(m)
		} else {
			ok, msg = localmodels.Stop(m)
		}
		// 动作发起后延迟一拍推送状态（给服务起来/退出的时间）
		go func() {
			time.Sleep(2 * time.Second)
			a.emitLocalStatus()
		}()
		return ok, msg
	}
	return false, "未找到本地模型: " + key
}

// emitLocalStatus 推送全部本地模型状态到前端。
func (a *App) emitLocalStatus() {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "local:status", localmodels.StatusAll())
}

// startLocalPoll 每 10 秒轮询本地模型状态（domReady 后启动一次）。
func (a *App) startLocalPoll() {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		a.emitLocalStatus() // 立即推一次
		for {
			select {
			case <-ticker.C:
				a.emitLocalStatus()
			case <-a.ctx.Done():
				return
			}
		}
	}()
}

// ---------------- 缓存 ----------------

// CacheInfo 缓存状态。
func (a *App) CacheInfo() map[string]any { return cache.Stats() }

// ClearCache 清空缓存。
func (a *App) ClearCache() bool { return cache.Clear() }

// SaveCacheSettings 写缓存设置（backend/llm_ttl/tool_ttl）。
func (a *App) SaveCacheSettings(backend string, llmTTL, toolTTL int) map[string]any {
	return cache.SaveSettings(map[string]any{
		"backend": backend, "llm_ttl": llmTTL, "tool_ttl": toolTTL,
	})
}

// ---------------- 模型派发（Tk 版 ⚡ 面板对齐） ----------------

// GetDispatchConfig 读派发配置。
func (a *App) GetDispatchConfig() map[string]any { return config.GetDispatchConfig() }

// SetDispatchConfig 写派发配置（只处理已知键）。
func (a *App) SetDispatchConfig(cfg map[string]any) {
	if v, ok := cfg["model_dispatch"].(bool); ok {
		config.SetModelDispatch(v)
	}
	if v, ok := cfg["dispatch_smart"].(bool); ok {
		config.SetDispatchSmart(v)
	}
	if v, ok := cfg["auto_cloud_fallback"].(bool); ok {
		_ = v
	}
	if v, ok := cfg["dispatch_model"].(string); ok {
		config.SetDispatchModel(v)
	}
	if v, ok := cfg["dispatch_flash"].(string); ok {
		config.SetDispatchFlash(v)
	}
	if v, ok := cfg["dispatch_pro"].(string); ok {
		config.SetDispatchPro(v)
	}
	if v, ok := cfg["dispatch_vision"].(string); ok {
		config.SetDispatchVision(v)
	}
}

// ---------------- MCP 服务器管理（Tk 版 MCP 面板对齐） ----------------

// GetMCPServers 读 mcp.json 原始配置。
func (a *App) GetMCPServers() map[string]any { return config.LoadMCPServers() }

// SaveMCPServer 新增/更新一个 MCP 服务器配置并重连。
func (a *App) SaveMCPServer(name string, cfg map[string]any) {
	data := config.LoadMCPServers()
	servers, _ := data["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[name] = cfg
	data["servers"] = servers
	config.SaveMCPServers(data)
	a.ReconnectMCP()
}

// DeleteMCPServer 删除并重连。
func (a *App) DeleteMCPServer(name string) {
	data := config.LoadMCPServers()
	if servers, ok := data["servers"].(map[string]any); ok {
		delete(servers, name)
		data["servers"] = servers
		config.SaveMCPServers(data)
	}
	a.ReconnectMCP()
}

// ReconnectMCP 重连全部 MCP 服务器（后台；日志经 mcp:log 事件）。
func (a *App) ReconnectMCP() {
	go func() {
		mcp.ResetManager()
		mcp.GetManager().Connect(func(line string) {
			runtime.EventsEmit(a.ctx, "mcp:log", map[string]any{"line": line})
		})
		runtime.EventsEmit(a.ctx, "mcp:reconnected", nil)
	}()
}
