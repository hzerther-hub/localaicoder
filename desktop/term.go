// desktop 内置终端：多标签 PTY 管理。
// Windows 走 ConPTY（真伪终端，ANSI/交互完整）；其它平台退回管道 shell。
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type termInstance struct {
	id     string
	mu     sync.Mutex
	write  func([]byte) (int, error)
	resize func(int, int) error
	close  func()
	proc   *os.Process
}

// TerminalManager 全部终端实例。
type TerminalManager struct {
	mu   sync.Mutex
	term map[string]*termInstance
	ctx  context.Context
}

// NewTerminalManager 创建管理器（startup 后 SetContext）。
func NewTerminalManager() *TerminalManager {
	return &TerminalManager{term: map[string]*termInstance{}}
}

// SetCtx 注入 Wails context（startup 里调用）。
func (m *TerminalManager) SetCtx(ctx context.Context) { m.ctx = ctx }

func (m *TerminalManager) emit(id string, data []byte) {
	if m.ctx == nil {
		return
	}
	// 截断超大块，避免事件风暴（正常输出远小于此）
	if len(data) > 64<<10 {
		data = data[len(data)-64<<10:]
	}
	wruntime.EventsEmit(m.ctx, "term:data", map[string]any{"id": id, "data": string(data)})
}

// TermStart 启动一个终端（cwd 为工作目录）；返回终端 id。
func (m *TerminalManager) TermStart(cwd string) (string, error) {
	id := fmt.Sprintf("t%d", time.Now().UnixNano()%1e9)
	shell := "powershell.exe -NoLogo"
	if runtime.GOOS != "windows" {
		shell = "$SHELL"
		if sh := os.Getenv("SHELL"); sh != "" {
			shell = sh
		}
	}
	var inst *termInstance
	var err error
	if runtime.GOOS == "windows" {
		inst, err = startPTY(id, shell, cwd, m.emit)
	} else {
		inst, err = startPTY(id, shell, cwd, m.emit)
	}
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.term[id] = inst
	m.mu.Unlock()
	return id, nil
}

// TermWrite 向终端写入按键/输入。
func (m *TerminalManager) TermWrite(id string, data string) {
	m.mu.Lock()
	inst := m.term[id]
	m.mu.Unlock()
	if inst == nil {
		return
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	_, _ = inst.write([]byte(data))
}

// TermResize 调整终端尺寸（前端 xterm 回报 cols/rows）。
func (m *TerminalManager) TermResize(id string, cols, rows int) {
	m.mu.Lock()
	inst := m.term[id]
	m.mu.Unlock()
	if inst == nil || inst.resize == nil {
		return
	}
	_ = inst.resize(cols, rows)
}

// TermStop 关闭终端。
func (m *TerminalManager) TermStop(id string) {
	m.mu.Lock()
	inst := m.term[id]
	delete(m.term, id)
	m.mu.Unlock()
	if inst == nil {
		return
	}
	if inst.proc != nil {
		_ = inst.proc.Kill()
	}
	inst.close()
}

// TermList 当前终端 id 列表。
func (m *TerminalManager) TermList() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.term))
	for k := range m.term {
		out = append(out, k)
	}
	return out
}
