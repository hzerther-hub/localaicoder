// Package msg 定义内核通用的消息/事件表示。
//
// 与 Python 版一致：消息、事件、工具 schema 都是 OpenAI 兼容的
// JSON 字典，直接用 map[string]any 保持线上格式 1:1，不做二次建模。
package msg

import "encoding/json"

// Msg 一条 OpenAI chat 消息（role/content/tool_calls...）。
type Msg = map[string]any

// Event agent 循环向 UI 发的事件（type 为判别键）。
type Event = map[string]any

// S 取字符串字段（缺失/类型不符返回空串）。
func S(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// B 取布尔字段。
func B(m map[string]any, k string) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return false
}

// F 取数值字段（兼容 float64/int/int64/json.Number —— 默认配置是 int，
// JSON 解析出来是 float64，两者都必须支持）。
func F(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

// I 取整型字段。
func I(m map[string]any, k string) int {
	return int(F(m, k))
}

// M 取子对象。
func M(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

// L 取列表。
func L(m map[string]any, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

// Role 消息角色。
func Role(m Msg) string { return S(m, "role") }
