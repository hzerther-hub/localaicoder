// Package config 全局配置：模型端点、默认参数（对译 Python config.py）。
//
// 模型列表从 models.json 加载，用户可以自行编辑该文件增删模型。
// 配置目录跨平台：Windows = %APPDATA%\local-ai-studio，
// Linux/macOS = ~/.config/local-ai-studio；旧目录（wellfuture-coder /
// qwen-coder）自动迁移。数据格式与 Python 版完全互通。
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"localai/internal/msg"
	"localai/internal/products"
)

// 生成参数
const (
	Temperature = 0.2
	MaxTokens   = 256000
)

// Agent 循环安全上限
const (
	MaxToolRounds   = 12 // 最多 tool-calling 轮次
	ToolExecTimeout = 60 // 单个工具执行超时（秒）
)

// 上下文管理（预算 + 渐进压缩）
const (
	ContextBudget     = 1000000 // 对齐 DSH：deepseek 1M 上下文，超此才压缩
	ContextKeepRounds = 2       // 最近 N 轮保留原文
	ToolResultKeep    = 3000    // 超长工具结果截断保留的字符数
)

// 界面字号范围
const (
	FontSizeMin = 8
	FontSizeMax = 24
)

// 发给识图模型的图片最大长边（像素）
const AttachImageMaxPix = 1568

// ---------------- 沙箱开关 ----------------

var (
	sandboxOnce sync.Once
	sandbox     bool
)

// Sandbox 写操作沙箱是否开启（环境变量 LAS_SANDBOX=off 关闭）。
func Sandbox() bool {
	sandboxOnce.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("LAS_SANDBOX")))
		switch v {
		case "off", "0", "false", "no":
			sandbox = false
		default:
			sandbox = true
		}
	})
	return sandbox
}

// ---------------- 配置目录 ----------------

var (
	dirMu       sync.Mutex
	dirOverride string // 测试重定向用
	dirResolved string
	dirDone     bool
)

// SetDir 重定向配置目录（测试隔离用；传空恢复默认解析）。
func SetDir(dir string) {
	dirMu.Lock()
	defer dirMu.Unlock()
	dirOverride = dir
	dirDone, dirResolved = false, ""
}

// Dir 返回配置目录（首次调用时解析 + 旧目录迁移）。
func Dir() string {
	dirMu.Lock()
	defer dirMu.Unlock()
	if dirOverride != "" {
		return dirOverride
	}
	if dirDone {
		return dirResolved
	}
	home, _ := os.UserHomeDir()
	var base, d string
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			base = home
		}
		d = filepath.Join(base, "local-ai-studio")
	} else {
		d = filepath.Join(home, ".config", "local-ai-studio")
	}
	if _, err := os.Stat(d); os.IsNotExist(err) {
		// 旧目录自动迁移，老用户配置不丢（按优先级找第一个存在的）
		legacy := []string{}
		if runtime.GOOS == "windows" {
			legacy = append(legacy, filepath.Join(base, "wellfuture-coder"))
		}
		legacy = append(legacy,
			filepath.Join(home, ".config", "wellfuture-coder"),
			filepath.Join(home, ".config", "qwen-coder"))
		for _, old := range legacy {
			if samePath(old, d) {
				continue
			}
			if st, err := os.Stat(old); err == nil && st.IsDir() {
				_ = copyTree(old, d)
				break
			}
		}
	}
	dirDone, dirResolved = true, d
	return dirResolved
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

// 各配置文件路径（统一从 Dir() 取，不要各自硬编码）。
func ModelsFile() string        { return filepath.Join(Dir(), "models.json") }
func MCPFile() string           { return filepath.Join(Dir(), "mcp.json") }
func StateFile() string         { return filepath.Join(Dir(), "state.json") }
func MediaDir() string          { return filepath.Join(Dir(), "media") }
func ExtractDir() string        { return filepath.Join(Dir(), "extract") }
func IndexDir() string          { return filepath.Join(Dir(), "index") }
func KBDir() string             { return filepath.Join(Dir(), "kb") }
func SessionsDB() string        { return filepath.Join(Dir(), "sessions.db") }
func LegacySessionsDir() string { return filepath.Join(Dir(), "sessions") }

// ---------------- state.json（最近工作目录等） ----------------

func loadState() map[string]any {
	data, err := os.ReadFile(StateFile())
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}

