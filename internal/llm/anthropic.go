package llm

// anthropic_messages 传输：Anthropic Messages 原生协议。
// 内部语言（OpenAI 风格）→ Anthropic：system 提为顶层、tool_calls ↔ tool_use、
// role:tool ↔ tool_result、image_url ↔ base64 source；流事件反向翻译回内部契约。

import (
	"fmt"
	"strings"

	"localai/internal/msg"
)

func streamAnthropic(args streamArgs, apiKey string) error {
	enc, err := encodeBody(buildAnthropicBody(args))
	if err != nil {
		return err
	}
	header := authHeaders(args.model, apiKey, "x-api-key", apiKey)
	header["anthropic-version"] = "2023-06-01"
	resp, err := postJSON(anthropicURL(args.model.BaseURL), header, enc)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	st := &anthropicStream{onEvent: args.onEvent, acc: map[int]*slot{}}
	return streamSSE(resp.Body, st.handle)
}

// anthropicURL 拼端点：容忍 base 带/不带 /v1、甚至已带 /messages；
// base 已含查询串（?）时视为用户给的完整端点，原样使用。
func anthropicURL(base string) string {
	b := strings.TrimRight(base, "/")
	switch {
	case strings.Contains(b, "?"):
		return b
	case strings.HasSuffix(b, "/messages"):
		return b
	case strings.HasSuffix(b, "/v1"):
		return b + "/messages"
	}
	return b + "/v1/messages"
}

// thinkingBudget 推理等级 → Anthropic thinking budget_tokens；0 = 不开 thinking。
func thinkingBudget(effort string) int {
	switch strings.ToLower(effort) {
	case "none":
		return 0
	case "low", "minimal":
		return 2048
	case "medium":
		return 8192
	case "high":
		return 16384
	case "xhigh", "max":
		return 31999
	}
	return 0
}

func buildAnthropicBody(args streamArgs) map[string]any {
	system, msgs := toAnthropicMessages(args.messages)
	body := map[string]any{
		"model":      args.model.ModelID,
		"messages":   msgs,
		"max_tokens": args.maxTokens,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if len(args.tools) > 0 {
		body["tools"] = toAnthropicTools(args.tools)
	}
	if budget := thinkingBudget(args.model.ReasoningEffort); budget > 0 {
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		// thinking 开启时 max_tokens 必须大于 budget，且 temperature 必须为 1
		if args.maxTokens < budget+1024 {
			body["max_tokens"] = budget + 1024
		}
		body["temperature"] = 1
	}
	return body
}

// toAnthropicTools OpenAI function schema → Anthropic {name,description,input_schema}。
func toAnthropicTools(tools []msg.Msg) []any {
	out := []any{}
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		schema, _ := fn["parameters"].(map[string]any)
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"name":         msg.S(fn, "name"),
			"description":  msg.S(fn, "description"),
			"input_schema": schema,
		})
	}
	return out
}

// toAnthropicMessages 内部消息列表 → (system 字符串, Anthropic messages)。
// 连续同角色合并（tool_result 连发合并进一条 user；Anthropic 不接受连续同角色）。
func toAnthropicMessages(messages []msg.Msg) (string, []map[string]any) {
	var systemParts []string
	type entry struct {
		role   string
		blocks []any
	}
	var out []entry
	appendBlock := func(role string, blocks ...any) {
		if len(out) > 0 && out[len(out)-1].role == role {
			out[len(out)-1].blocks = append(out[len(out)-1].blocks, blocks...)
			return
		}
		out = append(out, entry{role: role, blocks: blocks})
	}
	for _, m := range messages {
		switch msg.Role(m) {
		case "system":
			if s := msg.S(m, "content"); s != "" {
				systemParts = append(systemParts, s)
			}
		case "user":
			appendBlock("user", userContentBlocks(m)...)
		case "assistant":
			var blocks []any
			if s := msg.S(m, "content"); s != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": s})
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
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    msg.S(tc, "id"),
					"name":  msg.S(fn, "name"),
					"input": parseToolArguments(msg.S(fn, "arguments")),
				})
			}
			if len(blocks) > 0 {
				appendBlock("assistant", blocks...)
			}
		case "tool":
			appendBlock("user", map[string]any{
				"type":        "tool_result",
				"tool_use_id": msg.S(m, "tool_call_id"),
				"content":     msg.S(m, "content"),
			})
		}
	}
	msgs := make([]map[string]any, len(out))
	for i, e := range out {
		msgs[i] = map[string]any{"role": e.role, "content": e.blocks}
	}
	return strings.Join(systemParts, "\n\n"), msgs
}

