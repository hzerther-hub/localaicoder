// index_search / kb_search / lsp_diagnostics / call_model 执行器 + 派发校验
//（对译 Python tools.py 的派发与知识库段）。
package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"localai/internal/codeindex"
	"localai/internal/config"
	"localai/internal/codera"
	"localai/internal/llm"
	"localai/internal/lsp"
	"localai/internal/msg"
	"localai/internal/products"
)

// ---------------- index_search（工作区语义检索） ----------------

func execIndexSearch(args map[string]any) string {
	query := strOf(args["query"])
	topK := 5
	if v, ok := args["top_k"].(float64); ok && int(v) > 0 {
		topK = int(v)
	}
	ws := GetWorkspace()
	codeindex.Ensure(ws, nil)
	hits := codeindex.Search(ws, query, topK)
	if len(hits) == 0 {
		return "未检索到相关代码（索引可能为空，或换个关键词试试）"
	}
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("### %s:%d-%d（相关度 %.4f）\n%s",
			h.File, h.StartLine, h.EndLine, h.Score, h.Content))
	}
	return strings.Join(out, "\n\n")
}

// ---------------- kb_search（公司知识库 RAG） ----------------

func execKBSearch(args map[string]any) string {
	roots := config.GetKBRoots()
	if len(roots) == 0 {
		return "错误：公司知识库尚未配置知识根目录。请在「📚 知识库」面板添加代码/文档目录后再检索。"
	}
	query := strings.TrimSpace(strOf(args["query"]))
	if query == "" {
		return "错误：query 不能为空"
	}
	topK := 0
	if v, ok := args["top_k"].(float64); ok && int(v) > 0 {
		topK = int(v)
	}
	if topK == 0 {
		topK = config.GetKBTopK()
	}
	codera.MaybeAutoRefresh(roots)
	hits := codera.Search(query, topK, roots)
	if len(hits) == 0 {
		return "未在知识库检索到相关内容（索引可能为空，或换个关键词；可在「📚 知识库」面板重建索引后重试）"
	}
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("### [%s 相关度 %.4f] %s/%s:%d-%d\n%s",
			h.Source, h.Score, h.Root, h.File, h.StartLine, h.EndLine, h.Content))
	}
	return strings.Join(out, "\n\n")
}

const kbSearchSchemaJSON = `{
  "type": "function",
  "function": {
    "name": "kb_search",
    "description": "检索公司知识库（企业代码 + 文档）：按相关度返回最相关的代码/文档片段（含知识根目录、文件与行号）。回答涉及公司内部代码/文档、跨仓库问题时优先用它，比逐个读文件快且省上下文。首次调用会自动建索引；与 index_search（当前工作目录）不同，kb_search 覆盖配置的多个公司根目录。",
    "parameters": {
      "type": "object",
      "properties": {
        "query": {"type": "string", "description": "检索内容：功能描述、函数名、类名、文档关键词等"},
        "top_k": {"type": "integer", "description": "返回片段数，默认取配置（4）"}
      },
      "required": ["query"]
    }
  }
}`

// KBSchema 公司知识库「已启用」时才提供 kb_search 工具
//（前提：rag 产品功能开启 + kb_enabled + 已配置知识根目录）。
func KBSchema() []map[string]any {
	if !products.Feature("rag", false) {
		return nil
	}
	if !config.GetKBEnabled() || len(config.GetKBRoots()) == 0 {
		return nil
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(kbSearchSchemaJSON), &s)
	return []map[string]any{s}
}

// ---------------- lsp_diagnostics ----------------

func execLSPDiagnostics(args map[string]any) string {
	path := strings.TrimSpace(strOf(args["path"]))
	if path == "" {
		return "错误：缺少参数 path"
	}
	full := resolve(path)
	lang := lsp.LanguageOf(full)
	if lang == "" {
		return "错误：不识别的文件类型（无对应语言）：" + path
	}
	if !lsp.AvailableFor(lang) {
		return fmt.Sprintf("提示：%s 的语言（%s）未安装 LSP 服务器，无法检查。可安装后重试。",
			path, lsp.LangIDOf(lang))
	}
	content := readFileText(full)
	diags, err := lsp.DiagnosticsForFile(full, content, 2.0)
	if err != nil {
		return "错误：LSP 检查失败：" + err.Error()
	}
	if len(diags) == 0 {
		return "✓ " + path + "：无错误/警告"
	}
	lines := []string{fmt.Sprintf("%s：%d 条诊断", path, len(diags))}
	for i, d := range diags {
		if i >= 30 {
			lines = append(lines, fmt.Sprintf("  …（其余 %d 条略）", len(diags)-30))
			break
		}
		lines = append(lines, fmt.Sprintf("  第%d行 %s %s", d.Line, d.Mark, d.Msg))
	}
	return strings.Join(lines, "\n")
}

