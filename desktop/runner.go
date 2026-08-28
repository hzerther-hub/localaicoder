// desktop runner：agent 会话运行器 —— goroutine 跑循环，
// 事件经 Wails runtime 推送，审批走 事件请求 + 方法应答（600s 超时）。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/sessions"
	"localai/internal/tools"
)

const approvalTimeout = 600 * time.Second

// RunManager 管理当前唯一的 agent 运行。
type RunManager struct {
	ctx     context.Context
	mu      sync.Mutex
	running bool
	history map[string][]msg.Msg // sessionID -> messages
	stop    atomic.Bool
	approve map[string]chan bool
	apMu    sync.Mutex
	last    UsageSnapshot // 最近一次运行统计
}

// UsageSnapshot 一次运行的用量/费用快照。
type UsageSnapshot struct {
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Total      int     `json:"total_tokens"`
	Cached     int     `json:"cached_tokens"`
	Requests   int     `json:"requests"`
	CostUSD    float64 `json:"cost_usd"`
}

// NewRunManager 创建运行器。
func NewRunManager(ctx context.Context) *RunManager {
	return &RunManager{ctx: ctx, history: map[string][]msg.Msg{}, approve: map[string]chan bool{}}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Send 启动一次 agent 运行（会话内串行；已在跑则返回错误）。
// attachments 元素为文件路径字符串或 {"kind":"snippet",...} 片段字典。
func (r *RunManager) Send(sessionID string, model config.ModelConfig,
	text string, attachments []any, mode string) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return errBusy
	}
	r.running = true
	r.mu.Unlock()
	r.stop.Store(false)

	hist := r.history[sessionID]
	if config.GetStandalone() {
		hist = nil // 独立提问：不带历史
	}

	runtime.EventsEmit(r.ctx, "run:started", map[string]any{"sessionId": sessionID})

	go func() {
		var notes []any
		a := &agent.Agent{
			Mode:  mode,
			Model: &model,
			OnEvent: func(e msg.Event) {
				if msg.S(e, "type") == "model_switch" {
					notes = append(notes, map[string]any{
						"kind": "model_switch",
						"from": msg.S(e, "from"),
						"to":   msg.S(msg.M(e, "to"), "key"),
					})
				}
				runtime.EventsEmit(r.ctx, "agent:event", e)
			},
			OnApproval: func(name string, args map[string]any, summary string) bool {
				return r.requestApproval(name, summary)
			},
			OnStop: func() bool { return r.stop.Load() },
		}
		_, runErr := a.Run(text, hist, attachments)

		r.mu.Lock()
		r.running = false
		r.history[sessionID] = a.Messages
		r.mu.Unlock()

		// 记录本次用量/费用快照
		r.mu.Lock()
		r.last = UsageSnapshot{
			Prompt: a.UsageTotal.PromptTokens, Completion: a.UsageTotal.CompletionTokens,
			Total: a.UsageTotal.TotalTokens, Cached: a.UsageTotal.CachedTokens,
			Requests: a.UsageTotal.Requests, CostUSD: a.UsageTotal.CostUSDFloat,
		}
		r.mu.Unlock()

		// 持久化会话（标题取首条用户消息）
		title := sessions.MakeTitle(text)
		if s := sessions.Load(sessionID); s != nil && s.Title != "" && s.Title != "新会话" {
			title = s.Title
		}
		_ = sessions.Save(sessionID, a.Messages, title, tools.GetWorkspace(), notes)
		runtime.EventsEmit(r.ctx, "run:finished", map[string]any{
			"sessionId": sessionID, "error": errStr(runErr),
		})
	}()
	return nil
}

// Stop 请求协作停止（流中/工具间生效）。
func (r *RunManager) Stop() { r.stop.Store(true) }

// RespondApproval 前端应答审批。
func (r *RunManager) RespondApproval(id string, allow bool) {
	r.apMu.Lock()
	ch, ok := r.approve[id]
	delete(r.approve, id)
	r.apMu.Unlock()
	if ok {
		ch <- allow
	}
}

func (r *RunManager) requestApproval(name, summary string) bool {
	id := randID()
	ch := make(chan bool, 1)
	r.apMu.Lock()
	r.approve[id] = ch
	r.apMu.Unlock()
	runtime.EventsEmit(r.ctx, "approval:request", map[string]any{
		"id": id, "name": name, "summary": summary,
	})
	select {
	case allow := <-ch:
		return allow
	case <-time.After(approvalTimeout):
		r.apMu.Lock()
		delete(r.approve, id)
		r.apMu.Unlock()
		return false // 超时视为拒绝
	case <-r.ctx.Done():
		return false
	}
}

// UseStats 返回最近一次运行统计。
func (r *RunManager) UseStats() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, _ := json.Marshal(r.last)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ResetHistory 清空某会话内存历史（新建会话时）。
func (r *RunManager) ResetHistory(sessionID string) {
	r.mu.Lock()
	delete(r.history, sessionID)
	r.mu.Unlock()
}

type busyErr struct{}

func (busyErr) Error() string { return "已有任务在运行" }

var errBusy error = busyErr{}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
