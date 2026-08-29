// 技能蒸馏（对齐 Python 版 skills_distill.py）：会话成功结束后，
// 把完整对话记录交给蒸馏模型判断是否含【下次可复用】的经验；
// 有 → 产出技能【草稿】待人工确认；没有 → 静默放弃，绝不打扰用户。
//
// 触发判定（宁缺毋滥，全部满足才蒸馏）：
//  1. 会话正常收尾（有 final 文本且无运行错误）
//  2. 技能开关开（skills_enabled）
//  3. 工具调用次数 ≥ MIN_TOOL_CALLS（过滤闲聊）
//  4. 确实写成功过文件（证明任务真完成，而非只读闲逛）
//
// 并发与成本控制：会话级"先占名额"防并发重复蒸馏；草稿数上限防堆积；
// 本地模型要求健康检查通过（未运行只跳过，不自动拉起）；传输失败静默放弃。
package skills

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"localai/internal/config"
	"localai/internal/llm"
	"localai/internal/msg"
)

// MIN_TOOL_CALLS 蒸馏最低工具调用次数（工作量足够，过滤闲聊）。
const MIN_TOOL_CALLS = 3

// MAX_DRAFTS 草稿堆积上限（防无限产出无人处理）。
const MAX_DRAFTS = 20

// maxTranscriptChars 蒸馏输入的会话记录截断上限（约 2 万 token 量级）。
const maxTranscriptChars = 60000

// DISTILL_PROMPT 蒸馏提示词（严格约束输出：技能片段 或 NO）。
const DISTILL_PROMPT = `你是技能蒸馏器。阅读下面这次 AI 编码助手的完整会话记录，判断其中是否包含【下次遇到同类任务可直接复用】的经验：可复用的流程、踩坑与规避方法、平台 API 映射、特定领域的操作顺序。
若有：只输出一个 Markdown 技能片段。首行必须是 ---，frontmatter 字段：
name: 小写连字符短名
description: 一句话说明这条经验
when: 触发词1,触发词2,触发词3
frontmatter 之后是正文：不超过 400 字，只写可执行结论（步骤/命令/映射表），不要寒暄、不要复述会话。
若无：只输出两个字符：NO
不要输出任何其它内容。`

var (
	distillMu   sync.Mutex
	distillDone = map[string]bool{} // sessionID -> 已蒸馏（先占名额，并发收尾不重复）
)

// ShouldDistill 蒸馏触发判定（全部满足才蒸馏）。
// final 为本轮最终回复文本；runErr 为运行错误；toolCalls 为本轮工具调用次数。
func ShouldDistill(final string, runErr error, toolCalls int, messages []msg.Msg) bool {
	if runErr != nil || strings.TrimSpace(final) == "" {
		return false // 非正常收尾（停止/出错）不蒸馏
	}
	if !config.GetSkillsEnabled() {
		return false
	}
	if toolCalls < MIN_TOOL_CALLS {
		return false
	}
	return HadWriteSuccess(messages)
}

// HadWriteSuccess 判断会话中是否确实写成功过文件（write_file/run_shell
// 执行结果不以"错误："开头即视为成功）。
func HadWriteSuccess(messages []msg.Msg) bool {
	for _, m := range messages {
		if msg.S(m, "role") != "assistant" {
			continue
		}
		for _, tci := range msg.L(m, "tool_calls") {
			tc, ok := tci.(map[string]any)
			if !ok {
				continue
			}
			name := msg.S(msg.M(tc, "function"), "name")
			if name != "write_file" && name != "run_shell" {
				continue
			}
			id := msg.S(tc, "id")
			for _, tm := range messages {
				if msg.S(tm, "role") != "tool" || msg.S(tm, "tool_call_id") != id {
					continue
				}
				if !strings.HasPrefix(msg.S(tm, "content"), "错误：") {
					return true
				}
			}
		}
	}
	return false
}

// ToolCallCount 统计消息列表中的工具调用次数（assistant.tool_calls 总数）。
func ToolCallCount(messages []msg.Msg) int {
	n := 0
	for _, m := range messages {
		if msg.S(m, "role") == "assistant" {
			n += len(msg.L(m, "tool_calls"))
		}
	}
	return n
}

// Distill 执行蒸馏：成功返回草稿路径，任何一环不满足返回空串（静默）。
// 由前端运行器在会话收尾后以后台方式调用；workspace 用于项目级查重。
func Distill(sessionID, workspace string, messages []msg.Msg, final string) string {
	distillMu.Lock()
	if distillDone[sessionID] {
		distillMu.Unlock()
		return ""
	}
	distillDone[sessionID] = true // 先占名额
	distillMu.Unlock()

	if !config.GetSkillsEnabled() {
		return ""
	}
	mc := resolveDistillModel()
	if mc == nil {
		return ""
	}
	if strings.HasPrefix(mc.Key, "gpulocal") && !localHealthy(mc.BaseURL) {
		return "" // 本地模型未运行：只跳过，不自动拉起
	}
	if len(ListDrafts()) >= MAX_DRAFTS {
		return "" // 草稿已堆积，防继续产出
	}

	out := askDistillModel(*mc, SerializeMessages(messages))
	if out == "" {
		return ""
	}
	text := extractSkillText(out)
	if text == "" {
		return ""
	}
	sk, err := parseSkillText(text)
	if err != nil || strings.TrimSpace(sk.Body) == "" {
		return ""
	}
	sk.Name = CleanName(sk.Name)
	if sk.Name == "" {
		return ""
	}
	sk.Body = truncateBytes(sk.Body, maxBodyChars)
	if SimilarExisting(workspace, sk) {
		return "" // 已有近似技能，不重复入库
	}
	path, err := Save(sk, ScopeDraft, workspace)
	if err != nil {
		return ""
	}
	return path
}

