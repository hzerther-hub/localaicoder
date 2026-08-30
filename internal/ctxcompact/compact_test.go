package ctxcompact

import (
	"strings"
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

func TestEstimateTextTokens(t *testing.T) {
	if got := EstimateTextTokens(""); got != 0 {
		t.Fatalf("空串应为 0, got %d", got)
	}
	zh := EstimateTextTokens("你好世界") // 4 CJK ≈ 4 tokens
	if zh != 4 {
		t.Fatalf("4 个汉字应约 4 token, got %d", zh)
	}
	en := EstimateTextTokens("abcdefgh") // 8 ASCII ≈ 2 token
	if en != 2 {
		t.Fatalf("8 ASCII 应约 2 token, got %d", en)
	}
}

func TestEstimateTokensMultimodal(t *testing.T) {
	msgs := []msg.Msg{
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "hi"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
		}},
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"function": map[string]any{"name": "f", "arguments": "12345678"}},
		}},
	}
	n := EstimateTokens(msgs)
	// text "hi"(≈1)+8 + image 1100 + arguments 8 字符(≈2)+16
	if n < 1100 || n > 1200 {
		t.Fatalf("多模态估算不符: %d", n)
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("a", 1000)
	out := Truncate(s, 100)
	if len(out) <= 100 || len(out) > 140 {
		t.Fatalf("截断长度不符: %d", len(out))
	}
	if !strings.Contains(out, "已压缩，原 1000 字符") {
		t.Fatal("截断标记缺失")
	}
}

func TestRoundsGrouping(t *testing.T) {
	msgs := []msg.Msg{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "q1"},
		{"role": "assistant", "content": "", "tool_calls": []any{}},
		{"role": "tool", "tool_call_id": "1", "content": "r1"},
		{"role": "tool", "tool_call_id": "2", "content": "r2"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "q2"},
	}
	rounds := Rounds(msgs)
	if len(rounds) != 4 {
		t.Fatalf("应分 4 轮, got %d: %v", len(rounds), rounds)
	}
	if len(rounds[1]) != 3 { // assistant + 2 tool
		t.Fatalf("第 2 轮应含 assistant+2 tool, got %v", rounds[1])
	}
}

func TestMaybeCompactUnderBudgetNoop(t *testing.T) {
	msgs := []msg.Msg{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "world"},
	}
	out := MaybeCompact(msgs, nil, nil)
	if len(out) != 3 {
		t.Fatal("预算内不应压缩")
	}
}

func TestMaybeCompactFoldsMiddleRounds(t *testing.T) {
	big := strings.Repeat("x", 400) // 单条 100 token
	var msgs []msg.Msg = []msg.Msg{{"role": "system", "content": "s"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			msg.Msg{"role": "user", "content": big},
			msg.Msg{"role": "assistant", "content": big, "tool_calls": []any{
				map[string]any{"id": "t", "type": "function",
					"function": map[string]any{"name": "read_file", "arguments": "{}"}},
			}},
			msg.Msg{"role": "tool", "tool_call_id": "t", "content": big},
		)
	}
	// 小预算模型 → 触发折叠
	model := &config.ModelConfig{Key: "test/small", ContextWindow: 4096}
	var compactEvents []msg.Event
	out := MaybeCompact(msgs, func(e msg.Event) { compactEvents = append(compactEvents, e) }, model)
	if len(out) >= len(msgs) {
		t.Fatalf("折叠后应更短: %d -> %d", len(msgs), len(out))
	}
	if len(compactEvents) == 0 {
		t.Fatal("应发出 context_compact 事件")
	}
	// system + 首个 user 原文 + 折叠摘要
	if !strings.Contains(msg.S(out[2], "content"), "历史对话已压缩摘要") {
		t.Fatalf("折叠摘要消息应在第 3 位: %v", out[2])
	}
	// 最近 2 轮保留原文
	last := out[len(out)-1]
	if msg.Role(last) != "tool" {
		t.Fatalf("最后应保留原文 tool 消息, got %v", msg.Role(last))
	}
}

func TestEffectiveBudget(t *testing.T) {
	if EffectiveBudget(nil) != config.ContextBudget {
		t.Fatal("无模型用全局预算")
	}
	local := &config.ModelConfig{Key: "gpulocal-1/m", ContextWindow: 0}
	if b := EffectiveBudget(local); b != 131072-(16384+1024) {
		t.Fatalf("gpulocal 兜底预算不符: %d", b)
	}
	small := &config.ModelConfig{Key: "x/m", ContextWindow: 8192}
	wantOut := 8192 / 4 // 2048，小于全局 256000，故取窗口/4
	if b := EffectiveBudget(small); b != 8192-(wantOut+1024) {
		t.Fatalf("小窗口预算不符: %d", b)
	}
}
