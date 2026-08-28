package codeindex

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
	t.Cleanup(func() {
		CloseAll()
		config.SetDir("")
	})
	return dir
}

func TestTokenize(t *testing.T) {
	terms := Tokenize("getUserAccountInfo user_name 中文注释")
	joined := " " + strings.Join(terms, " ") + " "
	for _, want := range []string{"getuseraccountinfo", "user", "account", "info", "name", "中文"} {
		if !strings.Contains(joined, want) {
			t.Errorf("分词缺 %q: %v", want, terms)
		}
	}
	if strings.Contains(joined, " the ") {
		t.Error("停用词应被过滤")
	}
}

func TestChunkLines(t *testing.T) {
	chunks := ChunkLinesOf(120) // 50 行块 / 40 步进
	if len(chunks) < 3 {
		t.Fatalf("120 行应至少 3 块, got %d", len(chunks))
	}
	if chunks[0][0] != 1 || chunks[0][1] != 50 || chunks[1][0] != 41 {
		t.Fatalf("块边界不符（含 10 行重叠）: %v", chunks)
	}
}

func TestBuildAndSearch(t *testing.T) {
	dir := setup(t)
	ws := filepath.Join(dir, "proj")
	_ = os.MkdirAll(filepath.Join(ws, "app"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "app", "auth.go"),
		[]byte("package app\n\nfunc Login(user string) error { return nil }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, "util.py"),
		[]byte("def parse_config(path):\n    return {}\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(ws, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "node_modules", "junk.js"),
		[]byte("var login = 1\n"), 0o644)

	stats := Build(ws, false, nil)
	if stats.FilesIndexed != 2 {
		t.Fatalf("应索引 2 个文件（跳过 node_modules）, got %+v", stats)
	}
	hits := Search(ws, "login 用户登录", 5)
	found := false
	for _, h := range hits {
		if strings.Contains(h.File, "auth.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("login 检索应命中 auth.go: %+v", hits)
	}
	if hits2 := Search(ws, "junk", 5); len(hits2) > 0 {
		t.Fatal("跳过目录的内容不应被检索")
	}
	// 增量：未变化跳过
	stats2 := Build(ws, false, nil)
	if stats2.Updated != 0 || stats2.SkippedUnchanged != 2 {
		t.Fatalf("增量应全部跳过: %+v", stats2)
	}
}

func TestEnsureAndStats(t *testing.T) {
	dir := setup(t)
	ws := filepath.Join(dir, "empty")
	_ = os.MkdirAll(ws, 0o755)
	s := Ensure(ws, nil)
	if s["chunks"].(int) != 0 {
		t.Fatalf("空目录应为 0 块: %v", s)
	}
	if !strings.HasSuffix(s["db"].(string), ".db") {
		t.Fatalf("库路径不符: %v", s["db"])
	}
}
