package llm

// gemini 传输：Google Gemini 原生 generateContent 协议（SSE 流式）。
// system → system_instruction；assistant → role "model"；tool_calls ↔
// functionCall parts；role:tool ↔ functionResponse parts（需 id→name 映射）。

import (
	"encoding/json"
	"strconv"
	"strings"

	"localai/internal/config"
	"localai/internal/msg"
)

func streamGemini(args streamArgs, apiKey string) error {
	enc, err := encodeBody(buildGeminiBody(args))
	if err != nil {
		return err
	}
	url := geminiURL(args.model.BaseURL, args.model.ModelID)
	header := authHeaders(args.model, apiKey, "x-goog-api-key", apiKey)
	resp, err := postJSONCtx(args.ctx, url, header, enc)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	st := &geminiStream{onEvent: args.onEvent, acc: map[int]*slot{}}
	return streamSSE(resp.Body, st.handle)
}

// geminiURL 拼 streamGenerateContent 端点；容忍 base 带 /v1beta。
func geminiURL(base, modelID string) string {
	b := strings.TrimRight(base, "/")
	if !strings.HasSuffix(b, "/v1beta") && !strings.HasSuffix(b, "/v1") {
		b += "/v1beta"
	}
	return b + "/models/" + modelID + ":streamGenerateContent?alt=sse"
}

func buildGeminiBody(args streamArgs) map[string]any {
	system, contents := toGeminiContents(args.messages)
	body := map[string]any{
		"contents": contents,
		"generationConfig": map[string]any{
			"temperature":     config.Temperature,
			"maxOutputTokens": args.maxTokens,
		},
	}
	if system != "" {
		body["system_instruction"] = map[string]any{
			"parts": []any{map[string]any{"text": system}},
		}
	}
	if decls := toGeminiDecls(args.tools); len(decls) > 0 {
		body["tools"] = []any{map[string]any{"function_declarations": decls}}
	}
	return body
}

// toGeminiDecls function schema → functionDeclarations（剔除 Gemini 不接受的
// $schema / additionalProperties 等键）。
func toGeminiDecls(tools []msg.Msg) []any {
	out := []any{}
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		out = append(out, map[string]any{
			"name":        msg.S(fn, "name"),
			"description": msg.S(fn, "description"),
			"parameters":  geminiSanitizeSchema(fn["parameters"]),
		})
	}
	return out
}

func geminiSanitizeSchema(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		if k == "$schema" || k == "additionalProperties" {
			continue
		}
		switch k {
		case "properties":
			props, _ := val.(map[string]any)
			cleaned := map[string]any{}
			for name, schema := range props {
				cleaned[name] = geminiSanitizeSchema(schema)
			}
			out[k] = cleaned
		case "items":
			out[k] = geminiSanitizeSchema(val)
		default:
			out[k] = val
		}
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	return out
}

// toGeminiContents 内部消息 → (system 字符串, contents)。
// tool_call_id → functionCall name 映射同时记录，供 tool 消息回填 functionResponse。
func toGeminiContents(messages []msg.Msg) (string, []map[string]any) {
	var systemParts []string
	var contents []map[string]any
	idToName := map[string]string{}

	appendParts := func(role string, parts ...any) {
		if len(parts) == 0 {
			return
		}
		if len(contents) > 0 && msg.S(contents[len(contents)-1], "role") == role {
			cur, _ := contents[len(contents)-1]["parts"].([]any)
			contents[len(contents)-1]["parts"] = append(cur, parts...)
			return
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}

	for _, m := range messages {
		switch msg.Role(m) {
		case "system":
			if s := msg.S(m, "content"); s != "" {
				systemParts = append(systemParts, s)
			}
		case "user":
			appendParts("user", geminiUserParts(m)...)
		case "assistant":
			var parts []any
			if s := msg.S(m, "content"); s != "" {
				parts = append(parts, map[string]any{"text": s})
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
				name := msg.S(fn, "name")
				idToName[msg.S(tc, "id")] = name
				parts = append(parts, map[string]any{
					"functionCall": map[string]any{
						"name": name,
						"args": parseToolArguments(msg.S(fn, "arguments")),
					},
				})
			}
			appendParts("model", parts...)
		case "tool":
			name := idToName[msg.S(m, "tool_call_id")]
			if name == "" {
				name = "unknown_tool"
			}
			appendParts("user", map[string]any{
				"functionResponse": map[string]any{
					"name":     name,
					"response": map[string]any{"result": msg.S(m, "content")},
				},
			})
		}
	}
	if len(contents) == 0 {
		// Gemini 不接受空 contents
		contents = []map[string]any{{"role": "user", "parts": []any{map[string]any{"text": "你好"}}}}
	}
	return strings.Join(systemParts, "\n\n"), contents
}

// geminiUserParts 用户消息 → parts（text / inlineData 图片）。
func geminiUserParts(m msg.Msg) []any {
	switch c := m["content"].(type) {
	case string:
		if c == "" {
			return nil
		}
		return []any{map[string]any{"text": c}}
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
					out = append(out, map[string]any{"text": s})
				}
			case "image_url":
				iu, _ := p["image_url"].(map[string]any)
				if part := geminiImagePart(msg.S(iu, "url")); part != nil {
					out = append(out, part)
				}
			}
		}
		return out
	}
	return nil
}

