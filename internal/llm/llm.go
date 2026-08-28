// Package llm OpenAI 兼容 LLM 客户端：流式 chat + tool_calls 累积
//（对译 Python llm.py；仅标准库，无 SDK）。
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

// LLMError 传输/解析失败（工具错误是普通文本，这里是异常）。
type LLMError struct{ Msg string }

func (e *LLMError) Error() string { return e.Msg }

// ErrStop 协作停止：onEvent 返回它时中断流式读取（对齐 Python 版
// agent 在 generator 循环里 break 的行为）。
var ErrStop = errors.New("llm: cooperative stop")

// Event 类型（对齐 Python llm.stream_chat 的 yield 契约）：
//   {"type":"text","delta":str}
//   {"type":"reasoning","delta":str}
//   {"type":"tool_calls","tool_calls":[...]}   本轮最终 tool_calls（累积完成）
//   {"type":"finish","reason":str}
//   {"type":"usage","usage":{...}}

type slot struct {
	ID, Name, Arguments string
}

// StreamChat 流式对话；onEvent 每收到一个事件回调一次，
// 回调返回 ErrStop 则立刻中断（不报错），返回其它 error 直接上抛。
func StreamChat(model config.ModelConfig, messages []msg.Msg,
	tools []msg.Msg, onEvent func(msg.Event) error) error {
	maxTokens := config.MaxTokens
	if strings.HasPrefix(model.Key, "gpulocal") {
		// 本地(gpulocal)模型 n_ctx 仅 ~131K，输出上限单独钳到 16K
		maxTokens = 16384
	}
	// 输出预算（Reasonix output_budget 思想）：模型声明了窗口时，
	// 输出上限 = min(全局上限, 窗口的 1/4)，避免输出把剩余上下文挤爆
	if model.ContextWindow > 0 {
		if quarter := model.ContextWindow / 4; quarter < maxTokens {
			maxTokens = quarter
		}
	}
	body := map[string]any{
		"model":       model.ModelID,
		"messages":    messages,
		"temperature": config.Temperature,
		"max_tokens":  maxTokens,
		"stream":      true,
		// 请求在流末尾附带 usage（不支持的後端会忽略）
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if model.ReasoningEffort != "" {
		body["reasoning_effort"] = model.ReasoningEffort
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return &LLMError{Msg: "构造请求失败: " + err.Error()}
	}

	req, err := http.NewRequest("POST", strings.TrimRight(model.BaseURL, "/")+"/chat/completions", &buf)
	if err != nil {
		return &LLMError{Msg: "构造请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+model.APIKey)

	// 显式不走系统代理（对齐 Python ProxyHandler({})）
	client := &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 4,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return &LLMError{Msg: "连接失败: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b := io_ReadAll(bufio.NewReader(resp.Body), 300)
		return &LLMError{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, b)}
	}

	acc := map[int]*slot{}
	var order []int

	reader := bufio.NewReaderSize(resp.Body, 1<<20)
	for {
		line, err := reader.ReadString('\n')
		if len(line) == 0 && err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(line[5:])
			if data == "[DONE]" {
				break
			}
			var obj map[string]any
			if json.Unmarshal([]byte(data), &obj) != nil {
				if err != nil { // 读失败且解析失败 → 结束
					break
				}
				continue
			}
			// 部分后端会以 SSE 事件返回 {"error":{...}} 而非 HTTP 错误，
			// 必须显式抛出让错误可见（否则表现为“没有返回”）。
			if e, ok := obj["error"].(map[string]any); ok {
				code := msg.S(e, "code")
				m := msg.S(e, "message")
				if m == "" {
					m = "未知服务端错误"
				}
				if code != "" {
					return &LLMError{Msg: "服务端错误: " + m + " (code=" + code + ")"}
				}
				return &LLMError{Msg: "服务端错误: " + m}
			}
			stop, err := handleChunk(obj, acc, &order, onEvent)
			if stop {
				// 协作停止：必须返回 ErrStop（而非 nil），供 agent 区分「用户停止」与「正常结束」
				return ErrStop
			}
			if err != nil {
				return err
			}
		}
		if err != nil {
			break
		}
	}
	return nil
}

// handleChunk 处理一个 SSE JSON 对象；返回 (stop, err)。
func handleChunk(obj map[string]any, acc map[int]*slot, order *[]int,
	onEvent func(msg.Event) error) (bool, error) {
	if usage, ok := obj["usage"].(map[string]any); ok {
		if err := emit(onEvent, msg.Event{"type": "usage", "usage": parseUsage(usage)}); err != nil {
			return errors.Is(err, ErrStop), err
		}
	}
	choices, ok := obj["choices"].([]any)
	if !ok || len(choices) == 0 {
		return false, nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return false, nil
	}
	delta, _ := choice["delta"].(map[string]any)

	if s := msg.S(delta, "content"); s != "" {
		if err := emit(onEvent, msg.Event{"type": "text", "delta": s}); err != nil {
			return errors.Is(err, ErrStop), err
		}
	}
	if s := msg.S(delta, "reasoning_content"); s != "" {
		if err := emit(onEvent, msg.Event{"type": "reasoning", "delta": s}); err != nil {
			return errors.Is(err, ErrStop), err
		}
	}
	for _, tcv := range msg.L(delta, "tool_calls") {
		tc, ok := tcv.(map[string]any)
		if !ok {
			continue
		}
		idx := msg.I(tc, "index")
		s := acc[idx]
		if s == nil {
			s = &slot{}
			acc[idx] = s
			*order = append(*order, idx)
		}
		if id := msg.S(tc, "id"); id != "" {
			s.ID = id
		}
		if fn, ok := tc["function"].(map[string]any); ok {
			if n := msg.S(fn, "name"); n != "" {
				s.Name = n
			}
			if a := msg.S(fn, "arguments"); a != "" {
				s.Arguments += a
			}
		}
	}
	if finish := msg.S(choice, "finish_reason"); finish != "" {
		if finish == "tool_calls" && len(acc) > 0 {
			toolCalls := []any{}
			for _, i := range sortedInts(*order) {
				s := acc[i]
				toolCalls = append(toolCalls, map[string]any{
					"id":   s.ID,
					"type": "function",
					"function": map[string]any{
						"name":      s.Name,
						"arguments": s.Arguments,
					},
				})
			}
			if err := emit(onEvent, msg.Event{"type": "tool_calls", "tool_calls": toolCalls}); err != nil {
				return errors.Is(err, ErrStop), err
			}
		}
		if err := emit(onEvent, msg.Event{"type": "finish", "reason": finish}); err != nil {
			return errors.Is(err, ErrStop), err
		}
	}
	return false, nil
}

func emit(onEvent func(msg.Event) error, e msg.Event) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(e)
}

