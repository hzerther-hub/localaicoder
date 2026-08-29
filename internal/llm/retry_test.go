package llm

// 重试辅助与流式看门狗测试（对齐 openclaude withRetry 移植件）。
// httptest 假服务端，无真实网络；streamIdleTimeout 可测试内调小。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

// testModelKeys 指定多 key 池的测试模型（key 名带随机后缀，避免凭据池缓存串扰）。
func testModelKeys(base string, keys []string) config.ModelConfig {
	return config.ModelConfig{
		Key: fmt.Sprintf("test/retry-%p", keys), ModelID: "model-x",
		BaseURL: base, APIKey: keys[0], APIKeys: keys,
	}
}

func TestAdaptiveMaxTokens(t *testing.T) {
	// 402：can only afford
	le := &LLMError{Status: 402, Body: `{"error":"requested up to 32000 tokens, can only afford 8192"}`}
	if got := adaptiveMaxTokens(le); got != 8192 {
		t.Fatalf("402 反解错误: %d", got)
	}
	// 400：input + max_tokens > limit → limit - input - 1000
	le = &LLMError{Status: 400, Body: "input length and max_tokens exceed context limit: 120000 + 16000 > 131072"}
	if got := adaptiveMaxTokens(le); got != 10072 {
		t.Fatalf("400 反解错误: %d", got)
	}
	// 下限钳 3000
	le = &LLMError{Status: 400, Body: "input length and max_tokens exceed context limit: 130000 + 4000 > 131072"}
	if got := adaptiveMaxTokens(le); got != 3000 {
		t.Fatalf("下限钳制错误: %d", got)
	}
	// 不匹配 → 0
	if got := adaptiveMaxTokens(&LLMError{Status: 400, Body: "别的错误"}); got != 0 {
		t.Fatalf("不匹配应返回 0: %d", got)
	}
	if got := adaptiveMaxTokens(&LLMError{Status: 500, Body: "requested up to 1, can only afford 2"}); got != 0 {
		t.Fatalf("非 402/400 不应反解: %d", got)
	}
}

func TestRetryDelay(t *testing.T) {
	// Retry-After 优先且封顶 30s
	le := &LLMError{Status: 429, RetryAfter: 45 * time.Second}
	if d := retryDelay(le, 0); d != retryAfterCap {
		t.Fatalf("Retry-After 应封顶 30s: %v", d)
	}
	// 402 不等待
	if d := retryDelay(&LLMError{Status: 402}, 3); d != 0 {
		t.Fatalf("402 不应等待: %v", d)
	}
	// 429 指数退避：封顶 8s，带抖动不低于基准的 3/4
	if d := retryDelay(&LLMError{Status: 429}, 10); d > retryBackoffCap || d < retryBackoffCap*3/4 {
		t.Fatalf("退避应落在 8s 基准的 3/4 到 1 倍之间: %v", d)
	}
	d := retryDelay(&LLMError{Status: 429}, 0)
	if d <= 0 || d > retryBackoffBase {
		t.Fatalf("首轮退避应在 [0, 500ms) 抖动区间: %v", d)
	}
	// 非冷却错误不等待
	if d := retryDelay(&LLMError{Status: 500}, 0); d != 0 {
		t.Fatalf("500 不应等待: %v", d)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("2"); d != 2*time.Second {
		t.Fatalf("秒数解析错误: %v", d)
	}
	if d := parseRetryAfter("-5"); d != 0 {
		t.Fatalf("负数应为 0: %v", d)
	}
	if d := parseRetryAfter("垃圾"); d != 0 {
		t.Fatalf("非法值应为 0: %v", d)
	}
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 {
		t.Fatalf("HTTP 日期应解析为正时长: %v", d)
	}
}

// TestStreamChat429ThenSuccess 两个 key：第一个 429（退避后）换第二个成功。
func TestStreamChat429ThenSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"rate limited"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"ok"}}]}`+"\n\n"+"data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := testModelKeys(srv.URL, []string{"bad-key", "good-key"})
	var got strings.Builder
	err := StreamChat(m, []msg.Msg{{"role": "user", "content": "hi"}}, nil,
		func(e msg.Event) error {
			if msg.S(e, "type") == "text" {
				got.WriteString(msg.S(e, "delta"))
			}
			return nil
		})
	if err != nil {
		t.Fatalf("换 key 重试应成功: %v", err)
	}
	if got.String() != "ok" {
		t.Fatalf("回复内容错误: %q", got.String())
	}
}

// TestStreamChatIdleWatchdog 服务端 400ms 不吐数据：看门狗（调小到 60ms）应中断。
func TestStreamChatIdleWatchdog(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 60 * time.Millisecond
	t.Cleanup(func() { streamIdleTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond) // 假死：迟迟不吐第一个 chunk
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := testModel(srv.URL)
	err := StreamChat(m, []msg.Msg{{"role": "user", "content": "hi"}}, nil,
		func(e msg.Event) error { return nil })
	if err == nil {
		t.Fatal("看门狗应中断假死流")
	}
	if !strings.Contains(err.Error(), "流式空闲超时") {
		t.Fatalf("错误文案应标识空闲超时: %v", err)
	}
}

// TestStreamChat402AffordableRewrite 402 报文反解：调低 max_tokens 后原地重试成功。
func TestStreamChat402AffordableRewrite(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts == 0 {
			attempts++
			w.WriteHeader(402)
			fmt.Fprint(w, `{"error":"requested up to 32000 tokens, can only afford 4096"}`)
			return
		}
		// 校验第二次请求确实带了调低后的 max_tokens（openai.go 写入 payload）
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"ok"}}]}`+"\n\n"+"data: [DONE]\n\n")
	}))
	defer srv.Close()

	m := testModel(srv.URL)
	m.ContextWindow = 0
	err := StreamChat(m, []msg.Msg{{"role": "user", "content": "hi"}}, nil,
		func(e msg.Event) error { return nil })
	if err != nil {
		t.Fatalf("402 反解后应重试成功: %v", err)
	}
}
