// todo_write 任务步骤清单工具：模型在动手前一次性列出全部步骤（计划先行），
// 执行中随进度更新各项状态；前端把这份清单渲染为任务步骤条。
// 只读工具（不需要审批），本身不改任何文件——它只声明计划。
package tools

import (
	"fmt"
	"strings"
	"sync"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "todo_write",
    "description": "写入任务步骤清单。开始任务前【一次性】列出全部步骤（不要逐条追加）；执行中随进度更新各项 status：开始某项置 in_progress，完成置 completed。用户界面会将清单渲染为任务步骤条。",
    "parameters": {
      "type": "object",
      "properties": {
        "todos": {
          "type": "array",
          "description": "完整步骤清单（每次调用都要传全量）",
          "items": {
            "type": "object",
            "properties": {
              "title": {"type": "string", "description": "步骤内容（一句话，动词开头）"},
              "status": {"type": "string", "enum": ["pending", "in_progress", "completed"], "description": "默认 pending"}
            },
            "required": ["title"]
          }
        }
      },
      "required": ["todos"]
    }
  }
}`,
		ReadOnly: true,
		Exec:     execTodoWrite,
	})
}

// 会话级步骤清单状态：agent 完成纪律兜底用（模型想收尾但清单未完成 → 驱动继续）。
var (
	todoMu    sync.Mutex
	todoState []TodoItem
)

// TodoItem 步骤清单条目。
type TodoItem struct {
	Title  string
	Status string // pending / in_progress / completed
}

// PendingTodoCount 未完成步骤数（pending + in_progress）；无清单返回 0。
func PendingTodoCount() int {
	todoMu.Lock()
	defer todoMu.Unlock()
	n := 0
	for _, it := range todoState {
		if it.Status != "completed" {
			n++
		}
	}
	return n
}

// ResetTodos 清空步骤清单（新建会话时调用）。
func ResetTodos() {
	todoMu.Lock()
	todoState = nil
	todoMu.Unlock()
}

func execTodoWrite(args map[string]any) string {
	todos, ok := args["todos"].([]any)
	if !ok || len(todos) == 0 {
		return "错误：todos 必须是非空数组"
	}
	if len(todos) > 20 {
		return "错误：步骤过多（最多 20 项），请合并"
	}
	var lines []string
	var next []TodoItem
	for i, tvi := range todos {
		tm, ok := tvi.(map[string]any)
		if !ok {
			return fmt.Sprintf("错误：第 %d 项格式不正确", i+1)
		}
		title := strings.TrimSpace(strOf(tm["title"]))
		if title == "" {
			return fmt.Sprintf("错误：第 %d 项缺少 title", i+1)
		}
		status := strings.TrimSpace(strOf(tm["status"]))
		if status == "" {
			status = "pending"
		}
		switch status {
		case "pending", "in_progress", "completed":
		default:
			return fmt.Sprintf("错误：第 %d 项 status 非法（%s）", i+1, status)
		}
		next = append(next, TodoItem{Title: title, Status: status})
		marker := "○"
		if status == "in_progress" {
			marker = "◐"
		} else if status == "completed" {
			marker = "✅"
		}
		lines = append(lines, fmt.Sprintf("%s %d. %s", marker, i+1, title))
	}
	todoMu.Lock()
	todoState = next
	todoMu.Unlock()
	return "任务步骤清单已更新：\n" + strings.Join(lines, "\n")
}
