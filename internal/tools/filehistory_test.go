package tools

// 文件改动历史（快照/diff/还原）测试。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localai/internal/config"
)

func setupFH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetDir(dir)
	t.Cleanup(func() {
		config.SetDir("")
		ResetFileHistory()
	})
	return dir
}

func TestFileDiffAndRevert(t *testing.T) {
	setupFH(t)
	ws := t.TempDir()
	p := filepath.Join(ws, "code.go")
	if err := os.WriteFile(p, []byte("package main\n\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 模拟 AI 写入：先快照，再覆盖
	SnapshotBefore(p)
	if err := os.WriteFile(p, []byte("package main\n\nfunc new() {}\n// added\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := len(ChangedFiles()); got != 1 {
		t.Fatalf("ChangedFiles 应为 1: %d", got)
	}
	diff := FileDiff(p)
	if !strings.Contains(diff, "-func old() {}") || !strings.Contains(diff, "+func new() {}") {
		t.Fatalf("diff 应包含增删行:\n%s", diff)
	}
	if !strings.Contains(diff, "@@") {
		t.Fatalf("diff 应含 hunk 头:\n%s", diff)
	}

	// 还原到 AI 动手前
	if err := RevertFile(p); err != nil {
		t.Fatalf("还原失败: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "package main\n\nfunc old() {}\n" {
		t.Fatalf("还原后内容错误: %q", data)
	}
	if len(ChangedFiles()) != 0 {
		t.Fatal("还原后应清掉快照")
	}
	// 无记录的文件
	if err := RevertFile(p); err == nil {
		t.Fatal("无快照还原应报错")
	}
}

func TestFileDiffNewFileAndRevert(t *testing.T) {
	setupFH(t)
	p := filepath.Join(t.TempDir(), "new.txt")
	SnapshotBefore(p) // 不存在
	if err := os.WriteFile(p, []byte("hello\nworld"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := FileDiff(p)
	if !strings.Contains(diff, "新文件") || !strings.Contains(diff, "+hello") {
		t.Fatalf("新文件 diff 异常:\n%s", diff)
	}
	if err := RevertFile(p); err != nil {
		t.Fatalf("新文件还原失败: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("新文件还原后应删除")
	}
}

func TestFileDiffLargeFallback(t *testing.T) {
	setupFH(t)
	p := filepath.Join(t.TempDir(), "big.go")
	// 先写旧内容再快照，再写新内容（46×100 行 vs 45.5×100 行 → DP 超限 → 摘要）
	old := strings.Repeat(strings.Repeat("x", 80)+"\n", 4600)
	cur := strings.Repeat(strings.Repeat("y", 80)+"\n", 4550)
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	SnapshotBefore(p)
	if err := os.WriteFile(p, []byte(cur), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := FileDiff(p)
	if !strings.Contains(diff, "变更过大") {
		t.Fatalf("超限应退化为摘要:\n%s", diff[:200])
	}
}
