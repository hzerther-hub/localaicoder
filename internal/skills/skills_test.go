package skills

// 技能系统测试：解析/规范名/作用域存取/蒸馏判定/蒸馏链路（httptest 假 LLM）。
// 隔离模式对齐 internal/agent：config.SetDir(t.TempDir()) + t.Cleanup 复位。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localai/internal/config"
	"localai/internal/msg"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config.SetDir(dir)
	t.Cleanup(func() {
		config.SetDir("")
		ResetForTest()
	})
	return dir
}

// sseText 构造纯文本回复的 SSE 响应体（chat_completions 形态）。
func sseText(text string) string {
	escaped, _ := json.Marshal(text)
	return "data: " + fmt.Sprintf(`{"choices":[{"delta":{"content":%s}}]}`, escaped) + "\n\n" +
		"data: [DONE]\n\n"
}

func TestCleanName(t *testing.T) {
	dir := setup(t)
	_ = dir
	if got := CleanName("  Quant JoinQuant_to-QMT!! "); got != "quant-joinquant-to-qmt" {
		t.Fatalf("CleanName 规范化错误: %q", got)
	}
	if got := CleanName("A  --  B"); got != "a-b" {
		t.Fatalf("连续连字符未收敛: %q", got)
	}
	if got := CleanName("///"); got != "" {
		t.Fatalf("全非法字符应为空: %q", got)
	}
}

func TestParseRenderRoundTrip(t *testing.T) {
	setup(t)
	src := "---\nname: my-skill\ndescription: 一句话说明\nwhen: 触发词1,触发词2\n---\n\n正文第一行\n正文第二行\n"
	sk, err := parseSkillText(src)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if sk.Name != "my-skill" || sk.Description != "一句话说明" || sk.When != "触发词1,触发词2" {
		t.Fatalf("frontmatter 字段错误: %+v", sk)
	}
	if !strings.Contains(sk.Body, "正文第一行") || !strings.Contains(sk.Body, "正文第二行") {
		t.Fatalf("正文丢失: %q", sk.Body)
	}
	// Render 后再解析应一致
	sk2, err := parseSkillText(sk.Render())
	if err != nil || sk2.Name != sk.Name || sk2.Body != sk.Body {
		t.Fatalf("Render 往返不一致: %+v err=%v", sk2, err)
	}
	// 缺 frontmatter 报错
	if _, err := parseSkillText("没有 frontmatter 的普通文本"); err == nil {
		t.Fatal("缺 frontmatter 应报错")
	}
}

func TestSaveLoadAllScopes(t *testing.T) {
	setup(t)
	ws := t.TempDir()
	// 用户级两条 + 项目级一条
	if _, err := Save(Skill{Name: "alpha", Description: "A", Body: "A 正文"}, ScopeUser, ""); err != nil {
		t.Fatalf("保存用户级失败: %v", err)
	}
	if _, err := Save(Skill{Name: "beta", Description: "B", Body: "B 正文"}, ScopeUser, ""); err != nil {
		t.Fatalf("保存用户级失败: %v", err)
	}
	if _, err := Save(Skill{Name: "gamma", Description: "G", Body: "G 正文"}, ScopeProject, ws); err != nil {
		t.Fatalf("保存项目级失败: %v", err)
	}
	all := LoadAll(ws)
	if len(all) != 3 {
		t.Fatalf("应载入 3 条技能，实得 %d: %+v", len(all), all)
	}
	if all[0].Name != "alpha" || all[1].Name != "beta" || all[2].Name != "gamma" {
		t.Fatalf("未按名称排序: %+v", all)
	}
	// 同名：项目级覆盖用户级
	_, _ = Save(Skill{Name: "alpha", Description: "项目级覆盖", Body: "P"}, ScopeProject, ws)
	all = LoadAll(ws)
	for _, sk := range all {
		if sk.Name == "alpha" && sk.Description != "项目级覆盖" {
			t.Fatalf("同名技能项目级应覆盖用户级: %+v", sk)
		}
	}
	// 不带工作区时看不到项目级技能
	if len(LoadAll("")) != 2 {
		t.Fatalf("空工作区不应载入项目级技能")
	}
}