func saveState(data map[string]any) {
	_ = os.MkdirAll(Dir(), 0o755)
	_ = os.WriteFile(StateFile(), marshalIndent(data), 0o644)
}

// LoadLastWorkspace 启动时恢复上一次使用的目录，找不到则回退家目录。
func LoadLastWorkspace() string {
	ws := msg.S(loadState(), "workspace")
	if ws != "" {
		if st, err := os.Stat(ws); err == nil && st.IsDir() {
			return ws
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// SaveLastWorkspace 记住最近一次使用的工作目录。
func SaveLastWorkspace(path string) {
	data := loadState()
	data["workspace"] = path
	saveState(data)
}

// GetQuantLLMModel 量化面板「LLM 兜底」指定的模型 key；空 = 未指定。
func GetQuantLLMModel() string { return msg.S(loadState(), "quant_llm_model") }

// SetQuantLLMModel 记住量化 LLM 兜底用哪个模型。
func SetQuantLLMModel(key string) {
	data := loadState()
	data["quant_llm_model"] = key
	saveState(data)
}

// ---------------- 系统提示词 ----------------

const systemPrompt = "你是编码助手。优先用本地能力完成任务：用工具读文件、搜索代码、执行 shell、联网搜索，" +
	"尽量本地解决（本地不额外花钱）。" +
	"只有当你确认任务超出本地能力/上下文，或本地缺少所需能力（如识图、超强推理/重分析）时，" +
	"才用 call_model 委派给云端：复杂/重推理→deepseek/deepseek-v4-pro；识图→deepseek-v4-flash-vision-exp。" +
	"本地能搞定就别委派。" +
	"无论本地还是委派，始终精炼作答：只给结论与必要依据，绝不输出大段文件内容或重复列表。" +
	"修改/增强代码文件时，必须调用 write_file 把改动真正写回文件（不要只把新内容输出在回复里）；" +
	"写完再用 read_file 抽查确认。" +
	"完成纪律（必须遵守）：" +
	"1) 动手前列出完成该任务所需的步骤 todo（读文件→改动→验证→收尾），并逐项完成、逐项确认；" +
	"2) 改完必须用 lsp_diagnostics 或运行相关测试/脚本验证，发现问题就修，直到通过；" +
	"3) 只有当所有步骤完成且验证通过、目标真正达成时，才给出最终答复；" +
	"绝不在半途（改了一部分、还没验证通过）就草草结束。" +
	"请始终基于真实工具/委派结果作答，不要编造文件内容。" +
	"用户消息可能附带本地媒体文件（图片/音频/视频）路径：图片直接以视觉输入提供；" +
	"音频/视频可用 run_shell 调 ffmpeg（ffprobe）提取信息后再分析。"

// GetSystemPrompt 按当前界面语言返回系统提示（含回复语言指令，中英跟随界面）。
func GetSystemPrompt() string {
	if GetLanguage() == "zh" {
		return systemPrompt + "\n请使用中文回复（与当前界面语言一致）。"
	}
	return systemPrompt + "\nPlease respond in English (matching the current UI language)."
}

// ---------------- models.json ----------------

// ModelConfig 一个可用的模型：provider + model + 端点。
type ModelConfig struct {
	Key              string // 唯一键，格式 "provider_id/model_id"
	ProviderName     string
	ModelID          string
	DisplayName      string
	BaseURL          string
	APIKey           string
	Vision           bool     // 支持识图（多模态输入）
	Reasoning        bool     // 支持推理等级
	ReasoningEffort  string   // 推理等级；空 = 不发送，用模型默认
	ReasoningChoices []string // 支持的等级列表（空 = 按 provider 推断）
	ContextWindow    int      // 上下文窗口（token 数；0 = 未知）
	// 费用单价（USD/1M tokens）；Provider 级 pricing 字段，缺失为 0 = 不统计
	PriceInHitPerM  float64 `json:"price_in_hit_per_m"`
	PriceInMissPerM float64 `json:"price_in_miss_per_m"`
	PriceOutPerM    float64 `json:"price_out_per_m"`
}

// 推理等级支持集合：DeepSeek 扩展集 vs 标准集
var (
	dsReasoning  = []string{"", "none", "low", "medium", "high", "xhigh", "max"}
	stdReasoning = []string{"", "low", "medium", "high"}
)

// ReasoningChoicesFor 返回某模型支持的推理等级集合。
// 模型条目可显式声明 "reasoning_efforts"；否则按 provider 推断
// （名字含 deepseek 用扩展集，其余用标准集）。
// providerPrices 从 provider 读取 pricing: [in_hit, in_miss, out]（USD/1M）。
// 兼容对象格式 {"input_hit":x,"input":y,"output":z} 或数组 [x,y,z]。
func providerPrices(p map[string]any) [3]float64 {
	pv, ok := p["pricing"].(map[string]any)
	if !ok {
		return [3]float64{}
	}
	num := func(k string) float64 {
		return msg.F(pv, k)
	}
	return [3]float64{num("input_hit"), num("input"), num("output")}
}

func ReasoningChoicesFor(providerID, providerName string, m map[string]any) []string {
	if raw, ok := m["reasoning_efforts"].([]any); ok && len(raw) > 0 {
		out := make([]string, 0, len(raw))
		for _, x := range raw {
			out = append(out, msg.S(map[string]any{"x": x}, "x"))
		}
		return out
	}
	blob := strings.ToLower(providerID + " " + providerName)
	if strings.Contains(blob, "deepseek") {
		return dsReasoning
	}
	return stdReasoning
}

var defaultModels = map[string]any{
	"default": "qwen38-local/qwen3.8-27b-q8",
	"providers": []any{
		map[string]any{
			"id": "qwen38-local", "name": "本地 Qwen",
			"base_url": "http://127.0.0.1:8097/v1", "api_key": "local-noauth",
			"models": []any{map[string]any{"id": "qwen3.8-27b-q8", "name": "Qwen3.8-27B (DFlash2 加速)"}},
		},
		map[string]any{
			"id": "deepseek", "name": "DeepSeek",
			"base_url": "https://api.deepseek.com/v1", "api_key": "",
			"models": []any{
				map[string]any{"id": "deepseek-chat", "name": "DeepSeek Chat"},
				map[string]any{"id": "deepseek-reasoner", "name": "DeepSeek Reasoner"},
			},
		},
	},
}

// 桌面版可通过 go:embed 注册内置种子（CLI/内核走 exe 同目录或工作目录的 models.json）。
var (
	bundledMu   sync.Mutex
	bundledData []byte
)

// SetBundledModels 注册内置 models.json 内容（首次运行种子）。
func SetBundledModels(data []byte) {
	bundledMu.Lock()
	bundledData = data
	bundledMu.Unlock()
}

func ensureModelsFile() {
	p := ModelsFile()
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = os.MkdirAll(Dir(), 0o755)
	// 优先内置注册（桌面 embed），其次 exe/工作目录旁的 models.json，最后默认
	bundledMu.Lock()
	b := bundledData
	bundledMu.Unlock()
	if b != nil {
		_ = os.WriteFile(p, b, 0o644)
		return
	}
	for _, cand := range bundledCandidates() {
		if data, err := os.ReadFile(cand); err == nil {
			_ = os.WriteFile(p, data, 0o644)
			return
		}
	}
	_ = os.WriteFile(p, marshalIndent(defaultModels), 0o644)
}

func bundledCandidates() []string {
	out := []string{"models.json"}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(exe), "models.json"))
	}
	return out
}

