package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"localai/internal/cache"
	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/tools"
)

func setup(t *testing.T) (string, *config.ModelConfig) {
	t.Helper()
	dir := t.TempDir()
	config.SetDir(dir)
	cache.Reset()
	t.Cleanup(func() {
		config.SetDir("")
		cache.Reset()
	})
	// 默认模型指向测试服务器（由调用方传入 URL 再改）
	models, def := config.LoadModels()
	_ = def
	m := &models[0]
	return dir, m
}

func sseHandler(fn func(reqCount int32) string) *httptest.Server {
	var n int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, fn(i))
	}))
}

func sse(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: " + l + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// toolCallJSON 生成带工具调用的 SSE 块（arguments 是 JSON 转义后的字符串，
// 与真实服务端一致）。
func toolCallJSON(name, args string) string {
	escaped, _ := json.Marshal(args)
	return fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call1","function":{"name":"%s","arguments":%s}}]},"finish_reason":"tool_calls"}]}`,
		name, string(escaped))
}

func collectTypes(events *[]msg.Event) func(msg.Event) {
	return func(e msg.Event) { *events = append(*events, e) }
}

func TestAgentToolLoop(t *testing.T) {
	ws, model := setup(t)
	srv := sseHandler(func(i int32) string {
		if i == 1 { // 第一轮：发起工具调用
			return sse(toolCallJSON("read_file", `{"path":"note.txt"}`))
		}
		// 第二轮：基于工具结果给最终回答
		return sse(
			`{"choices":[{"delta":{"content":"文件内容是"}}]}`,
			`{"choices":[{"delta":{"content":"测试"}},{"delta":{}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		)
	})
	model.BaseURL = srv.URL

	_ = osWriteFile(ws, "note.txt", "测试内容")
	restore := tools.PushWorkspace(ws)
	defer restore()

	var events []msg.Event
	a := New(collectTypes(&events), nil, nil, ModeAlways, model)
	final, err := a.Run("读 note.txt 并告诉我内容", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(final, "文件内容是") {
		t.Fatalf("最终文本不符: %q", final)
	}
	var sawToolStart, sawToolResult, sawUsage bool
	for _, e := range events {
		switch msg.S(e, "type") {
		case "tool_start":
			sawToolStart = msg.S(e, "name") == "read_file"
		case "tool_result":
			sawToolResult = strings.Contains(msg.S(e, "result"), "测试内容")
		case "usage":
			sawUsage = true
		}
	}
	if !sawToolStart || !sawToolResult {
		t.Fatalf("应发出工具事件: start=%v result=%v", sawToolStart, sawToolResult)
	}
	if !sawUsage || a.UsageTotal.Requests != 1 || a.UsageTotal.TotalTokens != 7 {
		t.Fatalf("usage 累计不符: %+v", a.UsageTotal)
	}
	// 消息序列：system + user + assistant(tool_calls) + tool + assistant
	if len(a.Messages) != 5 {
		t.Fatalf("消息序列长度应 5, got %d", len(a.Messages))
	}
	if msg.Role(a.Messages[3]) != "tool" || msg.S(a.Messages[3], "tool_call_id") != "call1" {
		t.Fatalf("tool 回填消息不符: %v", a.Messages[3])
	}
}

func TestAgentAskModeDeny(t *testing.T) {
	ws, model := setup(t)
	srv := sseHandler(func(i int32) string {
		if i == 1 {
			return sse(toolCallJSON("write_file", `{"path":"x.txt","content":"1"}`))
		}
		return sse(`{"choices":[{"delta":{"content":"好的"}}],"usage":null}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	})
	model.BaseURL = srv.URL
	restore := tools.PushWorkspace(ws)
	defer restore()

	var events []msg.Event
	denied := false
	a := New(collectTypes(&events), func(name string, args map[string]any, summary string) bool {
		return false // 拒绝一切
	}, nil, ModeAsk, model)
	if _, err := a.Run("写个文件", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if msg.S(e, "type") == "tool_denied" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("ask 模式拒绝应发出 tool_denied")
	}
	// 拒绝结果进 tool 消息
	toolMsg := a.Messages[3]
	if !strings.Contains(msg.S(toolMsg, "content"), "用户拒绝") {
		t.Fatalf("拒绝结果应回填: %v", toolMsg)
	}
}

func TestAgentLLMCacheHit(t *testing.T) {
	ws, model := setup(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"content":"缓存答案"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	}))
	model.BaseURL = srv.URL
	restore := tools.PushWorkspace(ws)
	defer restore()

	a1 := New(func(msg.Event) {}, nil, nil, ModeAlways, model)
	if _, err := a1.Run("同样的问题", nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("首次应真调后端, got %d", calls)
	}
	a2 := New(func(msg.Event) {}, nil, nil, ModeAlways, model)
	if _, err := a2.Run("同样的问题", nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("第二次应命中缓存不再请求, got %d", calls)
	}
	if a2.CacheHits != 1 {
		t.Fatalf("应记一次缓存命中, got %d", a2.CacheHits)
	}
}

func TestAgentCloudFallback(t *testing.T) {
	_, model := setup(t)
	// 本地模型 → 503；云端回退正常回答
	var localHits int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&localHits, 1)
		http.Error(w, "loading", http.StatusServiceUnavailable)
	}))
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"content":"云端回答"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	}))
	model.Key = "gpulocal-8097/qwen"
	model.BaseURL = local.URL

	// 写入云端回退配置：dispatch_flash 指向 test/flash（cloud.URL 端点）
	data := config.LoadModelsData()
	data["dispatch_flash"] = "test/flash"
	data["auto_cloud_fallback"] = true
	data["providers"] = append(msg.L(data, "providers"), map[string]any{
		"id": "test", "name": "T", "base_url": cloud.URL, "api_key": "k",
		"models": []any{map[string]any{"id": "flash", "name": "Flash"}},
	})
	config.SaveModelsData(data)

	var events []msg.Event
	a := New(collectTypes(&events), nil, nil, ModeAlways, model)
	final, err := a.Run("hi", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(final, "云端回答") {
		t.Fatalf("应回退到云端作答: %q (err=%v)", final, err)
	}
	if localHits == 0 {
		t.Fatal("应先尝试本地")
	}
	var switched bool
	for _, e := range events {
		if msg.S(e, "type") == "model_switch" {
			switched = true
		}
	}
	if !switched {
		t.Fatal("应发出 model_switch 事件")
	}
	if a.Model.Key != "test/flash" {
		t.Fatalf("Agent 模型应已切换: %s", a.Model.Key)
	}
}

func TestAgentStop(t *testing.T) {
	ws, model := setup(t)
	srv := sseHandler(func(i int32) string {
		return sse(
			`{"choices":[{"delta":{"content":"很长的回答开始"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	})
	model.BaseURL = srv.URL
	restore := tools.PushWorkspace(ws)
	defer restore()

	a := New(func(msg.Event) {}, nil, func() bool { return true }, ModeAlways, model)
	final, err := a.Run("x", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(final, "已按用户请求停止") {
		t.Fatalf("应带停止提示退出: %q", final)
	}
}

// ---------------- helpers ----------------

func osWriteFile(dir, name, content string) string {
	p := dir + "/" + name
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return p
	}
	return p
}
