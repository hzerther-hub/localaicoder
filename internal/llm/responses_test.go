package llm

import (
	"testing"

	"localai/internal/msg"
)

func TestResponsesInputConversion(t *testing.T) {
	messages := []msg.Msg{
		{"role": "system", "content": "S1"},
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "好的",
			"tool_calls": []any{map[string]any{
				"id": "c1", "type": "function",
				"function": map[string]any{"name": "run_shell", "arguments": `{"cmd":"ls"}`},
			}}},
		{"role": "tool", "tool_call_id": "c1", "content": "a.txt"},
	}
	system, input := toResponsesInput(messages)
	if system != "S1" {
		t.Fatalf("system → instructions, got %q", system)
	}
	if len(input) != 4 {
		t.Fatalf("应有 4 个 input items, got %d: %v", len(input), input)
	}
	if msg.S(input[0], "type") != "message" || msg.S(input[0], "role") != "user" {
		t.Fatalf("用户消息形态不符: %v", input[0])
	}
	fc := input[2]
	if msg.S(input[1], "role") != "assistant" {
		t.Fatalf("assistant 消息应保留 role: %v", input[1])
	}
	if msg.S(fc, "type") != "function_call" || msg.S(fc, "call_id") != "c1" || msg.S(fc, "arguments") != `{"cmd":"ls"}` {
		t.Fatalf("function_call 形态不符: %v", fc)
	}
	fo := input[3]
	if msg.S(fo, "type") != "function_call_output" || msg.S(fo, "call_id") != "c1" || msg.S(fo, "output") != "a.txt" {
		t.Fatalf("function_call_output 形态不符: %v", fo)
	}
}

func TestResponsesStreamParsing(t *testing.T) {
	var events []msg.Event
	st := &responsesStream{
		onEvent: func(e msg.Event) error { events = append(events, e); return nil },
		acc:     map[int]*slot{},
	}
	fixture := []string{
		`{"type":"response.output_text.delta","delta":"部分"}`,
		`{"type":"response.output_text.delta","delta":"回答"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"想"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"fc1","name":"list_dir"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"pat"}`,
		`{"type":"response.function_call_arguments.delta","delta":"h\":\".\"}"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":3,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}}`,
	}
	for _, d := range fixture {
		if err := st.handle(d); err != nil {
			t.Fatal(err)
		}
	}
	var text, reasoning string
	var toolCalls []any
	var finish string
	var usage map[string]any
	for _, e := range events {
		switch msg.S(e, "type") {
		case "text":
			text += msg.S(e, "delta")
		case "reasoning":
			reasoning += msg.S(e, "delta")
		case "tool_calls":
			toolCalls = msg.L(e, "tool_calls")
		case "finish":
			finish = msg.S(e, "reason")
		case "usage":
			usage = msg.M(e, "usage")
		}
	}
	if text != "部分回答" || reasoning != "想" {
		t.Fatalf("文本/推理事件不符: %q %q", text, reasoning)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("应产出 1 个 tool_call: %v", toolCalls)
	}
	fn := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if msg.S(fn, "name") != "list_dir" || msg.S(fn, "arguments") != `{"path":"."}` {
		t.Fatalf("tool_call 不符: %v", fn)
	}
	if msg.S(toolCalls[0].(map[string]any), "id") != "fc1" {
		t.Fatal("call_id 应作为工具 id")
	}
	if finish != "tool_calls" {
		t.Fatalf("有工具调用时 finish 应为 tool_calls, got %q", finish)
	}
	if msg.I(usage, "cached_tokens") != 2 || msg.I(usage, "reasoning_tokens") != 1 || msg.I(usage, "prompt_tokens") != 9 {
		t.Fatalf("usage 换算不符: %v", usage)
	}
}

func TestResponsesFailedEvent(t *testing.T) {
	st := &responsesStream{acc: map[int]*slot{}}
	err := st.handle(`{"type":"response.failed","response":{"error":{"message":"quota"}}}`)
	if err == nil || err.Error() != "服务端错误: quota" {
		t.Fatalf("response.failed 应转 LLMError, got %v", err)
	}
}
