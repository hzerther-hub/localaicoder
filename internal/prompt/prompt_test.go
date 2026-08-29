package prompt

import (
	"strings"
	"testing"

	"localai/internal/config"
)

func testModel() *config.ModelConfig {
	return &config.ModelConfig{
		Key: "p/m1", ModelID: "m1", DisplayName: "模型一", BaseURL: "http://x/v1",
		ContextWindow: 128000,
	}
}

func opts() Options {
	return Options{
		Model:     testModel(),
		Workspace: "/tmp/ws",
		Language:  "zh",
		ToolNames: []string{"read_file", "write_file", "run_shell", "index_search", "call_model", "lsp_diagnostics", "web_search"},
		GitInfo:   "main（2 个文件有改动）",
		Date:      "2026-08-29",
	}
}

func TestBuildSectionOrder(t *testing.T) {
	sections := Build(opts())
	joined := Join(sections)
	idx := func(s string) int { return strings.Index(joined, s) }
	cases := []struct {
		name string
		i, j int
	}{
		{"身份在工具政策前", idx("你是 Local AI Studio"), idx("工具使用政策")},
		{"工具政策在任务纪律前", idx("工具使用政策"), idx("完成纪律")},
		{"任务纪律在输出要求前", idx("完成纪律"), idx("输出要求")},
		{"边界在动态区前", idx(SYSTEM_PROMPT_DYNAMIC_BOUNDARY), idx("# 环境")},
		{"环境在模型身份前", idx("# 环境"), idx("当前模型")},
		{"模型身份在语言指令前", idx("当前模型"), idx("请使用中文回复")},
	}
	for _, c := range cases {
		if c.i < 0 || c.j < 0 || c.i >= c.j {
			t.Fatalf("%s: 顺序不符 (%d, %d)", c.name, c.i, c.j)
		}
	}
}

func TestStaticPrefixStable(t *testing.T) {
	// 动态区变化（工作目录/日期/git/模型名）不应影响静态前缀
	mk := func(mod func(*Options)) string {
		o := opts()
		mod(&o)
		sections := Build(o)
		for i, s := range sections {
			if s == SYSTEM_PROMPT_DYNAMIC_BOUNDARY {
				return Join(sections[:i+1])
			}
		}
		t.Fatal("缺少边界标记")
		return ""
	}
	base := mk(func(*Options) {})
	changed := mk(func(o *Options) {
		o.Workspace = "/other"
		o.Date = "2027-01-01"
		o.GitInfo = ""
		o.Model = &config.ModelConfig{Key: "q/x", DisplayName: "另一个"}
	})
	if base != changed {
		t.Fatalf("静态前缀应逐字节稳定:\nbase   %q\nchanged %q", base, changed)
	}
}

func TestToolPolicyConditional(t *testing.T) {
	o := opts()
	o.ToolNames = []string{"read_file", "grep_search"}
	joined := Join(Build(o))
	if strings.Contains(joined, "write_file") || strings.Contains(joined, "call_model") {
		t.Fatal("未暴露的工具不应出现在政策里")
	}
}

func TestMinimalMode(t *testing.T) {
	// gpulocal → 极简
	o := opts()
	o.Model = &config.ModelConfig{Key: "gpulocal-8097/x", ContextWindow: 0}
	sections := Build(o)
	if len(sections) != 1 {
		t.Fatalf("极简模式应只有 1 段, got %d", len(sections))
	}
	// 小窗口 → 极简
	o.Model = &config.ModelConfig{Key: "p/small", ContextWindow: 32768}
	if sections = Build(o); len(sections) != 1 {
		t.Fatalf("小窗口模型应极简, got %d 段", len(sections))
	}
	// 极简模式仍带工作目录与语言
	if !strings.Contains(sections[0], "/tmp/ws") || !strings.Contains(sections[0], "中文") {
		t.Fatalf("极简提示缺少关键信息: %q", sections[0])
	}
}

func TestAddendumAppended(t *testing.T) {
	o := opts()
	o.Addendum = "公司规范：禁止使用 any 类型。"
	joined := Join(Build(o))
	if !strings.Contains(joined, "禁止使用 any 类型") {
		t.Fatal("附加段应追加在末尾")
	}
	if strings.LastIndex(joined, "公司规范") < strings.LastIndex(joined, "请使用中文回复") {
		t.Fatal("附加段应在语言指令之后")
	}
}

func TestGitSummary(t *testing.T) {
	if GitSummary("") != "" {
		t.Fatal("空目录应返回空")
	}
	if GitSummary(t.TempDir()) != "" {
		t.Fatal("非 git 目录应返回空")
	}
}
