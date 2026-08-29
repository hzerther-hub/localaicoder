package main

// desktop/schedule.go 定时任务：分钟级调度器，到期后在任务专属会话中自动发送
// prompt（复用 RunManager——运行出现在侧栏运行列表，可停止/暂停）。
// 任务持久化在 models.json 顶层 "scheduled_tasks"（config.Get/SetScheduledTasks），
// 字段：id / name / workspace / prompt / interval_min / enabled / session_id /
// last_run / next_run（Unix 秒）。
// 执行语义：串行（工作目录是全局状态，同一时刻至多一个定时任务在跑）；
// 执行前切换到任务目录、结束后恢复，不发 workspace:changed（不打扰前台 UI）。

import (
	"sort"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/sessions"
	"localai/internal/tools"
)

// schedHold 任务执行互斥：工作目录为全局状态，定时任务串行执行。
var schedMu sync.Mutex

// WaitIdle 等待某会话的运行结束（调度器串行化用；超时到期即返回）。
func (r *RunManager) WaitIdle(sessionID string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		_, busy := r.runs[sessionID]
		r.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// scheduleLoop 每 30s 轮询一次到期任务（startup 起的常驻 goroutine）。
func (a *App) scheduleLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.runDueTasks()
		}
	}
}

// runDueTasks 执行全部到期任务（execTask 内部串行）。
func (a *App) runDueTasks() {
	now := time.Now().Unix()
	for _, t := range config.GetScheduledTasks() {
		if !msg.B(t, "enabled") {
			continue
		}
		if nr := int64(msg.F(t, "next_run")); nr > 0 && nr > now {
			continue
		}
		a.execTask(msg.S(t, "id"))
	}
}

// execTask 执行单个任务：切工作目录 → 任务会话发送 prompt → 等待结束 → 恢复目录。
func (a *App) execTask(id string) {
	schedMu.Lock()
	defer schedMu.Unlock()

	var task map[string]any
	for _, t := range config.GetScheduledTasks() {
		if msg.S(t, "id") == id {
			task = t
			break
		}
	}
	if task == nil || !msg.B(task, "enabled") {
		return
	}
	ws := msg.S(task, "workspace")
	prompt := msg.S(task, "prompt")
	interval := msg.I(task, "interval_min")
	if interval < 1 {
		interval = 60
	}
	model := config.FindModel(a.modelKey)
	if model == nil || prompt == "" || ws == "" {
		return
	}
	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()

	prev := tools.GetWorkspace()
	tools.SetWorkspace(ws) // 结束即恢复；不发 workspace:changed，不打扰前台
	sid := msg.S(task, "session_id")
	if sid == "" {
		sid = sessions.NewID() // 任务专属会话：反复执行可积累上下文
	}
	if err := a.runner.Send(sid, *model, prompt, nil, mode); err == nil {
		a.runner.WaitIdle(sid, 30*time.Minute)
	}
	tools.SetWorkspace(prev)

	// 结算：last_run / next_run 落盘并通知前端刷新
	now := time.Now().Unix()
	tasks := config.GetScheduledTasks()
	for _, t := range tasks {
		if msg.S(t, "id") == id {
			t["session_id"] = sid
			t["last_run"] = float64(now)
			t["next_run"] = float64(now + int64(interval)*60)
		}
	}
	config.SetScheduledTasks(tasks)
	a.emitScheduleChanged()
}

// emitScheduleChanged 通知前端任务表已变化（下次运行时间等）。
func (a *App) emitScheduleChanged() {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "schedule:changed", nil)
	}
}

// ---------------- 绑定（前端 bridge.ts 对接） ----------------

// ListScheduledTasks 定时任务列表（按下次运行时间升序）。
func (a *App) ListScheduledTasks() []map[string]any {
	tasks := config.GetScheduledTasks()
	sort.Slice(tasks, func(i, j int) bool {
		return msg.F(tasks[i], "next_run") < msg.F(tasks[j], "next_run")
	})
	return tasks
}

// SaveScheduledTask 新建/更新任务（前端整对象提交；id 为空自动生成）。
// 返回保存后的完整列表。
func (a *App) SaveScheduledTask(in map[string]any) []map[string]any {
	name := strings.TrimSpace(msg.S(in, "name"))
	if name == "" {
		name = "定时任务"
	}
	interval := msg.I(in, "interval_min")
	if interval < 1 {
		interval = 60
	}
	enabled := msg.B(in, "enabled")

	tasks := config.GetScheduledTasks()
	now := time.Now().Unix()
	var updated map[string]any
	for _, t := range tasks {
		if msg.S(t, "id") == msg.S(in, "id") {
			updated = t
			break
		}
	}
	if updated == nil {
		updated = map[string]any{
			"id":         randID(),
			"session_id": "",
			"last_run":   float64(0),
			"next_run":   float64(now + int64(interval)*60), // 新任务：一个周期后首跑
		}
		tasks = append(tasks, updated)
	} else if enabled && msg.F(updated, "next_run") < float64(now) {
		updated["next_run"] = float64(now) // 重新启用且已过期：下个 tick 立即执行
	}
	updated["name"] = name
	updated["workspace"] = msg.S(in, "workspace")
	updated["prompt"] = msg.S(in, "prompt")
	updated["interval_min"] = interval
	updated["enabled"] = enabled
	config.SetScheduledTasks(tasks)
	a.emitScheduleChanged()
	return a.ListScheduledTasks()
}

// DeleteScheduledTask 删除任务，返回剩余列表。
func (a *App) DeleteScheduledTask(id string) []map[string]any {
	tasks := config.GetScheduledTasks()
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		if msg.S(t, "id") != id {
			out = append(out, t)
		}
	}
	config.SetScheduledTasks(out)
	a.emitScheduleChanged()
	return out
}

// RunScheduledTaskNow 立即执行一次（异步触发，不阻塞 UI）。
func (a *App) RunScheduledTaskNow(id string) bool {
	go a.execTask(id)
	return true
}
