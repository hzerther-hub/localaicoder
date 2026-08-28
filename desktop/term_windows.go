//go:build windows

// Windows：ConPTY 实现真伪终端。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/UserExistsError/conpty"
)

func startPTY(id, shell, cwd string, emit func(string, []byte)) (*termInstance, error) {
	cwd2 := cwd
	if cwd2 == "" {
		cwd2, _ = os.Getwd()
	}
	pty, err := conpty.Start(shell,
		conpty.ConPtyDimensions(120, 30),
		conpty.ConPtyWorkDir(cwd2),
	)
	if err != nil {
		return nil, fmt.Errorf("ConPTY 启动失败: %w", err)
	}
	inst := &termInstance{
		id: id,
		write: func(b []byte) (int, error) { return pty.Write(b) },
		resize: func(c, r int) error { return pty.Resize(c, r) },
		close:  func() { _ = pty.Close() },
	}
	// 读循环：PTY 输出 → term:data 事件
	go func() {
		buf := make([]byte, 16<<10)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				emit(id, buf[:n])
			}
			if err != nil {
				emit(id, []byte("\r\n\x1b[2m[进程已退出]\x1b[0m\r\n"))
				return
			}
		}
	}()
	// 进程句柄：conpty 不直接暴露 proc；Close 会随 Wait 释放。
	// 用 Wait 监控退出，顺带把进程号暴露给 Kill（PowerShell 退出后终端自然结束）。
	go func() {
		_, _ = pty.Wait(context.Background())
	}()
	return inst, nil
}