func TestSimilarExisting(t *testing.T) {
	setup(t)
	ws := t.TempDir()
	_, _ = Save(Skill{Name: "deploy-flow", Description: "部署流程经验", Body: "步骤"}, ScopeUser, "")
	if !SimilarExisting(ws, Skill{Name: "deploy-flow", Description: "x"}) {
		t.Fatal("同名技能应查重命中")
	}
	if !SimilarExisting(ws, Skill{Name: "other-name", Description: "部署流程经验"}) {
		t.Fatal("同描述技能应查重命中")
	}
	if SimilarExisting(ws, Skill{Name: "fresh", Description: "全新经验"}) {
		t.Fatal("全新技能不应命中查重")
	}
}

func TestShouldDistill(t *testing.T) {
	setup(t)
	msgs := writeSuccessMessages()
	if ShouldDistill("完成", nil, MIN_TOOL_CALLS, msgs) {
		t.Fatal("未开技能开关不应蒸馏")
	}
	config.SetSkillsEnabled(true)
	if ShouldDistill("", nil, MIN_TOOL_CALLS, msgs) {
		t.Fatal("无最终文本不应蒸馏")
	}
	if ShouldDistill("完成", fmt.Errorf("x"), MIN_TOOL_CALLS, msgs) {
		t.Fatal("运行出错不应蒸馏")
	}
	if ShouldDistill("完成", nil, MIN_TOOL_CALLS-1, msgs) {
		t.Fatal("工具调用不足不应蒸馏")
	}
	if ShouldDistill("完成", nil, MIN_TOOL_CALLS, readOnlyMessages()) {
		t.Fatal("只读会话（无写成功）不应蒸馏")
	}
	if !ShouldDistill("完成", nil, MIN_TOOL_CALLS, msgs) {
		t.Fatal("全部条件满足应蒸馏")
	}
}

// writeSuccessMessages 构造含 write_file 成功的会话（3 次工具调用）。
func writeSuccessMessages() []msg.Msg {
	return []msg.Msg{
		{"role": "user", "content": "修复 bug"},
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
			map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": "write_file", "arguments": "{}"}},
		}},
		{"role": "tool", "tool_call_id": "c1", "content": "文件内容"},
		{"role": "tool", "tool_call_id": "c2", "content": "已写入 10 字节"},
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "c3", "type": "function", "function": map[string]any{"name": "run_shell", "arguments": "{}"}},
		}},
		{"role": "tool", "tool_call_id": "c3", "content": "测试全部通过"},
		{"role": "assistant", "content": "修复完成"},
	}
}

// readOnlyMessages 只读会话（read_file 成功也不算写成功）。
func readOnlyMessages() []msg.Msg {
	return []msg.Msg{
		{"role": "user", "content": "看看代码"},
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "r1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
		}},
		{"role": "tool", "tool_call_id": "r1", "content": "文件内容"},
		{"role": "assistant", "content": "看完了"},
	}
}

func TestHadWriteSuccess(t *testing.T) {
	setup(t)
	if !HadWriteSuccess(writeSuccessMessages()) {
		t.Fatal("write_file 成功应判定为 true")
	}
	if HadWriteSuccess(readOnlyMessages()) {
		t.Fatal("只读会话应判定为 false")
	}
	// 写失败（错误前缀）不算成功
	msgs := []msg.Msg{
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "w1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": "{}"}},
		}},
		{"role": "tool", "tool_call_id": "w1", "content": "错误：文件不存在"},
	}
	if HadWriteSuccess(msgs) {
		t.Fatal("写失败不应判定为 true")
	}
}

