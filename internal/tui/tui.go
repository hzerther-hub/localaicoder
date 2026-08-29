// Package tui 终端交互界面（bubbletea）：与桌面/CLI 共享同一 Go 内核。
// `localai tui` 启动。功能：流式聊天、工具事件、审批 y/n、斜杠命令、用量条。
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/msg"
	"localai/internal/products"
	msgpkg "localai/internal/msg"
	"localai/internal/tools"
)

var (
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("135"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	amberStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
)

// 事件通道消息
type deltaMsg string
type eventMsg = msg.Event // 别名：保持 msg.S/M 直接可用
type doneMsg struct{ err error }
type askMsg struct{ id int; name, summary string; reply chan bool }

type model struct {
	vp      viewport.Model
	ta      textarea.Model
	spinner spinner.Model
	lines   []string
	buf     string   // 当前流式回复缓冲
	a       *agent.Agent
	ready   bool
	running bool
	usage   string
	pendAsk *askMsg
	err     error
	events  chan msg.Event
	done    chan doneMsg
}

// Run 启动 TUI。
func Run() {
	profile := products.Active()
	models, def := config.LoadModels()
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, "没有可用模型，请先配置", config.ModelsFile())
		os.Exit(1)
	}
	mc := config.FindModel(def)
	if mc == nil {
		mc = &models[0]
	}
	tools.SetWorkspace(config.LoadLastWorkspace())

	m := &model{}
	m.ta = textarea.New()
	m.ta.Placeholder = "输入问题，Enter 发送（/help 查看命令）…"
	m.ta.Prompt = ""
	m.ta.CharLimit = 0
	m.ta.Focus()
	m.spinner = spinner.New(spinner.WithSpinner(spinner.Meter))

	p := tea.NewProgram(m, tea.WithAltScreen())
	// agent 在 TUI 模型内部按需启动；这里预创建回调通道由 model.run() 建立
	_ = p
	fmt.Printf("%s %s · %s (%s)\n", titleStyle.Render("localai tui"),
		dimStyle.Render(profile.Title), mc.DisplayName, dimStyle.Render(mc.Key))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI 退出:", err)
		os.Exit(1)
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m *model) run(text string) tea.Cmd {
	if m.a == nil || m.a.Model == nil {
		// 首次运行前无 agent：创建一个占位（真模型在下方 agent.New 替换）
		mc := config.FindModel(config.LoadModelsData()["default"].(string))
		if mc == nil {
			models, _ := config.LoadModels()
			mc = &models[0]
		}
		m.a = agent.New(nil, nil, nil, agent.ModeAlways, mc)
	}
	curModel := *m.a.Model
	m.running = true
	events := make(chan msg.Event, 128)
	m.events = events
	done := make(chan doneMsg, 1)

	m.a = agent.New(
		func(e msg.Event) { events <- e },
		func(name string, args map[string]any, summary string) bool {
			reply := make(chan bool, 1)
			// 审批请求也走事件通道（带 reply channel）
			events <- msg.Event{"type": "_approval", "name": name, "summary": summary, "reply": reply}
			return <-reply
		},
		nil, m.mode(), &curModel,
	)
	go func() {
		hist := m.a.Messages
		if config.GetStandalone() {
			hist = nil
		}
		_, err := m.a.Run(text, hist, nil)
		done <- doneMsg{err}
	}()

	m.done = done
	return waitEvent(events)
}

func (m *model) mode() string { return agent.ModeAlways }

func waitEvent(events chan msg.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-events
		if !ok {
			return nil
		}
		return eventMsg(e)
	}
}

