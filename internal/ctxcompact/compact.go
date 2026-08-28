// Package ctxcompact 上下文管理（类 DeepSeek Harness）：预算 + 渐进压缩
//（对译 Python context.py；Go 版用启发式 token 估计，tiktoken 可后置接入）。
package ctxcompact

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"localai/internal/config"
	"localai/internal/msg"
)

// CJK 统一表意文字 + 扩展 A + 兼容表意 + 全角标点/符号（≈1 字 1 token）
var cjkRe = regexp.MustCompile(
	`[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}\x{3000}-\x{303F}\x{FF00}-\x{FFEF}]`)

// ImageTokenEstimate 多模态消息里一张图片的粗估 token。
const ImageTokenEstimate = 1100

// EstimateTextTokens 估算单段文本的 token 数（CJK ≈1 token/字，ASCII ≈1 token/4 字符）。
func EstimateTextTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := utf8.RuneCountInString(s)
	cjk := len(cjkRe.FindAllString(s, -1))
	return cjk + (runes-cjk+3)/4
}

// EstimateTokens 估算消息列表的 token 数（支持多模态 content 列表；含结构开销）。
func EstimateTokens(messages []msg.Msg) int {
	n := 0
	for _, m := range messages {
		switch c := m["content"].(type) {
		case string:
			n += EstimateTextTokens(c) + 8
		case []any:
			for _, pv := range c {
				part, ok := pv.(map[string]any)
				if !ok {
					n += 32
					continue
				}
				switch msg.S(part, "type") {
				case "text":
					n += EstimateTextTokens(msg.S(part, "text")) + 8
				case "image_url", "image", "input_image":
					n += ImageTokenEstimate
				default:
					n += 32
				}
			}
		default:
			n += 200
		}
		for _, tcv := range msg.L(m, "tool_calls") {
			tc, ok := tcv.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn != nil {
				n += EstimateTextTokens(msg.S(fn, "arguments")) + 16
			}
		}
	}
	return n
}

// Truncate 保头尾截断（头 2/3 尾 1/3，带原始长度标记）。
func Truncate(s string, keep int) string {
	if len(s) <= keep {
		return s
	}
	head := keep * 2 / 3
	tail := keep - head
	return s[:head] + fmt.Sprintf("\n…[已压缩，原 %d 字符]\n", len(s)) + s[len(s)-tail:]
}

// Rounds 把 messages[1:] 按轮次分组。
// 一轮 = 一条 assistant 消息（可带 tool_calls）+ 其后连续的 tool 消息；
// user 消息单独成轮。
func Rounds(messages []msg.Msg) [][]int {
	var rounds [][]int
	i := 1
	for i < len(messages) {
		if msg.Role(messages[i]) == "assistant" {
			idxs := []int{i}
			j := i + 1
			for j < len(messages) && msg.Role(messages[j]) == "tool" {
				idxs = append(idxs, j)
				j++
			}
			rounds = append(rounds, idxs)
			i = j
		} else {
			rounds = append(rounds, []int{i})
			i++
		}
	}
	return rounds
}

// ProtectedIndices 最近 keepFrom 之后各轮的下标集合（这些消息不压缩图片）。
func ProtectedIndices(rounds [][]int, keepFrom int) map[int]bool {
	keep := map[int]bool{}
	for _, idxs := range rounds[keepFrom:] {
		for _, i := range idxs {
			keep[i] = true
		}
	}
	return keep
}

// stripOldImages 把旧消息里的 image_url 部分换成占位文本；changed 表示是否替换过。
func stripOldImages(content []any) (out []any, changed bool) {
	for _, pv := range content {
		if part, ok := pv.(map[string]any); ok && msg.S(part, "type") == "image_url" {
			out = append(out, map[string]any{
				"type": "text", "text": "[早期图片已省略以压缩上下文]"})
			changed = true
			continue
		}
		out = append(out, pv)
	}
	return out, changed
}

// RoundSummary 把一轮压成一行摘要。
func RoundSummary(messages []msg.Msg, idxs []int) string {
	head := messages[idxs[0]]
	var parts []string
	if text := msg.S(head, "content"); strings.TrimSpace(text) != "" {
		t := strings.ReplaceAll(strings.TrimSpace(text), "\n", " ")
		if len(t) > 120 {
			t = t[:120]
		}
		parts = append(parts, "输出『"+t+"』")
	}
	var names []string
	for _, tcv := range msg.L(head, "tool_calls") {
		if tc, ok := tcv.(map[string]any); ok {
			if fn, ok := tc["function"].(map[string]any); ok {
				if n := msg.S(fn, "name"); n != "" {
					names = append(names, n)
				}
			}
		}
	}
	if len(names) > 0 {
		parts = append(parts, "调用工具 "+strings.Join(names, "/"))
	}
	if len(parts) == 0 {
		return "· （空轮）"
	}
	return "· " + strings.Join(parts, "；")
}

