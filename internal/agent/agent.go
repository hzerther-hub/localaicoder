// Package agent Agent 循环：流式 function-calling + 权限控制
// （对译 Python agent.py；事件契约 1:1）。
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"localai/internal/attach"
	"localai/internal/cache"
	"localai/internal/config"
	"localai/internal/ctxcompact"
	"localai/internal/llm"
	"localai/internal/mcp"
	"localai/internal/media"
	"localai/internal/msg"
	"localai/internal/prompt"
	"localai/internal/routing"
	"localai/internal/skills"
	"localai/internal/tools"
	"localai/internal/weblinks"
)

// 权限模式
const (
	ModeReadonly = "readonly" // 只读：不提供可写工具
	ModeAsk      = "ask"      // 询问：可写工具执行前需批准
	ModeAlways   = "always"   // 总是允许：直接执行
)

// Usage 一次对话累计 token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	Requests         int `json:"requests"`
	// 费用估算（USD）；按当前模型 pricing 换算
	CostUSDFloat float64 `json:"cost_usd"`
}

// RoutingTally 智能路由会话级计数（随 usage 事件透出给 UI）。
type RoutingTally struct {
	Simple      int `json:"simple"`
	Strong      int `json:"strong"`
	Escalations int `json:"escalations"`
}

// Agent 执行一次完整的对话：提问 → 工具调用 → 最终回答。
type Agent struct {
	OnEvent    func(msg.Event)                                             // 事件回调（12 种，见 agent.py 契约）
	OnApproval func(name string, args map[string]any, summary string) bool // ask 模式审批
	OnStop     func() bool                                                 // 返回 true 则中止循环
	OnPause    func()                                                      // 暂停点：非阻塞协作，间隙调用
	Mode       string
	Model      *config.ModelConfig

	Messages     []msg.Msg // run 后是（可能被压缩过的）完整消息列表
	UsageTotal   Usage
	CacheHits    int
	CacheSaved   int
	Routing      RoutingTally // 智能路由计数（会话级累计）
	fallbackUsed bool

	turnCount  int              // 用户轮计数（路由分类用）
	turnRouted routing.Decision // 本轮路由决策；空 = 未路由
	escalated  bool             // 本轮 simple→strong 是否已升级
}

// New 创建 Agent；mode 空值默认 always。
func New(onEvent func(msg.Event), onApproval func(string, map[string]any, string) bool,
	onStop func() bool, mode string, model *config.ModelConfig) *Agent {
	if mode == "" {
		mode = ModeAlways
	}
	return &Agent{
		OnEvent:    onEvent,
		OnApproval: onApproval,
		OnStop:     onStop,
		Mode:       mode,
		Model:      model,
	}
}

func (a *Agent) emit(e msg.Event) {
	if a.OnEvent != nil {
		a.OnEvent(e)
	}
}

// buildUserMessage 构造用户消息；有附件时转多模态 content 列表。
// attachments 元素：文件路径字符串，或 {"kind":"snippet",...} 片段字典。
func (a *Agent) buildUserMessage(userMessage string, attachments []any) msg.Msg {
	if len(attachments) == 0 {
		return msg.Msg{"role": "user", "content": userMessage}
	}
	if strings.TrimSpace(userMessage) == "" {
		userMessage = "请分析我附加的文件。"
	}
	parts := []any{map[string]any{"type": "text", "text": userMessage}}
	var mediaLines []string
	for _, av := range attachments {
		// 代码片段附件（编辑器选中区右键加入）：直接内联文件+行号+内容
		if att, ok := av.(map[string]any); ok && msg.S(att, "kind") == "snippet" {
			parts = append(parts, map[string]any{"type": "text", "text": attach.FormatSnippet(att)})
			continue
		}
		path, _ := av.(string)
		kind := media.Classify(path)
		if kind == "image" {
			url, err := media.ImageToDataURL(path)
			if err == nil {
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": url}})
				continue
			}
			mediaLines = append(mediaLines, fmt.Sprintf("[图片读取失败: %s（%v）]", path, err))
			continue
		}
		if kind == "audio" || kind == "video" {
			label := map[string]string{"audio": "音频", "video": "视频"}[kind]
			mediaLines = append(mediaLines, fmt.Sprintf("[附件%s: %s]", label, path))
			continue
		}
		// 文档/压缩包：就地分析（文本内联 / 解压清单），模型直接可操作
		if analysis := attach.Analyze(path); analysis != "" {
			parts = append(parts, map[string]any{"type": "text", "text": analysis})
			continue
		}
		mediaLines = append(mediaLines, fmt.Sprintf("[附件文件: %s]", path))
	}
	if len(mediaLines) > 0 {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": strings.Join(mediaLines, "\n") +
				"\n（音视频附件可用 run_shell 调 ffmpeg/ffprobe 分析处理）",
		})
	}
	return msg.Msg{"role": "user", "content": parts}
}

