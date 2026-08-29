package tools

import (
	"fmt"
	"strings"

	"localai/internal/lsp"
)

func init() {
	register(&Tool{
		Schema: `{
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
}`,
		ReadOnly: true,
		Exec:     execLSPDiagnostics,
	})
}

func execLSPDiagnostics(args map[string]any) string {
	path := strings.TrimSpace(strOf(args["path"]))
	if path == "" {
		return "错误：缺少参数 path"
	}
	full := resolve(path)
	lang := lsp.LanguageOf(full)
	if lang == "" {
		return "错误：不识别的文件类型（无对应语言）：" + path
	}
	if !lsp.AvailableFor(lang) {
		return fmt.Sprintf("提示：%s 的语言（%s）未安装 LSP 服务器，无法检查。可安装后重试。",
			path, lsp.LangIDOf(lang))
	}
	content := readFileText(full)
	diags, err := lsp.DiagnosticsForFile(full, content, 2.0)
	if err != nil {
		return "错误：LSP 检查失败：" + err.Error()
	}
	if len(diags) == 0 {
		return "✓ " + path + "：无错误/警告"
	}
	lines := []string{fmt.Sprintf("%s：%d 条诊断", path, len(diags))}
	for i, d := range diags {
		if i >= 30 {
			lines = append(lines, fmt.Sprintf("  …（其余 %d 条略）", len(diags)-30))
			break
		}
		lines = append(lines, fmt.Sprintf("  第%d行 %s %s", d.Line, d.Mark, d.Msg))
	}
	return strings.Join(lines, "\n")
}
