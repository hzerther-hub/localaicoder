// desktop App：暴露给前端（window.go.main.App）的绑定方法集合。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
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
	"localai/internal/ctxcompact"
	"localai/internal/llm"
	"localai/internal/localmodels"
	"localai/internal/lsp"
	"localai/internal/mcp"
	"localai/internal/msg"
	"localai/internal/products"
	"localai/internal/sessions"
	"localai/internal/skills"
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
	a.updateWindowTitle()
	go a.scheduleLoop() // 定时任务调度器（desktop/schedule.go）
	// 自建中继：启动时若已配置（server+token 均非空）自动连接，避免重启后手机端一直重连
	if cfg := config.GetRelayConfig(); cfg["server_url"] != "" && cfg["device_token"] != "" {
		go relayC.Connect(a, msg.S(cfg, "server_url"), msg.S(cfg, "device_token"))
	}
}

// updateWindowTitle 窗口标题同步"产品名 · 模型名"（对齐 Python 版 _title_with_model：
// 切模型时窗口标题跟随刷新）。
func (a *App) updateWindowTitle() {
	if a.ctx == nil {
		return
	}
	title := products.Active().Title
	if m := config.FindModel(a.modelKey); m != nil {
		title += " · " + m.DisplayName
	}
	runtime.WindowSetTitle(a.ctx, title)
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
		a.runner.StopAll()
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
	broadcastSessions(a)
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
	Priced           bool     `json:"priced"` // 配置了官方定价（统计条费用显示开关）
	PriceHit         float64  `json:"price_in_hit_per_m"`
	PriceMiss        float64  `json:"price_in_miss_per_m"`
	PriceOut         float64  `json:"price_out_per_m"`
}

func toModelInfo(m config.ModelConfig, def, cur string) ModelInfo {
	return ModelInfo{
		Key: m.Key, ProviderName: m.ProviderName, ModelID: m.ModelID,
		DisplayName: m.DisplayName, BaseURL: m.BaseURL,
		Vision: m.Vision, Reasoning: m.Reasoning,
		ReasoningEffort: m.ReasoningEffort, ReasoningChoices: m.ReasoningChoices,
		ContextWindow: m.ContextWindow,
		IsDefault:     m.Key == def, IsCurrent: m.Key == cur,
		Local:    strings.Contains(m.BaseURL, "127.0.0.1") || strings.Contains(m.BaseURL, "localhost"),
		Priced:   m.PriceInHitPerM > 0 || m.PriceInMissPerM > 0 || m.PriceOutPerM > 0,
		PriceHit: m.PriceInHitPerM, PriceMiss: m.PriceInMissPerM, PriceOut: m.PriceOutPerM,
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
	APIKeys   []string    `json:"api_keys"`   // 凭据池（与 api_key 合并去重后的有效列表）
	APIFormat string      `json:"api_format"` // chat_completions / anthropic_messages / responses / gemini
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
					// 凭据池：数组或逗号分隔字符串都支持（与内核 parseAPIKeys 一致）
					switch v := pv["api_keys"].(type) {
					case []any:
						for _, x := range v {
							if s := str(x); s != "" {
								p.APIKeys = append(p.APIKeys, s)
							}
						}
					case string:
						for _, s := range strings.Split(v, ",") {
							if s = strings.TrimSpace(s); s != "" {
								p.APIKeys = append(p.APIKeys, s)
							}
						}
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
// apiKeys 为凭据池（一个 key 也可只填 api_key；传 nil 不改动已有 api_keys）。
func (a *App) SaveProvider(id, name, baseURL, apiKey, apiFormat string, apiKeys []string) bool {
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
			if apiKeys != nil {
				if len(apiKeys) == 0 {
					delete(p, "api_keys")
				} else {
					p["api_keys"] = toAnySlice(apiKeys)
				}
			}
			found = true
		}
	}
	if !found {
		entry := map[string]any{
			"id": id, "name": name, "base_url": baseURL, "api_key": apiKey,
			"api_format": orStr(apiFormat, "chat_completions"), "models": []any{},
		}
		if len(apiKeys) > 0 {
			entry["api_keys"] = toAnySlice(apiKeys)
		}
		providers = append(providers, entry)
	}
	data["providers"] = providers
	config.SaveModelsData(data)
	return true
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// ---------------- 智能路由（简单/复杂轮次分流） ----------------

// GetSmartRouting 读智能路由配置（含默认值回退后的视图）。
func (a *App) GetSmartRouting() map[string]any {
	cfg := config.GetSmartRouting()
	return map[string]any{
		"enabled":          cfg.Enabled,
		"configured":       cfg.Configured,
		"simple_model":     cfg.SimpleModel,
		"strong_model":     cfg.StrongModel,
		"simple_max_chars": cfg.SimpleMaxChars,
		"simple_max_words": cfg.SimpleMaxWords,
		"arbitrate":        cfg.Arbitrate,
	}
}

// SetSmartRouting 归一并落盘 smart_routing 块。
func (a *App) SetSmartRouting(block map[string]any) { config.SetSmartRouting(block) }

func orStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// SetModelPricing 设置模型官方定价（$/百万：缓存命中输入/未命中输入/输出；
// 传 0 清除该项）。保存后统计条「费用」按此折算。
func (a *App) SetModelPricing(key string, hit, miss, out float64) map[string]any {
	config.SetModelPricing(key, &hit, &miss, &out)
	runtime.EventsEmit(a.ctx, "model:changed", key)
	return map[string]any{"ok": true}
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
	a.updateWindowTitle()
	// 手机端实时同步模型（中继/局域网手机页）
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "model:changed", "key": key})
	}
}