// resolveDistillModel 蒸馏模型：显式配置优先，否则默认模型；解析不到返回 nil。
func resolveDistillModel() *config.ModelConfig {
	key := config.GetSkillsDistillModel()
	if key == "" {
		_, key = config.LoadModels() // 回退默认模型 key
	}
	if key == "" {
		return nil
	}
	return config.FindModel(key)
}

// localHealthy 本地模型健康检查（GET /models，2s 超时）。
func localHealthy(baseURL string) bool {
	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/models")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// askDistillModel 调用蒸馏模型（一次性，收集全部文本）；失败返回空串。
func askDistillModel(mc config.ModelConfig, transcript string) string {
	if transcript == "" {
		return ""
	}
	content := DISTILL_PROMPT + "\n\n# 会话记录\n" + transcript
	var sb strings.Builder
	err := llm.StreamChat(mc, []msg.Msg{{"role": "user", "content": content}}, nil,
		func(e msg.Event) error {
			if msg.S(e, "type") == "text" {
				sb.WriteString(msg.S(e, "delta"))
			}
			return nil
		})
	if err != nil {
		return "" // 传输失败静默放弃
	}
	out := strings.TrimSpace(sb.String())
	if out == "" || out == "NO" {
		return ""
	}
	return out
}

// extractSkillText 从模型输出提取技能片段：优先代码围栏，其次 --- 开头原文。
func extractSkillText(out string) string {
	if s := fencedBlock(out); s != "" {
		return s
	}
	if strings.HasPrefix(out, "---") {
		return out
	}
	// 模型可能在片段前后加了说明：尝试截取首个 --- 到末尾
	if idx := strings.Index(out, "\n---"); idx >= 0 {
		return strings.TrimSpace(out[idx+1:])
	}
	return ""
}

// fencedBlock 提取首个 ``` 代码围栏内容。
func fencedBlock(s string) string {
	start := strings.Index(s, "```")
	if start < 0 {
		return ""
	}
	rest := s[start+3:]
	if nl := strings.Index(rest, "\n"); nl >= 0 { // 跳过语言标记行
		rest = rest[nl+1:]
	}
	end := strings.Index(rest, "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// SerializeMessages 把消息列表序列化为蒸馏用文本（大对象就地瘦身：
// 剥图片 data URL、超长工具结果截断保留头尾），总长截 maxTranscriptChars。
func SerializeMessages(messages []msg.Msg) string {
	var b strings.Builder
	for _, m := range messages {
		role := msg.S(m, "role")
		switch role {
		case "system":
			continue // 系统提示不进蒸馏输入
		case "user", "assistant":
			b.WriteString("## " + role + "\n")
			if c := contentText(m); c != "" {
				b.WriteString(c + "\n")
			}
			for _, tci := range msg.L(m, "tool_calls") {
				tc, ok := tci.(map[string]any)
				if !ok {
					continue
				}
				fn := msg.M(tc, "function")
				b.WriteString(fmt.Sprintf("→ 调用工具 %s(%s)\n", msg.S(fn, "name"), msg.S(fn, "arguments")))
			}
		case "tool":
			b.WriteString("## tool 结果（" + msg.S(m, "tool_call_id") + "）\n")
			b.WriteString(truncateMiddle(msg.S(m, "content"), 1200) + "\n")
		}
	}
	return truncateBytes(strings.TrimSpace(b.String()), maxTranscriptChars)
}

// contentText 取消息文本内容：字符串直接用；多模态数组只取 text 部分
// （图片 data URL 就地剥离）。
func contentText(m msg.Msg) string {
	if c, ok := m["content"].(string); ok {
		return stripDataURLs(c)
	}
	parts, ok := m["content"].([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok || pm["type"] != "text" {
			continue
		}
		if t, ok := pm["text"].(string); ok && t != "" {
			texts = append(texts, t)
		}
	}
	return stripDataURLs(strings.Join(texts, "\n"))
}

// stripDataURLs 剥离 base64 图片数据（保留标记说明有图）。
func stripDataURLs(s string) string {
	const marker = "data:"
	for {
		idx := strings.Index(s, marker)
		if idx < 0 {
			return s
		}
		end := idx + 4
		for end < len(s) && s[end] != '"' && s[end] != ')' && s[end] != ' ' && s[end] != '\n' {
			end++
		}
		s = s[:idx] + "（图片附件）" + s[end:]
	}
}

// truncateMiddle 中段截断：保留头部与尾部提示。头尾均按 UTF-8 边界安全截断，
// 避免在中文等多字节 rune 中间切片产生无效字节序列。
func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := max * 2 / 3
	tail := max / 3
	headStr := truncateBytes(s, head)
	tailStr := s[len(s)-tail:]
	for len(tailStr) > 0 && !utf8.RuneStart(tailStr[0]) {
		tailStr = tailStr[1:] // 切在 rune 中间：向后移到下一个 rune 起始
	}
	return headStr + "\n…（中段截断）\n" + tailStr
}

// ResetForTest 测试隔离：清空已蒸馏标记。
func ResetForTest() {
	distillMu.Lock()
	distillDone = map[string]bool{}
	distillMu.Unlock()
}
