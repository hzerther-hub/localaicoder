package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"localai/internal/config"
)

func init() {
	register(&Tool{
		Schema: `{
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
}`,
		// ReadOnly=false（fail-closed 默认，可写且需审批）
		Exec: execWriteFile,
		Describe: func(args map[string]any) string {
			content := strOf(args["content"])
			preview := content
			if len(content) > 300 {
				preview = content[:300] + "\n…(截断)"
			}
			return "文件: " + strOf(args["path"]) + "\n\n" + preview
		},
	})
}

func execWriteFile(args map[string]any) string {
	p := resolve(strOf(args["path"]))
	if config.Sandbox() && !PathInWorkspace(p, "") {
		return "错误：沙箱模式禁止写入工作目录之外的路径：" + p + "\n" +
			"（工作目录：" + GetWorkspace() + "；确需写入请先切换工作目录，" +
			"或设环境变量 LAS_SANDBOX=off）"
	}
	if parent := filepath.Dir(p); parent != "" {
		_ = os.MkdirAll(parent, 0o755)
	}
	content := strOf(args["content"])
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "错误：写入失败：" + err.Error()
	}
	return fmt.Sprintf("已写入 %s（%d 字符）", p, len(content))
}
