package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localai/internal/config"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetDir(dir)
	t.Cleanup(func() { config.SetDir("") })
	return dir
}

// ---------------- 沙箱 ----------------

func TestShellCommandBlocked(t *testing.T) {
	mustBlock := []string{
		"rm -rf /",
		"rm -rf /*",
		"mkfs.ext4 /dev/sda1",
		"dd if=img of=/dev/sdb",
		":(){ :|:& };:",
		"shutdown -h now",
		"format C:",
		"rd /s /q C:\\",
		"del /s /q C:\\*",
	}
	for _, cmd := range mustBlock {
		if hit := ShellCommandBlocked(cmd); hit == "" {
			t.Errorf("高危命令未被拦截: %q", cmd)
		}
	}
	safe := []string{
		"ls -la",
		"git status",
		"go build ./...",
		"echo hello",
		"rm -rf ./build", // 相对路径不在拦截范围
	}
	for _, cmd := range safe {
		if hit := ShellCommandBlocked(cmd); hit != "" {
			t.Errorf("正常命令被误拦: %q (规则 %s)", cmd, hit)
		}
	}
}

func TestPathInWorkspace(t *testing.T) {
	dir := setup(t)
	ws := filepath.Join(dir, "ws")
	_ = os.MkdirAll(ws, 0o755)
	restore := PushWorkspace(ws)
	defer restore()

	if !PathInWorkspace(filepath.Join(ws, "a.txt"), "") {
		t.Fatal("工作目录内文件应放行")
	}
	if !PathInWorkspace(ws, "") {
		t.Fatal("工作目录自身应放行")
	}
	if PathInWorkspace(filepath.Join(dir, "outside.txt"), "") {
		t.Fatal("工作目录外应拒绝")
	}
	if PathInWorkspace(filepath.Join(ws, "..", "escape.txt"), "") {
		t.Fatal("路径穿越应拒绝")
	}
}

// ---------------- 内置工具 ----------------

func TestReadFileNumbered(t *testing.T) {
	dir := setup(t)
	p := filepath.Join(dir, "a.py")
	_ = os.WriteFile(p, []byte("hello\nworld"), 0o644)
	restore := PushWorkspace(dir)
	defer restore()
	out := ExecuteTool("read_file", map[string]any{"path": p})
	if !strings.Contains(out, "   1 | hello") || !strings.Contains(out, "   2 | world") {
		t.Fatalf("带行号输出不符:\n%s", out)
	}
	if out2 := ExecuteTool("read_file", map[string]any{"path": "no-such.txt"}); !strings.Contains(out2, "错误") {
		t.Fatal("缺失文件应返回错误文本")
	}
}