// SetReasoningEffort 为当前模型保存推理等级。
func (a *App) SetReasoningEffort(key, effort string) {
	config.UpdateModel(key, "", "", "", "", nil, nil, &effort)
	runtime.EventsEmit(a.ctx, "model:changed", key)
	a.updateWindowTitle()
	// 手机端实时同步思考等级
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "model:changed", "key": key, "effort": effort})
	}
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
	tools.ResetTodos() // 新会话：清空任务步骤清单（完成纪律状态）
	a.mu.Lock()
	defer a.mu.Unlock()
	// 复用本项目「空的会话」——不限标题（兼容历史遗留的重名/纯附件会话），
	// 保证一个项目最多一个空会话；多余的空会话直接清理。
	var keep string
	keepUpdated := int64(-1)
	var stale []string
	for _, m := range sessions.ListSessions(200, ws, "") {
		s := sessions.Load(m.ID)
		if s == nil || len(s.Messages) > 0 {
			continue
		}
		switch {
		case keep == "" || m.Updated > keepUpdated:
			if keep != "" {
				stale = append(stale, keep)
			}
			keep, keepUpdated = m.ID, m.Updated
		default:
			stale = append(stale, m.ID)
		}
	}
	for _, id := range stale {
		_ = sessions.Delete(id)
	}
	if keep != "" {
		_ = sessions.Save(keep, []msg.Msg{}, "新会话", ws, nil) // 标题归一
		a.sessionID = keep
		return keep
	}
	// 懒创建：新会话只登记 ID、不立即落盘——切换项目/分组时不会再到处
	// 留下空「新会话」；首次消息完成后由 runner.Save 真正入库。
	id := sessions.NewID()
	a.sessionID = id
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
	if a.runner != nil {
		a.runner.SeedHistory(id) // 内存历史为空时从 SQLite 播种（重启后续聊不丢上下文）
	}
	return &LoadedSession{
		ID: s.ID, Title: s.Title, Workspace: s.Workspace,
		Messages: s.Messages, Notes: s.Notes,
	}
}

// DeleteSession 删除会话。
func (a *App) DeleteSession(id string) bool {
	ok := sessions.Delete(id)
	if ok {
		broadcastSessions(a)
	}
	return ok
}

// ---------------- 项目垃圾箱 ----------------

// GetProjectTrash 垃圾箱中的项目（工作目录）列表。
func (a *App) GetProjectTrash() []string { return config.GetProjectTrash() }

// TrashProject 删除项目：仅移入垃圾箱（会话保留，可随时恢复）。
// 返回更新后的垃圾箱列表。
func (a *App) TrashProject(ws string) []string {
	if ws == "" {
		return config.GetProjectTrash()
	}
	list := config.GetProjectTrash()
	for _, v := range list {
		if v == ws {
			return list
		}
	}
	list = append(list, ws)
	config.SetProjectTrash(list)
	return list
}

