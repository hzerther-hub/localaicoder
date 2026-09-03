package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MemsearchStatus 响应形状 + 与 memsearchInstalled 探测结果一致（不触发真安装）。
func TestMemsearchStatusShape(t *testing.T) {
	a := &App{}
	st := a.MemsearchStatus()
	if st["install_cmd"] != memsearchInstallCmd {
		t.Fatalf("install_cmd = %v, want %v", st["install_cmd"], memsearchInstallCmd)
	}
	installed, ok := st["installed"].(bool)
	if !ok {
		t.Fatalf("installed 缺失或非 bool：%v", st["installed"])
	}
	if want, _ := memsearchInstalled(); installed != want {
		t.Fatalf("installed=%v 与 memsearchInstalled()=%v 不一致", installed, want)
	}
	if _, ok := st["uv_available"].(bool); !ok {
		t.Fatalf("uv_available 缺失或非 bool：%v", st["uv_available"])
	}
}

// memsearchEnsureConfig 写配置到隔离的家目录；已有配置文件绝不覆盖。
func TestMemsearchEnsureConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows
	t.Setenv("HOME", home)        // Linux/macOS

	memsearchEnsureConfig()
	data, err := os.ReadFile(filepath.Join(home, ".memsearch", "config.toml"))
	if err != nil {
		t.Fatalf("配置未写入：%v", err)
	}
	if got := string(data); !strings.Contains(got, `provider = "onnx"`) {
		t.Fatalf("配置缺 onnx provider：%q", got)
	}

	// 已有文件不被覆盖
	broken := "# 手工配置，别动\n"
	cfg := filepath.Join(home, ".memsearch", "config.toml")
	if err := os.WriteFile(cfg, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	memsearchEnsureConfig()
	data, _ = os.ReadFile(cfg)
	if string(data) != broken {
		t.Fatalf("已有配置被覆盖：%q", string(data))
	}
}
