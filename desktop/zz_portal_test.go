//go:build linux

package main

import (
	"os"
	"testing"
)

func TestZZPortalScreenshot(t *testing.T) {
	if !onWayland() {
		t.Skip("非 Wayland 会话")
	}
	p := "/home/wellfuture/build/localaicoder/.zz-test-shot.png"
	defer os.Remove(p)
	if !screenshotViaPortal(p) {
		t.Fatal("portal 截图失败（详见 [portal] 日志）")
	}
	if st, err := os.Stat(p); err != nil || st.Size() == 0 {
		t.Fatalf("产物异常: %v", err)
	}
	t.Log("portal 截图成功")
}