func sortedInts(s []int) []int {
	out := append([]int(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// parseUsage 把 OpenAI 兼容 usage 归一化，提取缓存命中的 token 数。
func parseUsage(u map[string]any) map[string]any {
	var cached any
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if v, ok := d["cached_tokens"]; ok && v != nil {
			cached = v
		} else if v, ok := d["cache_read_input_tokens"]; ok && v != nil {
			cached = v
		}
	}
	if cached == nil {
		cached = u["prompt_cache_hit_tokens"] // DeepSeek 格式
	}
	if cached == nil {
		cached = u["cached_tokens"]
	}
	var reasoning any
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		reasoning = d["reasoning_tokens"]
	}
	return map[string]any{
		"prompt_tokens":     u["prompt_tokens"],
		"completion_tokens": u["completion_tokens"],
		"total_tokens":      u["total_tokens"],
		"cached_tokens":     cached,
		"reasoning_tokens":  reasoning,
	}
}

// io_ReadAll 读最多 limit 字节（HTTP 错误体截断展示用）。
func io_ReadAll(r *bufio.Reader, limit int) string {
	var b []byte
	buf := make([]byte, limit)
	for len(b) < limit {
		n, err := r.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	if len(b) > limit {
		b = b[:limit]
	}
	return string(b)
}
