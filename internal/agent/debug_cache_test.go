package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"localai/internal/cache"
	"localai/internal/msg"
	"localai/internal/tools"
)

func TestDebugRunTwice(t *testing.T) {
	ws, model := setup(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"content":"缓存答案"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	}))
	model.BaseURL = srv.URL
	restore := tools.PushWorkspace(ws)
	defer restore()

	a1 := New(func(msg.Event) {}, nil, nil, ModeAlways, model)
	if _, err := a1.Run("同样的问题", nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("第一次后 calls=%d, backend=%s, stats=%v", atomic.LoadInt32(&calls), cache.BackendName(), cache.Stats())

	a2 := New(func(msg.Event) {}, nil, nil, ModeAlways, model)
	if _, err := a2.Run("同样的问题", nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("第二次后 calls=%d, CacheHits=%d, backend=%s, stats=%v",
		atomic.LoadInt32(&calls), a2.CacheHits, cache.BackendName(), cache.Stats())
}
