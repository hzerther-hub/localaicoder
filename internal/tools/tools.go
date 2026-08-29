// Package tools 内置工具：每工具一文件（schema + 执行器 + 条件暴露），
// registry.go 为注册表唯一事实源（对译 Python tools.py）。
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"localai/internal/config"
)

// ---------------- 工作目录 ----------------

var (
	wsMu       sync.RWMutex
	workspace  string
	wsInitOnce sync.Once
)

func initWorkspace() {
	workspace = config.LoadLastWorkspace()
}

// SetWorkspace 切换工作目录。
func SetWorkspace(path string) {
	wsMu.Lock()
	workspace = path
	wsMu.Unlock()
}

// GetWorkspace 当前工作目录（工具默认在此目录内操作）。
func GetWorkspace() string {
	wsInitOnce.Do(initWorkspace)
	wsMu.RLock()
	defer wsMu.RUnlock()
	return workspace
}

// PushWorkspace 临时切换工作目录；返回恢复函数（测试隔离用）。
func PushWorkspace(path string) (restore func()) {
	old := GetWorkspace()
	SetWorkspace(path)
	return func() { SetWorkspace(old) }
}

// ---------------- 共享小工具 ----------------

// strOf any → string（非 string 返回空串）。
func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func resolve(path string) string {
	return ResolvePath(path)
}

// ResolvePath 把相对路径解析到工作目录（绝对路径原样返回）。
func ResolvePath(path string) string {
	if path == "" {
		return GetWorkspace()
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(GetWorkspace(), path)
}

// readFileText 读文件全文；失败返回空串。
func readFileText(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// trimLine 截断超长行展示。
func trimLine(line string, max int) string {
	t := strings.TrimSpace(line)
	if len(t) > max {
		t = t[:max]
	}
	return t
}
