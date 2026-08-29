package llm

import (
	"strings"
	"testing"

	"localai/internal/msg"
)

func TestGeminiURLJoin(t *testing.T) {
	cases := [][2]string{
		{"https://generativelanguage.googleapis.com",
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-x:streamGenerateContent?alt=sse"},
		{"https://generativelanguage.googleapis.com/v1beta",
			"https://generativelanguage.googleapis.com/v1beta/models/gemini-x:streamGenerateContent?alt=sse"},
	}
	for _, c := range cases {
		if got := geminiURL(c[0], "gemini-x"); got != c[1] {
			t.Fatalf("geminiURL(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestGeminiContentsConversion(t *testing.T) {
	messages := []msg.Msg{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "画了什么 data:image/jpeg;base64,QUJD"},
		{"role": "assistant", "content": "看看",
			"tool_calls": []any{map[string]any{
				"id": "g1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{"path":"b.txt"}`},
			}}},
		{"role": "tool", "tool_call_id": "g1", "content": "内容"},
	}
	system, contents := toGeminiContents(messages)
	if system != "sys" {
		t.Fatalf("system_instruction 提取不符: %q", system)
	}
	if len(contents) != 3 {
		t.Fatalf("应有 3 条 contents, got %d: %v", len(contents), contents)
	}
	// model 轮：text + functionCall
	parts, _ := contents[1]["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("model 轮应 text+functionCall, got %v", parts)
	}
	fc := parts[1].(map[string]any)["functionCall"].(map[string]any)
	if msg.S(fc, "name") != "read_file" || msg.S(fc["args"].(map[string]any), "path") != "b.txt" {
		t.Fatalf("functionCall 转换不符: %v", fc)
	}
	// tool → functionResponse（名字从 id 映射回来）
	respParts, _ := contents[2]["parts"].([]any)
	fr := respParts[0].(map[string]any)["functionResponse"].(map[string]any)
	if msg.S(fr, "name") != "read_file" {
		t.Fatalf("functionResponse 应映射回工具名, got %v", fr)
	}
	if msg.S(fr["response"].(map[string]any), "result") != "内容" {
		t.Fatalf("functionResponse 内容不符: %v", fr)
	}
}

func TestGeminiSchemaSanitize(t *testing.T) {
	in := map[string]any{
		"type":                 "object",
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "$schema": "x"},
			"list": map[string]any{"type": "array", "items": map[string]any{"type": "string", "additionalProperties": true}},
		},
	}
	out := geminiSanitizeSchema(in).(map[string]any)
	if _, ok := out["$schema"]; ok {
		t.Fatal("应剔除 $schema")
	}
	if _, ok := out["additionalProperties"]; ok {
		t.Fatal("应剔除 additionalProperties")
	}
	props := out["properties"].(map[string]any)
	if _, ok := props["path"].(map[string]any)["$schema"]; ok {
		t.Fatal("嵌套 schema 也应剔除")
	}
	items := props["list"].(map[string]any)["items"].(map[string]any)
	if _, ok := items["additionalProperties"]; ok {
		t.Fatal("items 内也应剔除")
	}
}

func TestGeminiStreamParsing(t *testing.T) {
	var events []msg.Event
	st := &geminiStream{
		onEvent: func(e msg.Event) error { events = append(events, e); return nil },
		acc:     map[int]*slot{},
	}
	fixture := []string{
		`{"candidates":[{"content":{"parts":[{"text":"想","thought":true},{"text":"答"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"grep_search","args":{"pattern":"x"}}}]}}]}`,
		`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":3,"thoughtsTokenCount":2,"cachedContentTokenCount":5}}`,
		`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":3}}`,
	}
	for _, d := range fixture {
		if err := st.handle(d); err != nil {
			t.Fatal(err)
		}
	}
	var reasoning, text string
	var toolCalls []any
	var finish string
	for _, e := range events {
		switch msg.S(e, "type") {
		case "text":
			text += msg.S(e, "delta")
		case "reasoning":
			reasoning += msg.S(e, "delta")
		case "tool_calls":
			toolCalls = msg.L(e, "tool_calls")
		case "finish":
			finish = msg.S(e, "reason")
		}
	}
	if reasoning != "想" || text != "答" {
		t.Fatalf("thought→reasoning 映射不符: %q %q", reasoning, text)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("应产出 1 个 tool_call: %v", toolCalls)
	}
	fn := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if msg.S(fn, "name") != "grep_search" || !strings.Contains(msg.S(fn, "arguments"), `"pattern"`) {
		t.Fatalf("functionCall args 应序列化为 JSON 字符串: %v", fn)
	}
	if finish != "tool_calls" {
		t.Fatalf("有 functionCall 时 finish 应为 tool_calls, got %q", finish)
	}
}