// RestoreProject 从垃圾箱恢复项目。返回更新后的垃圾箱列表。
func (a *App) RestoreProject(ws string) []string {
	old := config.GetProjectTrash()
	list := make([]string, 0, len(old))
	for _, v := range old {
		if v != ws {
			list = append(list, v)
		}
	}
	config.SetProjectTrash(list)
	return list
}

// RenameSession 改名。
// RenameSession 重命名会话；「新会话」为系统保留名（空会话复用依赖），禁止使用。
func (a *App) RenameSession(id, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || title == "新会话" {
		return false
	}
	return sessions.Rename(id, title)
}

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
	// 用户消息事件：手机远程页与桌面聊天区以同一事件渲染用户气泡（双端同步）
	ev := msg.Event{"type": "user_message", "sessionId": sid, "text": text + attachmentLabels(attachments)}
	runtime.EventsEmit(a.ctx, "agent:event", ev)
	a.runner.fanout(ev)
	return a.runner.Send(sid, *model, text, attachments, mode)
}

// StopRun 停止某会话运行（默认当前会话）。
func (a *App) StopRun(sessionID string) {
	if sessionID == "" {
		a.mu.Lock()
		sessionID = a.sessionID
		a.mu.Unlock()
	}
	a.runner.Stop(sessionID)
}

// PauseRun 暂停某会话运行。
func (a *App) PauseRun(sessionID string) bool {
	if sessionID == "" {
		a.mu.Lock()
		sessionID = a.sessionID
		a.mu.Unlock()
	}
	return a.runner.Pause(sessionID)
}

// ResumeRun 恢复某会话运行。
func (a *App) ResumeRun(sessionID string) bool {
	if sessionID == "" {
		a.mu.Lock()
		sessionID = a.sessionID
		a.mu.Unlock()
	}
	return a.runner.Resume(sessionID)
}

// ListRuns 当前进行中/暂停的任务（后台运行标记用）。
func (a *App) ListRuns() []map[string]any { return a.runner.ListRuns() }

// RespondApproval 应答审批请求。
func (a *App) RespondApproval(id string, allow bool) { a.runner.RespondApproval(id, allow) }

// SetPermissionMode 设置权限模式（readonly/ask/always）。
func (a *App) SetPermissionMode(mode string) {
	a.mu.Lock()
	a.mode = mode
	a.mu.Unlock()
	// 通知桌面前端同步权限徽标（手机端切换时电脑即时更新）
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "permission:changed", mode)
	}
	// 手机端实时同步权限
	if a.runner != nil {
		a.runner.fanout(msg.Event{"type": "permission:changed", "value": mode})
	}
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

