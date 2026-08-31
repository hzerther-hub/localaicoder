package agent

// 识图预路由端到端：本地大脑不识图时图片轮换识图目标；
// 本地大脑识图时保持本地不换。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"localai/internal/cache"
	"localai/internal/config"
	"localai/internal/msg"
)

func writeFakePNG(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(p, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAgentVisionRoutesToVisionTarget(t *testing.T) {
	config.SetDir(t.TempDir())
	cache.Reset()
	t.Cleanup(func() { config.SetDir(""); cache.Reset() })

	img := writeFakePNG(t)

	var visionHits int32
	visionSrv := textServer(t, "V", &visionHits)
	defer visionSrv.Close()

	config.SaveModelsData(map[string]any{
		"default":         "gpulocal-8080/qwen3-coder-30b",
		"dispatch_model":  "gpulocal-8080/qwen3-coder-30b", // 本地大脑：不识图
		"dispatch_vision": "v/vision",                       // 云端识图目标
		"providers": []any{
			map[string]any{
				"id": "gpulocal-8080", "name": "本地不识图", "base_url": "http://127.0.0.1:8080/v1", "api_key": "local-noauth",
				"models": []any{map[string]any{"id": "qwen3-coder-30b", "vision": false}},
			},
			map[string]any{
				"id": "v", "name": "云端识图", "base_url": visionSrv.URL, "api_key": "k",
				"models": []any{map[string]any{"id": "vision", "vision": true}},
			},
		},
	})

	m := config.FindModel("gpulocal-8080/qwen3-coder-30b")
	if m == nil {
		t.Fatal("找不到本地模型")
	}
	var events []msg.Event
	a := New(func(e msg.Event) { events = append(events, e) }, nil, nil, ModeAlways, m)

	text, err := a.Run("这张图是什么", nil, []any{img})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "V") {
		t.Fatalf("识图轮应由识图目标完成, got %q", text)
	}
	if atomic.LoadInt32(&visionHits) != 1 {
		t.Fatalf("识图目标应命中 1 次, got %d", visionHits)
	}
	visionDecisions := 0
	for _, e := range events {
		if msg.S(e, "type") == "routing" && msg.S(e, "decision") == "vision" {
			visionDecisions++
		}
	}
	if visionDecisions != 1 {
		t.Fatalf("应发出 1 个 vision 路由事件, got %d", visionDecisions)
	}
}

func TestAgentVisionStaysLocalWhenVisionCapable(t *testing.T) {
	config.SetDir(t.TempDir())
	cache.Reset()
	t.Cleanup(func() { config.SetDir(""); cache.Reset() })

	img := writeFakePNG(t)

	var localHits int32
	// 区分健康探测（/models）与真正的对话请求，避免健康探测计入命中
	localSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(200)
			return
		}
		atomic.AddInt32(&localHits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(`{"choices":[{"delta":{"content":"L"},"finish_reason":"stop"}]}`))
	}))
	defer localSrv.Close()

	config.SaveModelsData(map[string]any{
		"default":         "gpulocal-8099/ornith-1.5-35b",
		"dispatch_model":  "gpulocal-8099/ornith-1.5-35b", // 本地大脑：识图
		"dispatch_vision": "v/vision",
		"providers": []any{
			map[string]any{
				"id": "gpulocal-8099", "name": "本地识图", "base_url": localSrv.URL, "api_key": "local-noauth",
				"models": []any{map[string]any{"id": "ornith-1.5-35b", "vision": true}},
			},
			map[string]any{
				"id": "v", "name": "云端识图", "base_url": "http://127.0.0.1:8080/v1", "api_key": "k",
				"models": []any{map[string]any{"id": "vision", "vision": true}},
			},
		},
	})

	m := config.FindModel("gpulocal-8099/ornith-1.5-35b")
	if m == nil {
		t.Fatal("找不到本地模型")
	}
	var events []msg.Event
	a := New(func(e msg.Event) { events = append(events, e) }, nil, nil, ModeAlways, m)

	text, err := a.Run("这张图是什么", nil, []any{img})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "L") {
		t.Fatalf("识图轮应由本地识图大脑完成, got %q", text)
	}
	if atomic.LoadInt32(&localHits) != 1 {
		t.Fatalf("本地识图大脑应命中 1 次, got %d", localHits)
	}
	for _, e := range events {
		if msg.S(e, "type") == "routing" && msg.S(e, "decision") == "vision" {
			t.Fatalf("本地识图大脑不应触发 vision 路由: %v", e)
		}
	}
}