func readFileText(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------- call_model（模型派发） ----------------

// dispatchSubPrompt call_model 子任务时注入的子系统提示。
const dispatchSubPrompt = "你是一个子任务执行模型。请只完成用户给你的这段子任务，输出简洁、准确、" +
	"可用的结果即可，不要复述任务，不要询问上下文，不要在结果里加入与任务无关的内容。"

// DispatchResultKeep 子任务结果回填主循环前的字符上限。
const DispatchResultKeep = 8000

const callModelSchemaJSON = `{
  "type": "function",
  "function": {
    "name": "call_model",
    "description": "把一段文本子任务委派给另一个（云端）模型处理（云端才花钱，非必要不用）。只有当本地确实搞不定时才用：任务复杂/重推理/超出你上下文与能力，或本地缺少所需能力（识图、专门领域专长等），或你尝试后仍无法高质量完成。本地能搞定就别委派。目标选择：复杂、重推理、需更强能力→deepseek/deepseek-v4-pro；含图片/识图→deepseek/deepseek-v4-flash-vision-exp。model=目标模型 key；task=交给它的完整子任务提示词（尽量自包含）；reasoning_effort 可选（复杂任务可设 high）。只做文本子任务，不要传图片。",
    "parameters": {
      "type": "object",
      "properties": {
        "model": {"type": "string", "description": "目标模型 key（已配置的派发目标之一）"},
        "task": {"type": "string", "description": "交给目标模型的子任务提示词"},
        "reasoning_effort": {"type": "string", "description": "可选：推理等级（low/medium/high）"}
      },
      "required": ["model", "task"]
    }
  }
}`

// isLocalKey 模型是否指向本地端点（127.0.0.1 / localhost）。
func isLocalKey(modelKey string) bool {
	mc := config.FindModel(modelKey)
	if mc == nil {
		return false
	}
	return strings.Contains(mc.BaseURL, "127.0.0.1") || strings.Contains(mc.BaseURL, "localhost")
}

// localHealthy 给定本地模型 key，判断对应服务是否已启动且健康
//（GET {base_url}/models 返回 2xx 即健康；Go 版用 HTTP 探测替代 systemd 查询）。
func localHealthy(modelKey string) bool {
	mc := config.FindModel(modelKey)
	if mc == nil {
		return false
	}
	req, err := http.NewRequest("GET", strings.TrimRight(mc.BaseURL, "/")+"/models", nil)
	if err != nil {
		return false
	}
	if mc.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+mc.APIKey)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// ValidateDispatchTarget 校验 call_model 的派发目标是否允许。
// 允许：配置里的三个云端目标（flash/pro/vision）；
// 拒绝：本地模型（自我调用/串行互斥）与白名单外的任何目标。
func ValidateDispatchTarget(model string) (bool, string) {
	cfg := config.GetDispatchConfig()
	cloud := map[string]bool{
		msg.S(cfg, "dispatch_flash"): true,
		msg.S(cfg, "dispatch_pro"):   true,
		msg.S(cfg, "dispatch_vision"): true,
	}
	if cloud[model] {
		return true, ""
	}
	if isLocalKey(model) {
		if model == msg.S(cfg, "dispatch_model") {
			return false, "错误：不能派发给当前本地大脑自身（避免自我调用）"
		}
		return false, "错误：本地模型互斥，不能派发给其它本地模型"
	}
	return false, "错误：派发目标不在白名单内：" + model
}

// ResolveDispatchVisionKey 识图预路由应选用的模型 key
//（本地大脑带识图且健康 → 本地；否则云端识图）。
func ResolveDispatchVisionKey() string {
	cfg := config.GetDispatchConfig()
	lb := msg.S(cfg, "dispatch_model")
	if lb != "" && isLocalKey(lb) && localHealthy(lb) {
		if mc := config.FindModel(lb); mc != nil && mc.Vision {
			return lb
		}
	}
	return msg.S(cfg, "dispatch_vision")
}

// CallModelSchema 模型派发「已生效」时才提供 call_model 工具
//（总开关开启 + 指定本地大脑 + 该模型已在运行且健康）。
func CallModelSchema() []map[string]any {
	if !config.GetModelDispatch() {
		return nil
	}
	key := config.GetDispatchModel()
	if key == "" || !localHealthy(key) {
		return nil
	}
	var s map[string]any
	_ = json.Unmarshal([]byte(callModelSchemaJSON), &s)
	return []map[string]any{s}
}

func truncateDispatch(text string, keep int) string {
	if len(text) <= keep {
		return text
	}
	head := keep * 2 / 3
	tail := keep - head
	return text[:head] + fmt.Sprintf("\n…[已压缩，原 %d 字符]\n", len(text)) + text[len(text)-tail:]
}

func execCallModel(args map[string]any) string {
	model := strings.TrimSpace(strOf(args["model"]))
	task := strings.TrimSpace(strOf(args["task"]))
	effort := strings.TrimSpace(strOf(args["reasoning_effort"]))
	if model == "" || task == "" {
		return "错误：call_model 需要 model 与 task 参数"
	}
	if !config.GetModelDispatch() {
		return "错误：模型派发未开启，无法调用其它模型"
	}
	if ok, err := ValidateDispatchTarget(model); !ok {
		return err
	}
	mc := config.FindModel(model)
	if mc == nil {
		return "错误：派发目标不存在：" + model
	}
	if effort != "" {
		mc.ReasoningEffort = effort // 按次覆盖，不改全局配置
	}
	messages := []msg.Msg{
		{"role": "system", "content": dispatchSubPrompt},
		{"role": "user", "content": task},
	}
	var collected []string
	err := llm.StreamChat(*mc, messages, nil, func(e msg.Event) error {
		if msg.S(e, "type") == "text" {
			collected = append(collected, msg.S(e, "delta"))
		}
		return nil
	})
	if err != nil {
		return "错误：派发失败：" + err.Error()
	}
	text := strings.Join(collected, "")
	if text == "" {
		return "（派发目标未返回内容）"
	}
	return truncateDispatch(text, DispatchResultKeep)
}
