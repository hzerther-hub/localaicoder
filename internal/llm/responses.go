package llm

// responses 传输：OpenAI Responses API（/v1/responses）。
// system → instructions；消息 → input items（message / function_call /
// function_call_output）；流事件（output_text.delta 等）→ 内部契约。

import (
	"strings"

	"localai/internal/msg"
)

func streamResponses(args streamArgs, apiKey string) error {
	enc, err := encodeBody(buildResponsesBody(args))
	if err != nil {
		return err
	}
	url := strings.TrimRight(args.model.BaseURL, "/") + "/responses"
	header := authHeaders(args.model, apiKey, "Authorization", "Bearer "+apiKey)
	resp, err := postJSON(url, header, enc)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	st := &responsesStream{onEvent: args.onEvent, acc: map[int]*slot{}}
	return streamSSE(resp.Body, st.handle)
}

func buildResponsesBody(args streamArgs) map[string]any {
	system, input := toResponsesInput(args.messages)
	body := map[string]any{
		"model":             args.model.ModelID,
		"instructions":      system,
		"input":             input,
		"max_output_tokens": args.maxTokens,
		"stream":            true,
		"store":             false, // 无状态使用，不落 OpenAI 侧会话
	}
	if len(args.tools) > 0 {
		body["tools"] = toResponsesTools(args.tools)
	}
	if args.model.ReasoningEffort != "" {
		switch strings.ToLower(args.model.ReasoningEffort) {
		case "none", "minimal", "":
			// 不发送 reasoning
		default:
			body["reasoning"] = map[string]any{"effort": strings.ToLower(args.model.ReasoningEffort)}
		}
	}
	return body
}

// toResponsesTools OpenAI function schema → Responses 扁平工具格式。
func toResponsesTools(tools []msg.Msg) []any {
	out := []any{}
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		params, _ := fn["parameters"].(map[string]any)
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type":        "function",
			"name":        msg.S(fn, "name"),
			"description": msg.S(fn, "description"),
			"parameters":  params,
		})
	}
	return out
}

// toResponsesInput 内部消息 → (instructions, input items)。
func toResponsesInput(messages []msg.Msg) (string, []map[string]any) {
	var systemParts []string
	var out []map[string]any
	for _, m := range messages {
		switch msg.Role(m) {
		case "system":
			if s := msg.S(m, "content"); s != "" {
				systemParts = append(systemParts, s)
			}
		case "user":
			out = append(out, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": responsesUserContent(m),
			})
		case "assistant":
			if s := msg.S(m, "content"); s != "" {
				out = append(out, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": s}},
				})
			}
			for _, tcv := range msg.L(m, "tool_calls") {
				tc, ok := tcv.(map[string]any)
				if !ok {
					continue
				}
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					fn = map[string]any{}
				}
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   msg.S(tc, "id"),
					"name":      msg.S(fn, "name"),
					"arguments": msg.S(fn, "arguments"),
				})
			}
		case "tool":
			out = append(out, map[string]any{
				"type":    "function_call_output",
				"call_id": msg.S(m, "tool_call_id"),
				"output":  msg.S(m, "content"),
			})
		}
	}
	return strings.Join(systemParts, "\n\n"), out
}

// responsesUserContent 用户消息 → Responses content parts。
func responsesUserContent(m msg.Msg) []any {
	switch c := m["content"].(type) {
	case string:
		return []any{map[string]any{"type": "input_text", "text": c}}
	case []any:
		var out []any
		for _, pv := range c {
			p, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			switch msg.S(p, "type") {
			case "text":
				if s := msg.S(p, "text"); s != "" {
					out = append(out, map[string]any{"type": "input_text", "text": s})
				}
			case "image_url":
				iu, _ := p["image_url"].(map[string]any)
				if url, _ := iu["url"].(string); url != "" {
					out = append(out, map[string]any{"type": "input_image", "image_url": url})
				}
			}
		}
		return out
	}
	return []any{}
}

// responsesStream 单轮流解析状态。
type responsesStream struct {
	onEvent func(msg.Event) error
	acc     map[int]*slot
	order   []int
	usage   map[string]any
}

func (st *responsesStream) handle(data string) error {
	obj := parseSSEObject(data)
	if obj == nil {
		return nil
	}
	if e, ok := obj["error"].(map[string]any); ok {
		m := msg.S(e, "message")
		if m == "" {
			m = "未知服务端错误"
		}
		return &LLMError{Msg: "服务端错误: " + m}
	}
	switch msg.S(obj, "type") {
	case "response.output_text.delta":
		if s := msg.S(obj, "delta"); s != "" {
			if err := emit(st.onEvent, msg.Event{"type": "text", "delta": s}); err != nil {
				return err
			}
		}
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		if s := msg.S(obj, "delta"); s != "" {
			if err := emit(st.onEvent, msg.Event{"type": "reasoning", "delta": s}); err != nil {
				return err
			}
		}
	case "response.output_item.added", "response.output_item.done":
		// function_call 条目：added 带名字开头，done 带完整 arguments（兜底）。
		// 两者幂等：重复设置同名字段无害。
		item, _ := obj["item"].(map[string]any)
		if msg.S(item, "type") != "function_call" {
			return nil
		}
		idx := len(st.order)
		s := st.acc[idx]
		if s == nil {
			s = &slot{}
			st.acc[idx] = s
			st.order = append(st.order, idx)
		}
		if id := msg.S(item, "call_id"); id != "" {
			s.ID = id
		}
		if n := msg.S(item, "name"); n != "" {
			s.Name = n
		}
		if a := msg.S(item, "arguments"); a != "" && msg.S(obj, "type") == "response.output_item.done" {
			s.Arguments = a
		}
	case "response.function_call_arguments.delta":
		if len(st.order) == 0 {
			return nil
		}
		s := st.acc[st.order[len(st.order)-1]]
		s.Arguments += msg.S(obj, "delta")
	case "response.completed", "response.incomplete":
		if r, ok := obj["response"].(map[string]any); ok {
			st.usage = responsesUsage(r)
		}
		if st.usage != nil {
			if err := emit(st.onEvent, msg.Event{"type": "usage", "usage": st.usage}); err != nil {
				return err
			}
		}
		if len(st.acc) > 0 {
			if err := emit(st.onEvent, msg.Event{
				"type": "tool_calls", "tool_calls": compiledToolCalls(st.acc, st.order),
			}); err != nil {
				return err
			}
			return emit(st.onEvent, msg.Event{"type": "finish", "reason": "tool_calls"})
		}
		return emit(st.onEvent, msg.Event{"type": "finish", "reason": "stop"})
	case "response.failed":
		r, _ := obj["response"].(map[string]any)
		e, _ := r["error"].(map[string]any)
		m := msg.S(e, "message")
		if m == "" {
			m = "响应失败"
		}
		return &LLMError{Msg: "服务端错误: " + m}
	}
	return nil
}

// responsesUsage Responses usage → 内部（chat.completions 风格）usage。
func responsesUsage(resp map[string]any) map[string]any {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return nil
	}
	in := msg.I(u, "input_tokens")
	out := msg.I(u, "output_tokens")
	var cached, reasoning int
	if d, ok := u["input_tokens_details"].(map[string]any); ok {
		cached = msg.I(d, "cached_tokens")
	}
	if d, ok := u["output_tokens_details"].(map[string]any); ok {
		reasoning = msg.I(d, "reasoning_tokens")
	}
	return map[string]any{
		"prompt_tokens":     in,
		"completion_tokens": out,
		"total_tokens":      in + out,
		"cached_tokens":     cached,
		"reasoning_tokens":  reasoning,
	}
}