func marshalIndent(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // 对齐 Python ensure_ascii=False 的裸 UTF-8 风格
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.Bytes()
}

var modelsMu sync.Mutex

// LoadModelsData 读 models.json 原始数据（保留未知键；损坏回退默认）。
func LoadModelsData() map[string]any {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	return loadModelsDataLocked()
}

// SaveModelsData 写回 models.json（桌面模型管理面板等用）。
func SaveModelsData(data map[string]any) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	saveModelsDataLocked(data)
}

func loadModelsDataLocked() map[string]any {
	ensureModelsFile()
	data, err := os.ReadFile(ModelsFile())
	if err != nil {
		return defaultModels
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil || m == nil {
		return defaultModels
	}
	return m
}

func saveModelsDataLocked(data map[string]any) {
	_ = os.WriteFile(ModelsFile(), marshalIndent(data), 0o644)
}

// LoadModels 加载 models.json，返回 (模型列表, 默认模型 key)。
func LoadModels() ([]ModelConfig, string) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	return loadModelsLocked()
}

func loadModelsLocked() ([]ModelConfig, string) {
	data := loadModelsDataLocked()
	var models []ModelConfig
	for _, pv := range msg.L(data, "providers") {
		p, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		pid := msg.S(p, "id")
		pname := msg.S(p, "name")
		if pname == "" {
			pname = pid
		}
		baseURL := msg.S(p, "base_url")
		apiKey := msg.S(p, "api_key")
		for _, mv := range msg.L(p, "models") {
			m, ok := mv.(map[string]any)
			if !ok {
				continue
			}
			mid := msg.S(m, "id")
			mname := msg.S(m, "name")
			if mname == "" {
				mname = mid
			}
			prices := providerPrices(p)
			models = append(models, ModelConfig{
				Key:              pid + "/" + mid,
				ProviderName:     pname,
				ModelID:          mid,
				DisplayName:      mname,
				BaseURL:          baseURL,
				APIKey:           apiKey,
				Vision:           msg.B(m, "vision"),
				Reasoning:        msg.B(m, "reasoning") || msg.S(m, "reasoning_effort") != "",
				ReasoningEffort:  msg.S(m, "reasoning_effort"),
				ReasoningChoices: ReasoningChoicesFor(pid, pname, m),
				ContextWindow:    msg.I(m, "context_window"),
				PriceInHitPerM:   prices[0],
				PriceInMissPerM:  prices[1],
				PriceOutPerM:     prices[2],
			})
		}
	}
	return models, msg.S(data, "default")
}

