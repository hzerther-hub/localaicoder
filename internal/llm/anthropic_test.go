package llm

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

// newKeyServer 记录每次请求的 Authorization 头，首个 key 返回 401，
// 其余返回 OpenAI 风格文本流（用于换 key 端到端测试）。
func newKeyServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	var n int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&n, 1)
		*seen = append(*seen, r.Header.Get("Authorization"))
		if i == 1 {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)))
	}))
}

func TestTransportDispatch(t *testing.T) {
	cases := map[string]string{
		"":                   "chat_completions",
		"chat_completions":   "chat_completions",
		"opencode":           "chat_completions",
		"anthropic_messages": "anthropic_messages",
		"responses":          "responses",
		"gemini":             "gemini",
		"bogus":              "chat_completions",
	}
	for in, want := range cases {
		if got := config.NormalizedFormat(in); got != want {
			t.Fatalf("NormalizedFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicURLJoin(t *testing.T) {
	cases := map[string]string{
		"https://api.anthropic.com":            "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":         "https://api.anthropic.com/v1/messages",
		"https://x.cn/v1/messages":             "https://x.cn/v1/messages",
		"https://x.cn/api/anthropic":           "https://x.cn/api/anthropic/v1/messages",
		"https://x.cn/v1/messages?beta=true":   "https://x.cn/v1/messages?beta=true",
	}
	for base, want := range cases {
		if got := anthropicURL(base); got != want {
			t.Fatalf("anthropicURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestAnthropicMessageConversion(t *testing.T) {
	messages := []msg.Msg{
		{"role": "system", "content": "你是助手"},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "看这张图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:image/png;base64,QUJD"}},
		}},
		{"role": "assistant", "content": "我看一下",
			"tool_calls": []any{map[string]any{
				"id": "t1", "type": "function",
				"function": map[string]any{"name": "read_file", "arguments": `{"path":"a.go"}`},
			}}},
		{"role": "tool", "tool_call_id": "t1", "content": "文件内容"},
	}
	system, out := toAnthropicMessages(messages)
	if system != "你是助手" {
		t.Fatalf("system 应提取顶层, got %q", system)
	}
	if len(out) != 3 {
		t.Fatalf("应有 3 条消息(system 提取后), got %d: %v", len(out), out)
	}
	// user：文本 + 图片块
	userBlocks, _ := out[0]["content"].([]any)
	if len(userBlocks) != 2 {
		t.Fatalf("用户消息应有文本+图片两块, got %v", userBlocks)
	}
	img := userBlocks[1].(map[string]any)
	src := img["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != "QUJD" {
		t.Fatalf("图片块转换不符: %v", img)
	}
	// assistant：text + tool_use
	asstBlocks, _ := out[1]["content"].([]any)
	if len(asstBlocks) != 2 || msg.S(asstBlocks[1].(map[string]any), "type") != "tool_use" {
		t.Fatalf("assistant 应有 text+tool_use, got %v", asstBlocks)
	}
	tu := asstBlocks[1].(map[string]any)
	if tu["id"] != "t1" || msg.S(tu["input"].(map[string]any), "path") != "a.go" {
		t.Fatalf("tool_use 转换不符: %v", tu)
	}
	// tool → user + tool_result（与 tool_use 分属两条 user 消息？不——连续同角色应合并）
	lastBlocks, _ := out[2]["content"].([]any)
	if msg.S(lastBlocks[0].(map[string]any), "type") != "tool_result" {
		t.Fatalf("tool 消息应转为 tool_result 块: %v", lastBlocks)
	}
	if lastBlocks[0].(map[string]any)["tool_use_id"] != "t1" {
		t.Fatal("tool_use_id 应为 t1")
	}
}

func TestAnthropicStreamParsing(t *testing.T) {
	st := &anthropicStream{onEvent: func(msg.Event) error { return nil }, acc: map[int]*slot{}}
	fixture := []string{
		`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":10,"cache_read_input_tokens":4}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t9","name":"grep_search"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pat"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"tern\":\"x\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
	var events []msg.Event
	st.onEvent = func(e msg.Event) error { events = append(events, e); return nil }
	for _, d := range fixture {
		if err := st.handle(d); err != nil {
			t.Fatal(err)
		}
	}
	var text, toolCalls []any
	var finish string
	var usage map[string]any
	for _, e := range events {
		switch msg.S(e, "type") {
		case "text":
			text = append(text, msg.S(e, "delta"))
		case "tool_calls":
			toolCalls = msg.L(e, "tool_calls")
		case "finish":
			finish = msg.S(e, "reason")
		case "usage":
			usage = msg.M(e, "usage")
		}
	}
	if len(text) != 1 || text[0] != "你好" {
		t.Fatalf("文本事件不符: %v", text)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("应产出 1 个 tool_call, got %v", toolCalls)
	}
	fn := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if msg.S(fn, "name") != "grep_search" || msg.S(fn, "arguments") != `{"pattern":"x"}` {
		t.Fatalf("tool_call 参数累积不符: %v", fn)
	}
	if finish != "tool_calls" {
		t.Fatalf("stop_reason tool_use 应映射为 tool_calls, got %q", finish)
	}
	if msg.I(usage, "prompt_tokens") != 14 || msg.I(usage, "cached_tokens") != 4 || msg.I(usage, "completion_tokens") != 7 {
		t.Fatalf("usage 换算不符: %v", usage)
	}
}

func TestAnthropicErrorEvent(t *testing.T) {
	st := &anthropicStream{acc: map[int]*slot{}}
	err := st.handle(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if err == nil || err.Error() != "服务端错误: Overloaded" {
		t.Fatalf("error 事件应转 LLMError, got %v", err)
	}
}
