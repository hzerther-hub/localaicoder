// Package prompt 系统提示词构建（对齐 openclaude constants/prompts 的设计）：
//
//   - 分区组装：身份 → 工具政策（按实际暴露的工具生成）→ 任务纪律 →
//     输出效率 → 静/动边界 → 环境信息 → 模型身份 → 语言指令 → 自定义附加段；
//   - 静/动边界（SYSTEM_PROMPT_DYNAMIC_BOUNDARY）：边界之前跨会话逐字节稳定，
//     服务端 prompt cache 可全局复用；之后是会话相关内容；
//   - 小模型极简模式（对齐 CLAUDE_CODE_SIMPLE）：本地小上下文模型坍缩为
//     数行短提示，省 token 且小模型更易遵循。
//
// 提示词文案沿用项目既有中文版本，只做分区重组，不改变规则语义。
package prompt

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

// SYSTEM_PROMPT_DYNAMIC_BOUNDARY 静态/动态分区边界标记。
const SYSTEM_PROMPT_DYNAMIC_BOUNDARY = "__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__"

// Options 提示词构建输入（纯数据）。
type Options struct {
	Model     *config.ModelConfig
	Workspace string   // 当前工作目录
	Language  string   // zh / en（回复语言跟随界面）
	ToolNames []string // 本轮实际暴露给模型的内置工具名
	GitInfo   string   // git 分支/状态摘要；空 = 非 git 仓库
	Date      string   // 当前日期（YYYY-MM-DD）
	Addendum  string   // provider/model 级自定义附加段（prompt_addendum）
	Skills    string   // 技能注入段（skills.PromptSection 产出；空 = 不注入）
}

// Build 返回提示词分区（调用方以 Join 连接为 system 消息）。
func Build(o Options) []string {
	if minimalMode(o.Model) {
		return buildMinimal(o)
	}
	out := []string{
		identitySection(o),
		toolPolicySection(o),
		taskDisciplineSection(),
		outputSection(),
		SYSTEM_PROMPT_DYNAMIC_BOUNDARY,
		envSection(o),
		modelSection(o),
		languageSection(o.Language),
	}
	// 技能注入：动态区（随工作区/触发词变化，不能进静态缓存区）
	if s := strings.TrimSpace(o.Skills); s != "" {
		out = append(out, s)
	}
	if s := strings.TrimSpace(o.Addendum); s != "" {
		out = append(out, s)
	}
	return out
}

// Join 以空行连接分区。
func Join(sections []string) string { return strings.Join(sections, "\n\n") }

// minimalMode 本地/小上下文模型启用极简提示。
func minimalMode(m *config.ModelConfig) bool {
	if m == nil {
		return false
	}
	if strings.HasPrefix(m.Key, "gpulocal") {
		return true
	}
	return m.ContextWindow > 0 && m.ContextWindow <= 32*1024
}

// ---------------- 静态区（跨会话缓存稳定） ----------------

func identitySection(o Options) string {
	s := "你是 Local AI Studio 编码助手，在用户的本地工作目录中借助工具完成软件工程任务。" +
		"优先用本地能力：读文件、搜索代码、本地执行命令，尽量本地解决（本地不额外花钱）。"
	if contains(o.ToolNames, "call_model") {
		s += "只有当你确认任务超出本地能力/上下文，或本地缺少所需能力（如识图、超强推理/重分析）时，" +
			"才用 call_model 委派给云端。本地能搞定就别委派。"
	}
	if contains(o.ToolNames, "web_search") {
		s += "需要最新信息、外部库/文档或排查线上报错时，用 web_search 求证，不要凭记忆编造。"
	}
	return s
}

