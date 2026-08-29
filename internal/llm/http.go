package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient 显式不走系统代理（对齐 Python ProxyHandler({})）。
func httpClient() *http.Client {
	return &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 4,
		},
	}
}

// postJSON 发一次 POST；非 2xx 时关闭并返回带 Status 的 LLMError。
func postJSON(url string, header map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, &LLMError{Msg: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, &LLMError{Msg: "连接失败: " + err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		return nil, &LLMError{
			Msg:    fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b)),
			Status: resp.StatusCode,
		}
	}
	return resp, nil
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
func streamSSE(body io.Reader, onData func(data string) error) error {
	reader := bufio.NewReaderSize(body, 1<<20)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && readErr != nil {
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
