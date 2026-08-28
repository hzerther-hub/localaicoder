// Package localmodels 本地 GPU 模型桥（对译 Python localmodels.py 的核心能力）。
//
// Go 版不动态加载 gpulocal Python 面板，改为约定式管理：
//   - models.json 里 base_url 指向 127.0.0.1/localhost 的 provider 视为本地模型；
//   - provider 可选字段 "start_command" / "stop_command"（shell 命令）用于启停
//     （llama-server / vllm serve / systemctl --user start xxx 等由用户配置）；
//   - 健康三态：GET {base_url}/models → 200 = active，503 = loading，其余 = stopped。
package localmodels

import (
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
)

// State 本地模型服务状态。
type State string

const (
	StateActive  State = "active"  // ● 运行中且健康
	StateLoading State = "loading" // ◐ 模型加载中（503）
	StateStopped State = "stopped" // ○ 未运行/不可达
)

// LocalModel 一个本地模型条目。
type LocalModel struct {
	Key          string `json:"key"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ModelID      string `json:"model_id"`
	DisplayName  string `json:"display_name"`
	BaseURL      string `json:"base_url"`
	Vision       bool   `json:"vision"`
	StartCommand string `json:"start_command,omitempty"`
	StopCommand  string `json:"stop_command,omitempty"`
	State        State  `json:"state,omitempty"`
}

// List 列出全部本地模型（不带状态；状态用 StatusOf/StatusAll 查询）。
func List() []LocalModel {
	models, _ := config.LoadModels()
	var out []LocalModel
	for _, m := range models {
		if !isLocalURL(m.BaseURL) {
			continue
		}
		pid, _, _ := strings.Cut(m.Key, "/")
		out = append(out, LocalModel{
			Key: m.Key, ProviderID: pid, ProviderName: m.ProviderName,
			ModelID: m.ModelID, DisplayName: m.DisplayName, BaseURL: m.BaseURL,
			Vision: m.Vision,
			StartCommand: providerField(pid, "start_command"),
			StopCommand:  providerField(pid, "stop_command"),
		})
	}
	return out
}

// providerField 从 models.json 原始数据取 provider 级扩展字段。
func providerField(providerID, field string) string {
	data := config.LoadModelsData()
	for _, pv := range mustArray(data["providers"]) {
		p, ok := pv.(map[string]any)
		if !ok || str(p["id"]) != providerID {
			continue
		}
		return str(p[field])
	}
	return ""
}

func mustArray(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func isLocalURL(baseURL string) bool {
	return strings.Contains(baseURL, "127.0.0.1") || strings.Contains(baseURL, "localhost")
}

// StatusOf 检查单个本地模型健康状态（/models 探测，2s 超时）。
func StatusOf(m LocalModel) State {
	req, err := http.NewRequest("GET", strings.TrimRight(m.BaseURL, "/")+"/models", nil)
	if err != nil {
		return StateStopped
	}
	req.Header.Set("Authorization", "Bearer local-noauth")
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return StateStopped
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return StateActive
	case resp.StatusCode == 503:
		return StateLoading
	default:
		return StateStopped
	}
}

// StatusAll 并发检查全部本地模型状态。
func StatusAll() map[string]State {
	list := List()
	out := make(map[string]State, len(list))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, m := range list {
		wg.Add(1)
		go func(m LocalModel) {
			defer wg.Done()
			s := StatusOf(m)
			mu.Lock()
			out[m.Key] = s
			mu.Unlock()
		}(m)
	}
	wg.Wait()
	return out
}

// Start 启动本地模型服务（执行 provider 配置的 start_command）。
// 命令发起成功即返回（模型加载需要时间，状态由轮询更新）。
func Start(m LocalModel) (bool, string) {
	if m.StartCommand == "" {
		return false, "未配置 start_command（在 models.json 对应 provider 里添加）"
	}
	return runCommand(m.StartCommand)
}

// Stop 停止本地模型服务。
func Stop(m LocalModel) (bool, string) {
	if m.StopCommand == "" {
		return false, "未配置 stop_command"
	}
	return runCommand(m.StopCommand)
}

func runCommand(cmdStr string) (bool, string) {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/c", cmdStr)
	} else {
		c = exec.Command("sh", "-c", cmdStr)
	}
	if err := c.Start(); err != nil {
		return false, err.Error()
	}
	go func() { _ = c.Wait() }() // 不阻塞：服务进程自行常驻
	return true, ""
}
