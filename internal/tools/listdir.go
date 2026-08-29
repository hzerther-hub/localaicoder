package tools

import (
	"os"
	"strings"
)

func init() {
	register(&Tool{
		Schema: `{
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
}`,
		ReadOnly: true,
		Exec:     execListDir,
	})
}

func execListDir(args map[string]any) string {
	p := GetWorkspace()
	if v := strOf(args["path"]); v != "" {
		p = resolve(v)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "错误：目录不存在 " + p
		}
		return "错误：读取目录失败 " + p
	}
	var out []string
	for _, e := range entries { // ReadDir 已按名排序
		marker := ""
		if e.IsDir() {
			marker = "/"
		}
		out = append(out, e.Name()+marker)
	}
	if len(out) == 0 {
		return "(空目录)"
	}
	return strings.Join(out, "\n")
}