// GetCompactInfo 当前模型的上下文压缩预算与窗口（统计条「压缩阈值」用）。
func (a *App) GetCompactInfo() map[string]any {
	m := config.FindModel(a.modelKey)
	win := 0
	if m != nil {
		win = m.ContextWindow
	}
	return map[string]any{"budget": ctxcompact.EffectiveBudget(m), "window": win}
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
	// 空目录列表必须序列化为 [] 而非 null（nil 切片），否则前端 roots.map 直接抛错导致弹窗崩溃。
	roots := config.GetKBRoots()
	if roots == nil {
		roots = []string{}
	}
	return KBConfig{
		Enabled: config.GetKBEnabled(), Inject: config.GetKBInject(),
		Auto: config.GetKBAuto(), TopK: config.GetKBTopK(),
		EmbedKey: config.GetKBEmbedding(), Roots: roots,
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

// ---------------- 技能系统（注入 + 蒸馏草稿确认，对齐 Python 版 ui_panel_skills） ----------------

// SkillInfo 前端技能条目。
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	When        string `json:"when"`
	Body        string `json:"body"`
	Scope       string `json:"scope"` // user / project / draft
	Path        string `json:"path"`
}

func skillInfo(sk skills.Skill, scope string) SkillInfo {
	return SkillInfo{sk.Name, sk.Description, sk.When, sk.Body, scope, sk.Path}
}

// ListSkills 已生效技能（用户级 + 项目级 + 外部源，同名项目级覆盖用户级）。
func (a *App) ListSkills() []SkillInfo {
	ws := tools.GetWorkspace()
	all := skills.LoadAll(ws)
	out := make([]SkillInfo, 0, len(all))
	for _, sk := range all {
		scope := "user"
		if pd := skills.ProjectDir(ws); pd != "" && strings.HasPrefix(sk.Path, pd) {
			scope = "project"
		} else {
			// 外部技能源（Claude Code / OpenCode 目录约定）
			for _, src := range skills.ExternalDirs(ws) {
				if strings.HasPrefix(sk.Path, src[1]) {
					scope = src[0]
					break
				}
			}
		}
		out = append(out, skillInfo(sk, scope))
	}
	return out
}

// ListSkillDrafts 蒸馏草稿列表（待人工确认）。
func (a *App) ListSkillDrafts() []SkillInfo {
	drafts := skills.ListDrafts()
	out := make([]SkillInfo, 0, len(drafts))
	for _, sk := range drafts {
		out = append(out, skillInfo(sk, "draft"))
	}
	return out
}

// LoadSkillText 载入技能/草稿 Markdown 全文（前端可编辑）。
func (a *App) LoadSkillText(path string) string {
	if !pathInSkillsDirs(path) {
		return ""
	}
	sk, err := skills.LoadFile(path)
	if err != nil {
		return ""
	}
	return sk.Render()
}

// SaveSkillDraft 保存草稿编辑内容（仅限草稿目录内文件）。
func (a *App) SaveSkillDraft(path, content string) bool {
	if !pathInDir(skills.DraftsDir(), path) {
		return false
	}
	if _, err := skills.LoadFile(path); err != nil {
		return false // 只允许覆写已存在的合法草稿
	}
	return os.WriteFile(path, []byte(content), 0o644) == nil
}

// SaveSkillText 保存正式技能（用户级/项目级）的编辑内容；仅限已存在的
// 技能文件，且新内容必须仍是合法技能格式（frontmatter 完整），防手误破坏注入。
func (a *App) SaveSkillText(path, content string) bool {
	if !pathInSkillsDirs(path) || pathInDir(skills.DraftsDir(), path) {
		return false
	}
	if _, err := skills.LoadFile(path); err != nil {
		return false
	}
	if _, err := skills.ParseText(content); err != nil {
		return false
	}
	return os.WriteFile(path, []byte(content), 0o644) == nil
}

// AcceptDraft 草稿转正：进用户级技能库并删除草稿，立即参与注入。
func (a *App) AcceptDraft(path string) bool {
	if !pathInDir(skills.DraftsDir(), path) {
		return false
	}
	sk, err := skills.LoadFile(path)
	if err != nil {
		return false
	}
	if _, err := skills.Save(sk, skills.ScopeUser, tools.GetWorkspace()); err != nil {
		return false
	}
	return skills.Remove(path) == nil
}

// DiscardDraft 丢弃草稿。
func (a *App) DiscardDraft(path string) bool {
	if !pathInDir(skills.DraftsDir(), path) {
		return false
	}
	return skills.Remove(path) == nil
}

// DeleteSkill 删除正式技能（用户级/项目级）。
func (a *App) DeleteSkill(path string) bool {
	if !pathInDir(skills.UserDir(), path) && !pathInDir(skills.ProjectDir(tools.GetWorkspace()), path) {
		return false
	}
	return skills.Remove(path) == nil
}

// GetSkillsSettings 技能设置（开关 + 蒸馏模型 key）。
func (a *App) GetSkillsSettings() map[string]any {
	return map[string]any{
		"enabled":       config.GetSkillsEnabled(),
		"distill_model": config.GetSkillsDistillModel(),
	}
}

// SetSkillsSettings 写技能设置。
func (a *App) SetSkillsSettings(enabled bool, distillModel string) {
	config.SetSkillsEnabled(enabled)
	config.SetSkillsDistillModel(distillModel)
}

// InstallSkill 从远程安装技能到用户级技能库（对齐其它平台技能生态）：
//   - https://.../*.md：直接下载安装单个技能；
//   - 其余 URL 视为 git 仓库，浅克隆后扫描 skills/<name>/SKILL.md 批量安装
//     （Claude Code / OpenCode 目录约定），同名跳过。
//
// 返回 {installed: [名...], skipped: n, error: ""}。
func (a *App) InstallSkill(rawURL string) map[string]any {
	u := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return map[string]any{"error": "仅支持 http(s) URL"}
	}
	path := strings.Split(u, "?")[0]
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		name, err := skills.InstallFromMarkdownURL(u)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"installed": []string{name}}
	}
	tmp, err := os.MkdirTemp("", "las-skill-")
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer os.RemoveAll(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--quiet", u, tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return map[string]any{"error": "克隆失败（需要已安装 git）: " + strings.TrimSpace(string(out))}
	}
	installed, skipped, err := skills.InstallFromDir(tmp)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"installed": installed, "skipped": skipped}
}

