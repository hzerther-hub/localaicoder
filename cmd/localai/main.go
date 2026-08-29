// localai CLI：与桌面版共享同一 Go 内核的命令行入口。
//
//	localai chat                  # 流式 REPL（工具/审批/停止全支持）
//	localai chat --model KEY      # 指定模型
//	localai chat --mode ask       # 权限模式 readonly/ask/always
//	localai models                # 列出已配置模型
//	localai sessions              # 最近会话列表
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"localai/internal/agent"
	"localai/internal/config"
	"localai/internal/mcp"
	"localai/internal/msg"
	"localai/internal/products"
	"localai/internal/sessions"
	"localai/internal/tools"
	"localai/internal/tui"
)

const version = "0.1.0"

func main() {
	args := os.Args[1:]
	cmd := "chat"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "chat":
		chatCmd(args)
	case "tui":
		tui.Run()
	case "models":
		modelsCmd()
	case "sessions":
		sessionsCmd()
	case "version", "-v", "--version":
		fmt.Println("localai", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "未知命令:", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`localai — Local AI Studio (Go 内核 CLI)

用法:
  localai chat [--model provider/model] [--mode readonly|ask|always] [--workspace DIR]
  localai models
  localai sessions
`)
}

func chatCmd(args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	modelKey := fs.String("model", "", "模型 key（provider/model）")
	mode := fs.String("mode", agent.ModeAlways, "权限模式 readonly/ask/always")
	workspace := fs.String("workspace", "", "工作目录（默认当前目录）")
	_ = fs.Parse(args)

	profile := products.Active()
	fmt.Printf("localai %s · %s\n", version, profile.Title)

	models, defKey := config.LoadModels()
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, "没有可用模型：请在", config.ModelsFile(), "里配置")
		os.Exit(1)
	}
	key := *modelKey
	if key == "" {
		key = defKey
	}
	model := config.FindModel(key)
	if model == nil {
		fmt.Fprintln(os.Stderr, "模型不存在:", key)
		for _, m := range models {
			fmt.Fprintln(os.Stderr, "  -", m.Key)
		}
		os.Exit(1)
	}
	fmt.Printf("模型: %s (%s)\n", model.DisplayName, model.Key)

	if *workspace != "" {
		tools.SetWorkspace(*workspace)
		config.SaveLastWorkspace(tools.GetWorkspace())
	} else {
		tools.SetWorkspace(cwd())
	}
	fmt.Println("工作目录:", tools.GetWorkspace())
	if br := tools.GitBranch(""); br != "" {
		fmt.Println("分支:", br)
	}
	fmt.Println()

	// MCP 后台连接（日志打到 stderr）
	go mcp.GetManager().Connect(func(line string) {
		fmt.Fprintln(os.Stderr, "[mcp]", line)
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	a := &agent.Agent{
		Mode:  *mode,
		Model: model,
		OnEvent: func(e msg.Event) {
			switch msg.S(e, "type") {
			case "text":
				fmt.Print(msg.S(e, "delta"))
			case "reasoning":
				fmt.Print("\x1b[2m" + msg.S(e, "delta") + "\x1b[0m")
			case "tool_start":
				fmt.Printf("\n🔧 %s %v\n", msg.S(e, "name"), shortArgs(e))
			case "tool_result":
				fmt.Printf("   ↳ %s\n", firstLine(msg.S(e, "result"), 200))
			case "tool_denied":
				fmt.Printf("\n🚫 已拒绝: %s\n", msg.S(e, "name"))
			case "usage":
				fmt.Fprintf(os.Stderr, "  [tokens] total=%v cached=%v\n",
					shortJSON(e["total"]), shortJSON(msg.M(e, "usage")))
			case "model_switch":
				fmt.Printf("\n⚠️ 模型切换: %s → %s\n", msg.S(e, "from"),
					msg.S(msg.M(e, "to"), "display_name"))
			case "routing":
				label := map[string]string{"simple": "简单轮→轻量模型", "strong": "复杂轮→强力模型",
					"escalate": "轻量失败→升级重试"}[msg.S(e, "decision")]
				if label == "" {
					label = msg.S(e, "decision")
				}
				fmt.Printf("\n🧭 %s: %s\n", label,
					msg.S(msg.M(e, "model"), "display_name"))
			}
			out.Flush()
		},
		OnApproval: func(name string, args map[string]any, summary string) bool {
			fmt.Printf("\n❓ 请求执行可写工具 %s:\n%s\n允许? [y/N] ", name, summary)
			out.Flush()
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			return line == "y" || line == "yes"
		},
		OnStop: func() bool { return ctx.Err() != nil },
	}

	var history []msg.Msg
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("\n你> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println()
			return
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		switch text {
		case "/exit", "/quit", "exit", "quit":
			return
		case "/new":
			history = nil
			fmt.Println("（已开新会话）")
			continue
		case "/branch":
			fmt.Println(tools.GitBranch(""))
			continue
		}
		if ctx.Err() != nil {
			ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
		}
		fmt.Print("助手> ")
		out.Flush()
		var hist []msg.Msg
		if !config.GetStandalone() && history != nil {
			hist = history
		}
		if _, err := a.Run(text, hist, nil); err != nil {
			fmt.Printf("\n❌ %v\n", err)
		} else {
			history = a.Messages
		}
		fmt.Println()
	}
}

func modelsCmd() {
	models, def := config.LoadModels()
	for _, m := range models {
		mark := " "
		if m.Key == def {
			mark = "✓"
		}
		vision, reason := "", ""
		if m.Vision {
			vision = " 👁"
		}
		if m.Reasoning {
			reason = " 🧠"
		}
		fmt.Printf("%s %-40s %s%s  %s\n", mark, m.Key, vision, reason, m.BaseURL)
	}
	fmt.Println("\n默认:", def, "· 配置文件:", config.ModelsFile())
}

func sessionsCmd() {
	list := sessions.ListSessions(20, "", "")
	for _, s := range list {
		fmt.Printf("%s  %s  %s\n", s.ID, s.Title, s.Workspace)
	}
	if len(list) == 0 {
		fmt.Println("（暂无会话）")
	}
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func shortArgs(e msg.Event) string {
	args := msg.M(e, "args")
	b := strings.Builder{}
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		fmt.Fprintf(&b, "%s=%s ", k, s)
	}
	return strings.TrimSpace(b.String())
}

func shortJSON(v any) string {
	if v == nil {
		return "?"
	}
	switch t := v.(type) {
	case map[string]any:
		return fmt.Sprintf("{total:%v prompt:%v}", t["total_tokens"], t["prompt_tokens"])
	default:
		return fmt.Sprintf("%v", v)
	}
}
