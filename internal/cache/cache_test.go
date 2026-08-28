package cache

import (
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

func setup(t *testing.T) {
	t.Helper()
	config.SetDir(t.TempDir())
	t.Cleanup(func() {
		Reset()
		config.SetDir("")
	})
	Reset()
}

func TestMemoryBackend(t *testing.T) {
	setup(t)
	SaveSettings(map[string]any{"backend": "memory"})
	if BackendName() != "memory" {
		t.Fatalf("后端应为 memory, got %s", BackendName())
	}
	Put("k1", "v1", 60)
	if v, ok := Get("k1"); !ok || v != "v1" {
		t.Fatalf("应命中 k1, got %q %v", v, ok)
	}
	Put("k2", "v2", -1) // ttl 为负 → 立即过期
	if _, ok := Get("k2"); ok {
		t.Fatal("负 ttl 应过期")
	}
	if !Clear() {
		t.Fatal("清空应成功")
	}
	if _, ok := Get("k1"); ok {
		t.Fatal("清空后不应命中")
	}
}

func TestSQLiteBackend(t *testing.T) {
	setup(t) // 默认 auto → sqlite
	if BackendName() != "sqlite" {
		t.Fatalf("后端应为 sqlite, got %s", BackendName())
	}
	Put("a", "1", 60)
	if v, ok := Get("a"); !ok || v != "1" {
		t.Fatalf("sqlite 命中失败: %q", v)
	}
	s := Stats()
	if s["backend"] != "sqlite" || s["entries"].(int) < 1 {
		t.Fatalf("stats 不符: %v", s)
	}
}

func TestLLMCacheRoundtrip(t *testing.T) {
	setup(t)
	messages := []msg.Msg{{"role": "user", "content": "ping"}}
	events := []any{
		map[string]any{"type": "text", "delta": "pong"},
	}
	PutLLM("m", messages, nil, events)
	got := GetLLM("m", messages, nil)
	if got == nil || len(got) != 1 {
		t.Fatalf("LLM 缓存应命中: %v", got)
	}
	// 不同消息 → 不同键
	if GetLLM("m", []msg.Msg{{"role": "user", "content": "other"}}, nil) != nil {
		t.Fatal("不同消息不应命中")
	}
}

func TestToolCacheTTLGate(t *testing.T) {
	setup(t)
	SaveSettings(map[string]any{"tool_ttl": 0, "backend": "memory"})
	if _, ok := GetTool("read_file", map[string]any{"path": "x"}, "/ws"); ok {
		t.Fatal("tool_ttl=0 时不应读写缓存")
	}
	SaveSettings(map[string]any{"tool_ttl": 300, "backend": "memory"})
	PutTool("read_file", map[string]any{"path": "x"}, "/ws", "content...")
	if v, ok := GetTool("read_file", map[string]any{"path": "x"}, "/ws"); !ok || v != "content..." {
		t.Fatalf("工具缓存应命中: %q", v)
	}
}

func TestKeyDeterministic(t *testing.T) {
	a := llmKey("m", []msg.Msg{{"role": "user", "content": "x"}}, nil)
	b := llmKey("m", []msg.Msg{{"role": "user", "content": "x"}}, nil)
	if a != b {
		t.Fatal("相同输入的缓存键应一致")
	}
	if a == llmKey("m2", []msg.Msg{{"role": "user", "content": "x"}}, nil) {
		t.Fatal("不同模型的键应不同")
	}
}
