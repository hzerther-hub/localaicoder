// 沙箱（写操作护栏，非 OS 级隔离）+ git 分支读取（对译 Python tools.py 沙箱段）。
package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// PathInWorkspace path（解析符号链接后）是否落在工作目录内。
// 大小写/分隔符归一化后判断；root 为空时用当前工作目录。
func PathInWorkspace(path, root string) bool {
	if root == "" {
		root = GetWorkspace()
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	// 注意：EvalSymlinks 失败时返回空串，必须只在成功时采用返回值
	if resolved, err := filepath.EvalSymlinks(r); err == nil {
		r = resolved
	} else {
		r = filepath.Clean(r)
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	} else {
		// 目标可能尚不存在（write_file 新建文件）：退回 Clean 判断
		p = filepath.Clean(p)
	}
	if runtime.GOOS == "windows" {
		r = strings.ToLower(r)
		p = strings.ToLower(p)
	}
	return p == r || strings.HasPrefix(p, r+string(filepath.Separator))
}

// 高危 shell 命令（大小写不敏感）：只拦"一眼 destructive"的操作。
var blockedShellPatterns = []string{
	`\brm\s+(?:-\w+\s+)*-\w*[rf]\w*\s+/(?:\s|$|\*)`,  // rm -rf / 或 rm -rf /*
	`\bmkfs(?:\.\w+)?\b`,                              // 格式化文件系统
	`\bdd\s+[^|;]*\bof=/dev/`,                          // dd 直写块设备
	`:\(\)\s*\{`,                                       // fork bomb :(){ :|:& };:
	`\b(?:shutdown|reboot|poweroff|halt)\b`,            // 关机/重启
	`\bformat\s+[a-zA-Z]:`,                             // Windows 格式化盘符
	`\b(?:rd|rmdir)\s+/s\s+/q\s+[a-zA-Z]:[\\/]?\s*$`,   // rd /s /q C:\
	`\bdel\s+(?:/[sqfSQF]+\s+)+[a-zA-Z]:[\\/]\*`,          // del /s /q C:\*
}

var compiledBlocked = func() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range blockedShellPatterns {
		out = append(out, regexp.MustCompile(`(?i)`+p))
	}
	return out
}()

// ShellCommandBlocked 命中高危模式返回命中的模式，否则空串。
func ShellCommandBlocked(cmd string) string {
	for i, rx := range compiledBlocked {
		if rx.MatchString(cmd) {
			return blockedShellPatterns[i]
		}
	}
	return ""
}

// GitBranch 读工作区（或上级目录）当前 git 分支；非仓库/失败返回空。
// 只读 .git/HEAD，不调 git 子进程；支持 worktree（.git 为文件）。
func GitBranch(workspaceDir string) string {
	root := workspaceDir
	if root == "" {
		root = GetWorkspace()
	}
	if root == "" {
		return ""
	}
	cur, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	var gitPath string
	for {
		cand := filepath.Join(cur, ".git")
		if _, err := os.Stat(cand); err == nil {
			gitPath = cand
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	gitDir := gitPath
	if st, err := os.Stat(gitPath); err == nil && !st.IsDir() {
		// worktree/submodule：.git 是文件，内容 "gitdir: <path>"
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
			return ""
		}
		gitDir = strings.TrimSpace(line[len("gitdir:"):])
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(filepath.Dir(gitPath), gitDir)
		}
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref:") {
		name := strings.TrimSpace(ref[4:])
		if strings.HasPrefix(name, "refs/heads/") {
			return name[len("refs/heads/"):]
		}
		if strings.HasPrefix(name, "refs/") {
			parts := strings.SplitN(name, "/", 3)
			if len(parts) == 3 {
				return parts[2]
			}
		}
		return name
	}
	if len(ref) >= 7 {
		return ref[:7]
	}
	return ref
}