// executeWithCache 执行工具；只读工具的结果走缓存。
func (a *Agent) executeWithCache(name string, args map[string]any) string {
	if strings.HasPrefix(name, "mcp_") {
		// MCP 工具：经管理器路由；返回的图片发 media 事件给 UI 内嵌显示。
		// 外部服务器可能返回时效性数据，不走缓存。
		text, mediaPaths := mcp.GetManager().Call(name, args)
		if len(mediaPaths) > 0 {
			a.emit(msg.Event{"type": "media", "paths": toAny(mediaPaths)})
		}
		return text
	}
	if !tools.IsWriteTool(name) {
		ws := tools.GetWorkspace()
		if hit, ok := cache.GetTool(name, args, ws); ok {
			a.CacheHits++
			return hit
		}
		result := tools.ExecuteTool(name, args)
		cache.PutTool(name, args, ws, result)
		return result
	}
	return tools.ExecuteTool(name, args) // 可写工具不缓存
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (a *Agent) emitCacheHit(textLen int) {
	saved := textLen/2 + len(a.Model.ModelID) // 粗略估算
	if saved < 1 {
		saved = 1
	}
	a.CacheSaved += saved
	a.emit(msg.Event{"type": "cache_hit", "saved": saved, "hits": a.CacheHits})
}

func (a *Agent) accumulateUsage(u map[string]any) {
	a.UsageTotal.Requests++
	add := func(dst *int, key string) {
		if v := msg.I(u, key); v != 0 {
			*dst += v
		}
	}
	add(&a.UsageTotal.PromptTokens, "prompt_tokens")
	add(&a.UsageTotal.CompletionTokens, "completion_tokens")
	add(&a.UsageTotal.TotalTokens, "total_tokens")
	add(&a.UsageTotal.CachedTokens, "cached_tokens")
	add(&a.UsageTotal.ReasoningTokens, "reasoning_tokens")
	// 费用：cached 输入走 hit 价，其余输入走 miss 价，输出走 out 价（USD/1M）
	if a.Model != nil && (a.Model.PriceInHitPerM > 0 || a.Model.PriceInMissPerM > 0 || a.Model.PriceOutPerM > 0) {
		cached := float64(msg.I(u, "cached_tokens"))
		in := float64(msg.I(u, "prompt_tokens"))
		out := float64(msg.I(u, "completion_tokens"))
		missIn := in - cached
		if missIn < 0 {
			missIn = 0
		}
		cost := cached*a.Model.PriceInHitPerM/1e6 +
			missIn*a.Model.PriceInMissPerM/1e6 +
			out*a.Model.PriceOutPerM/1e6
		a.UsageTotal.CostUSDFloat += cost
	}
}

func (a *Agent) isMCPWrite(name string) bool {
	return strings.HasPrefix(name, "mcp_") && mcp.GetManager().IsWriteTool(name)
}

// systemPrompt 构建本轮会话的系统提示（分区式：静态区跨会话缓存稳定，
// 动态区携带环境/模型/语言/技能注入）。仅会话首轮调用一次，此后随
// messages 复用，多轮请求前缀保持逐字节稳定。
func (a *Agent) systemPrompt(userText string) string {
	ws := tools.GetWorkspace()
	names := builtinToolNames(a.Mode == ModeReadonly)
	return prompt.Join(prompt.Build(prompt.Options{
		Model:     a.Model,
		Workspace: ws,
		Language:  config.GetLanguage(),
		ToolNames: names,
		GitInfo:   prompt.GitSummary(ws),
		Date:      time.Now().Format("2006-01-02"),
		Addendum:  a.Model.PromptAddendum,
		Skills:    skills.PromptSection(ws, userText),
	}))
}

// builtinToolNames 当前条件暴露的内置工具名（与 toolSchemas 同源，MCP 除外）。
func builtinToolNames(readonly bool) []string {
	var out []string
	for _, s := range tools.EnabledSchemas(readonly) {
		if fn, ok := s["function"].(map[string]any); ok {
			if n := msg.S(fn, "name"); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

func (a *Agent) toolSchemas() []map[string]any {
	// 内置工具：注册表统一处理条件暴露（kb_search/call_model 等）与只读过滤
	base := tools.EnabledSchemas(a.Mode == ModeReadonly)
	mgr := mcp.GetManager() // 合并 MCP 外部服务器工具
	if mgr.Connected() && mgr.ToolMapLen() > 0 {
		extra := mgr.ToolSchemas()
		if a.Mode == ModeReadonly {
			var ro []map[string]any
			for _, s := range extra {
				if fn, ok := s["function"].(map[string]any); ok {
					if !mgr.IsWriteTool(msg.S(fn, "name")) {
						ro = append(ro, s)
					}
				}
			}
			extra = ro
		}
		base = append(base, extra...)
	}
	sortToolSchemas(base) // 前缀稳定：工具按名字排序，保证多轮请求前缀逐字节一致
	return base
}

// sortToolSchemas 按工具名稳定排序（Reasonix 式 prefix-cache 稳定性）：
// 服务端 prompt cache 按前缀匹配，工具顺序抖动会让每轮请求的前缀失效、
// cached_tokens 归零；排序后多轮对话前缀只增不变，命中率最大化。
func sortToolSchemas(schemas []map[string]any) {
	sort.Slice(schemas, func(i, j int) bool {
		fi, _ := schemas[i]["function"].(map[string]any)
		fj, _ := schemas[j]["function"].(map[string]any)
		return msg.S(fi, "name") < msg.S(fj, "name")
	})
}

// localUnavailable 错误是否为本地模型不可用（HTTP 503 / Loading model / 没在运行）。
func localUnavailable(errText string) bool {
	return strings.Contains(errText, "HTTP 503") ||
		strings.Contains(strings.ToLower(errText), "loading model") ||
		strings.HasPrefix(errText, "连接失败")
}

// cloudFallback 本地模型不可用时挑一个可用的云端回退模型；不适用返回 nil。
// 仅对本机端点模型生效；候选 flash → pro；受 auto_cloud_fallback 开关控制。
func (a *Agent) cloudFallback(errText string) *config.ModelConfig {
	if a.Model == nil || !config.IsLocalModelKey(a.Model.Key) {
		return nil
	}
	if !localUnavailable(errText) || !config.GetAutoCloudFallback() {
		return nil
	}
	cfg := config.GetDispatchConfig()
	for _, key := range []string{msg.S(cfg, "dispatch_flash"), msg.S(cfg, "dispatch_pro")} {
		if key == "" || config.IsLocalModelKey(key) {
			continue // 跳过本地回退目标
		}
		if mc := config.FindModel(key); mc != nil && mc.APIKey != "" {
			return mc
		}
	}
	return nil
}

// ---------------- 智能路由（简单/复杂轮次分流） ----------------

// routeTurn 每次用户提问分类一次并钉住本轮：简单轮走轻量模型、
// 复杂轮走强力模型。路由不改变消息历史，只切换本轮的 a.Model。
func (a *Agent) routeTurn(userMessage string, attachments []any) {
	if a.Model == nil {
		return
	}
	cfg := config.GetSmartRouting()
	if !cfg.Enabled {
		return
	}
	in := routing.Input{
		UserText:          userMessage,
		HasNonTextContent: hasNonTextAttachment(attachments),
		TurnNumber:        a.turnCount,
	}
	key, decision := routing.Resolve(in, routing.Config{
		Enabled:        cfg.Enabled,
		SimpleModel:    cfg.SimpleModel,
		StrongModel:    cfg.StrongModel,
		SimpleMaxChars: cfg.SimpleMaxChars,
		SimpleMaxWords: cfg.SimpleMaxWords,
		Arbitrate:      cfg.Arbitrate,
	})
	if key == "" {
		return
	}
	// 计数与事件按「决策」记（决策=当前模型也记），UI 才能看到每轮走向
	a.turnRouted = decision
	switch decision {
	case routing.DecisionSimple:
		a.Routing.Simple++
	case routing.DecisionStrong:
		a.Routing.Strong++
	}
	if key == a.Model.Key {
		a.emit(msg.Event{"type": "routing", "decision": string(decision),
			"from": a.Model.Key, "model": modelInfo(a.Model)})
		return
	}
	mc := config.FindModel(key)
	if mc == nil {
		return
	}
	old := *a.Model
	*a.Model = *mc
	a.emit(msg.Event{"type": "routing", "decision": string(decision),
		"from": old.Key, "model": modelInfo(mc)})
}

// routeVision 识图预路由：本轮有图片附件、但当前模型不带识图能力时，
// 换到识图目标（本地识图大脑优先，否则云端识图）。能力路由，独立于智能路由开关。
func (a *Agent) routeVision(attachments []any) {
	if a.Model == nil || a.Model.Vision || !hasImageAttachment(attachments) {
		return
	}
	key := tools.ResolveDispatchVisionKey()
	if key == "" || key == a.Model.Key {
		return
	}
	mc := config.FindModel(key)
	if mc == nil {
		return
	}
	old := *a.Model
	*a.Model = *mc
	a.emit(msg.Event{"type": "routing", "decision": "vision",
		"from": old.Key, "model": modelInfo(mc)})
}

// escalateToStrong 简单路由模型出错时的升级重试：换 strong 再来一轮。
// 返回是否升级成功（每轮最多一次）。
func (a *Agent) escalateToStrong() bool {
	if a.escalated || a.turnRouted != routing.DecisionSimple || a.Model == nil {
		return false
	}
	cfg := config.GetSmartRouting()
	if cfg.StrongModel == "" {
		return false
	}
	mc := config.FindModel(cfg.StrongModel)
	if mc == nil {
		return false
	}
	a.escalated = true
	a.Routing.Escalations++
	old := *a.Model
	*a.Model = *mc
	a.emit(msg.Event{"type": "text", "delta": fmt.Sprintf(
		"\n⚠️ 轻量模型 %s 处理失败，升级到 %s 重试本轮。\n", old.DisplayName, mc.DisplayName)})
	a.emit(msg.Event{"type": "routing", "decision": "escalate",
		"from": old.Key, "model": modelInfo(mc)})
	return true
}

// routedRetryable 简单模型的错是否值得升级重试：404/429/5xx/网络错误可重试；
// 400/401/403 是请求侧问题，升级也救不了。
func routedRetryable(errText string) bool {
	if strings.HasPrefix(errText, "连接失败") {
		return true
	}
	for _, code := range []string{"HTTP 404", "HTTP 429", "HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504"} {
		if strings.Contains(errText, code) {
			return true
		}
	}
	return false
}

func modelInfo(mc *config.ModelConfig) map[string]any {
	return map[string]any{
		"key": mc.Key, "display_name": mc.DisplayName,
		"base_url": mc.BaseURL, "model_id": mc.ModelID,
	}
}

// hasNonTextAttachment 附件里是否有非文本内容（图片/音视频等媒体）。
// 文档类附件经就地分析后本质是文本，不算非文本。
func hasNonTextAttachment(attachments []any) bool {
	for _, av := range attachments {
		if att, ok := av.(map[string]any); ok {
			if msg.S(att, "kind") == "snippet" {
				continue // 代码片段是文本
			}
			return true
		}
		if path, ok := av.(string); ok {
			switch media.Classify(path) {
			case "image", "audio", "video":
				return true
			}
		}
	}
	return false
}

// hasImageAttachment 附件里是否有图片（识图预路由只关心真正的图片，音视频另算）。
func hasImageAttachment(attachments []any) bool {
	for _, av := range attachments {
		if att, ok := av.(map[string]any); ok && msg.S(att, "kind") == "snippet" {
			continue
		}
		if path, ok := av.(string); ok && media.Classify(path) == "image" {
			return true
		}
	}
	return false
}

// Run 同步运行整个 Agent 循环，返回最终文本。
// history 非空则在其后追加本轮提问（多会话延续对话）；
// attachments 为本轮附件（路径字符串或 snippet 字典）。
func (a *Agent) Run(userMessage string, history []msg.Msg, attachments []any) (string, error) {
	a.turnCount++
	a.turnRouted = ""
	a.escalated = false
	a.fallbackUsed = false
	// 智能路由：本轮提问先分类钉住模型（不做也完全兼容）
	a.routeTurn(userMessage, attachments)
	// 链接取材（对齐 Python 版 weblinks.py）：消息里的 http(s) URL 自动抓取——
	// 图片→视觉附件、网页→内联正文、其它→存档说明、失败→注释行，绝不阻断发送。
	if wl := weblinks.Process(userMessage); len(wl.Inline) > 0 || len(wl.Images) > 0 || len(wl.Notes) > 0 {
		for _, p := range wl.Images {
			attachments = append(attachments, p)
		}
		var extra []string
		extra = append(extra, wl.Inline...)
		extra = append(extra, wl.Notes...)
		if len(extra) > 0 {
			userMessage += "\n\n" + strings.Join(extra, "\n")
		}
	}
	// 识图预路由：链接取材也可能追加图片，故在取材之后判断本轮是否有图
	a.routeVision(attachments)
	userMsg := a.buildUserMessage(userMessage, attachments)
	var messages []msg.Msg
	if history != nil {
		messages = history
		messages = append(messages, userMsg)
	} else {
		messages = []msg.Msg{
			{"role": "system", "content": a.systemPrompt(userMessage)},
			userMsg,
		}
	}
	// 自动注入公司知识库片段：开启后每次提问检索 top-k 片段进上下文
	if config.GetKBInject() && config.GetKBEnabled() && len(config.GetKBRoots()) > 0 {
		defer func() { _ = recover() }() // 知识库异常不阻断对话
		if block := kbRetrieve(userMessage, config.GetKBTopK()); block != "" {
			insertAt := len(messages) - 1
			if insertAt < 0 {
				insertAt = 0
			}
			messages = append(messages[:insertAt],
				append([]msg.Msg{{"role": "system", "content": block}}, messages[insertAt:]...)...)
		}
	}
	a.Messages = messages
	var finalText []string

	nudgeCount := 0 // 完成纪律兜底：清单未完成时注入「继续」的次数
	for roundNo := 1; roundNo <= config.MaxToolRounds; roundNo++ {
		// 暂停点：用户暂停时在此协作阻塞（不中断已生成的流）
		if a.OnPause != nil {
			a.OnPause()
		}
		// 用户请求停止：带上已有内容立即退出
		if a.OnStop != nil && a.OnStop() {
			a.emit(msg.Event{"type": "text", "delta": "\n（已按用户请求停止）"})
			finalText = append(finalText, "\n（已按用户请求停止）")
			break
		}
		var textCollected []string
		var toolCalls []any
		schemas := a.toolSchemas()

		// 上下文预算：过大时渐进压缩（截断旧工具结果 → 折叠中间轮）
		messages = ctxcompact.MaybeCompact(messages, a.emit, a.Model)

		// ---- 缓存命中：直接重放事件，不调后端 ----
		if cached := cache.GetLLM(a.Model.ModelID, messages, schemas); cached != nil {
			a.CacheHits++
			for _, ev := range cached {
				if e, ok := ev.(map[string]any); ok {
					if msg.S(e, "type") == "text" {
						textCollected = append(textCollected, msg.S(e, "delta"))
					}
					a.emit(e)
				}
			}
			a.emitCacheHit(len(strings.Join(textCollected, "")))
			finalText = append(finalText, textCollected...)
			break
		}

		var roundEvents []any
		aborted := false // 用户中途停止：半程回复落盘但不进缓存
		streamErr := llm.StreamChat(*a.Model, messages, schemas, func(e msg.Event) error {
			// 流中协作式停止：关窗/点停止不必等整轮流完
			if a.OnStop != nil && a.OnStop() {
				aborted = true
				return llm.ErrStop
			}
			switch msg.S(e, "type") {
			case "text":
				textCollected = append(textCollected, msg.S(e, "delta"))
				a.emit(e)
			case "reasoning":
				a.emit(e)
			case "tool_calls":
				toolCalls = msg.L(e, "tool_calls")
			case "usage":
				if u, ok := e["usage"].(map[string]any); ok {
					a.accumulateUsage(u)
					a.emit(msg.Event{
						"type": "usage", "usage": u, "total": a.usageTotalAny(),
						"routing": a.routingTotalAny(),
					})
				}
			}
			// 记录除 usage 外的事件用于缓存（usage 是后端实时数据）
			if msg.S(e, "type") != "usage" {
				roundEvents = append(roundEvents, e)
			}
			return nil
		})
		if streamErr != nil && !errors.Is(streamErr, llm.ErrStop) {
			errText := streamErr.Error()
			// 智能路由升级：简单路由模型可重试错误 → 升 strong 再来一轮（每轮一次）
			if a.turnRouted == routing.DecisionSimple && routedRetryable(errText) && a.escalateToStrong() {
				continue
			}
			// 本地模型不可用（加载中/未运行）→ 自动切云端重试本轮；
			// 回退模型再失败就直接报错，不连环回退。
			fb := a.cloudFallback(errText)
			reason := "未在运行，请先启动本地模型"
			if strings.Contains(errText, "503") {
				reason = "正在加载，请稍候"
			}
			if fb == nil {
				if a.Model != nil && strings.HasPrefix(a.Model.Key, "gpulocal") &&
					localUnavailable(errText) {
					return "", &llm.LLMError{Msg: fmt.Sprintf(
						"本地模型 %s %s，且没有可用的云端回退模型。原始错误：%s",
						a.Model.DisplayName, reason, errText)}
				}
				return "", streamErr
			}
			if a.fallbackUsed {
				return "", streamErr
			}
			a.fallbackUsed = true
			old := *a.Model
			*a.Model = *fb
			a.emit(msg.Event{"type": "text", "delta": fmt.Sprintf(
				"\n⚠️ 本地模型 %s %s，已自动切换到云端 %s 继续。\n",
				old.DisplayName, reason, fb.DisplayName)})
			// 通知 UI 把模型按钮切到实际使用的模型
			a.emit(msg.Event{
				"type": "model_switch", "from": old.DisplayName,
				"to": map[string]any{
					"key": fb.Key, "display_name": fb.DisplayName,
					"base_url": fb.BaseURL, "model_id": fb.ModelID,
				},
			})
			continue
		}
		if errors.Is(streamErr, llm.ErrStop) {
			aborted = true
		}

		// 服务端"正常结束"却既无正文也无工具调用（如空流）：
		// 明确提示，避免界面静默空白（用户主动停止除外）
		if !aborted && len(textCollected) == 0 && len(toolCalls) == 0 {
			a.emit(msg.Event{"type": "text",
				"delta": "\n⚠️ 模型未返回任何内容（服务端可能出错或上下文异常）\n"})
			finalText = append(finalText, "模型未返回任何内容（服务端可能出错或上下文异常）")
			break
		}

		// 用户已中止：带已生成文本直接退出（不执行任何工具，不动 messages）
		if aborted {
			finalText = append(finalText, textCollected...)
			break
		}

		// 本轮没有工具调用 → 理论上完成；但若步骤清单仍有未完成项，
		// 注入「继续」驱动（完成纪律兜底，最多 2 次，防死循环）——
		// 模型宣布收尾但任务烂尾是最常见的失败模式，必须打断。
		if len(toolCalls) == 0 {
			if nudgeCount < 2 && !aborted {
				if pending := tools.PendingTodoCount(); pending > 0 {
					messages = append(messages, msg.Msg{
						"role": "assistant", "content": strings.Join(textCollected, ""),
					})
					messages = append(messages, msg.Msg{
						"role":    "user",
						"content": fmt.Sprintf("任务尚未完成：步骤清单还有 %d 项未完成。请继续执行剩余步骤（开始某项置 in_progress、完成置 completed），不要总结、不要询问，直接继续动手。", pending),
					})
					nudgeCount++
					continue // 继续下一轮（消耗一个轮次名额；中间轮不写缓存）
				}
			}
			finalText = append(finalText, textCollected...)
			// 缓存键必须用「请求时」的消息列表（不含本轮回复）
			if !aborted {
				cache.PutLLM(a.Model.ModelID, messages, schemas, roundEvents)
			}
			messages = append(messages, msg.Msg{"role": "assistant", "content": strings.Join(textCollected, "")})
			break
		}

		assistantMsg := msg.Msg{
			"role":       "assistant",
			"content":    strings.Join(textCollected, ""),
			"tool_calls": toolCalls,
		}
		messages = append(messages, assistantMsg)

		for _, tcv := range toolCalls {
			tc, ok := tcv.(map[string]any)
			if !ok {
				continue
			}
			// 暂停点：工具执行间隙也允许暂停
			if a.OnPause != nil {
				a.OnPause()
			}
			// 每个工具执行前也检查停止
			if a.OnStop != nil && a.OnStop() {
				a.emit(msg.Event{"type": "tool_denied", "name": "(用户停止)"})
				messages = append(messages, msg.Msg{
					"role": "tool", "tool_call_id": msg.S(tc, "id"),
					"content": "用户请求停止，未执行此工具",
				})
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				fn = map[string]any{}
			}
			name := msg.S(fn, "name")
			var args map[string]any
			if err := json.Unmarshal([]byte(msg.S(fn, "arguments")), &args); err != nil {
				args = map[string]any{}
			}

			// 权限判断：ask 模式下可写工具（内置或 MCP）需审批
			var result string
			if a.Mode == ModeAsk && (tools.IsWriteTool(name) || a.isMCPWrite(name)) {
				summary := tools.DescribeArguments(name, args)
				allow := true
				if a.OnApproval != nil {
					allow = a.OnApproval(name, args, summary)
				}
				if !allow {
					a.emit(msg.Event{"type": "tool_denied", "name": name})
					result = "用户拒绝了工具调用 " + name
				} else {
					result = a.executeWithCache(name, args)
				}
			} else {
				result = a.executeWithCache(name, args)
			}

			a.emit(msg.Event{"type": "tool_start", "name": name, "args": args})
			a.emit(msg.Event{"type": "tool_result", "name": name, "result": result, "args": args})
			messages = append(messages, msg.Msg{
				"role": "tool", "tool_call_id": msg.S(tc, "id"), "content": result,
			})
		}

		a.emit(msg.Event{"type": "round", "n": roundNo})
	}

	a.Messages = messages
	return strings.Join(finalText, ""), nil
}

func (a *Agent) usageTotalAny() map[string]any {
	b, _ := json.Marshal(a.UsageTotal)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (a *Agent) routingTotalAny() map[string]any {
	b, _ := json.Marshal(a.Routing)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// kbRetrieve 知识库自动注入（惰性导入避免内核对 codera 的静态依赖噪音）。
func kbRetrieve(query string, topK int) string {
	defer func() { _ = recover() }()
	return coderaRetrieve(query, topK)
}