// pathInDir 判断 path 是否位于 dir 之下（防越权路径写删）。
func pathInDir(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	absDir, err1 := filepath.Abs(dir)
	absPath, err2 := filepath.Abs(path)
	return err1 == nil && err2 == nil && strings.HasPrefix(absPath, absDir+string(os.PathSeparator))
}

// pathInSkillsDirs 是否在任一技能目录内。
func pathInSkillsDirs(path string) bool {
	return pathInDir(skills.UserDir(), path) ||
		pathInDir(skills.DraftsDir(), path) ||
		pathInDir(skills.ProjectDir(tools.GetWorkspace()), path)
}

// ---------------- Git 改动 / 分支 ----------------

// GitChanges 工作区改动总览：本会话 AI 变更 + 未提交改动 + 最近提交。
// 返回 {is_git, branch, session:[path], changes:[{path,status,dir}], history:[{hash,subject}]}。
func (a *App) GitChanges() map[string]any {
	ws := tools.GetWorkspace()
	out := map[string]any{
		"is_git": false, "branch": "", "session": a.runner.ChangedFiles(),
		"changes": []any{}, "history": []any{},
	}
	if ws == "" {
		return out
	}
	if err := exec.Command("git", "-C", ws, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return out // 非 git 仓库
	}
	out["is_git"] = true
	if b, err := exec.Command("git", "-C", ws, "branch", "--show-current").Output(); err == nil {
		out["branch"] = strings.TrimSpace(string(b))
	}
	if b, err := exec.Command("git", "-C", ws, "status", "--porcelain").Output(); err == nil {
		changes := []map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			if len(line) < 4 {
				continue
			}
			code, path := strings.TrimSpace(line[:2]), strings.TrimSpace(line[3:])
			if path == "" {
				continue
			}
			changes = append(changes, map[string]string{
				"path": path, "status": gitStatusText(code), "dir": filepath.Dir(path),
			})
		}
		out["changes"] = changes
	}
	if b, err := exec.Command("git", "-C", ws, "log", "--oneline", "-10").Output(); err == nil {
		history := []map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 {
				history = append(history, map[string]string{"hash": parts[0], "subject": parts[1]})
			}
		}
		out["history"] = history
	}
	return out
}

// GitBranches 列出本地分支与当前分支。
func (a *App) GitBranches() map[string]any {
	ws := tools.GetWorkspace()
	out := map[string]any{"ok": false, "current": "", "branches": []string{}}
	if ws == "" {
		return out
	}
	b, err := exec.Command("git", "-C", ws, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		return out
	}
	cur := ""
	if cb, err := exec.Command("git", "-C", ws, "branch", "--show-current").Output(); err == nil {
		cur = strings.TrimSpace(string(cb))
	}
	branches := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	return map[string]any{"ok": true, "current": cur, "branches": branches}
}

// SwitchGitBranch 切换本地分支；工作区有未提交改动导致冲突时返回失败信息。
func (a *App) SwitchGitBranch(name string) map[string]any {
	ws := tools.GetWorkspace()
	name = strings.TrimSpace(name)
	if ws == "" || name == "" || strings.ContainsAny(name, " ;&|$\n") {
		return map[string]any{"ok": false, "error": "分支名非法"}
	}
	out, err := exec.Command("git", "-C", ws, "checkout", name).CombinedOutput()
	if err != nil {
		return map[string]any{"ok": false, "error": strings.TrimSpace(string(out))}
	}
	runtime.EventsEmit(a.ctx, "git:branch", name)
	return map[string]any{"ok": true}
}

