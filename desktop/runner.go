// desktop runner：agent 会话运行器 —— 每个会话一个 goroutine 跑循环，
// 支持多会话并发（后台运行不阻塞新会话）、暂停/恢复、协作停止。
// 事件经 Wails runtime 推送（携带 sessionId，前端按会话分流），
// 审批走 事件请求 + 方法应答（600s 超时）。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/sessions"
	"localai/internal/skills"
	"localai/internal/tools"
)

const approvalTimeout = 600 * time.Second

// runState 一个会话的一次运行状态。
type runState struct {
	sessionID string
	cancel    context.CancelFunc
	paused    atomic.Bool
	resume    chan struct{}
	model     string    // 显示用模型名
	startAt   time.Time
	finished  bool
	finalErr  string
}

// RunManager 管理各会话的 agent 运行（同一会话内串行；不同会话可并发）。
type RunManager struct {
	ctx     context.Context
	mu      sync.Mutex
	runs    map[string]*runState // sessionID -> run
	history map[string][]msg.Msg // sessionID -> messages
	changed map[string]bool      // 本会话 AI 成功写入的文件（write_file 成功）
	pending map[string]bool      // 进行中的 write_file 目标（tool_start 记录，结果确认）
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
	Reasoning  int     `json:"reasoning_tokens"`
	Requests   int     `json:"requests"`
	CostUSD    float64 `json:"cost_usd"`
}

// NewRunManager 创建运行器。
func NewRunManager(ctx context.Context) *RunManager {
	return &RunManager{ctx: ctx, runs: map[string]*runState{}, history: map[string][]msg.Msg{},
		changed: map[string]bool{}, pending: map[string]bool{}, approve: map[string]chan bool{}}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Send 启动一次 agent 运行。同一会话内串行（已在跑返回错误）；
// 不同会话可并发（后台任务不阻塞当前会话）。
// attachments 元素为文件路径字符串或 {"kind":"snippet",...} 片段字典。
func (r *RunManager) Send(sessionID string, model config.ModelConfig,
	text string, attachments []any, mode string) error {
	r.mu.Lock()
	if _, ok := r.runs[sessionID]; ok {
		r.mu.Unlock()
		return errBusy
	}
	rctx, cancel := context.WithCancel(r.ctx)
	rs := &runState{sessionID: sessionID, cancel: cancel,
		resume: make(chan struct{}, 1), model: model.DisplayName, startAt: time.Now()}
	r.runs[sessionID] = rs
	r.mu.Unlock()

	hist := r.history[sessionID]
	if config.GetStandalone() {
		hist = nil // 独立提问：不带历史
	}

	runtime.EventsEmit(r.ctx, "run:started", map[string]any{"sessionId": sessionID, "model": model.DisplayName})

	go func() {
		var notes []any
		a := &agent.Agent{
			Mode:  mode,
			Model: &model,
			// 停止：context 驱动（协作）
			OnStop: func() bool { return rctx.Err() != nil },
			OnPause: func() {
				for rs.paused.Load() {
					select {
					case <-rs.resume:
					case <-rctx.Done():
						return
					}
				}
			},
			OnEvent: func(e msg.Event) {
				ev := cloneEvent(e, sessionID)
				switch msg.S(ev, "type") {
				case "model_switch":
					notes = append(notes, map[string]any{
						"kind": "model_switch",
						"from": msg.S(ev, "from"),
						"to":   msg.S(msg.M(ev, "to"), "key"),
					})
				case "tool_start":
					if msg.S(ev, "name") == "write_file" {
						if p := strings.TrimSpace(msg.S(msg.M(ev, "args"), "path")); p != "" {
							r.mu.Lock()
							r.pending[p] = true
							r.mu.Unlock()
						}
					}
				case "tool_result":
					if msg.S(ev, "name") == "write_file" {
						r.mu.Lock()
						if !strings.HasPrefix(msg.S(ev, "result"), "错误：") {
							for p := range r.pending {
								r.changed[p] = true
							}
						}
						r.pending = map[string]bool{}
						r.mu.Unlock()
					}
				}
				runtime.EventsEmit(r.ctx, "agent:event", ev)
			},
			OnApproval: func(name string, args map[string]any, summary string) bool {
				return r.requestApproval(name, summary)
			},
		}
		final, runErr := a.Run(text, hist, attachments)

		r.mu.Lock()
		rs.finished = true
		rs.finalErr = errStr(runErr)
		r.history[sessionID] = a.Messages
		r.mu.Unlock()

		// 技能蒸馏：会话正常收尾且满足条件时，后台产出技能草稿
		if skills.ShouldDistill(final, runErr, skills.ToolCallCount(a.Messages), a.Messages) {
			go skills.Distill(sessionID, tools.GetWorkspace(), a.Messages, final)
		}

		r.mu.Lock()
		r.last = UsageSnapshot{
			Prompt: a.UsageTotal.PromptTokens, Completion: a.UsageTotal.CompletionTokens,
			Total: a.UsageTotal.TotalTokens, Cached: a.UsageTotal.CachedTokens,
			Reasoning: a.UsageTotal.ReasoningTokens,
			Requests:  a.UsageTotal.Requests, CostUSD: a.UsageTotal.CostUSDFloat,
		}
		r.mu.Unlock()

		title := sessions.MakeTitle(text)
		if s := sessions.Load(sessionID); s != nil && s.Title != "" && s.Title != "新会话" {
			title = s.Title
		}
		if title == "新会话" && len(a.Messages) > 0 {
			title = firstUserTitle(a.Messages)
		}
		_ = sessions.Save(sessionID, a.Messages, title, tools.GetWorkspace(), notes)
		r.mu.Lock()
		delete(r.runs, sessionID) // 任务结束：从运行表移除
		r.mu.Unlock()
		runtime.EventsEmit(r.ctx, "run:finished", map[string]any{
			"sessionId": sessionID, "error": errStr(runErr),
		})
	}()
	return nil
}

// Stop 请求某会话协作停止（流中/工具间生效；context 驱动）。
func (r *RunManager) Stop(sessionID string) {
	r.mu.Lock()
	rs := r.runs[sessionID]
	r.mu.Unlock()
	if rs != nil {
		rs.cancel()
	}
}

// StopAll 停止所有运行中的应用关闭。
func (r *RunManager) StopAll() {
	r.mu.Lock()
	all := make([]*runState, 0, len(r.runs))
	for _, rs := range r.runs {
		all = append(all, rs)
	}
	r.mu.Unlock()
	for _, rs := range all {
		rs.cancel()
	}
}

// Pause 暂停某会话运行（阻塞在下一个事件/工具间隙）。
func (r *RunManager) Pause(sessionID string) bool {
	r.mu.Lock()
	rs := r.runs[sessionID]
	r.mu.Unlock()
	if rs == nil || rs.paused.Load() {
		return false
	}
	rs.paused.Store(true)
	runtime.EventsEmit(r.ctx, "run:paused", map[string]any{"sessionId": sessionID})
	return true
}

// Resume 恢复某会话运行。
func (r *RunManager) Resume(sessionID string) bool {
	r.mu.Lock()
	rs := r.runs[sessionID]
	r.mu.Unlock()
	if rs == nil || !rs.paused.Load() {
		return false
	}
	rs.paused.Store(false)
	select {
	case rs.resume <- struct{}{}:
	default:
	}
	runtime.EventsEmit(r.ctx, "run:resumed", map[string]any{"sessionId": sessionID})
	return true
}

// ListRuns 当前所有运行中/暂停的任务（侧栏后台任务标记用）。
func (r *RunManager) ListRuns() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, 0, len(r.runs))
	for id, rs := range r.runs {
		out = append(out, map[string]any{
			"sessionId": id, "model": rs.model,
			"paused": rs.paused.Load(), "start": rs.startAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return numI(out[i]["start"]) < numI(out[j]["start"]) })
	return out
}