// userContentBlocks 用户消息 → Anthropic 内容块（文本 + 图片）。
func userContentBlocks(m msg.Msg) []any {
	switch c := m["content"].(type) {
	case string:
		if c == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": c}}
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
					out = append(out, map[string]any{"type": "text", "text": s})
				}
			case "image_url":
				iu, _ := p["image_url"].(map[string]any)
				url, _ := iu["url"].(string)
				if block := anthropicImageBlock(url); block != nil {
					out = append(out, block)
				}
			}
		}
		return out
	}
	return nil
}

// anthropicImageBlock data:image/...;base64,xxx 或 http(s) URL → 图片块。
func anthropicImageBlock(url string) map[string]any {
	if url == "" {
		return nil
	}
	if strings.HasPrefix(url, "data:") {
		rest := strings.TrimPrefix(url, "data:")
		semi := strings.Index(rest, ";base64,")
		if semi < 0 {
			return nil
		}
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": rest[:semi],
				"data":       rest[semi+len(";base64,"):],
			},
		}
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "url", "url": url},
		}
	}
	return nil
}

// anthropicStopReason Anthropic stop_reason → 内部（OpenAI 风格）reason。
func anthropicStopReason(reason string) string {
	switch reason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return "stop"
	}
}

// anthropicStream 单轮流解析状态。
type anthropicStream struct {
	onEvent    func(msg.Event) error
	acc        map[int]*slot
	order      []int
	inputTok   int
	cacheRead  int
	outputTok  int
	sawToolUse bool
}

func (st *anthropicStream) handle(data string) error {
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
	case "message_start":
		// usage 嵌套在 message.usage 下
		if m, ok := obj["message"].(map[string]any); ok {
			if u, ok := m["usage"].(map[string]any); ok {
				st.takeUsage(u)
			}
		}
	case "content_block_start":
		cb, _ := obj["content_block"].(map[string]any)
		if msg.S(cb, "type") == "tool_use" {
			idx := msg.I(obj, "index")
			st.acc[idx] = &slot{ID: msg.S(cb, "id"), Name: msg.S(cb, "name")}
			st.order = append(st.order, idx)
		}
	case "content_block_delta":
		delta, _ := obj["delta"].(map[string]any)
		idx := msg.I(obj, "index")
		switch msg.S(delta, "type") {
		case "text_delta":
			if s := msg.S(delta, "text"); s != "" {
				if err := emit(st.onEvent, msg.Event{"type": "text", "delta": s}); err != nil {
					return err
				}
			}
		case "thinking_delta":
			if s := msg.S(delta, "thinking"); s != "" {
				if err := emit(st.onEvent, msg.Event{"type": "reasoning", "delta": s}); err != nil {
					return err
				}
			}
		case "input_json_delta":
			if s := st.acc[idx]; s != nil {
				s.Arguments += msg.S(delta, "partial_json")
			}
		}
	case "message_delta":
		delta, _ := obj["delta"].(map[string]any)
		if u, ok := obj["usage"].(map[string]any); ok {
			st.takeUsage(u)
		}
		if u := st.usageMap(); u != nil {
			if err := emit(st.onEvent, msg.Event{"type": "usage", "usage": u}); err != nil {
				return err
			}
		}
		reason := msg.S(delta, "stop_reason")
		st.sawToolUse = reason == "tool_use" && len(st.acc) > 0
		if st.sawToolUse {
			if err := emit(st.onEvent, msg.Event{
				"type": "tool_calls", "tool_calls": compiledToolCalls(st.acc, st.order),
			}); err != nil {
				return err
			}
		}
		if err := emit(st.onEvent, msg.Event{"type": "finish", "reason": anthropicStopReason(reason)}); err != nil {
			return err
		}
	case "error":
		return &LLMError{Msg: fmt.Sprintf("服务端错误: %v", obj)}
	}
	return nil
}

// takeUsage 累加 message_start / message_delta 里的 usage 字段。
func (st *anthropicStream) takeUsage(m map[string]any) {
	st.inputTok += msg.I(m, "input_tokens")
	st.outputTok += msg.I(m, "output_tokens")
	st.cacheRead += msg.I(m, "cache_read_input_tokens")
}

// usageMap 翻回内部（OpenAI 风格）usage 形态；prompt 含缓存读（与
// agent 计费 missIn = in - cached 的约定一致）。nil = 尚无任何 usage。
func (st *anthropicStream) usageMap() map[string]any {
	if st.inputTok == 0 && st.outputTok == 0 && st.cacheRead == 0 {
		return nil
	}
	return map[string]any{
		"prompt_tokens":     st.inputTok + st.cacheRead,
		"completion_tokens": st.outputTok,
		"total_tokens":      st.inputTok + st.cacheRead + st.outputTok,
		"cached_tokens":     st.cacheRead,
	}
}
