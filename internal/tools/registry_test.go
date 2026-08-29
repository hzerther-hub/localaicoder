package tools

// 金测：注册表产出的 schema 集合必须与重构前 tools.go 的 schemasJSON
// 完全一致（描述文案是提示词工程的一部分，不允许无意漂移）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// legacySchemasJSON 重构前单一大字面量的内容（原样拷贝）。
// 新增工具（todo_write）随功能扩展同步追加于此。
const legacySchemasJSON = `[
  {
    "type": "function",
    "function": {
      "name": "read_file",
      "description": "读取文件内容。返回带行号的文本。",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "文件路径（绝对或相对路径）"}
        },
        "required": ["path"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "write_file",
      "description": "写入（或覆盖）文件内容。沙箱限制：只能写工作目录内的路径。",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "文件路径"},
          "content": {"type": "string", "description": "要写入的完整内容"}
        },
        "required": ["path", "content"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "list_dir",
      "description": "列出目录内容（文件和子目录）。",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "目录路径，默认当前工作目录"}
        },
        "required": ["path"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "glob_search",
      "description": "按通配符查找文件，如 '*.py' 或 '**/*.js'。",
      "parameters": {
        "type": "object",
        "properties": {
          "pattern": {"type": "string", "description": "glob 通配符模式"}
        },
        "required": ["pattern"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "grep_search",
      "description": "在文件内容中按正则/关键字搜索。",
      "parameters": {
        "type": "object",
        "properties": {
          "pattern": {"type": "string", "description": "要搜索的关键字或正则"},
          "path": {"type": "string", "description": "目录或文件路径，默认工作目录"}
        },
        "required": ["pattern", "path"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "lsp_diagnostics",
      "description": "用 LSP 语言服务器检查代码文件的错误/警告（比正则更准确，懂类型与导入）。修改代码前后均可调用以核实。仅支持已安装对应语言服务器的语言（Python/JS/TS/Go/Rust/C++/Java 等），不支持时返回提示。",
      "parameters": {
        "type": "object",
        "properties": {
          "path": {"type": "string", "description": "要检查的文件路径（相对工作目录）"}
        },
        "required": ["path"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "index_search",
      "description": "语义检索整个代码库：按相关度返回最相关的代码片段（含文件和行号）。回答「XX在哪实现的/怎么用的」这类问题时优先用它，比逐个读文件快且省上下文。首次调用会自动建索引。",
      "parameters": {
        "type": "object",
        "properties": {
          "query": {"type": "string", "description": "检索内容：功能描述、函数名、类名等"},
          "top_k": {"type": "integer", "description": "返回片段数，默认 5"}
        },
        "required": ["query"]
      }
    }
  },
  {
    "type": "function",
    "function": {
      "name": "run_shell",
      "description": "执行 shell 命令并返回 stdout/stderr。（命令需与当前操作系统兼容：Linux/macOS 用 POSIX shell，Windows 用 cmd；格式化磁盘、rm -rf /、关机等高危命令会被沙箱拦截）",
      "parameters": {
        "type": "object",
        "properties": {
          "command": {"type": "string", "description": "要执行的 shell 命令"}
        },
        "required": ["command"]
      }
    }
  },
  {
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
  },
  {
    "type": "function",
    "function": {
      "name": "web_search",
      "description": "联网搜索（Bing/DuckDuckGo/百度自动回退）。返回结果列表（标题/链接/摘要）。查最新信息、找库/文档、排查报错时优先使用。",
      "parameters": {
        "type": "object",
        "properties": {
          "query": {"type": "string", "description": "搜索关键词"},
          "max_results": {"type": "integer", "description": "返回条数，默认 8，最大 10"}
        },
        "required": ["query"]
      }
    }
  }
]`

func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSchemasGold(t *testing.T) {
	var legacy []map[string]any
	if err := json.Unmarshal([]byte(legacySchemasJSON), &legacy); err != nil {
		t.Fatal(err)
	}
	got := ToolSchemas()
	if len(got) != len(legacy) {
		t.Fatalf("工具数应 %d, got %d", len(legacy), len(got))
	}
	byName := map[string]map[string]any{}
	for _, s := range got {
		fn := s["function"].(map[string]any)
		byName[fn["name"].(string)] = s
	}
	for _, want := range legacy {
		fn := want["function"].(map[string]any)
		name := fn["name"].(string)
		have, ok := byName[name]
		if !ok {
			t.Fatalf("缺少工具 %s", name)
		}
		if canonical(t, have) != canonical(t, want) {
			t.Fatalf("工具 %s 的 schema 与重构前不一致:\n got %s\nwant %s", name,
				canonical(t, have), canonical(t, want))
		}
	}
}

func TestReadOnlyFilter(t *testing.T) {
	names := func(schemas []map[string]any) string {
		var out []string
		for _, s := range schemas {
			fn := s["function"].(map[string]any)
			out = append(out, fn["name"].(string))
		}
		return strings.Join(out, ",")
	}
	ro := ReadOnlySchemas()
	if strings.Contains(names(ro), "write_file") || strings.Contains(names(ro), "run_shell") {
		t.Fatalf("只读列表不应含可写工具: %s", names(ro))
	}
	full := ToolSchemas()
	if !strings.Contains(names(full), "write_file") || !strings.Contains(names(full), "run_shell") {
		t.Fatalf("完整列表应含可写工具: %s", names(full))
	}
}

func TestIsWriteToolAndDescribe(t *testing.T) {
	if !IsWriteTool("write_file") || !IsWriteTool("run_shell") {
		t.Fatal("write_file/run_shell 应为可写")
	}
	if IsWriteTool("read_file") {
		t.Fatal("read_file 应为只读")
	}
	if ExecuteTool("no_such_tool", nil) == "" {
		t.Fatal("未知工具应返回错误文本")
	}
	if d := DescribeArguments("run_shell", map[string]any{"command": "ls"}); d != "ls" {
		t.Fatalf("run_shell 审批摘要应为命令本身, got %q", d)
	}
}