// FindModel 按 key 查找模型配置；找不到返回 nil。
func FindModel(key string) *ModelConfig {
	models, _ := LoadModels()
	for i := range models {
		if models[i].Key == key {
			return &models[i]
		}
	}
	return nil
}

// ---------------- 界面语言 / 独立提问 / 字号 ----------------

func zhOnlyProduct() bool {
	return products.Feature("zh_only", false)
}

// GetLanguage 界面语言（models.json 顶层 "language"，缺省英文；zh_only 产品强制 zh）。
func GetLanguage() string {
	if zhOnlyProduct() {
		return "zh"
	}
	data := LoadModelsData()
	if v := msg.S(data, "language"); v != "" {
		return v
	}
	return "en"
}

// SetLanguage 保存界面语言；「仅中文」产品强制 zh。
func SetLanguage(lang string) {
	if zhOnlyProduct() {
		lang = "zh"
	}
	if strings.ToLower(lang) != "zh" {
		lang = "en"
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if msg.S(data, "language") != lang {
		data["language"] = lang
		saveModelsDataLocked(data)
	}
}

// GetStandalone 独立提问模式：true = 每条消息不带历史上下文单独发送。
func GetStandalone() bool { return msg.B(LoadModelsData(), "standalone") }

// SetStandalone 设置独立提问模式。
func SetStandalone(on bool) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if msg.B(data, "standalone") != on {
		data["standalone"] = on
		saveModelsDataLocked(data)
	}
}

func fontSize(key string) int {
	n := msg.I(LoadModelsData(), key)
	if n == 0 {
		n = 10
	}
	if n < FontSizeMin {
		n = FontSizeMin
	}
	if n > FontSizeMax {
		n = FontSizeMax
	}
	return n
}

