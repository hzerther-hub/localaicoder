package main

import (
	"encoding/json"
	"os"
	"testing"

	"localai/internal/config"
)

// 回归：无模型的供应商也要出现在供应商列表里（否则侧栏选不中它，
// 手动输入模型 ID 的第一次添加会静默落空）。
func TestListProvidersIncludesModellessProvider(t *testing.T) {
	dir := t.TempDir()
	config.SetDir(dir)
	t.Cleanup(func() { config.SetDir("") })

	raw := map[string]any{
		"default": "",
		"providers": []any{
			map[string]any{
				"id": "deepseek", "name": "DeepSeek",
				"base_url": "https://api.deepseek.com/v1", "api_key": "sk-test",
				"models": []any{
					map[string]any{"id": "deepseek-v4-pro", "name": "deepseek-v4-pro"},
				},
			},
			map[string]any{
				"id": "fresh", "name": "Fresh",
				"base_url": "https://fresh.example.com/v1", "api_key": "sk-x",
				"models": []any{},
			},
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ModelsFile(), b, 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	ps := a.ListProviders()
	byID := map[string]int{}
	for i, p := range ps {
		byID[p.ID] = i
	}
	i, ok := byID["fresh"]
	if !ok {
		t.Fatal("无模型的供应商 fresh 未出现在列表中（复现第一次添加失败的根因）")
	}
	if len(ps[i].Models) != 0 {
		t.Fatalf("fresh 应有 0 个模型, got %d", len(ps[i].Models))
	}
	if ps[i].BaseURL != "https://fresh.example.com/v1" || ps[i].APIKey != "sk-x" {
		t.Fatal("fresh 的 base_url/api_key 未正确带出")
	}
	if _, ok := byID["deepseek"]; !ok {
		t.Fatal("有模型的 deepseek 不应受影响")
	}
}
