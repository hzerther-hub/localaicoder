// 工具注册表：唯一事实源（对齐 openclaude buildTool 模式）。
//
// 每个工具一个文件，init() 时自注册；元数据（schema/只读性/条件暴露）
// 与行为（执行器、审批摘要）声明在一起。发给模型的仍是 OpenAI function
// schema（逐字保留原描述文案），schema 不属于线上新类型。
//
// fail-closed 默认：新工具忘了标 ReadOnly 就按可写处理（readonly 模式
// 不暴露、ask 模式审批），与 openclaude 的默认语义一致。
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Tool 单个工具的完整描述（非线上类型）。
type Tool struct {
	// Schema OpenAI function schema JSON（name/description/parameters）。
	Schema string
	// ReadOnly true = 只读工具（readonly 模式可用、结果可缓存）。
	// false = 可写（readonly 模式移除；ask 模式执行前需审批）。
	ReadOnly bool
	// Enabled 条件暴露判定（每轮请求前调用）；nil = 恒可用。
	// 例：kb_search 需知识库开启、call_model 需派发开启且本地大脑健康。
	Enabled func() bool
	// Exec 执行器；返回值作为工具结果文本回给模型。
	Exec func(args map[string]any) string
	// Describe 审批弹窗的人类可读摘要；nil = 通用逐字段格式化。
	Describe func(args map[string]any) string
}

func (t *Tool) name() string { return parsedSchema(t.Schema)["__name__"] }

var (
	regMu     sync.RWMutex
	registry  = map[string]*Tool{}
	parsed    = map[string]map[string]string{} // name → {__name__, __desc__, __params__}
	schemaSeq []string                         // 注册序（输出前按名排序）
)

// register 工具自注册入口（各工具文件 init() 调用）。
func register(t *Tool) {
	if t == nil || t.Exec == nil || t.Schema == "" {
		panic("tools: 工具注册缺少 schema 或执行器")
	}
	var top struct {
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(t.Schema), &top); err != nil || top.Function.Name == "" {
		panic("tools: 工具 schema 解析失败: " + err.Error())
	}
	name := top.Function.Name
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("tools: 重复注册工具 " + name)
	}
	registry[name] = t
	schemaSeq = append(schemaSeq, name)
}

func lookup(name string) *Tool {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[name]
}

// enabled 工具当前是否应暴露给模型。
func (t *Tool) enabled() bool { return t.Enabled == nil || t.Enabled() }

// schemaMap 还原完整的 {"type":"function","function":{...}} map（发给模型）。
// 解析结果缓存：schema 是常量，反复 json.Unmarshal 纯浪费。
var (
	schemaCacheMu sync.Mutex
	schemaCache   = map[string]map[string]any{}
)

func schemaMap(name string) map[string]any {
	schemaCacheMu.Lock()
	defer schemaCacheMu.Unlock()
	if m, ok := schemaCache[name]; ok {
		return m
	}
	t := registry[name]
	var out map[string]any
	_ = json.Unmarshal([]byte(t.Schema), &out)
	schemaCache[name] = out
	return out
}

func parsedSchema(schema string) map[string]string {
	var top struct {
		Function struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"function"`
	}
	_ = json.Unmarshal([]byte(schema), &top)
	return map[string]string{"__name__": top.Function.Name, "__desc__": top.Function.Description}
}

// ToolSchemas 当前可用的内置工具 schema 列表（按工具名排序，保证多轮
// 请求前缀逐字节稳定；MCP 外部工具由 agent 追加在后）。
func ToolSchemas() []map[string]any { return collectSchemas(false) }

// ReadOnlySchemas 只读模式工具列表（剔除可写工具）。
func ReadOnlySchemas() []map[string]any { return collectSchemas(true) }

// EnabledSchemas 按权限模式取工具列表（readonly 剔除可写工具）。
func EnabledSchemas(readonly bool) []map[string]any { return collectSchemas(readonly) }

func collectSchemas(readonly bool) []map[string]any {
	regMu.RLock()
	names := append([]string(nil), schemaSeq...)
	regMu.RUnlock()
	sort.Strings(names)
	var out []map[string]any
	for _, n := range names {
		t := lookup(n)
		if readonly && !t.ReadOnly {
			continue
		}
		if !t.enabled() {
			continue
		}
		out = append(out, schemaMap(n))
	}
	return out
}

// IsWriteTool 是否可写工具（ask 模式下需审批）。
func IsWriteTool(name string) bool {
	t := lookup(name)
	return t != nil && !t.ReadOnly
}

// DescribeArguments 工具参数的人类可读摘要（审批弹窗用）。
func DescribeArguments(name string, args map[string]any) string {
	if t := lookup(name); t != nil && t.Describe != nil {
		return t.Describe(args)
	}
	var lines []string
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %v", k, args[k]))
	}
	return strings.Join(lines, "\n")
}

// ExecuteTool 执行工具，返回结果字符串。未知工具/执行异常都返回错误文本
// （错误只作为普通工具结果回给模型，循环不中断，并附「继续任务」提示）。
func ExecuteTool(name string, arguments map[string]any) (result string) {
	t := lookup(name)
	if t == nil {
		return "错误：未知工具 " + name
	}
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("错误: %v\n（此工具执行失败。请检查参数后重试，或改用其他工具/其他方法继续完成当前任务，不要因此停止。）", r)
		}
	}()
	return t.Exec(arguments)
}
