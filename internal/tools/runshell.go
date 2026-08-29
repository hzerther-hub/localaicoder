package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"localai/internal/config"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "run_shell",
    "description": "执行 shell 命令并返回 stdout/stderr。（命令需与当前操作系统兼容：Linux/macOS 用 POSIX shell，Windows 用 cmd；格式化磁盘、rm -rf /、关机等高危命令会被沙箱拦截）",
    "parameters": {
      "type": "object",
      "properties": {
        "command": {"type": "string", "description": "要执行的 shell 命令"}
      },
      "required": ["command"]
    }
  }
}`,
		// ReadOnly=false：可写工具（readonly 模式移除、ask 模式审批）
		Exec: execRunShell,
		Describe: func(args map[string]any) string {
			return strOf(args["command"])
		},
	})
}

func execRunShell(args map[string]any) string {
	cmd := args["command"]
	if cmd == nil || strings.TrimSpace(fmt.Sprintf("%v", cmd)) == "" {
		return "错误：command 参数为空，无法执行。请修正参数后重试，或改用其他工具/方法继续当前任务。"
	}
	cmdStr := fmt.Sprintf("%v", cmd)
	if config.Sandbox() {
		if hit := ShellCommandBlocked(cmdStr); hit != "" {
			return "错误：沙箱拦截了高危命令（规则 " + hit + "）。" +
				"确需执行请由用户手动运行，或设 LAS_SANDBOX=off 关闭沙箱。"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(config.ToolExecTimeout)*time.Second)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "cmd", "/c", cmdStr)
	} else {
		c = exec.CommandContext(ctx, "sh", "-c", cmdStr)
	}
	c.Dir = GetWorkspace()
	var outBuf, errBuf strings.Builder
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	runErr := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("错误：命令超时（>%d秒）", config.ToolExecTimeout)
	}
	out := strings.TrimSpace(outBuf.String())
	errS := strings.TrimSpace(errBuf.String())
	var parts []string
	if out != "" {
		parts = append(parts, out)
	}
	if errS != "" {
		parts = append(parts, "[stderr]\n"+errS)
	}
	if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() != 0 {
		parts = append(parts, fmt.Sprintf("[退出码 %d]", ee.ExitCode()))
	}
	if len(parts) == 0 {
		return "(无输出)"
	}
	return strings.Join(parts, "\n")
}