// EffectiveBudget 按当前模型返回生效的上下文预算。
func EffectiveBudget(model *config.ModelConfig) int {
	budget := config.ContextBudget
	var win int
	var key string
	if model != nil {
		win = model.ContextWindow
		key = model.Key
	}
	outMax := config.MaxTokens
	if strings.HasPrefix(key, "gpulocal") {
		outMax = 16384
	}
	margin := outMax + 1024
	if strings.HasPrefix(key, "gpulocal") {
		if win == 0 {
			win = 131072 // 本地窗口常被同步重置为 0，按已知 131072 兜底
		}
		b := win - margin
		if b < 1024 {
			b = 1024
		}
		return b
	}
	if win > 0 {
		b := win - margin
		if b < budget {
			return b
		}
		return budget
	}
	return budget
}

// MaybeCompact 超过预算时压缩；返回（可能新的）消息列表。
// emit 用于向 UI 报告 {"type":"context_compact","before":n,"after":n}。
func MaybeCompact(messages []msg.Msg, emit func(msg.Event), model *config.ModelConfig) []msg.Msg {
	budget := EffectiveBudget(model)
	keepRounds := config.ContextKeepRounds
	toolKeep := config.ToolResultKeep

	before := EstimateTokens(messages)
	if before <= budget {
		return messages
	}

	rounds := Rounds(messages)
	keepFrom := len(rounds) - keepRounds
	if keepFrom < 0 {
		keepFrom = 0
	}

	// ---- 阶段 0：任何超长工具结果就地截断 ----
	oldToolIdx := map[int]bool{}
	for ri, idxs := range rounds {
		if ri < keepFrom {
			for _, i := range idxs {
				if msg.Role(messages[i]) == "tool" {
					oldToolIdx[i] = true
				}
			}
		}
	}

	protected := ProtectedIndices(rounds, keepFrom)
	msgs := make([]msg.Msg, len(messages))
	for i, m := range messages {
		msgs[i] = m
	}
	changed := false
	for i, m := range msgs {
		if msg.Role(m) == "tool" {
			if s, ok := m["content"].(string); ok {
				limit := toolKeep
				if oldToolIdx[i] {
					limit = 400
				}
				if len(s) > limit {
					c := copyMsg(m)
					c["content"] = Truncate(s, limit)
					msgs[i] = c
					changed = true
				}
			}
		}
		// 多模态 user 消息：旧轮次里的图片 data URL 换成占位文本
		if content, ok := m["content"].([]any); ok && !protected[i] {
			newContent, did := stripOldImages(content)
			if did {
				c := copyMsg(m)
				c["content"] = newContent
				msgs[i] = c
				changed = true
			}
		}
	}

	// 没有可折叠的中间轮：截断后即返回
	if len(rounds) <= keepRounds+1 {
		after := EstimateTokens(msgs)
		if emit != nil && changed && after < before {
			emit(msg.Event{"type": "context_compact", "before": before, "after": after})
		}
		return msgs
	}

	// ---- 阶段 2：仍超预算 → 折叠中间轮 ----
	if EstimateTokens(msgs) > budget {
		out := []msg.Msg{msgs[0]} // system
		var midRange []int
		if len(rounds) > 0 && rounds[0][0] == 1 {
			out = append(out, msgs[1]) // 首个 user 原文保留
			for r := 1; r < keepFrom; r++ {
				midRange = append(midRange, r)
			}
		} else {
			for r := 0; r < keepFrom; r++ {
				midRange = append(midRange, r)
			}
		}
		var lines []string
		for _, r := range midRange {
			lines = append(lines, RoundSummary(msgs, rounds[r]))
		}
		if len(lines) > 0 {
			out = append(out, msg.Msg{
				"role": "assistant",
				"content": "（历史对话已压缩摘要）\n" + strings.Join(lines, "\n"),
			})
		}
		for r := keepFrom; r < len(rounds); r++ {
			for _, idx := range rounds[r] {
				out = append(out, msgs[idx])
			}
		}
		msgs = out
	}

	after := EstimateTokens(msgs)
	if emit != nil && after < before {
		emit(msg.Event{"type": "context_compact", "before": before, "after": after})
	}
	return msgs
}

func copyMsg(m msg.Msg) msg.Msg {
	c := make(msg.Msg, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
