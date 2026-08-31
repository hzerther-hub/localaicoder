package tools

// call_model（模型派发）：把子任务委派给云端模型 + 派发目标校验。

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
	"localai/internal/llm"
	"localai/internal/msg"
)

func init() {
	register(&Tool{
		Schema: `{
  "type": "function",
  "function": {
    "name": "call_model",
    "description": "把一段文本子任务委派给另一个（云端）模型处理（云端才花钱，非必要不用）。只有当本地确实搞不定时才用：任务复杂/重推理/超出你上下文与能力，或你尝试后仍无法高质量完成。本地能搞定就别委派。本工具只做纯文本子任务，不转发图片等附件。目标选择：复杂、重推理、需更强能力→deepseek/deepseek-v4-pro。model=目标模型 key；task=交给它的完整子任务提示词（尽量自包含）；reasoning_effort 可选（复杂任务可设 high）。",
    "parameters": {
      "type": "object",
      "properties": {
        "model": {"type": "string", "description": "目标模型 key（已配置的派发目标之一）"},
        "task": {"type": "string", "description": "交给目标模型的子任务提示词"},
        "reasoning_effort": {"type": "string", "description": "可选：推理等级（low/medium/high）"}
      },
      "required": ["model", "task"]
    }
  }
}`,
		ReadOnly: true,
		// 条件暴露：派发总开关 + 本地大脑已配置且健康
		Enabled: func() bool {
			if !config.GetModelDispatch() {
				return false
			}
			key := config.GetDispatchModel()
			return key != "" && localHealthy(key)
		},
		Exec: execCallModel,
	})
}

// dispatchSubPrompt call_model 子任务时注入的子系统提示。
const dispatchSubPrompt = "你是一个子任务执行模型。请只完成用户给你的这段子任务，输出简洁、准确、" +
	"可用的结果即可，不要复述任务，不要询问上下文，不要在结果里加入与任务无关的内容。"

// DispatchResultKeep 子任务结果回填主循环前的字符上限。
const DispatchResultKeep = 8000

// isLocalKey 模型是否指向本地端点（127.0.0.1 / localhost）。
func isLocalKey(modelKey string) bool {
	return config.IsLocalModelKey(modelKey)
}

// healthTTL 本地服务健康探测结果的有效期：结果按 baseURL 缓存一小段时间，
// 避免每轮构造工具 schema 时都发一次真实 HTTP 探测（3s 超时）拖慢对话。
const healthTTL = 15 * time.Second

type healthProbe struct {
	ok bool
	at time.Time
}

var (
	healthMu    sync.Mutex
	healthCache = map[string]healthProbe{}
)

// localHealthy 给定本地模型 key，判断对应服务是否已启动且健康
// （GET {base_url}/models 返回 2xx 即健康；结果按 baseURL 做短 TTL 缓存）。
func localHealthy(modelKey string) bool {
	mc := config.FindModel(modelKey)
	if mc == nil {
		return false
	}
	url := strings.TrimRight(mc.BaseURL, "/") + "/models"
	healthMu.Lock()
	if p, ok := healthCache[url]; ok && time.Since(p.at) < healthTTL {
		healthMu.Unlock()
		return p.ok
	}
	healthMu.Unlock()

	ok := probeHealthy(mc, url)

	healthMu.Lock()
	if len(healthCache) >= 64 {
		healthCache = map[string]healthProbe{}
	}
	healthCache[url] = healthProbe{ok: ok, at: time.Now()}
	healthMu.Unlock()
	return ok
}

// probeHealthy 真实探测一次本地服务健康（不做缓存）。
func probeHealthy(mc *config.ModelConfig, url string) bool {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	if mc.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+mc.APIKey)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// resetHealthCache 清空健康缓存（测试隔离用）。
func resetHealthCache() {
	healthMu.Lock()
	healthCache = map[string]healthProbe{}
	healthMu.Unlock()
}

// ValidateDispatchTarget 校验 call_model 的派发目标是否允许。
// 允许：配置里的三个云端目标（flash/pro/vision）；
// 拒绝：本地模型（自我调用/串行互斥）与白名单外的任何目标。
func ValidateDispatchTarget(model string) (bool, string) {
	cfg := config.GetDispatchConfig()
	cloud := map[string]bool{}
	for _, k := range []string{
		msg.S(cfg, "dispatch_flash"),
		msg.S(cfg, "dispatch_pro"),
		msg.S(cfg, "dispatch_vision"),
	} {
		if k != "" {
			cloud[k] = true
		}
	}
	if cloud[model] {
		return true, ""
	}
	if isLocalKey(model) {
		if model == msg.S(cfg, "dispatch_model") {
			return false, "错误：不能派发给当前本地大脑自身（避免自我调用）"
		}
		return false, "错误：本地模型互斥，不能派发给其它本地模型"
	}
	return false, "错误：派发目标不在白名单内：" + model
}

// ResolveDispatchVisionKey 识图预路由应选用的模型 key
// （本地大脑带识图且健康 → 本地；否则云端识图）。
func ResolveDispatchVisionKey() string {
	cfg := config.GetDispatchConfig()
	lb := msg.S(cfg, "dispatch_model")
	if lb != "" && isLocalKey(lb) && localHealthy(lb) {
		if mc := config.FindModel(lb); mc != nil && mc.Vision {
			return lb
		}
	}
	return msg.S(cfg, "dispatch_vision")
}

func truncateDispatch(text string, keep int) string {
	if len(text) <= keep {
		return text
	}
	head := keep * 2 / 3
	tail := keep - head
	return text[:head] + fmt.Sprintf("\n…[已压缩，原 %d 字符]\n", len(text)) + text[len(text)-tail:]
}

func execCallModel(args map[string]any) string {
	model := strings.TrimSpace(strOf(args["model"]))
	task := strings.TrimSpace(strOf(args["task"]))
	effort := strings.TrimSpace(strOf(args["reasoning_effort"]))
	if model == "" || task == "" {
		return "错误：call_model 需要 model 与 task 参数"
	}
	if !config.GetModelDispatch() {
		return "错误：模型派发未开启，无法调用其它模型"
	}
	if ok, err := ValidateDispatchTarget(model); !ok {
		return err
	}
	mc := config.FindModel(model)
	if mc == nil {
		return "错误：派发目标不存在：" + model
	}
	if effort != "" {
		mc.ReasoningEffort = effort // 按次覆盖，不改全局配置
	}
	messages := []msg.Msg{
		{"role": "system", "content": dispatchSubPrompt},
		{"role": "user", "content": task},
	}
	var collected []string
	err := llm.StreamChat(*mc, messages, nil, func(e msg.Event) error {
		if msg.S(e, "type") == "text" {
			collected = append(collected, msg.S(e, "delta"))
		}
		return nil
	})
	if err != nil {
		return "错误：派发失败：" + err.Error()
	}
	text := strings.Join(collected, "")
	if text == "" {
		return "（派发目标未返回内容）"
	}
	return truncateDispatch(text, DispatchResultKeep)
}
