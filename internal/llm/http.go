package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	sharedClientOnce sync.Once
	sharedClient     *http.Client
)

// httpClient 返回共享的 HTTP 客户端（显式不走系统代理，对齐 Python
// ProxyHandler({})）。单例化让连接池跨请求复用：多轮 agent 循环里每个
// 请求都重新握手（TCP + TLS）是纯浪费，复用后显著降低延迟与端口压力。
func httpClient() *http.Client {
	sharedClientOnce.Do(func() {
		sharedClient = &http.Client{
			Timeout: 300 * time.Second,
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConnsPerHost: 4,
			},
		}
	})
	return sharedClient
}

// postJSONCtx 发一次带 context 的 POST；非 2xx 时关闭并返回带 Status/Body/
// RetryAfter 的 LLMError（Body 供 402/400 报文反解，RetryAfter 供退避遵守）。
func postJSONCtx(ctx context.Context, url string, header map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, &LLMError{Msg: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errStreamIdle // 看门狗取消：折算为流式空闲超时
		}
		return nil, &LLMError{Msg: "连接失败: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		le := &LLMError{
			Msg:    fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b)),
			Status: resp.StatusCode,
			Body:   string(b),
		}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			le.RetryAfter = parseRetryAfter(ra)
		}
		return nil, le
	}
	return resp, nil
}

// parseRetryAfter 解析 Retry-After 头：秒数（整数）或 HTTP 日期两种形态；
// 解析失败返回 0。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// encodeBody 序列化请求体（不转义 HTML，保持 UTF-8 裸输出）。
func encodeBody(body any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, &LLMError{Msg: "构造请求失败: " + err.Error()}
	}
	return buf.Bytes(), nil
}

// streamSSE 逐行读 SSE 流，把每个 data: 载荷交给 onData；
// 收到 [DONE] 或流结束返回 nil；onData 返回的 error 原样上抛中断。
// 请求 context 被取消（看门狗空闲超时）时返回 errStreamIdle——
// 正常流结束的 readErr 不带该标记，二者必须区分。
func streamSSE(body io.Reader, onData func(data string) error) error {
	reader := bufio.NewReaderSize(body, 1<<20)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && readErr != nil {
			if errors.Is(readErr, context.Canceled) {
				return errStreamIdle
			}
			return nil
		}
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(line[5:])
			if data == "[DONE]" {
				return nil
			}
			if err := onData(data); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) {
				return errStreamIdle
			}
			return nil
		}
	}
}

// parseSSEObject 把一个 SSE data 载荷解析为 JSON 对象；解析失败返回 nil。
func parseSSEObject(data string) map[string]any {
	var obj map[string]any
	if json.Unmarshal([]byte(data), &obj) != nil {
		return nil
	}
	return obj
}
