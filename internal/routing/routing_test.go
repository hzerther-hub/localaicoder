package routing

import (
	"crypto/sha1"
	"strings"
	"testing"

	"localai/internal/config"
)

func testCfg() Config {
	return Config{
		Enabled: true, SimpleModel: "p/simple", StrongModel: "p/strong",
		SimpleMaxChars: 160, SimpleMaxWords: 28,
	}
}

func TestClassifyOrder(t *testing.T) {
	cfg := testCfg()
	cases := []struct {
		name string
		in   Input
		want Decision
	}{
		{"非文本附件→strong", Input{UserText: "hi", HasNonTextContent: true, TurnNumber: 3}, DecisionStrong},
		{"空文本→simple", Input{UserText: "  ", TurnNumber: 3}, DecisionSimple},
		{"首轮→strong", Input{UserText: "hi", TurnNumber: 1}, DecisionStrong},
		{"代码块→strong", Input{UserText: "看看这段 `fmt.Println` 什么问题", TurnNumber: 3}, DecisionStrong},
		{"英文关键词→strong", Input{UserText: "please refactor this module", TurnNumber: 3}, DecisionStrong},
		{"中文关键词→strong", Input{UserText: "帮我排查一下这个报错的根因", TurnNumber: 3}, DecisionStrong},
		{"无关键词短句→simple", Input{UserText: "read the file and print its contents", TurnNumber: 3}, DecisionSimple},
		{"多段落→strong", Input{UserText: "第一段。\n\n第二段。", TurnNumber: 3}, DecisionStrong},
		{"超长→strong", Input{UserText: strings.Repeat("长", 200), TurnNumber: 3}, DecisionStrong},
		{"英文超词数→strong", Input{UserText: strings.Repeat("word ", 40), TurnNumber: 3}, DecisionStrong},
		{"短句→simple", Input{UserText: "继续", TurnNumber: 3}, DecisionSimple},
		{"中文弱信号区→unsure", Input{UserText: strings.Repeat("这", 130), TurnNumber: 3}, DecisionUnsure},
	}
	for _, c := range cases {
		if got := Classify(c.in, cfg); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestClassifyChineseLength(t *testing.T) {
	cfg := testCfg()
	// 中文按字符数：>160 → strong；≥120 弱信号区 → unsure；短句 → simple
	if got := Classify(Input{UserText: strings.Repeat("汉", 170), TurnNumber: 3}, cfg); got != DecisionStrong {
		t.Fatalf("170 汉字应 strong, got %v", got)
	}
	if got := Classify(Input{UserText: strings.Repeat("汉", 130), TurnNumber: 3}, cfg); got != DecisionUnsure {
		t.Fatalf("130 汉字应 unsure, got %v", got)
	}
	if got := Classify(Input{UserText: strings.Repeat("短", 10), TurnNumber: 3}, cfg); got != DecisionSimple {
		t.Fatalf("10 汉字应 simple, got %v", got)
	}
}

func TestResolveConfigFallbacks(t *testing.T) {
	// 隔离配置目录，注册 p/simple、p/strong 两个假模型
	config.SetDir(t.TempDir())
	data := map[string]any{
		"default": "p/simple",
		"providers": []any{map[string]any{
			"id": "p", "name": "P", "base_url": "http://x/v1", "api_key": "k",
			"models": []any{
				map[string]any{"id": "simple"},
				map[string]any{"id": "strong"},
			},
		}},
	}
	config.SaveModelsData(data)

	// 未启用 → 不路由
	if k, _ := Resolve(Input{UserText: "x", TurnNumber: 3}, Config{Enabled: false, StrongModel: "p/strong"}); k != "" {
		t.Fatalf("禁用时应返回空 key, got %q", k)
	}
	// strong 缺失 → 整体禁用
	if k, _ := Resolve(Input{UserText: "x", TurnNumber: 3}, Config{Enabled: true, SimpleModel: "p/s"}); k != "" {
		t.Fatalf("缺 strong 应禁用, got %q", k)
	}
	// simple 缺失 → 塌缩 strong
	cfg := Config{Enabled: true, StrongModel: "p/strong"}
	k, d := Resolve(Input{UserText: "继续", TurnNumber: 3}, cfg)
	if k != "p/strong" || d != DecisionStrong {
		t.Fatalf("simple 缺失应塌缩 strong, got %q %v", k, d)
	}
}

func TestArbitrateCache(t *testing.T) {
	// 无本地大脑可用（FindModel 找不到）→ 恒 simple，但结果进缓存
	text := "某个拿不准的请求"
	d := Arbitrate(text)
	if d != DecisionSimple {
		t.Fatalf("无大脑应归 simple, got %v", d)
	}
	sum := sha1.Sum([]byte(text))
	arbMu.Lock()
	_, ok := arbCache[string(sum[:])]
	arbMu.Unlock()
	if !ok {
		t.Fatal("仲裁结果应写入缓存")
	}
}

// 阈值常量对齐 config 默认值（防止无意漂移）。
func TestDefaultThresholdsMatchConfig(t *testing.T) {
	config.SetDir(t.TempDir())
	if config.SmartRouteDefaultMaxChars != 160 || config.SmartRouteDefaultMaxWords != 28 {
		t.Fatal("config 默认阈值应为 160/28")
	}
	if cfg := config.GetSmartRouting(); cfg.SimpleMaxChars != 160 || cfg.SimpleMaxWords != 28 {
		t.Fatalf("GetSmartRouting 默认阈值漂移: %+v", cfg)
	}
}