// toolPolicySection 按本轮实际暴露的工具生成使用政策
// （对齐 openclaude getUsingYourToolsSection：政策跟随工具面，不提没有的工具）。
func toolPolicySection(o Options) string {
	var rules []string
	if contains(o.ToolNames, "index_search") {
		rules = append(rules, "回答「XX在哪实现的/怎么用的」这类问题时优先用 index_search 做代码库语义检索，比逐个读文件快且省上下文。")
	}
	if contains(o.ToolNames, "write_file") {
		rules = append(rules, "修改/增强代码文件时，必须调用 write_file 把改动真正写回文件（不要只把新内容输出在回复里）；写完再用 read_file 抽查确认。")
	}
	if contains(o.ToolNames, "lsp_diagnostics") {
		rules = append(rules, "改完代码用 lsp_diagnostics 或运行相关测试/脚本验证。")
	}
	if contains(o.ToolNames, "call_model") {
		cfg := config.GetDispatchConfig()
		pro := strOr(msg.S(cfg, "dispatch_pro"), "云端高性能目标")
		rules = append(rules, "确需委派时：复杂/重推理任务→"+pro+"；识图类任务→识图目标。模型 key 以 call_model 工具说明为准。")
	}
	if contains(o.ToolNames, "run_shell") {
		rules = append(rules, "音频/视频附件可用 run_shell 调 ffmpeg（ffprobe）提取信息后再分析。")
	}
	if len(rules) == 0 {
		return ""
	}
	return "工具使用政策：\n- " + strings.Join(rules, "\n- ")
}

func taskDisciplineSection() string {
	return "完成纪律（必须遵守）：\n" +
		"1) 动手前先调用 todo_write 【一次性】列出全部步骤（读文件→改动→验证→收尾），不要逐条追加；\n" +
		"2) 逐项执行：开始某项前置 in_progress、完成置 completed，任何时候清单未完成都不得给出最终答复；\n" +
		"3) 改完必须验证（诊断/测试/运行），发现问题就修，直到通过；\n" +
		"4) 只有当所有步骤完成且验证通过、目标真正达成时，才给出最终答复；\n" +
		"绝不在半途（改了一部分、还没验证通过）就草草结束。" +
		"请始终基于真实工具/委派结果作答，不要编造文件内容。" +
		"用户消息可能附带本地媒体文件（图片/音频/视频）：图片直接以视觉输入提供。"
}

func outputSection() string {
	return "输出要求：始终精炼作答——只给结论与必要依据，绝不输出大段文件内容或重复列表。"
}

// ---------------- 动态区（会话相关） ----------------

func envSection(o Options) string {
	var b strings.Builder
	b.WriteString("# 环境\n- 工作目录: ")
	if o.Workspace == "" {
		b.WriteString("(未设置)")
	} else {
		b.WriteString(o.Workspace)
	}
	b.WriteString("\n- 平台: " + runtime.GOOS + "/" + runtime.GOARCH)
	if o.Date != "" {
		b.WriteString("\n- 日期: " + o.Date)
	}
	if o.GitInfo != "" {
		b.WriteString("\n- git: " + o.GitInfo)
	}
	return b.String()
}

func modelSection(o Options) string {
	if o.Model == nil {
		return ""
	}
	return "当前模型：" + o.Model.DisplayName + "（" + o.Model.Key + "）"
}

func languageSection(lang string) string {
	if lang == "zh" {
		return "请使用中文回复（与当前界面语言一致）。"
	}
	return "Please respond in English (matching the current UI language)."
}

// ---------------- 小模型极简模式 ----------------

func buildMinimal(o Options) []string {
	var b strings.Builder
	b.WriteString("你是编码助手，用工具完成用户任务：精炼作答，改文件必须 write_file 真正写回，改完验证。")
	if o.Workspace != "" {
		b.WriteString("\n工作目录: " + o.Workspace)
	}
	b.WriteString("\n" + languageSection(o.Language))
	if s := strings.TrimSpace(o.Addendum); s != "" {
		b.WriteString("\n" + s)
	}
	return []string{b.String()}
}

// ---------------- 辅助 ----------------

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func strOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// GitSummary 工作目录的 git 摘要（分支 + 简要状态）；非 git 仓库返回空串。
// system 提示仅会话首轮构建一次，这里花几百毫秒跑 git 可接受。
func GitSummary(workspace string) string {
	if workspace == "" {
		return ""
	}
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, args...)...)
		out, err := c.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	branch := run("branch", "--show-current")
	if branch == "" {
		return "" // 非 git 仓库（branch 失败）
	}
	changes := run("status", "--porcelain")
	n := 0
	if changes != "" {
		n = len(strings.Split(changes, "\n"))
	}
	if n > 0 {
		return branch + "（" + strconv.Itoa(n) + " 个文件有改动）"
	}
	return branch + "（干净）"
}