// seedTestProvider 在测试配置目录写一个指向假服务器的 provider。
func seedTestProvider(t *testing.T, baseURL string) {
	t.Helper()
	content := fmt.Sprintf(`{"default":"test/d1","providers":[{"id":"test","name":"T","base_url":%q,"api_key":"k","models":[{"id":"d1"}]}]}`, baseURL)
	if err := os.WriteFile(filepath.Join(config.Dir(), "models.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("写测试 models.json 失败: %v", err)
	}
}

const distillReply = "---\nname: gtest-fix-flow\ndescription: 测试蒸馏产出的经验\nwhen: 修复,测试\n---\n先读文件，再改，再跑测试。"

func TestDistillCreatesDraft(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseText(distillReply))
	}))
	defer srv.Close()
	seedTestProvider(t, srv.URL)
	config.SetSkillsEnabled(true)
	config.SetSkillsDistillModel("test/d1")

	msgs := writeSuccessMessages()
	path := Distill("sess-1", "", msgs, "修复完成")
	if path == "" {
		t.Fatal("蒸馏应产出草稿")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("草稿文件不可读: %v", err)
	}
	if !strings.Contains(string(data), "name: gtest-fix-flow") {
		t.Fatalf("草稿内容缺失 frontmatter: %s", data)
	}
	drafts := ListDrafts()
	if len(drafts) != 1 || drafts[0].Name != "gtest-fix-flow" {
		t.Fatalf("草稿列表异常: %+v", drafts)
	}
	// 同会话重复收尾：不重复蒸馏
	if again := Distill("sess-1", "", msgs, "修复完成"); again != "" {
		t.Fatal("同会话二次蒸馏应跳过")
	}
	// 查重：近似经验再次蒸馏不重复入库
	ResetForTest()
	noSkillReply := "---\nname: gtest-fix-flow\ndescription: 测试蒸馏产出的经验\nwhen: 修复\n---\n近似内容。"
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseText(noSkillReply))
	}))
	defer srv2.Close()
	seedTestProvider(t, srv2.URL)
	if p := Distill("sess-2", "", msgs, "修复完成"); p != "" {
		t.Fatal("近似技能查重应跳过入库")
	}
}

func TestDistillGuards(t *testing.T) {
	setup(t)
	msgs := writeSuccessMessages()
	// 开关关闭：静默跳过（即使配置了模型）
	seedTestProvider(t, "http://127.0.0.1:1") // 不可达，验证未触达
	config.SetSkillsDistillModel("test/d1")
	if p := Distill("g1", "", msgs, "完成"); p != "" {
		t.Fatal("开关关闭不应蒸馏")
	}
	// 开关开但模型 NO：放弃
	config.SetSkillsEnabled(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseText("NO"))
	}))
	defer srv.Close()
	seedTestProvider(t, srv.URL)
	if p := Distill("g2", "", msgs, "完成"); p != "" {
		t.Fatal("模型回答 NO 应放弃")
	}
	// 只读会话（无写成功）：不触发
	ResetForTest()
	if p := Distill("g3", "", readOnlyMessages(), "完成"); p != "" {
		t.Fatal("只读会话不应蒸馏")
	}
}

func TestPromptSection(t *testing.T) {
	setup(t)
	ws := t.TempDir()
	if PromptSection(ws, "随便聊聊") != "" {
		t.Fatal("开关关闭应返回空")
	}
	config.SetSkillsEnabled(true)
	if PromptSection(ws, "随便聊聊") != "" {
		t.Fatal("无技能应返回空")
	}
	_, _ = Save(Skill{Name: "deploy", Description: "部署经验", When: "部署,上线", Body: "1. 构建 2. 推送"}, ScopeUser, "")
	_, _ = Save(Skill{Name: "quant", Description: "量化经验", When: "回测", Body: "先查 API 差异"}, ScopeUser, "")
	sec := PromptSection(ws, "帮我部署一下服务")
	if !strings.Contains(sec, "deploy") || !strings.Contains(sec, "1. 构建") {
		t.Fatalf("注入段缺技能内容: %s", sec)
	}
	// 触发词命中排序：命中技能在前
	if idx := strings.Index(sec, "deploy"); idx > strings.Index(sec, "quant") {
		t.Fatalf("命中触发词的技能应排前: %s", sec)
	}
}

