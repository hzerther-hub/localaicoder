// Package llm LLM 客户端：shim 式协议适配层。
//
// 内核只说一种「内部语言」——OpenAI 风格消息（msg.Msg）+ 流事件契约
// （text/reasoning/tool_calls/finish/usage）；各 transport 负责把它翻译成
// 不同线上协议并翻译回来，使 agent 与其余代码对 provider 差异无感知
// （对齐 openclaude openaiShim 的 duck-typing 边界思想）。
//
// 仅标准库，无 SDK。凭据池轮换在 StreamChat 统一处理（keypool.go）。
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"localai/internal/config"
	"localai/internal/msg"
)

// LLMError 传输/解析失败（工具错误是普通文本，这里是异常）。
// Status 为 HTTP 状态码；0 = 非 HTTP 错误（连接失败/流内错误/解析失败）。
// Body 为错误响应体原文（截断），供 402/400 报文反解修正；
// RetryAfter 为服务端 Retry-After 头换算的等待时长（>0 时退避优先遵守）。
type LLMError struct {
	Msg        string
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *LLMError) Error() string { return e.Msg }

// ErrStop 协作停止：onEvent 返回它时中断流式读取（对齐 Python 版
// agent 在 generator 循环里 break 的行为）。
var ErrStop = errors.New("llm: cooperative stop")

// Event 类型（对齐 Python llm.stream_chat 的 yield 契约，所有 transport 一致）：
//   {"type":"text","delta":str}
//   {"type":"reasoning","delta":str}
//   {"type":"tool_calls","tool_calls":[...]}   本轮最终 tool_calls（累积完成）
//   {"type":"finish","reason":str}             OpenAI 风格：tool_calls/length/stop
//   {"type":"usage","usage":{...}}             OpenAI 风格归一化 usage

// streamArgs 一次流式请求的全部输入（各 transport 共享的「内部语言」）。
type streamArgs struct {
	ctx       context.Context // 流生命周期（看门狗空闲超时主动取消）
	model     config.ModelConfig
	messages  []msg.Msg
	tools     []msg.Msg
	maxTokens int
	onEvent   func(msg.Event) error
}

// transport 单次 HTTP 尝试：用给定 apiKey 发一次流式请求并解析事件流。
// 返回的 LLMError.Status ∈ {401,403,402,429} 时，StreamChat 会换 key 重试。
type transport func(args streamArgs, apiKey string) error

// transports 协议格式 → 传输实现（shim 注册表）。
var transports = map[string]transport{
	config.FormatChatCompletions: streamChatCompletions,
	config.FormatAnthropic:       streamAnthropic,
	config.FormatResponses:       streamResponses,
	config.FormatGemini:          streamGemini,
}

func transportFor(format string) transport {
	if tr, ok := transports[config.NormalizedFormat(format)]; ok {
		return tr
	}
	return streamChatCompletions
}

// maxKeyAttempts 单次 StreamChat 最多换 key 尝试次数（池再大也不无限重试）。
const maxKeyAttempts = 8

// streamIdleTimeout 流式空闲看门狗（对齐 openclaude DEFAULT_STREAM_IDLE_TIMEOUT_MS=90s）：
// 超过该时长未收到任何事件即主动断流，避免本地模型假死挂死整个会话。
// var 仅为测试可调小。
var streamIdleTimeout = 90 * time.Second

