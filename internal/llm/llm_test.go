package llm

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: " + l + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func testModel(base string) config.ModelConfig {
	return config.ModelConfig{
		Key: "test/model-x", ModelID: "model-x", BaseURL: base, APIKey: "k",
	}
}

func TestStreamChatTextAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"content":"你"}}]}`,
			`{"choices":[{"delta":{"content":"好"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"思考中"}}]}`,
			`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	var events []msg.Event
	err := StreamChat(testModel(srv.URL), []msg.Msg{{"role": "user", "content": "hi"}}, nil,
		func(e msg.Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, e := range events {
		switch msg.S(e, "type") {
		case "text":
			text += msg.S(e, "delta")
		case "usage":
			u := msg.M(e, "usage")
			if msg.I(u, "prompt_tokens") != 10 || msg.I(u, "cached_tokens") != 4 {
				t.Fatalf("usage 归一化不符: %v", u)
			}
		case "finish":
			if msg.S(e, "reason") != "stop" {
				t.Fatalf("finish_reason 应为 stop: %v", e)
			}
		}
	}
	if text != "你好" {
		t.Fatalf("文本应拼接为 你好, got %q", text)
	}
}

func TestStreamChatToolCallsAccumulate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.py\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		))
	}))
	defer srv.Close()

	var toolCalls []any
	err := StreamChat(testModel(srv.URL), []msg.Msg{{"role": "user", "content": "x"}},
		nil, func(e msg.Event) error {
			if msg.S(e, "type") == "tool_calls" {
				toolCalls = msg.L(e, "tool_calls")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("应累积出 1 个 tool_call, got %d", len(toolCalls))
	}
	tc := toolCalls[0].(map[string]any)
	fn := tc["function"].(map[string]any)
	if msg.S(fn, "name") != "read_file" || msg.S(fn, "arguments") != `{"path":"a.py"}` {
		t.Fatalf("tool_call 累积不符: %v", tc)
	}
	if msg.S(tc, "type") != "function" {
		t.Fatal("tool_call type 应为 function")
	}
}

func TestStreamChatServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(`{"error":{"message":"boom","code":500}}`))
	}))
	defer srv.Close()
	err := StreamChat(testModel(srv.URL), []msg.Msg{{"role": "user", "content": "x"}},
		nil, func(e msg.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "服务端错误: boom") {
		t.Fatalf("应以 LLMError 暴露服务端错误, got %v", err)
	}
}

func TestStreamChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := StreamChat(testModel(srv.URL), []msg.Msg{{"role": "user", "content": "x"}},
		nil, func(e msg.Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("应为 HTTP 错误, got %v", err)
	}
}

func TestCooperativeStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"content":"a"}}]}`,
			`{"choices":[{"delta":{"content":"b"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()
	stopped := false
	err := StreamChat(testModel(srv.URL), []msg.Msg{{"role": "user", "content": "x"}},
		nil, func(e msg.Event) error {
			if msg.S(e, "type") == "text" && !stopped {
				stopped = true
				return ErrStop
			}
			return nil
		})
	if !errors.Is(err, ErrStop) {
		t.Fatalf("协作停止应返回 ErrStop, got %v", err)
	}
	if !stopped {
		t.Fatal("应已触发停止")
	}
}
