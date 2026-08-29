package llm

// chat_completions 传输：OpenAI 兼容协议（原生格式，无翻译）。
// 自 internal/llm.go 拆出；事件契约与请求行为保持 1:1 不变。

import (
	"encoding/json"
	"errors"
	"strings"

	"localai/internal/config"
	"localai/internal/msg"
)

type slot struct {
	ID, Name, Arguments string
}

func streamChatCompletions(args streamArgs, apiKey string) error {
	body := map[string]any{
		"model":       args.model.ModelID,
		"messages":    args.messages,
		"temperature": config.Temperature,
		"max_tokens":  args.maxTokens,
		"stream":      true,
		// 请求在流末尾附带 usage（不支持的後端会忽略）
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(args.tools) > 0 {
		body["tools"] = args.tools
	}
	if args.model.ReasoningEffort != "" {
		body["reasoning_effort"] = args.model.ReasoningEffort
	}
	enc, err := encodeBody(body)
	if err != nil {
		return err
	}
	url := strings.TrimRight(args.model.BaseURL, "/") + "/chat/completions"
	resp, err := postJSON(url, authHeaders(args.model, apiKey, "Authorization", "Bearer "+apiKey), enc)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	acc := map[int]*slot{}
	var order []int
	return streamSSE(resp.Body, func(data string) error {
		obj := parseSSEObject(data)
		if obj == nil {
			return nil
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
		stop, err := handleChunk(obj, acc, &order, args.onEvent)
		if stop {
			// 协作停止：必须返回 ErrStop（而非 nil），供 agent 区分「用户停止」与「正常结束」
			return ErrStop
		}
		return err
	})
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
			if err := emit(onEvent, msg.Event{"type": "tool_calls", "tool_calls": compiledToolCalls(acc, *order)}); err != nil {
				return errors.Is(err, ErrStop), err
			}
		}
		if err := emit(onEvent, msg.Event{"type": "finish", "reason": finish}); err != nil {
			return errors.Is(err, ErrStop), err
		}
	}
	return false, nil
}

// compiledToolCalls 把累积槽位编译为最终 tool_calls 数组（内部语言形态）。
func compiledToolCalls(acc map[int]*slot, order []int) []any {
	toolCalls := []any{}
	for _, i := range sortedInts(order) {
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
	return toolCalls
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

// authHeaders 组认证头；customHeader 非空时覆盖默认头名
// （自定义头放原始 key 值，不加 Bearer 前缀）。
func authHeaders(model config.ModelConfig, apiKey, defaultName, defaultValue string) map[string]string {
	h := map[string]string{}
	if model.AuthHeader != "" {
		if apiKey != "" {
			h[model.AuthHeader] = apiKey
		}
		return h
	}
	if defaultValue != "" {
		h[defaultName] = defaultValue
	}
	return h
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

// parseToolArguments 解析工具调用 arguments JSON；坏 JSON 返回空对象
// （对齐 agent 循环里的容错行为）。
func parseToolArguments(s string) map[string]any {
	var args map[string]any
	if json.Unmarshal([]byte(s), &args) != nil || args == nil {
		return map[string]any{}
	}
	return args
}
