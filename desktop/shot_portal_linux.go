//go:build linux

// XDG Desktop Portal 截屏（org.freedesktop.portal.Screenshot）：
// GNOME Wayland 下合成器把 X11 抓屏限制为黑屏/空图，portal 是拿到真实画面的
// 唯一可靠路径（对齐 Tk screenshot.py 的推荐方案）。依赖 godbus（wails 已带）。
//
// 实现要点：调用前先按【接口】订阅全部 Request 信号，调用时携带 handle_token，
// 之后在信号流里按「路径包含 token」匹配结果——不预测句柄路径，规避
// sender 名转换规则差异与信号竞态。
package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// portalDebugLog 调试输出开关（-ldflags 或测试内置 true 可观察 portal 交互）。
var portalDebugLog = os.Getenv("LAS_PORTAL_DEBUG") != ""

func plog(format string, args ...any) {
	if portalDebugLog {
		fmt.Printf("[portal] "+format+"\n", args...)
	}
}

// portalScreenshot 调 portal 抓整屏并把产物写到 path；成功返回 true。
func screenshotViaPortal(path string) bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		plog("SessionBus 失败: %v", err)
		return false
	}
	defer func() { _ = conn.Close() }()

	token := fmt.Sprintf("las_%d", time.Now().UnixNano())

	// 先订阅（接口级匹配，调用后按 token 过滤），防 Response 早于订阅到达
	sigCh := make(chan *dbus.Signal, 16)
	conn.Signal(sigCh)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
	); err != nil {
		plog("AddMatchSignal 失败: %v", err)
		return false
	}
	defer func() {
		_ = conn.RemoveMatchSignal(
			dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		)
	}()

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	plog("调用 Screenshot token=%s", token)
	call := obj.Call("org.freedesktop.portal.Screenshot.Screenshot", 0, "",
		map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(token),
			"interactive":  dbus.MakeVariant(false),
		})
	if call.Err != nil {
		plog("Screenshot 调用失败: %v", call.Err)
		return false
	}
	if len(call.Body) > 0 {
		if hp, ok := call.Body[0].(dbus.ObjectPath); ok {
			plog("请求句柄: %s", hp)
		}
	}

	// 等待带 token 的 Response 信号
	var uri string
	deadline := time.After(12 * time.Second)
wait:
	for {
		select {
		case s := <-sigCh:
			plog("信号: path=%s iface=%s body=%v", s.Path, s.Name, s.Body)
			if !strings.Contains(string(s.Path), token) {
				continue // 别的请求的响应
			}
			if len(s.Body) >= 1 {
				if code, ok := s.Body[0].(uint32); ok && code != 0 {
					plog("Response code=%d（用户取消或失败）", code)
					return false
				}
			}
			if len(s.Body) >= 2 {
				results, _ := s.Body[1].(map[string]dbus.Variant)
				if v, ok := results["uri"]; ok {
					uri, _ = v.Value().(string)
				}
			}
			break wait
		case <-deadline:
			plog("等待 Response 超时（12s）")
			return false
		}
	}
	plog("结果 URI: %s", uri)
	if !strings.HasPrefix(uri, "file://") {
		return false
	}
	src := strings.TrimPrefix(uri, "file://")
	// URI 百分号解码（路径含中文/空格时必现，如 /home/u/图片/Screenshot-4.png）
	if dec, err := url.PathUnescape(src); err == nil {
		src = dec
	}
	if src == "" {
		return false
	}
	data, err := os.ReadFile(src)
	if err != nil || len(data) == 0 {
		plog("读取 portal 产物失败: %v", err)
		return false
	}
	return os.WriteFile(path, data, 0o644) == nil
}

// onWayland 当前会话是否运行在 Wayland。
func onWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland"
}
