//go:build !windows

// 非 Windows：管道 shell 退回实现（行式交互，无 ANSI 伪终端）。
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func startPTY(id, shell, cwd string, emit func(string, []byte)) (*termInstance, error) {
	c := exec.Command(shell, "-i")
	if shell == "$SHELL" || strings.HasSuffix(shell, "/bash") || strings.HasSuffix(shell, "/zsh") {
		c = exec.Command(shell, "-i")
	}
	c.Dir = cwd
	stdin, _ := c.StdinPipe()
	stdout, _ := c.StdoutPipe()
	c.Stderr = c.Stdout
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("shell 启动失败: %w", err)
	}
	inst := &termInstance{
		id:    id,
		proc:  c.Process,
		write: func(b []byte) (int, error) { return stdin.Write(b) },
		resize: func(int, int) error { return nil },
		close: func() {
			_ = stdin.Close()
			_ = c.Process.Kill()
		},
	}
	go func() {
		r := bufio.NewReaderSize(stdout, 32<<10)
		buf := make([]byte, 16<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				emit(id, buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					_ = err
				}
				emit(id, []byte("\n[进程已退出]\n"))
				return
			}
		}
	}()
	return inst, nil
}

var _ = os.Getpid
