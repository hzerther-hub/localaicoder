// Package tools 工具定义 + 执行器（对译 Python tools.py）。
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
)

// ---------------- 工作目录 ----------------

var (
	wsMu       sync.RWMutex
	workspace  string
	wsInitOnce sync.Once
)

func initWorkspace() {
	workspace = config.LoadLastWorkspace()
}

// SetWorkspace 切换工作目录。
func SetWorkspace(path string) {
	wsMu.Lock()
	workspace = path
	wsMu.Unlock()
}

// GetWorkspace 当前工作目录（工具默认在此目录内操作）。
func GetWorkspace() string {
	wsInitOnce.Do(initWorkspace)
	wsMu.RLock()
	defer wsMu.RUnlock()
	return workspace
}

// PushWorkspace 临时切换工作目录；返回恢复函数（测试隔离用）。
func PushWorkspace(path string) (restore func()) {
	old := GetWorkspace()
	SetWorkspace(path)
	return func() { SetWorkspace(old) }
}

// ---------------- 工具 Schema（与 Python 版逐字一致） ----------------

const schemasJSON = `[
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

var (
	toolSchemas     []map[string]any
	toolSchemasOnce sync.Once
)

// ToolSchemas 内置工具的 JSON Schema 列表（发给模型）。
func ToolSchemas() []map[string]any {
	toolSchemasOnce.Do(func() {
		_ = json.Unmarshal([]byte(schemasJSON), &toolSchemas)
	})
	return toolSchemas
}

// ReadOnlySchemas 只读模式下的工具列表（去掉可写工具）。
func ReadOnlySchemas() []map[string]any {
	var out []map[string]any
	for _, s := range ToolSchemas() {
		fn, _ := s["function"].(map[string]any)
		if fn != nil && writeTools[nameOf(fn)] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func nameOf(fn map[string]any) string {
	if s, ok := fn["name"].(string); ok {
		return s
	}
	return ""
}

// writeTools 可写工具：执行前需要用户批准。
var writeTools = map[string]bool{"write_file": true, "run_shell": true}

// IsWriteTool 是否为可写工具（ask 模式下需审批）。
func IsWriteTool(name string) bool { return writeTools[name] }

// DescribeArguments 把工具参数格式化成人类可读的摘要，用于审批弹窗。
func DescribeArguments(name string, args map[string]any) string {
	if name == "run_shell" {
		return strOf(args["command"])
	}
	if name == "write_file" {
		content := strOf(args["content"])
		preview := content
		if len(content) > 300 {
			preview = content[:300] + "\n…(截断)"
		}
		return "文件: " + strOf(args["path"]) + "\n\n" + preview
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

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ---------------- 执行器 ----------------

func resolve(path string) string {
	return ResolvePath(path)
}

// ResolvePath 把相对路径解析到工作目录（绝对路径原样返回）。
func ResolvePath(path string) string {
	if path == "" {
		return GetWorkspace()
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(GetWorkspace(), path)
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

func execGlobSearch(args map[string]any) string {
	pattern := strOf(args["pattern"])
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(GetWorkspace(), pattern)
	}
	matches := globPattern(pattern)
	if len(matches) == 0 {
		return "未找到匹配文件"
	}
	sort.Strings(matches)
	if len(matches) > 200 {
		matches = matches[:200]
	}
	return strings.Join(matches, "\n")
}

// globPattern 支持 ** 的 glob（filepath.Glob 不支持 **，手工实现）。
func globPattern(pattern string) []string {
	if !strings.Contains(pattern, "**") {
		m, _ := filepath.Glob(pattern)
		return m
	}
	// 形如 <root>/**/rest 或 <root>/**；把 ** 段展开为任意深度
	idx := strings.Index(pattern, "**")
	root := filepath.Dir(pattern[:idx+1]) // ** 所在目录
	rest := ""
	if idx+2 < len(pattern) {
		rest = filepath.ToSlash(strings.TrimPrefix(pattern[idx+2:], string(filepath.Separator)))
	}
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rest == "" || rest == "*" {
			if info.IsDir() || rest == "*" {
				out = append(out, p)
			}
			return nil
		}
		if matchGlobRest(rel, rest) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// matchGlobRest 逐段匹配（rest 各段可含 *；开头的 ** 段匹配任意数量目录，
// 其余 ** 段等价任意深度；模式无 ** 时必须整体匹配）。
func matchGlobRest(rel, rest string) bool {
	if rel == "." {
		return false
	}
	rSegs := strings.Split(rel, "/")
	mSegs := strings.Split(rest, "/")
	var match func(r, m []string) bool
	match = func(r, m []string) bool {
		if len(m) == 0 {
			return len(r) == 0
		}
		if m[0] == "**" {
			for i := 0; i <= len(r); i++ {
				if match(r[i:], m[1:]) {
					return true
				}
			}
			return false
		}
		if len(r) == 0 {
			return false
		}
		if ok, _ := filepath.Match(m[0], r[0]); !ok {
			return false
		}
		return match(r[1:], m[1:])
	}
	// 模式以 ** 开头（外层已剥离）→ rest 匹配 rel 的任意尾部（含整体）
	if len(rSegs) >= len(mSegs) && match(rSegs[len(rSegs)-len(mSegs):], mSegs) {
		return true
	}
	return match(rSegs, mSegs)
}

// grep 跳过超过该大小的文件（多为二进制/生成物）
const maxGrepFile = 5 * 1024 * 1024

var grepSkipDirs = map[string]bool{
	"node_modules": true, "__pycache__": true, "dist": true, "build": true,
}

func execGrepSearch(args map[string]any) string {
	pattern := strOf(args["pattern"])
	root := GetWorkspace()
	if v := strOf(args["path"]); v != "" {
		root = resolve(v)
	}
	var targets []string
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		targets = []string{root}
	} else {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") || grepSkipDirs[name] {
					return filepath.SkipDir
				}
				return nil
			}
			targets = append(targets, p)
			return nil
		})
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		rx = regexp.MustCompile(regexp.QuoteMeta(pattern)) // 无效正则 → 字面量
	}
	var hits []string
	for _, fpath := range targets {
		st, err := os.Stat(fpath)
		if err != nil || st.Size() > maxGrepFile {
			continue
		}
		f, err := os.Open(fpath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		i := 0
		for scanner.Scan() {
			i++
			line := scanner.Text()
			if rx.MatchString(line) {
				t := strings.TrimSpace(line)
				if len(t) > 160 {
					t = t[:160]
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s", fpath, i, t))
				if len(hits) >= 100 {
					_ = f.Close()
					return strings.Join(hits, "\n") + "\n(结果已截断)"
				}
			}
		}
		_ = f.Close()
	}
	if len(hits) == 0 {
		return "未找到匹配内容"
	}
	return strings.Join(hits, "\n")
}

func execRunShell(args map[string]any) string {
	cmd := args["command"]
	if cmd == nil || strings.TrimSpace(fmt.Sprintf("%v", cmd)) == "" {
		return "错误：command 参数为空，无法执行。请修正参数后重试，或改用其他工具/方法继续当前任务。"
	}
	cmdStr := fmt.Sprintf("%v", cmd)
	if config.Sandbox() {
		if hit := ShellCommandBlocked(cmdStr); hit != "" {
			return "错误：沙箱拦截了高危命令（规则 " + hit + "）。" +
				"确需执行请由用户手动运行，或设 LAS_SANDBOX=off 关闭沙箱。"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(config.ToolExecTimeout)*time.Second)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", cmdStr)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	}
	c.Dir = GetWorkspace()
	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	runErr := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("错误：命令超时（>%d秒）", config.ToolExecTimeout)
	}
	out := strings.TrimSpace(outBuf.String())
	errS := strings.TrimSpace(errBuf.String())
	var parts []string
	if out != "" {
		parts = append(parts, out)
	}
	if errS != "" {
		parts = append(parts, "[stderr]\n"+errS)
	}
	if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() != 0 {
		parts = append(parts, fmt.Sprintf("[退出码 %d]", ee.ExitCode()))
	}
	if len(parts) == 0 {
		return "(无输出)"
	}
	return strings.Join(parts, "\n")
}

// executors 全部工具执行器（dispatch.go 补充 call_model 等条件工具）。
var executors = map[string]func(map[string]any) string{
	"read_file":        execReadFile,
	"write_file":       execWriteFile,
	"list_dir":         execListDir,
	"glob_search":      execGlobSearch,
	"grep_search":      execGrepSearch,
	"index_search":     execIndexSearch,
	"kb_search":        execKBSearch,
	"run_shell":        execRunShell,
	"web_search":       execWebSearch,
	"lsp_diagnostics":  execLSPDiagnostics,
	"call_model":       execCallModel,
}

// ExecuteTool 执行工具，返回结果字符串。未知工具/执行异常都返回错误文本
//（错误只作为普通工具结果回给模型，循环不中断，并附「继续任务」提示）。
func ExecuteTool(name string, arguments map[string]any) (result string) {
	executor, ok := executors[name]
	if !ok {
		return "错误：未知工具 " + name
	}
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("错误: %v\n（此工具执行失败。请检查参数后重试，或改用其他工具/其他方法继续完成当前任务，不要因此停止。）", r)
		}
	}()
	return executor(arguments)
}