func TestWriteFileSandbox(t *testing.T) {
	dir := setup(t)
	ws := filepath.Join(dir, "ws")
	_ = os.MkdirAll(ws, 0o755)
	restore := PushWorkspace(ws)
	defer restore()

	out := ExecuteTool("write_file", map[string]any{
		"path": "sub/b.txt", "content": "内容"})
	if !strings.Contains(out, "已写入") {
		t.Fatalf("写入应成功: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "sub", "b.txt")); err != nil || string(b) != "内容" {
		t.Fatalf("写入内容不符: %v", err)
	}
	outside := filepath.Join(dir, "outside.txt")
	out = ExecuteTool("write_file", map[string]any{"path": outside, "content": "x"})
	if !strings.Contains(out, "沙箱") {
		t.Fatalf("工作目录外写入应被沙箱拦截: %s", out)
	}
}

func TestListDirAndGlob(t *testing.T) {
	dir := setup(t)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "m.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "n.go"), []byte("y"), 0o644)
	restore := PushWorkspace(dir)
	defer restore()

	out := ExecuteTool("list_dir", map[string]any{})
	if !strings.Contains(out, "pkg/") || !strings.Contains(out, "m.go") {
		t.Fatalf("列目录不符:\n%s", out)
	}
	out = ExecuteTool("glob_search", map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(out, "m.go") || !strings.Contains(out, filepath.Join("pkg", "n.go")) {
		t.Fatalf("** glob 不符:\n%s", out)
	}
}

func TestGrepSearch(t *testing.T) {
	dir := setup(t)
	_ = os.WriteFile(filepath.Join(dir, "code.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "skip.txt"), []byte("alpha"), 0o644)
	restore := PushWorkspace(dir)
	defer restore()

	out := ExecuteTool("grep_search", map[string]any{"pattern": "alpha", "path": "."})
	if !strings.Contains(out, "code.txt") {
		t.Fatalf("grep 应命中 code.txt:\n%s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatal("grep 应跳过 node_modules")
	}
	// 无效正则退回字面量
	out = ExecuteTool("grep_search", map[string]any{"pattern": "(unclosed", "path": "."})
	if strings.Contains(out, "panic") {
		t.Fatal("无效正则不应崩溃")
	}
}

func TestRunShell(t *testing.T) {
	dir := setup(t)
	restore := PushWorkspace(dir)
	defer restore()
	out := ExecuteTool("run_shell", map[string]any{"command": "echo localai-test"})
	if !strings.Contains(out, "localai-test") {
		t.Fatalf("shell 输出不符: %s", out)
	}
	out = ExecuteTool("run_shell", map[string]any{"command": "rm -rf /"})
	if !strings.Contains(out, "沙箱拦截") {
		t.Fatalf("高危命令应被拦截: %s", out)
	}
}

func TestUnknownTool(t *testing.T) {
	out := ExecuteTool("no_such_tool", map[string]any{})
	if !strings.Contains(out, "未知工具") {
		t.Fatalf("未知工具提示不符: %s", out)
	}
}

func TestIsWriteToolAndSchemas(t *testing.T) {
	if !IsWriteTool("write_file") || !IsWriteTool("run_shell") {
		t.Fatal("write_file/run_shell 应为可写")
	}
	if IsWriteTool("read_file") || IsWriteTool("grep_search") {
		t.Fatal("只读工具不应标记可写")
	}
	all := ToolSchemas()
	if len(all) != 9 {
		t.Fatalf("应有 9 个内置工具, got %d", len(all))
	}
	ro := ReadOnlySchemas()
	if len(ro) != 7 {
		t.Fatalf("只读模式应剩 7 个工具, got %d", len(ro))
	}
}

func TestDescribeArguments(t *testing.T) {
	if got := DescribeArguments("run_shell", map[string]any{"command": "ls"}); got != "ls" {
		t.Fatalf("run_shell 摘要不符: %q", got)
	}
	long := strings.Repeat("x", 500)
	got := DescribeArguments("write_file", map[string]any{"path": "a.py", "content": long})
	if !strings.Contains(got, "文件: a.py") || !strings.Contains(got, "(截断)") {
		t.Fatalf("write_file 摘要不符: %q", got)
	}
}

func TestGitBranch(t *testing.T) {
	dir := setup(t)
	if GitBranch(dir) != "" {
		t.Fatal("非 git 目录应返回空")
	}
	gitDir := filepath.Join(dir, ".git", "refs", "heads")
	_ = os.MkdirAll(gitDir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	if got := GitBranch(dir); got != "main" {
		t.Fatalf("分支应为 main, got %q", got)
	}
}

func TestValidateDispatchTarget(t *testing.T) {
	dir := setup(t)
	_ = dir
	cfg := map[string]any{"default": "p/m"}
	_ = cfg
	// 默认配置：flash/pro/vision 三个云端目标在白名单
	flash := config.GetDispatchFlash()
	if ok, _ := ValidateDispatchTarget(flash); !ok {
		t.Fatalf("白名单目标 %s 应放行", flash)
	}
	if ok, err := ValidateDispatchTarget("no-such/target"); ok || !strings.Contains(err, "白名单") {
		t.Fatalf("白名单外应拒绝: %v", err)
	}
}
