//go:build linux

// Linux 截图：对齐 Tk screenshot.py 的外部工具链
// （import / scrot / maim / gnome-screenshot / grim），Wayland 走 grim。
package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// captureLinux 依次尝试各截图工具，成功返回 true（path 已写出 PNG）。
func captureLinux(path string) bool {
	dir := filepath.Dir(path)
	tools := [][]string{
		{"gnome-screenshot", "-f", path},
		{"scrot", "-o", path},
		{"maim", path},
		{"import", "-window", "root", path},
		{"grim", path}, // Wayland（sway/hyprland 等）
	}
	for _, argv := range tools {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue // 未安装该工具
		}
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir
		if err := c.Run(); err == nil {
			if st, err := os.Stat(path); err == nil && st.Size() > 0 {
				return true
			}
		}
	}
	return false
}
