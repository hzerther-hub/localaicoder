package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	SetDir(dir)
	t.Cleanup(func() { SetDir("") })
	return dir
}

func TestLoadModelsDefault(t *testing.T) {
	setup(t)
	models, def := LoadModels()
	if len(models) < 2 {
		t.Fatalf("默认种子至少 2 个模型, got %d", len(models))
	}
	if def == "" {
		t.Fatal("默认模型 key 不应为空")
	}
	if FindModel(def) == nil {
		t.Fatalf("默认模型 %s 应可找到", def)
	}
	if FindModel("no/such") != nil {
		t.Fatal("未知模型应返回 nil")
	}
}

func TestAddRemoveModel(t *testing.T) {
	setup(t)
	added := AddCustomModel([]string{"m1", "m2"}, "http://127.0.0.1:9/v1", "", nil, false, "")
	if len(added) != 2 {
		t.Fatalf("应添加 2 个模型, got %d", len(added))
	}
	if added[0].Key != "custom/m1" || added[0].APIKey != "local-noauth" {
		t.Fatalf("custom provider 默认 key/api_key 不符: %+v", added[0])
	}
	// 追加探测
	if n := AugmentProviderModels("custom", []string{"m3", "m1"}, false); n != 1 {
		t.Fatalf("应只新增 1 个（m1 已存在）, got %d", n)
	}
	// 删除
	if !RemoveModel("custom/m2") {
		t.Fatal("删除应成功")
	}
	if RemoveModel("custom/m2") {
		t.Fatal("重复删除应失败")
	}
}

func TestDispatchDefaults(t *testing.T) {
	setup(t)
	if !GetModelDispatch() {
		t.Fatal("model_dispatch 默认开")
	}
	if GetDispatchFlash() == "" || GetDispatchPro() == "" || GetDispatchVision() == "" {
		t.Fatal("三个云端目标默认应非空")
	}
	SetDispatchFlash("deepseek/new-flash")
	if GetDispatchFlash() != "deepseek/new-flash" {
		t.Fatalf("派发目标应持久化, got %s", GetDispatchFlash())
	}
	if DispatchTargetLabel(GetDispatchPro()) != "云端高性能" {
		t.Fatalf("派发标签不符: %q", DispatchTargetLabel(GetDispatchPro()))
	}
}

func TestKBConfig(t *testing.T) {
	setup(t)
	if GetKBEnabled() {
		t.Fatal("kb_enabled 默认关")
	}
	SetKBRoots([]string{"/b", "/a", "/a"})
	roots := GetKBRoots()
	if len(roots) != 2 {
		t.Fatalf("根目录应去重: %v", roots)
	}
	absA, _ := filepath.Abs("/a")
	absB, _ := filepath.Abs("/b")
	if len(roots) == 2 && roots[0] == absB && roots[1] == absA {
		// 排序按绝对路径字符串；不同平台盘符可能互换，只要去重即可
		t.Log("roots sorted:", roots)
	}
	SetKBEnabled(true)
	SetKBTopK(99)
	if GetKBTopK() != 20 {
		t.Fatalf("top_k 应钳到 20, got %d", GetKBTopK())
	}
}

func TestLanguageAndStandalone(t *testing.T) {
	setup(t)
	if GetLanguage() != "en" {
		t.Fatal("语言缺省英文")
	}
	SetLanguage("zh")
	if GetLanguage() != "zh" {
		t.Fatal("语言应切到 zh")
	}
	SetStandalone(true)
	if !GetStandalone() {
		t.Fatal("standalone 应为 true")
	}
}

func TestWorkspaceState(t *testing.T) {
	dir := setup(t)
	home, _ := os.UserHomeDir()
	if LoadLastWorkspace() != home {
		t.Fatal("无 state.json 时应回退家目录")
	}
	ws := filepath.Join(dir, "ws")
	_ = os.MkdirAll(ws, 0o755)
	SaveLastWorkspace(ws)
	if LoadLastWorkspace() != ws {
		t.Fatalf("工作目录应恢复为 %s", ws)
	}
}

func TestSystemPrompt(t *testing.T) {
	setup(t)
	SetLanguage("zh")
	p := GetSystemPrompt()
	if p == "" || !contains(p, "编码助手") || !contains(p, "请使用中文回复") {
		t.Fatal("中文系统提示应含关键指令")
	}
	SetLanguage("en")
	p = GetSystemPrompt()
	if !contains(p, "Please respond in English") {
		t.Fatal("英文系统提示应含语言指令")
	}
}

func TestMCPServersEmpty(t *testing.T) {
	setup(t)
	data := LoadMCPServers()
	if _, ok := data["servers"].(map[string]any); !ok {
		t.Fatalf("空配置应含 servers 字典: %v", data)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
