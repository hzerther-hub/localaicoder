package skills

// 安装器测试：目录扫描（SKILL.md 扁平化）/ 同名跳过 / 单文件回退 / 远程 .md 安装。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"localai/internal/config"
)

func TestInstallFromDir(t *testing.T) {
	setup(t)
	// 模拟 Claude Code 仓库布局：skills/<name>/SKILL.md + 无法解析的杂项
	repo := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("skills/alpha/SKILL.md", "---\nname: alpha\ndescription: 技能 A\nwhen: a\n---\nA 正文")
	mk("skills/beta/SKILL.md", "---\nname: beta\ndescription: 技能 B\nwhen: b\n---\nB 正文")
	mk("skills/junk/SKILL.md", "没有 frontmatter 的普通文件")
	mk("README.md", "# 只是说明文档")

	installed, skipped, err := InstallFromDir(repo)
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if len(installed) != 2 {
		t.Fatalf("应安装 2 个技能，实得 %v", installed)
	}
	if skipped != 0 {
		t.Fatalf("首次安装不应跳过: %d", skipped)
	}
	// 落盘在用户级目录（扁平 <name>.md）
	for _, p := range []string{filepath.Join(UserDir(), "alpha.md"), filepath.Join(UserDir(), "beta.md")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("安装文件缺失: %s", p)
		}
	}
	// 重装：同名全部跳过
	_, skipped2, err := InstallFromDir(repo)
	if err != nil {
		t.Fatalf("重装失败: %v", err)
	}
	if skipped2 != 2 {
		t.Fatalf("重装应跳过 2 个同名: %d", skipped2)
	}
	// 注入可见
	if n := len(LoadAll("")); n < 2 {
		t.Fatalf("安装后应可载入: %d", n)
	}
}

func TestInstallFromDirSingleFileFallback(t *testing.T) {
	setup(t)
	// 单文件技能仓库：根目录带 frontmatter 的 md（非 README）
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "my-skill.md"),
		[]byte("---\nname: single\ndescription: 单文件\nwhen: s\n---\n正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, skipped, err := InstallFromDir(repo)
	if err != nil || len(installed) != 1 || skipped != 0 {
		t.Fatalf("单文件回退安装异常: %v %v %d", installed, err, skipped)
	}
	// README 不应被安装
	if err := os.WriteFile(filepath.Join(repo, "README.md"),
		[]byte("---\nname: readme-skill\ndescription: x\nwhen: x\n---\nx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(UserDir(), "readme-skill.md")); err == nil {
		t.Fatal("README 不应被安装")
	}
}

func TestInstallFromMarkdownURL(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		fmt.Fprint(w, "---\nname: remote-skill\ndescription: 远程技能\nwhen: 远程\n---\n远程正文")
	}))
	defer srv.Close()

	name, err := InstallFromMarkdownURL(srv.URL)
	if err != nil || name != "remote-skill" {
		t.Fatalf("远程安装异常: %q %v", name, err)
	}
	// 同名再装：报错且不覆盖
	if _, err := InstallFromMarkdownURL(srv.URL); err == nil {
		t.Fatal("同名应报错")
	}
	// 非法内容报错
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "没有 frontmatter")
	}))
	defer bad.Close()
	if _, err := InstallFromMarkdownURL(bad.URL); err == nil {
		t.Fatal("非法内容应报错")
	}
	// config.SetDir 隔离校验（media 等目录不误触）
	_ = config.Dir()
}