func waitDone(done chan doneMsg, events chan msg.Event) tea.Cmd {
	return func() tea.Msg {
		d := <-done
		close(events)
		return d
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.vp.Width, m.vp.Height = msg.Width, msg.Height-8
		m.ta.SetWidth(msg.Width - 4)
		m.ready = true
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.running {
				// 二次 Ctrl+C 退出；一次视为停止
			}
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.ta.Value())
			if m.pendAsk != nil {
				allow := strings.EqualFold(text, "y") || strings.EqualFold(text, "yes")
				m.pendAsk.reply <- allow
				m.lines = append(m.lines, dimStyle.Render(
					map[bool]string{true: "  → 已允许", false: "  → 已拒绝"}[allow]))
				m.pendAsk = nil
				m.ta.Reset()
				return m, m.flush()
			}
			if text == "" {
				return m, nil
			}
			m.ta.Reset()
			switch text {
			case "/exit", "/quit":
				return m, tea.Quit
			case "/new":
				m.a.Messages = nil
				m.lines = append(m.lines, dimStyle.Render("（已开新会话）"))
				return m, m.flush()
			case "/help":
				m.lines = append(m.lines, dimStyle.Render(
					"命令：/new 新会话 · /exit 退出 · /branch 分支 · /model 切换见命令行参数"))
				return m, m.flush()
			case "/branch":
				m.lines = append(m.lines, toolStyle.Render("分支: "+tools.GitBranch("")))
				return m, m.flush()
			}
			m.lines = append(m.lines, accentStyle.Render("你> ")+text)
			return m, tea.Batch(m.flush(), m.run(text))
		}
	case spinner.TickMsg:
		s, cmd := m.spinner.Update(msg)
		m.spinner = s
		return m, cmd
	case eventMsg:
		e := msg
		switch msgpkg.S(e, "type") {
		case "text":
			m.buf += msgpkg.S(e, "delta")
		case "reasoning":
			// 折叠显示省略号，避免刷屏
		case "tool_start":
			m.flushBuf()
			m.lines = append(m.lines, toolStyle.Render("🔧 "+msgpkg.S(e, "name")+" …"))
		case "tool_result":
			r := msgpkg.S(e, "result")
			if len(r) > 120 {
				r = r[:120] + "…"
			}
			m.lines = append(m.lines, dimStyle.Render("   ↳ "+strings.ReplaceAll(r, "\n", " ")))
		case "tool_denied":
			m.flushBuf()
			m.lines = append(m.lines, amberStyle.Render("🚫 已拒绝 "+msgpkg.S(e, "name")))
		case "usage":
			t2 := msgpkg.M(e, "total")
			m.usage = fmt.Sprintf("tokens=%v in=%v out=%v cached=%v",
				t2["total_tokens"], t2["prompt_tokens"], t2["completion_tokens"], t2["cached_tokens"])
		case "routing":
			label := map[string]string{"simple": "简单轮", "strong": "复杂轮", "escalate": "升级重试"}[msgpkg.S(e, "decision")]
			if label == "" {
				label = msgpkg.S(e, "decision")
			}
			m.flushBuf()
			m.lines = append(m.lines, dimStyle.Render("🧭 "+label+" → "+msgpkg.S(msgpkg.M(e, "model"), "display_name")))
		case "model_switch":
			m.flushBuf()
			m.lines = append(m.lines, amberStyle.Render("⚠ 模型切换 → "+msgpkg.S(msgpkg.M(e, "to"), "display_name")))
		case "_approval":
			m.flushBuf()
			m.pendAsk = &askMsg{
				name: msgpkg.S(e, "name"), summary: msgpkg.S(e, "summary"),
				reply: e["reply"].(chan bool),
			}
			m.lines = append(m.lines, amberStyle.Render("❓ 审批 "+m.pendAsk.name+
				"（y 允许 / n 拒绝，回车提交）\n"+m.pendAsk.summary))
		}
		cmds = append(cmds, m.flush(), waitEvent(m.events))
		return m, tea.Batch(cmds...)
	case doneMsg:
		m.flushBuf()
		m.running = false
		if msg.err != nil {
			m.lines = append(m.lines, amberStyle.Render("❌ "+msg.err.Error()))
		}
		m.events = nil
		return m, m.flush()
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *model) flushBuf() {
	if m.buf != "" {
		m.lines = append(m.lines, m.buf)
		m.buf = ""
	}
}

func (m *model) flush() tea.Cmd {
	return func() tea.Msg { return nil }
}

func (m *model) View() string {
	if !m.ready {
		return "加载中…"
	}
	m.flushBuf()
	body := strings.Join(m.lines, "\n")
	if m.running {
		body += "\n" + m.spinner.View() + dimStyle.Render(" 思考中…")
	}
	if m.pendAsk != nil {
		body += "\n" + amberStyle.Render("等待审批输入 (y/n)：")
	}
	m.vp.SetContent(body)
	m.vp.GotoBottom()

	status := dimStyle.Render(" /exit 退出 · Enter 发送")
	if m.usage != "" {
		status += dimStyle.Render(" · "+m.usage)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.vp.View(),
		strings.Repeat("─", m.vp.Width),
		m.ta.View(),
		status,
	)
}
