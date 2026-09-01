package config

import (
	"encoding/json"
	"os"
	"testing"

	"localai/internal/msg"
)

// 回归：手动给既有 provider 追加模型，一次调用就要落盘并在 LoadModels 视图可见
// （对应桌面端「模型设置 → 输入模型 ID → 添加模型」的第一次点击）。
func TestAugmentProviderModelsOnce(t *testing.T) {
	setup(t)
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
		},
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ModelsFile(), b, 0o644); err != nil {
		t.Fatal(err)
	}

	if n := AugmentProviderModels("deepseek", []string{"deepseek-v4.6"}, false); n != 1 {
		t.Fatalf("一次调用应新增 1 个模型, got %d", n)
	}

	data, err := os.ReadFile(ModelsFile())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pv := range msg.L(m, "providers") {
		p, ok := pv.(map[string]any)
		if !ok || msg.S(p, "id") != "deepseek" {
			continue
		}
		for _, mv := range msg.L(p, "models") {
			mm, _ := mv.(map[string]any)
			if msg.S(mm, "id") == "deepseek-v4.6" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("添加后 models.json 里没有新模型")
	}

	models, _ := LoadModels()
	seen := false
	for _, mm := range models {
		if mm.Key == "deepseek/deepseek-v4.6" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("添加后 LoadModels 视图没有新模型")
	}

	// 重复添加应去重：返回 0，且不产生第二条
	if n := AugmentProviderModels("deepseek", []string{"deepseek-v4.6"}, false); n != 0 {
		t.Fatalf("重复添加应返回 0, got %d", n)
	}
}