// geminiImagePart data:image/png;base64,xxx → inlineData part；http URL 不支持，忽略。
func geminiImagePart(url string) map[string]any {
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	rest := strings.TrimPrefix(url, "data:")
	semi := strings.Index(rest, ";base64,")
	if semi < 0 {
		return nil
	}
	return map[string]any{
		"inlineData": map[string]any{
			"mimeType": rest[:semi],
			"data":     rest[semi+len(";base64,"):],
		},
	}
}

// geminiStream 单轮流解析状态。
type geminiStream struct {
	onEvent  func(msg.Event) error
	acc      map[int]*slot
	order    []int
	finished bool
}

func (st *geminiStream) handle(data string) error {
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
	if u, ok := obj["usageMetadata"].(map[string]any); ok {
		prompt := msg.I(u, "promptTokenCount")
		out := msg.I(u, "candidatesTokenCount") + msg.I(u, "thoughtsTokenCount")
		usage := map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": out,
			"total_tokens":      prompt + out,
			"cached_tokens":     msg.I(u, "cachedContentTokenCount"),
			"reasoning_tokens":  msg.I(u, "thoughtsTokenCount"),
		}
		if err := emit(st.onEvent, msg.Event{"type": "usage", "usage": usage}); err != nil {
			return err
		}
	}
	candidates, _ := obj["candidates"].([]any)
	if len(candidates) == 0 {
		return nil
	}
	cand, _ := candidates[0].(map[string]any)
	if content, ok := cand["content"].(map[string]any); ok {
		for _, pv := range msg.L(content, "parts") {
			p, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			// functionCall part：args 是对象 → 序列化为内部 arguments 字符串
			if fc, ok := p["functionCall"].(map[string]any); ok {
				idx := len(st.order)
				st.acc[idx] = &slot{
					ID:        "call_" + msg.S(fc, "name") + "_" + strconv.Itoa(idx),
					Name:      msg.S(fc, "name"),
					Arguments: marshalArgs(fc["args"]),
				}
				st.order = append(st.order, idx)
				continue
			}
			if s := msg.S(p, "text"); s != "" {
				kind := "text"
				if b, ok := p["thought"].(bool); ok && b {
					kind = "reasoning"
				}
				if err := emit(st.onEvent, msg.Event{"type": kind, "delta": s}); err != nil {
					return err
				}
			}
		}
	}
	if reason := msg.S(cand, "finishReason"); reason != "" && !st.finished {
		st.finished = true
		if len(st.acc) > 0 {
			if err := emit(st.onEvent, msg.Event{
				"type": "tool_calls", "tool_calls": compiledToolCalls(st.acc, st.order),
			}); err != nil {
				return err
			}
			return emit(st.onEvent, msg.Event{"type": "finish", "reason": "tool_calls"})
		}
		r := "stop"
		if reason == "MAX_TOKENS" {
			r = "length"
		}
		return emit(st.onEvent, msg.Event{"type": "finish", "reason": r})
	}
	return nil
}

// marshalArgs Gemini functionCall args（对象）→ JSON 字符串。
func marshalArgs(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
