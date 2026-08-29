//go:build linux

// Linux 截图平台分流：
//   - Wayland（GNOME 等）：X11 抓屏被合成器限制为黑屏，优先走
//     XDG Desktop Portal（真实画面）；外部工具（grim 等）作后备。
//   - X11：gnome-screenshot / scrot / maim / import 外部工具链，
//     全部不可用再退回库内 X11 捕获（由调用方决定）。
//
// 返回 (是否成功, 是否 Wayland 会话)——Wayland 下失败时调用方不应
// 退回库内捕获（必然黑屏，宁可不返回假图）。
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// captureLinux 依次尝试 portal 与各截图工具，成功返回 true（path 已写出 PNG）。
// 外部工具一律 10s 超时；Wayland 会话跳过 gnome-screenshot（部分会话挂起 30s+）。
func captureLinux(path string) (bool, bool) {
	wayland := onWayland()
	dir := filepath.Dir(path)
	// Wayland 会话：portal 优先（GNOME 无需额外工具即有真实画面）
	if wayland && screenshotViaPortal(path) {
		return true, true
	}
	var tools [][]string
	if wayland {
		tools = [][]string{
			{"grim", path}, // wlroots 系 Wayland（sway/hyprland 等）
		}
	} else {
		tools = [][]string{
			{"gnome-screenshot", "-f", path},
			{"scrot", "-o", path},
			{"maim", path},
			{"import", "-window", "root", path},
		}
	}
	for _, argv := range tools {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue // 未安装该工具
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Dir = dir
		err := c.Run()
		cancel()
		if err == nil {
			if st, err := os.Stat(path); err == nil && st.Size() > 0 {
				return true, wayland
			}
		}
	}
	return false, wayland
}
