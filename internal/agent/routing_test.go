package agent

// 智能路由端到端：首轮走 strong，后续简单轮走 simple；事件与计数正确。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"localai/internal/cache"
	"localai/internal/config"
	"localai/internal/msg"
)

func textServer(t *testing.T, mark string, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(fmt.Sprintf(
			`{"choices":[{"delta":{"content":%q},"finish_reason":"stop"}]}`, mark)))
	}))
}

func TestAgentSmartRoutingPicksSimple(t *testing.T) {
	config.SetDir(t.TempDir())
	cache.Reset()
	t.Cleanup(func() { config.SetDir(""); cache.Reset() })

	var strongHits, simpleHits int32
	strongSrv := textServer(t, "S", &strongHits)
	simpleSrv := textServer(t, "q", &simpleHits)
	defer strongSrv.Close()
	defer simpleSrv.Close()

	config.SaveModelsData(map[string]any{
		"default": "s/strong",
		"providers": []any{
			map[string]any{
				"id": "s", "name": "STRONG", "base_url": strongSrv.URL, "api_key": "k",
				"models": []any{map[string]any{"id": "strong"}},
			},
			map[string]any{
				"id": "q", "name": "SIMPLE", "base_url": simpleSrv.URL, "api_key": "k",
				"models": []any{map[string]any{"id": "simple"}},
			},
		},
		"smart_routing": map[string]any{
			"enabled":      true,
			"simple_model": "q/simple",
			"strong_model": "s/strong",
		},
	})

	m := config.FindModel("s/strong")
	if m == nil {
		t.Fatal("找不到 s/strong")
	}
	var events []msg.Event
	a := New(func(e msg.Event) { events = append(events, e) },
		nil, nil, ModeAlways, m)

	// 首轮（含"重构/架构"强关键词）→ strong
	if _, err := a.Run("帮我重构这个项目的架构", nil, nil); err != nil {
		t.Fatal(err)
	}
	// 第二轮简单句 → simple
	if _, err := a.Run("继续", nil, nil); err != nil {
		t.Fatal(err)
	}

	if atomic.LoadInt32(&strongHits) != 1 {
		t.Fatalf("首轮应命中 strong 服务器 1 次, got %d", strongHits)
	}
	if atomic.LoadInt32(&simpleHits) != 1 {
		t.Fatalf("简单轮应命中 simple 服务器 1 次, got %d", simpleHits)
	}
	if a.Routing.Strong != 1 || a.Routing.Simple != 1 {
		t.Fatalf("路由计数不符: %+v", a.Routing)
	}
	var routingDecisions []string
	for _, e := range events {
		if msg.S(e, "type") == "routing" {
			routingDecisions = append(routingDecisions, msg.S(e, "decision"))
		}
	}
	if strings.Join(routingDecisions, ",") != "strong,simple" {
		t.Fatalf("routing 事件序应为 strong,simple, got %v", routingDecisions)
	}
}

func TestAgentRoutingEscalation(t *testing.T) {
	config.SetDir(t.TempDir())
	cache.Reset()
	t.Cleanup(func() { config.SetDir(""); cache.Reset() })

	// simple 端点持续 429；strong 端点正常
	var strongHits int32
	simpleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	strongSrv := textServer(t, "S", &strongHits)
	defer simpleSrv.Close()
	defer strongSrv.Close()

	config.SaveModelsData(map[string]any{
		"default": "q/simple",
		"providers": []any{
			map[string]any{
				"id": "s", "name": "STRONG", "base_url": strongSrv.URL, "api_key": "k",
				"models": []any{map[string]any{"id": "strong"}},
			},
			map[string]any{
				"id": "q", "name": "SIMPLE", "base_url": simpleSrv.URL, "api_key": "k",
				"models": []any{map[string]any{"id": "simple"}},
			},
		},
		"smart_routing": map[string]any{
			"enabled":      true,
			"simple_model": "q/simple",
			"strong_model": "s/strong",
		},
	})

	m := config.FindModel("q/simple")
	var events []msg.Event
	a := New(func(e msg.Event) { events = append(events, e) },
		nil, nil, ModeAlways, m)

	// 第二轮简单句路由到 simple；429 → 升级 strong 完成本轮
	if _, err := a.Run("第一轮占位", nil, nil); err != nil {
		t.Fatal(err)
	}
	text, err := a.Run("继续", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "S") {
		t.Fatalf("升级后应由 strong 完成回答, got %q", text)
	}
	// 首轮路由到 strong（1 次）+ 第二轮升级重试（1 次）
	if atomic.LoadInt32(&strongHits) != 2 {
		t.Fatalf("升级应累计命中 strong 服务器 2 次, got %d", strongHits)
	}
	if a.Routing.Escalations != 1 {
		t.Fatalf("升级 tally 应为 1: %+v", a.Routing)
	}
	escalations := 0
	for _, e := range events {
		if msg.S(e, "type") == "routing" && msg.S(e, "decision") == "escalate" {
			escalations++
		}
	}
	if escalations != 1 {
		t.Fatalf("应发出 1 个 escalate 事件, got %d", escalations)
	}
}
