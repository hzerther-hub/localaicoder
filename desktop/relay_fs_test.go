package main

// relay_fs_test.go：WEB 端文件浏览/编辑的安全边界测试。
// 覆盖 fsGuard（安全目录模式路径守卫）与 fsRead（截断/二进制拒绝）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localai/internal/tools"
)

func TestFsGuardSafeMode(t *testing.T) {
	ws := t.TempDir()
	restore := tools.PushWorkspace(ws)
	defer restore()

	rc := &relayClient{}
	rc.setFsSafe(true) // 安全目录模式开启

	// 空路径 → 当前项目
	got, err := rc.fsGuard("")
	if err != nil || got != ws {
		t.Fatalf("空路径应落回工作区：got=%q err=%v", got, err)
	}
	// 项目内子路径 → 放行（返回绝对路径）
	sub := filepath.Join(ws, "sub", "a.go")
	if got, err = rc.fsGuard(sub); err != nil || !strings.HasPrefix(strings.ToLower(got), strings.ToLower(ws)) {
		t.Fatalf("项目内路径应放行：got=%q err=%v", got, err)
	}
	// 项目外路径 → 拒绝
	outside := filepath.Join(filepath.Dir(ws), "outside.txt")
	if strings.HasPrefix(strings.ToLower(outside), strings.ToLower(ws)) {
		t.Skip("临时目录前缀重叠，无法构造外部路径")
	}
	if _, err = rc.fsGuard(outside); err == nil {
		t.Fatalf("项目外路径应被拒绝")
	}
	// 盘符穿越（项目内前缀伪造，如 ws 少一段）→ 拒绝
	fake := ws + "x"
	if _, err = rc.fsGuard(fake); err == nil {
		t.Fatalf("前缀伪造路径应被拒绝")
	}

	// 安全目录模式关闭 → 任意路径放行
	rc.setFsSafe(false)
	if got, err = rc.fsGuard(outside); err != nil || got != outside {
		t.Fatalf("关闭安全模式应放行任意路径：got=%q err=%v", got, err)
	}
}

func TestFsReadTruncateAndBinary(t *testing.T) {
	dir := t.TempDir()

	// 大文件 → 截断标记
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("a", fsReadMax+1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	m := fsRead(big, 1)
	if m["error"] != nil {
		t.Fatalf("大文本读取失败：%v", m["error"])
	}
	if m["truncated"] != true {
		t.Fatalf("应标记截断")
	}
	if len(m["content"].(string)) != fsReadMax {
		t.Fatalf("截断后长度应为 %d，得到 %d", fsReadMax, len(m["content"].(string)))
	}

	// 二进制（含 NUL）→ 拒绝
	bin := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(bin, []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if m = fsRead(bin, 2); m["error"] == nil {
		t.Fatalf("二进制文件应被拒绝")
	}

	// 目录 → 拒绝
	if m = fsRead(dir, 3); m["error"] == nil {
		t.Fatalf("目录应被拒绝")
	}
}

func TestFsWriteLimit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	// 正常写入
	if m := fsWrite(target, "hello", 1); m["ok"] != true {
		t.Fatalf("正常写入失败：%v", m["error"])
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "hello" {
		t.Fatalf("写入内容不符：err=%v raw=%q", err, raw)
	}
	// 超限拒绝
	if m := fsWrite(target, strings.Repeat("a", fsWriteMax+1), 2); m["error"] == nil {
		t.Fatalf("超限写入应被拒绝")
	}
}