// StreamChat 流式对话；onEvent 每收到一个事件回调一次，
// 回调返回 ErrStop 则立刻中断（不报错），返回其它 error 直接上抛。
//
// 凭据池：按 model.APIKeys 轮换取 key；401/403 永久禁用当前 key、
// 402/429 冷却后换下一个重试（429/529 按 Retry-After 或指数退避等待）；
// 其它错误不换 key 直接上抛。
//
// 自适应修正（对齐 openclaude withRetry 报文反解）：402「can only afford M」
// 与 400「input length + max_tokens > context limit」各一次调低 max_tokens 重试。
func StreamChat(model config.ModelConfig, messages []msg.Msg,
	tools []msg.Msg, onEvent func(msg.Event) error) error {
	maxTokens := config.MaxTokens
	if strings.HasPrefix(model.Key, "gpulocal") {
		// 本地(gpulocal)模型 n_ctx 仅 ~131K，输出上限单独钳到 16K
		maxTokens = 16384
	}
	// 输出预算（Reasonix output_budget 思想）：模型声明了窗口时，
	// 输出上限 = min(全局上限, 窗口的 1/4)，避免输出把剩余上下文挤爆
	if model.ContextWindow > 0 {
		if quarter := model.ContextWindow / 4; quarter < maxTokens {
			maxTokens = quarter
		}
	}

	// 流式空闲看门狗：每个事件重置定时器，超时无数据则取消请求上下文
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idle := time.NewTimer(streamIdleTimeout)
	defer idle.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-idle.C:
			cancel() // 看门狗触发：断流（读取侧折算为 errStreamIdle）
		case <-done:
		}
	}()
	wrapped := func(e msg.Event) error {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(streamIdleTimeout)
		return onEvent(e)
	}

	args := streamArgs{
		ctx: ctx, model: model, messages: messages, tools: tools,
		maxTokens: maxTokens, onEvent: wrapped,
	}
	tr := transportFor(model.APIFormat)

	pool := GetPool(model.Key, model.APIKeys)
	var lastErr error
	rewritten := false // 402/400 报文反解只修正一次，防死循环
	for i := 0; i < pool.Size() && i < maxKeyAttempts; i++ {
		key, gen := pool.Next()
		err := tr(args, key)
		if err == nil {
			pool.ReportSuccess(key)
			return nil
		}
		if errors.Is(err, ErrStop) {
			pool.ReportSuccess(key)
			return err
		}
		if errors.Is(err, errStreamIdle) {
			// 看门狗中断：连接本身没问题，不惩罚 key，直接以超时错误上抛
			pool.ReportSuccess(key)
			return &LLMError{Msg: fmt.Sprintf("流式空闲超时（%d 秒无任何数据），已主动中断",
				int(streamIdleTimeout.Seconds()))}
		}
		// 402/400 报文反解（对齐 openclaude parseOpenRouterAffordableMaxTokensError /
		// parseMaxTokensContextOverflowError）：调低 max_tokens 立即原地重试一次
		var le *LLMError
		if !rewritten && errors.As(err, &le) && (le.Status == 402 || le.Status == 400) {
			if mt := adaptiveMaxTokens(le); mt > 0 && mt < args.maxTokens {
				args.maxTokens = mt
				rewritten = true
				pool.ReportSuccess(key)
				i-- // 不消耗换 key 名额
				continue
			}
		}
		switch failureKind(err) {
		case "":
			// 非凭据类失败（网络/流内/服务端）：换 key 也无意义
			return err
		case "auth", "cooldown":
			pool.ReportFailure(key, failureKind(err), gen)
			lastErr = err
			// 换 key 前的退避（对齐 openclaude withRetry）：优先尊重服务端
			// Retry-After，否则指数退避；有剩余尝试才等待
			if d := retryDelay(err, i); d > 0 && i+1 < pool.Size() && i+1 < maxKeyAttempts {
				time.Sleep(d)
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return &LLMError{Msg: "API key 池已耗尽：所有密钥均不可用"}
}

// failureKind 错误分类："auth"（401/403，换 key 有意义）/
// "cooldown"（402/429/529，冷却或换 key 后重试）/ ""（其它——网络/流内/解析失败，
// 换 key 无意义直接上抛）。流内错误对象没有 HTTP Status，天然落进 ""。
func failureKind(err error) string {
	var le *LLMError
	if !errors.As(err, &le) {
		return ""
	}
	switch le.Status {
	case 401, 403:
		return "auth"
	case 402, 429, 529:
		// 529 服务端过载（Anthropic overloaded_error）：归入冷却换 key 重试
		return "cooldown"
	}
	return ""
}
