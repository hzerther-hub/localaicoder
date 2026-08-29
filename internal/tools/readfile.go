package tools

import (
	"fmt"
	"os"
	"strings"
)

func init() {
	register(&Tool{
		Schema: `{
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
}`,
		ReadOnly: true,
		Exec:     execReadFile,
	})
}

func execReadFile(args map[string]any) string {
	p := resolve(strOf(args["path"]))
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "错误：文件不存在 " + p
		}
		if st, statErr := os.Stat(p); statErr == nil && st.IsDir() {
			return "错误：" + p + " 是目录"
		}
		return "错误：读取失败 " + err.Error()
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// 末尾多余空行（文件以换行结尾时 split 产生的空元素）与 Python splitlines 行为对齐
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%4d | %s\n", i+1, line)
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return "(空文件)"
	}
	return out
}