func setFontSize(key string, n int) {
	if n < FontSizeMin {
		n = FontSizeMin
	}
	if n > FontSizeMax {
		n = FontSizeMax
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	data[key] = n
	saveModelsDataLocked(data)
}

// GetFontSizeChat 聊天区基础字号（默认 10）。
func GetFontSizeChat() int { return fontSize("font_size_chat") }

// SetFontSizeChat 设置聊天区字号。
func SetFontSizeChat(n int) { setFontSize("font_size_chat", n) }

// GetFontSizeEditor 代码编辑器字号（默认 10）。
func GetFontSizeEditor() int { return fontSize("font_size_editor") }

// SetFontSizeEditor 设置编辑器字号。
func SetFontSizeEditor(n int) { setFontSize("font_size_editor", n) }

// ---------------- 模型派发（多模型路由） ----------------

var dispatchDefaults = map[string]any{
	"model_dispatch":      true,
	"dispatch_smart":      true,
	"auto_cloud_fallback": true,
	"dispatch_model":      "gpulocal-8097/qwen38-27b-q8",
	"dispatch_flash":      "deepseek/deepseek-v4-flash",
	"dispatch_pro":        "deepseek/deepseek-v4-pro",
	"dispatch_vision":     "deepseek/deepseek-v4-flash-vision-exp",
}

// GetDispatchConfig 返回模型派发配置，缺失字段用默认值补齐（归一类型）。
func GetDispatchConfig() map[string]any {
	data := LoadModelsData()
	out := make(map[string]any, len(dispatchDefaults))
	for k, dv := range dispatchDefaults {
		out[k] = dv
	}
	for k := range dispatchDefaults {
		v, ok := data[k]
		if !ok {
			continue
		}
		switch k {
		case "model_dispatch", "dispatch_smart", "auto_cloud_fallback":
			if b, ok := v.(bool); ok {
				out[k] = b
			}
		default:
			if s := msg.S(map[string]any{"v": v}, "v"); s != "" {
				out[k] = s
			}
		}
	}
	return out
}

func dispatchBool(key string) bool { return msg.B(GetDispatchConfig(), key) }
func dispatchStr(key string) string {
	v := GetDispatchConfig()[key]
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// GetModelDispatch 模型派发总开关是否开启。
func GetModelDispatch() bool { return dispatchBool("model_dispatch") }

// GetAutoCloudFallback 本地模型不可用时是否自动回退云端模型。
func GetAutoCloudFallback() bool { return dispatchBool("auto_cloud_fallback") }

// GetDispatchSmart 智排开关（按任务类型自动路由）。
func GetDispatchSmart() bool { return dispatchBool("dispatch_smart") }

// GetDispatchModel 本地大脑模型 key。
func GetDispatchModel() string { return dispatchStr("dispatch_model") }

// GetDispatchFlash 云端轻量/简单任务派发目标。
func GetDispatchFlash() string { return dispatchStr("dispatch_flash") }

// GetDispatchPro 云端高性能/复杂任务派发目标。
func GetDispatchPro() string { return dispatchStr("dispatch_pro") }

// GetDispatchVision 云端识图兜底目标（必选）。
func GetDispatchVision() string { return dispatchStr("dispatch_vision") }

func setDispatchBool(field string, on bool) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	old, ok := data[field].(bool)
	if !ok {
		old = dispatchDefaults[field].(bool)
	}
	if old != on {
		data[field] = on
		saveModelsDataLocked(data)
	}
}

func setDispatchStr(field, key string) {
	key = strings.TrimSpace(key)
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	old := msg.S(data, field)
	if old == "" {
		old = dispatchDefaults[field].(string)
	}
	if strings.TrimSpace(old) != key {
		data[field] = key
		saveModelsDataLocked(data)
	}
}

// SetModelDispatch 开关模型派发。
func SetModelDispatch(on bool) { setDispatchBool("model_dispatch", on) }

// SetDispatchSmart 开关智排。
func SetDispatchSmart(on bool) { setDispatchBool("dispatch_smart", on) }

// SetDispatchModel 设置本地大脑模型 key。
func SetDispatchModel(key string) { setDispatchStr("dispatch_model", key) }

// SetDispatchFlash 设置云端简单目标。
func SetDispatchFlash(key string) { setDispatchStr("dispatch_flash", key) }

// SetDispatchPro 设置云端高性能目标。
func SetDispatchPro(key string) { setDispatchStr("dispatch_pro", key) }

// SetDispatchVision 设置云端识图目标。
func SetDispatchVision(key string) { setDispatchStr("dispatch_vision", key) }

// DispatchTargetLabel 派发目标 key 的友好描述（界面/会话回放显示用）。
func DispatchTargetLabel(key string) string {
	cfg := GetDispatchConfig()
	switch key {
	case msg.S(cfg, "dispatch_pro"):
		return "云端高性能"
	case msg.S(cfg, "dispatch_flash"):
		return "云端简单"
	case msg.S(cfg, "dispatch_vision"):
		return "云端识图"
	}
	return key
}

// ---------------- 公司知识库（企业代码 RAG） ----------------

var kbDefaults = map[string]any{
	"kb_enabled":   false,
	"kb_inject":    false,
	"kb_auto":      true,
	"kb_top_k":     4,
	"kb_embedding": "",
	"kb_roots":     []any{},
}

// GetKBConfig 返回知识库配置，缺失字段用默认值补齐（归一类型）。
func GetKBConfig() map[string]any {
	data := LoadModelsData()
	out := make(map[string]any, len(kbDefaults))
	for k, dv := range kbDefaults {
		out[k] = dv
	}
	for k := range kbDefaults {
		v, ok := data[k]
		if !ok || v == nil {
			continue
		}
		switch k {
		case "kb_roots":
			if arr, ok := v.([]any); ok {
				roots := []any{}
				for _, x := range arr {
					roots = append(roots, msg.S(map[string]any{"x": x}, "x"))
				}
				out[k] = roots
			}
		case "kb_enabled", "kb_inject", "kb_auto":
			if b, ok := v.(bool); ok {
				out[k] = b
			}
		case "kb_top_k":
			n := msg.I(map[string]any{"v": v}, "v")
			if n >= 1 && n <= 20 {
				out[k] = n
			}
		default:
			if s := msg.S(map[string]any{"v": v}, "v"); s != "" {
				out[k] = s
			}
		}
	}
	return out
}

// GetKBRoots 知识根目录列表。
func GetKBRoots() []string {
	var out []string
	for _, r := range msg.L(GetKBConfig(), "kb_roots") {
		if s := msg.S(map[string]any{"x": r}, "x"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func kbBool(key string) bool { return msg.B(GetKBConfig(), key) }

// GetKBEnabled 知识库总开关。
func GetKBEnabled() bool { return kbBool("kb_enabled") }

// GetKBInject 自动注入开关。
func GetKBInject() bool { return kbBool("kb_inject") }

// GetKBAuto 自动增量开关。
func GetKBAuto() bool { return kbBool("kb_auto") }

// GetKBTopK 默认检索片段数（1..20）。
func GetKBTopK() int { return msg.I(GetKBConfig(), "kb_top_k") }

// GetKBEmbedding embedding 模型 key；空 = 纯 TF-IDF。
func GetKBEmbedding() string { return msg.S(GetKBConfig(), "kb_embedding") }

// SetKBRoots 设置知识根目录列表（转绝对路径、去重、排序后写盘）。
func SetKBRoots(roots []string) {
	seen := map[string]bool{}
	var abs []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		a, err := filepath.Abs(expandHome(r))
		if err != nil {
			continue
		}
		if !seen[a] {
			seen[a] = true
			abs = append(abs, a)
		}
	}
	sortStrings(abs)
	arr := make([]any, len(abs))
	for i, s := range abs {
		arr[i] = s
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	data["kb_roots"] = arr
	saveModelsDataLocked(data)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func setKBBool(field string, on bool) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	old := msg.B(data, field)
	if field == "kb_auto" && data[field] == nil {
		old = true
	}
	if old != on {
		data[field] = on
		saveModelsDataLocked(data)
	}
}

// SetKBEnabled 知识库总开关。
func SetKBEnabled(on bool) { setKBBool("kb_enabled", on) }

// SetKBInject 自动注入开关。
func SetKBInject(on bool) { setKBBool("kb_inject", on) }

// SetKBAuto 自动增量开关。
func SetKBAuto(on bool) { setKBBool("kb_auto", on) }

// SetKBTopK 默认检索片段数。
func SetKBTopK(n int) {
	if n < 1 {
		n = 1
	}
	if n > 20 {
		n = 20
	}
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if msg.I(data, "kb_top_k") != n && data["kb_top_k"] != nil {
		// 仅当显式配置过才比较；否则直接写入
	}
	if msg.I(data, "kb_top_k") == 0 && n == 4 {
		return // 与默认一致，不落盘
	}
	data["kb_top_k"] = n
	saveModelsDataLocked(data)
}

// SetKBEmbedding 设置 embedding 模型 key。
func SetKBEmbedding(key string) {
	key = strings.TrimSpace(key)
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if msg.S(data, "kb_embedding") != key {
		data["kb_embedding"] = key
		saveModelsDataLocked(data)
	}
}

// ---------------- 自定义模型管理 ----------------

// AddCustomModel 批量添加自定义模型（同一端点下可挂多个模型 ID）。
// 返回新添加（或已存在）的 ModelConfig 列表。
func AddCustomModel(modelIDs []string, baseURL, apiKey string,
	displayNames []string, vision bool, reasoningEffort string) []ModelConfig {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if apiKey == "" {
		apiKey = "local-noauth"
	}
	pid := "custom"
	var provider map[string]any
	for _, pv := range msg.L(data, "providers") {
		if p, ok := pv.(map[string]any); ok && msg.S(p, "id") == pid {
			provider = p
			break
		}
	}
	if provider == nil {
		provider = map[string]any{
			"id": pid, "name": "自定义", "base_url": baseURL,
			"api_key": apiKey, "models": []any{},
		}
		if data["providers"] == nil {
			data["providers"] = []any{}
		}
		data["providers"] = append(msg.L(data, "providers"), provider)
	} else {
		provider["base_url"] = baseURL
		provider["api_key"] = apiKey
	}
	if provider["models"] == nil {
		provider["models"] = []any{}
	}
	var added []ModelConfig
	for i, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		displayName := modelID
		if i < len(displayNames) && strings.TrimSpace(displayNames[i]) != "" {
			displayName = strings.TrimSpace(displayNames[i])
		}
		var existing map[string]any
		for _, mv := range msg.L(provider, "models") {
			if m, ok := mv.(map[string]any); ok && msg.S(m, "id") == modelID {
				existing = m
				break
			}
		}
		if existing == nil {
			entry := map[string]any{"id": modelID, "name": displayName}
			if vision {
				entry["vision"] = true
			}
			if reasoningEffort != "" {
				entry["reasoning_effort"] = reasoningEffort
			}
			provider["models"] = append(msg.L(provider, "models"), entry)
		} else if vision {
			existing["vision"] = true
		} else if reasoningEffort != "" {
			existing["reasoning_effort"] = reasoningEffort
		}
		added = append(added, ModelConfig{
			Key: pid + "/" + modelID, ProviderName: msg.S(provider, "name"),
			ModelID: modelID, DisplayName: displayName, BaseURL: baseURL,
			APIKey: apiKey, Vision: vision, ReasoningEffort: reasoningEffort,
			Reasoning: reasoningEffort != "",
		})
	}
	saveModelsDataLocked(data)
	return added
}

// AugmentProviderModels 给指定 provider 追加尚不存在的模型 ID（端点动态探测用）。
// 返回实际新增的模型数。
func AugmentProviderModels(providerID string, modelIDs []string, vision bool) int {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	var ids []string
	for _, m := range modelIDs {
		if s := strings.TrimSpace(m); s != "" {
			ids = append(ids, s)
		}
	}
	if len(ids) == 0 {
		return 0
	}
	data := loadModelsDataLocked()
	var provider map[string]any
	for _, pv := range msg.L(data, "providers") {
		if p, ok := pv.(map[string]any); ok && msg.S(p, "id") == providerID {
			provider = p
			break
		}
	}
	if provider == nil {
		return 0
	}
	existing := map[string]bool{}
	for _, mv := range msg.L(provider, "models") {
		if m, ok := mv.(map[string]any); ok {
			existing[msg.S(m, "id")] = true
		}
	}
	added := 0
	for _, mid := range ids {
		if existing[mid] {
			continue
		}
		entry := map[string]any{"id": mid, "name": mid}
		if vision {
			entry["vision"] = true
		}
		provider["models"] = append(msg.L(provider, "models"), entry)
		existing[mid] = true
		added++
	}
	if added > 0 {
		saveModelsDataLocked(data)
	}
	return added
}

// RemoveModel 删除一个模型（格式 "provider_id/model_id"）。成功返回 true。
// custom provider 下的模型删完后整个 provider 一并移除；
// 若删除的是 default，default 重置为第一个可用模型。
func RemoveModel(key string) bool {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	pid, mid, ok := splitKey(key)
	if !ok {
		return false
	}
	removed := false
	var keptProviders []any
	for _, pv := range msg.L(data, "providers") {
		p, ok := pv.(map[string]any)
		if !ok || msg.S(p, "id") != pid {
			keptProviders = append(keptProviders, pv)
			continue
		}
		before := len(msg.L(p, "models"))
		var keptModels []any
		for _, mv := range msg.L(p, "models") {
			if m, ok := mv.(map[string]any); ok && msg.S(m, "id") == mid {
				continue
			}
			keptModels = append(keptModels, mv)
		}
		if len(keptModels) < before {
			removed = true
			p["models"] = keptModels
		}
		if removed && pid == "custom" && len(keptModels) == 0 {
			continue // custom provider 空了就删掉
		}
		keptProviders = append(keptProviders, p)
		break
	}
	if !removed {
		return false
	}
	data["providers"] = keptProviders
	if msg.S(data, "default") == key {
		first := ""
		for _, pv := range msg.L(data, "providers") {
			if p, ok := pv.(map[string]any); ok {
				for _, mv := range msg.L(p, "models") {
					if m, ok := mv.(map[string]any); ok && msg.S(m, "id") != "" {
						first = msg.S(p, "id") + "/" + msg.S(m, "id")
						break
					}
				}
			}
			if first != "" {
				break
			}
		}
		data["default"] = first
	}
	saveModelsDataLocked(data)
	return true
}

// UpdateModel 修改一个已有模型；可改端点/密钥/模型 ID/显示名/识图/推理等级。
// 改 model_id 会同步更新 default 引用。找不到返回 nil。
func UpdateModel(key, baseURL, apiKey, modelID, displayName string,
	vision, reasoning *bool, reasoningEffort *string) *ModelConfig {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	pid, mid, ok := splitKey(key)
	if !ok {
		return nil
	}
	for _, pv := range msg.L(data, "providers") {
		p, ok := pv.(map[string]any)
		if !ok || msg.S(p, "id") != pid {
			continue
		}
		var m map[string]any
		for _, mv := range msg.L(p, "models") {
			if x, ok := mv.(map[string]any); ok && msg.S(x, "id") == mid {
				m = x
				break
			}
		}
		if m == nil {
			return nil
		}
		if s := strings.TrimSpace(displayName); s != "" {
			m["name"] = s
		}
		if s := strings.TrimSpace(modelID); s != "" && s != mid {
			m["id"] = s
		}
		if s := strings.TrimSpace(baseURL); s != "" {
			p["base_url"] = s
		}
		if s := strings.TrimSpace(apiKey); s != "" {
			p["api_key"] = s
		}
		if vision != nil {
			if *vision {
				m["vision"] = true
			} else {
				delete(m, "vision")
			}
		}
		if reasoning != nil {
			if *reasoning {
				m["reasoning"] = true
			} else {
				delete(m, "reasoning")
			}
		}
		if reasoningEffort != nil {
			if *reasoningEffort != "" {
				m["reasoning_effort"] = *reasoningEffort
				m["reasoning"] = true
			} else {
				delete(m, "reasoning_effort")
			}
		}
		newKey := pid + "/" + msg.S(m, "id")
		if msg.S(data, "default") == key {
			data["default"] = newKey
		}
		saveModelsDataLocked(data)
		out := &ModelConfig{
			Key: newKey, ProviderName: msg.S(p, "name"), ModelID: msg.S(m, "id"),
			DisplayName: msg.S(m, "name"), BaseURL: msg.S(p, "base_url"),
			APIKey: msg.S(p, "api_key"), Vision: msg.B(m, "vision"),
			ReasoningEffort: msg.S(m, "reasoning_effort"),
		}
		if p["name"] == nil || msg.S(p, "name") == "" {
			out.ProviderName = pid
		}
		out.Reasoning = out.ReasoningEffort != "" || msg.B(m, "reasoning")
		out.ReasoningChoices = ReasoningChoicesFor(pid, msg.S(p, "name"), m)
		return out
	}
	return nil
}

func splitKey(key string) (pid, mid string, ok bool) {
	i := strings.Index(key, "/")
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// SetDefaultModel 设置默认模型 key。
func SetDefaultModel(key string) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	data := loadModelsDataLocked()
	if msg.S(data, "default") != key {
		data["default"] = key
		saveModelsDataLocked(data)
	}
}

// ---------------- MCP 服务器配置 ----------------

// LoadMCPServers 读取 mcp.json：{"servers": {名称: {command,args,env,enabled}}}。
// 文件不存在或损坏时返回空配置（不报错，界面可自行新建）。
func LoadMCPServers() map[string]any {
	data, err := os.ReadFile(MCPFile())
	if err != nil {
		return map[string]any{"servers": map[string]any{}}
	}
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		if _, ok := m["servers"].(map[string]any); ok {
			return m
		}
	}
	return map[string]any{"servers": map[string]any{}}
}

// SaveMCPServers 保存 mcp.json。
func SaveMCPServers(data map[string]any) {
	_ = os.MkdirAll(Dir(), 0o755)
	_ = os.WriteFile(MCPFile(), marshalIndent(data), 0o644)
}