// gitStatusText porcelain 状态码 → 中文。
func gitStatusText(code string) string {
	switch {
	case code == "??":
		return "未跟踪"
	case strings.Contains(code, "M"):
		return "修改"
	case strings.Contains(code, "A"):
		return "新增"
	case strings.Contains(code, "D"):
		return "删除"
	case strings.Contains(code, "R"):
		return "重命名"
	}
	return code
}

// ---------------- 账户余额 ----------------

var (
	balMu       sync.Mutex
	balCache    map[string]any
	balAt       time.Time
	balFetching bool
)

// GetBalance 查询当前模型提供方的账户余额。当前仅 DeepSeek 端点支持
// /user/balance；结果缓存 60s。非 DeepSeek 模型返回 {ok:false}（前端隐藏）。
func (a *App) GetBalance() map[string]any {
	a.mu.Lock()
	mkey := a.modelKey
	a.mu.Unlock()
	mc := config.FindModel(mkey)
	if mc == nil || !strings.Contains(mc.BaseURL, "deepseek.com") {
		return map[string]any{"ok": false}
	}
	balMu.Lock()
	if balFetching {
		balMu.Unlock()
		return map[string]any{"ok": false} // 已有请求在途，避免重复打接口
	}
	if balCache != nil && time.Since(balAt) < time.Minute {
		cached := balCache
		balMu.Unlock()
		return cached
	}
	balFetching = true
	balMu.Unlock()
	defer func() {
		balMu.Lock()
		balFetching = false
		balMu.Unlock()
	}()

	key := mc.APIKey
	if key == "" && len(mc.APIKeys) > 0 {
		key = mc.APIKeys[0]
	}
	if key == "" || key == "local-noauth" {
		return map[string]any{"ok": false}
	}
	req, err := http.NewRequest("GET", strings.TrimSuffix(mc.BaseURL, "/")+"/user/balance", nil)
	if err != nil {
		return map[string]any{"ok": false}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"ok": false}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		IsAvailable  bool   `json:"is_available"`
		TotalBalance string `json:"total_balance"`
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance string `json:"total_balance"`
		} `json:"balanceInfos"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.TotalBalance == "" {
		return map[string]any{"ok": false}
	}
	currency := "CNY"
	if len(parsed.BalanceInfos) > 0 && parsed.BalanceInfos[0].Currency != "" {
		currency = parsed.BalanceInfos[0].Currency
	}
	out := map[string]any{"ok": parsed.IsAvailable, "total": parsed.TotalBalance, "currency": currency}
	balMu.Lock()
	balCache = out
	balAt = time.Now()
	balMu.Unlock()
	return out
}

// ---------------- 快捷输入（@文件 / !终端） ----------------

// SearchFiles 工作区内按子串匹配文件路径（@文件 补全用）；跳过重目录，
// 最多扫描 2 万个条目、返回 20 条。
func (a *App) SearchFiles(query string) []string {
	ws := tools.GetWorkspace()
	if ws == "" {
		return []string{}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	skip := map[string]bool{
		".git": true, "node_modules": true, "target": true, "dist": true,
		"bin": true, "obj": true, "__pycache__": true, ".venv": true, "venv": true, "vendor": true,
	}
	out := []string{}
	scanned := 0
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != ws && (skip[d.Name()] || (strings.HasPrefix(d.Name(), ".") && d.Name() != ".")) {
				return fs.SkipDir
			}
			if strings.Count(p[len(ws):], string(os.PathSeparator)) > 6 {
				return fs.SkipDir
			}
			return nil
		}
		scanned++
		if scanned > 20000 {
			return fs.SkipAll
		}
		if len(out) >= 20 {
			return fs.SkipAll
		}
		rel, rerr := filepath.Rel(ws, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if q == "" || strings.Contains(strings.ToLower(rel), q) {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// RunTerminalCommand 直接在工作区执行 shell 命令（聊天框 ! 前缀快捷方式，
// 不经过模型）；60s 超时，输出截断 16KB。
func (a *App) RunTerminalCommand(cmd string) map[string]any {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return map[string]any{"output": "（空命令）"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ws := tools.GetWorkspace()
	var c *exec.Cmd
	if goruntime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", cmd)
	} else {
		c = exec.CommandContext(ctx, "bash", "-c", cmd)
	}
	c.Dir = ws
	out, err := c.CombinedOutput()
	if len(out) > 16*1024 {
		out = out[:16*1024]
	}
	res := string(out)
	if err != nil {
		res += "\n[退出码非零: " + err.Error() + "]"
	}
	return map[string]any{"output": res}
}

// CompactHistory 手动压缩当前会话上下文（/compact）。
// 优先压缩内存历史；应用重启后内存为空，回退到会话数据库并写回。
// 返回 {ok, before, after}（估算 tokens）。
func (a *App) CompactHistory() map[string]any {
	a.mu.Lock()
	sid, mkey := a.sessionID, a.modelKey
	a.mu.Unlock()
	model := config.FindModel(mkey)
	r := a.runner

	hist := r.History(sid)
	fromDB := false
	if len(hist) == 0 {
		// 内存为空（重启后）：从会话库加载
		if s := sessions.Load(sid); s != nil && len(s.Messages) > 0 {
			hist = toMsgs(s.Messages)
			fromDB = true
		}
	}
	if len(hist) == 0 {
		return map[string]any{"ok": false, "msg": "当前会话无历史可压缩（新会话或尚未对话）"}
	}
	before := ctxcompact.EstimateTokens(hist)
	emit := func(e msg.Event) {
		if msg.S(e, "type") == "context_compact" {
			runtime.EventsEmit(a.ctx, "agent:event", e)
		}
	}
	after := ctxcompact.MaybeCompact(hist, emit, model)
	afterTokens := ctxcompact.EstimateTokens(after)
	if afterTokens >= before {
		return map[string]any{"ok": false, "msg": fmt.Sprintf("上下文 %d tokens 未超预算，无需压缩", before)}
	}
	r.SetHistory(sid, after)
	if fromDB {
		// 压缩结果写回会话库（保留标题/工作区/备注）
		if s := sessions.Load(sid); s != nil {
			_ = sessions.Save(sid, after, s.Title, s.Workspace, s.Notes)
		}
	}
	return map[string]any{"ok": true, "before": before, "after": afterTokens}
}

// toMsgs []any → []msg.Msg（会话库消息转换）。
func toMsgs(in []any) []msg.Msg {
	out := make([]msg.Msg, 0, len(in))
	for _, v := range in {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Doctor 健康自检（/doctor）：配置目录可写 / 模型配置 / git / 缓存后端。
func (a *App) Doctor() map[string]any {
	checks := []map[string]any{}
	add := func(name string, ok bool, detail string) {
		checks = append(checks, map[string]any{"name": name, "ok": ok, "detail": detail})
	}
	// 1 配置目录可写
	dir := config.Dir()
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		add(t2("配置目录"), false, dir+" 不可写")
	} else {
		_ = os.Remove(probe)
		add(t2("配置目录"), true, dir)
	}
	// 2 模型配置
	models, def := config.LoadModels()
	if len(models) == 0 {
		add(t2("模型配置"), false, "未配置任何模型")
	} else {
		add(t2("模型配置"), true, fmt.Sprintf("%d 个模型，默认 %s", len(models), def))
	}
	// 3 git
	if _, err := exec.LookPath("git"); err != nil {
		add(t2("git"), false, "未安装")
	} else {
		add(t2("git"), true, "可用")
	}
	// 4 缓存后端
	add(t2("缓存后端"), true, cache.Stats()["backend"].(string))
	// 5 会话库
	if _, err := os.Stat(filepath.Join(dir, "sessions.db")); err == nil {
		add(t2("会话库"), true, "sessions.db 就绪")
	} else {
		add(t2("会话库"), true, "首次使用时创建")
	}
	return map[string]any{"checks": checks}
}

// t2 占位：避免与 t 变量冲突的本地辅助。
func t2(s string) string { return s }

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
		config.SetAutoCloudFallback(v)
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
