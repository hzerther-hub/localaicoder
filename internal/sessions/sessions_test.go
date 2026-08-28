package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetDir(dir)
	t.Cleanup(func() { Close(); config.SetDir("") })
	return dir
}

func TestSaveLoadDelete(t *testing.T) {
	setup(t)
	sid := NewID()
	if len(sid) != 12 {
		t.Fatalf("ID 应为 12 位 hex, got %q", sid)
	}
	msgs := []msg.Msg{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "你好"},
		{"role": "assistant", "content": "在"},
	}
	if err := Save(sid, msgs, MakeTitle("你好世界这是一段很长的首条消息内容超过二十四个字符"), "/ws", nil); err != nil {
		t.Fatal(err)
	}
	got := Load(sid)
	if got == nil {
		t.Fatal("应能读回会话")
	}
	if got.Workspace != "/ws" || len(got.Messages) != 3 {
		t.Fatalf("会话内容不符: %+v", got)
	}
	if !strings.HasSuffix(got.Title, "…") || len([]rune(got.Title)) != 25 {
		t.Fatalf("标题应按 rune 截断 24 字加省略号: %q", got.Title)
	}
	if !Delete(sid) || Load(sid) != nil {
		t.Fatal("删除后不应再读到")
	}
}

func TestMakeTitle(t *testing.T) {
	if MakeTitle("  a\nb  c ") != "a b c" {
		t.Fatalf("应压平空白: %q", MakeTitle("  a\nb  c "))
	}
	if MakeTitle("") != "新会话" {
		t.Fatal("空标题兜底")
	}
}

func TestListFilterAndRename(t *testing.T) {
	setup(t)
	Save("aaa000000001", []msg.Msg{{"role": "user", "content": "python 问题"}}, "T1", "/ws1", nil)
	Save("bbb000000002", []msg.Msg{{"role": "user", "content": "go 问题"}}, "T2", "/ws2", nil)

	list := ListSessions(20, "", "")
	if len(list) != 2 {
		t.Fatalf("应列出 2 条, got %d", len(list))
	}
	if list := ListSessions(20, "/ws1", ""); len(list) != 1 || list[0].ID != "aaa000000001" {
		t.Fatalf("目录过滤不符: %v", list)
	}
	if list := ListSessions(20, "", "go"); len(list) != 1 {
		t.Fatalf("内容搜索不符: %v", list)
	}
	if !Rename("aaa000000001", "新标题") {
		t.Fatal("改名应成功")
	}
	if got := Load("aaa000000001"); got.Title != "新标题" {
		t.Fatalf("改名后标题不符: %q", got.Title)
	}
}

func TestLegacyJSONMigration(t *testing.T) {
	dir := setup(t)
	legacy := filepath.Join(dir, "sessions")
	_ = os.MkdirAll(legacy, 0o755)
	old := map[string]any{
		"title": "旧会话", "created": 1000000.0, "updated": 1000000.0,
		"workspace": "/old", "messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		}, "notes": []any{},
	}
	b, _ := json.Marshal(old)
	_ = os.WriteFile(filepath.Join(legacy, "legacy000001.json"), b, 0o644)

	got := Load("legacy000001")
	if got == nil {
		t.Fatal("旧 JSON 会话应迁移进 SQLite 并可读")
	}
	if got.Title != "旧会话" || got.Workspace != "/old" {
		t.Fatalf("迁移内容不符: %+v", got)
	}
	if got.Created != 1000000 {
		t.Fatalf("迁移应保留原时间戳: %d", got.Created)
	}
}
