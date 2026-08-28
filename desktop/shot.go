// desktop 截屏：
//   - Windows：微信式 —— 捕获全虚拟屏幕 → 前端全屏遮罩拖拽框选 → CropImage 裁剪落盘；
//   - Linux/macOS：沿用原 Tk 版方式 —— 直接整屏抓取作为附件（portal/标注后续接入）。
//
// 纯 Go（kbinani/screenshot 走 Win32/GDI syscall，无 CGO）。
package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kbinani/screenshot"

	"localai/internal/config"
)

// CaptureScreen 捕获全部显示器（虚拟屏幕联合区域），保存 PNG 返回路径。
// 平台分流（对齐 Tk screenshot.py）：
//   - Linux：外部工具链优先（gnome-screenshot/scrot/maim/import/grim，Wayland 走 grim）；
//   - Windows/macOS：kbinani/screenshot 库内捕获（Win32 GDI / macOS 抓屏）。
func (a *App) CaptureScreen() string {
	p := shotPath()
	if runtime.GOOS == "linux" {
		if captureLinux(p) {
			return p
		}
		// 外部工具都不可用 → 退回库内 X11 捕获
	}
	n := screenshot.NumActiveDisplays()
	if n <= 0 {
		return ""
	}
	// 多显示器：取联合包围盒（微信式整屏底图，供前端框选裁剪）
	union := screenshot.GetDisplayBounds(0)
	for i := 1; i < n; i++ {
		union = union.Union(screenshot.GetDisplayBounds(i))
	}
	if union.Dx() == 0 || union.Dy() == 0 {
		return ""
	}
	img, err := screenshot.CaptureRect(union)
	if err != nil {
		return ""
	}
	f, err := os.Create(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return ""
	}
	return p
}

// CropImage 按像素矩形裁剪已保存的截图（前端框选结果），返回新文件路径。
func (a *App) CropImage(path string, x, y, w, h int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return ""
	}
	b := img.Bounds()
	// 钳制到图像范围内
	x = clampInt(x, 0, b.Dx()-1)
	y = clampInt(y, 0, b.Dy()-1)
	w = clampInt(w, 1, b.Dx()-x)
	h = clampInt(h, 1, b.Dy()-y)

	sub := img.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(image.Rect(x, y, x+w, y+h))

	p := shotPath()
	out, err := os.Create(p)
	if err != nil {
		return ""
	}
	defer out.Close()
	if err := png.Encode(out, sub); err != nil {
		return ""
	}
	return p
}

func shotPath() string {
	dir := filepath.Join(config.MediaDir(), "shots")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, fmt.Sprintf("shot_%s.png", time.Now().Format("20060102_150405.000")))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