func TestPromptSectionDerivedTriggers(t *testing.T) {
	setup(t)
	config.SetSkillsEnabled(true)
	ws := t.TempDir()
	// 导入型技能：不填 when，只靠名称与描述自动推导
	_, _ = Save(Skill{Name: "xlsx", Description: "Create and edit Excel spreadsheets, formulas, charting", Body: "表格指引"}, ScopeUser, "")
	_, _ = Save(Skill{Name: "meeting-notes", Description: "Summarize meeting minutes and discussions", Body: "纪要指引"}, ScopeUser, "")
	// 中文消息提及 excel：应经描述词命中 xlsx 并排在无关技能前
	sec := PromptSection(ws, "帮我做一个excel表格统计销量")
	if !strings.Contains(sec, "xlsx") {
		t.Fatalf("无 when 也应按名称/描述命中: %s", sec)
	}
	if idx := strings.Index(sec, "xlsx"); idx > strings.Index(sec, "meeting-notes") {
		t.Fatalf("推导命中的技能应排前: %s", sec)
	}
	// 反向前缀匹配：用户写短词 ppt 也能命中名称为 pptx 的技能
	_, _ = Save(Skill{Name: "pptx", Description: "Create slide presentations", Body: "幻灯片指引"}, ScopeUser, "")
	sec = PromptSection(ws, "做一份ppt")
	if !strings.Contains(sec, "pptx") {
		t.Fatalf("用户短词 ppt 应前缀命中 pptx: %s", sec)
	}
	if idx := strings.Index(sec, "pptx"); idx > strings.Index(sec, "meeting-notes") {
		t.Fatalf("前缀命中的技能应排前: %s", sec)
	}
	// 显式 when 命中优先于推导命中
	_, _ = Save(Skill{Name: "alpha-guide", Description: "presentation deck guidance", When: "deck", Body: "a"}, ScopeUser, "")
	sec = PromptSection(ws, "给我做一份 deck 和 ppt")
	if idx := strings.Index(sec, "alpha-guide"); idx > strings.Index(sec, "pptx") {
		t.Fatalf("显式 when 命中应排在推导命中前: %s", sec)
	}
}

func TestDeriveNameAndWords(t *testing.T) {
	setup(t)
	sk := Skill{Name: "mcp-builder", Description: "Guide for creating MCP servers that expose tools. Servers, tools, data."}
	if got := deriveNameWords(sk.Name); fmt.Sprint(got) != "[builder mcp]" {
		t.Fatalf("名称分词异常: %v", got)
	}
	// 描述/正文词：剥尾部 s、滤停用词（原形与变形都查）、≥4 字母、排序确定
	got := deriveWords(sk)
	want := []string{"creating", "data", "expose", "guide", "server"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("推导词应去重/剥 s/滤停用词/排序: got %v want %v", got, want)
	}
	if again := deriveWords(sk); fmt.Sprint(again) != fmt.Sprint(want) {
		t.Fatalf("推导应确定: %v vs %v", again, got)
	}
}

func TestSkillScore(t *testing.T) {
	setup(t)
	// 显式 when 命中权重高于推导命中
	withWhen := Skill{Name: "unrelated-name", Description: "totally different topic", When: "部署"}
	derived := Skill{Name: "deploy", Description: "deployment pipeline guidance"}
	ut := "帮我部署一下服务"
	if skillScore(withWhen, ut) <= skillScore(derived, ut) {
		t.Fatalf("显式 when 命中应得分更高: when=%d derive=%d", skillScore(withWhen, ut), skillScore(derived, ut))
	}
	// 全不命中为 0（名称序补位不受影响）
	if s := skillScore(Skill{Name: "other", Description: "cooking recipes"}, ut); s != 0 {
		t.Fatalf("不命中应为 0 分: %d", s)
	}
}

func TestSerializeMessagesStripsImages(t *testing.T) {
	setup(t)
	msgs := []msg.Msg{
		{"role": "system", "content": "系统提示不应出现"},
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "看这张图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAAVeryLongBase64"}},
		}},
	}
	out := SerializeMessages(msgs)
	if strings.Contains(out, "系统提示") {
		t.Fatal("system 消息不应进蒸馏输入")
	}
	if strings.Contains(out, "base64") {
		t.Fatal("图片 data URL 应被剥离")
	}
	if !strings.Contains(out, "看这张图") {
		t.Fatalf("文本内容应保留: %s", out)
	}
}