func numI(v any) int64 {
	if n, ok := v.(int64); ok {
		return n
	}
	return 0
}

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
		return false
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

// ChangedFiles 本会话 AI 成功写入的文件（相对/绝对路径去重排序）。
func (r *RunManager) ChangedFiles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.changed))
	for p := range r.changed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ResetHistory 清空某会话内存历史（新建会话时）。
func (r *RunManager) ResetHistory(sessionID string) {
	r.mu.Lock()
	delete(r.history, sessionID)
	r.changed = map[string]bool{}
	r.pending = map[string]bool{}
	r.mu.Unlock()
}

// History 当前会话内存历史（/compact 读取）。
func (r *RunManager) History(sessionID string) []msg.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.history[sessionID]
}

// SetHistory 写回压缩后的历史（/compact）。
func (r *RunManager) SetHistory(sessionID string, msgs []msg.Msg) {
	r.mu.Lock()
	r.history[sessionID] = msgs
	r.mu.Unlock()
}

// cloneEvent 浅拷贝事件并注入 sessionId（避免污染原 map，前端按会话分流）。
func cloneEvent(e msg.Event, sessionID string) msg.Event {
	n := make(msg.Event, len(e)+1)
	for k, v := range e {
		n[k] = v
	}
	n["sessionId"] = sessionID
	return n
}

type busyErr struct{}

func (busyErr) Error() string { return "该会话已有任务在运行" }

var errBusy error = busyErr{}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// firstUserTitle 从消息列表取首条用户文本生成标题；全空（纯附件）时按时间命名。
func firstUserTitle(msgs []msg.Msg) string {
	for _, m := range msgs {
		if msg.S(m, "role") != "user" {
			continue
		}
		text := msg.S(m, "content")
		if text == "" {
			for _, pv := range msg.L(m, "content") {
				pm, ok := pv.(map[string]any)
				if !ok || msg.S(pm, "type") != "text" {
					continue
				}
				if t := msg.S(pm, "text"); t != "" {
					text = t
					break
				}
			}
		}
		if t := sessions.MakeTitle(text); t != "新会话" {
			return t
		}
	}
	return "会话 " + time.Now().Format("01-02 15:04")
}
