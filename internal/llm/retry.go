// 重试辅助（对齐 openclaude withRetry.ts 的退避与报文反解）：
//   - retryDelay：优先尊重服务端 Retry-After 头；否则指数退避
//     （500ms 起步、单次封顶 8s，±25% 抖动），避免多 key 间瞬时重连放大故障；
//   - adaptiveMaxTokens：402「can only afford M」与 400「input length +
//     max_tokens > context limit」两类报文的确定性反解，调低 max_tokens
//     原地重试一次——把含糊的服务端报错变成可自动恢复的修正。
package llm

import (
	"errors"
	"math/rand"
	"regexp"
	"strconv"
	"time"
)

// errStreamIdle 流式空闲看门狗触发的中断（内部哨兵，不出包）。
var errStreamIdle = errors.New("llm: stream idle timeout")

// retryBackoffBase/cap 指数退避基值与单次封顶。
// 封顶取 8s（openclaude 为 32s）：本实现退避期间无法响应协作停止，
// 压低单次等待上限以限制最坏阻塞。
const (
	retryBackoffBase = 500 * time.Millisecond
	retryBackoffCap  = 8 * time.Second
	retryAfterCap    = 30 * time.Second
)

// retryDelay 计算换 key 前的等待时长；无需等待返回 0。
// 优先 Retry-After 头（服务端权威）；429/529 用指数退避；402 不等待。
func retryDelay(err error, attempt int) time.Duration {
	var le *LLMError
	if !errors.As(err, &le) {
		return 0
	}
	if le.RetryAfter > 0 {
		if le.RetryAfter > retryAfterCap {
			return retryAfterCap
		}
		return le.RetryAfter
	}
	switch le.Status {
	case 429, 529:
		return jitterBackoff(attempt)
	}
	return 0
}

// jitterBackoff 指数退避 + 抖动：min(500ms×2^attempt, 8s) × [0.75, 1.0)。
func jitterBackoff(attempt int) time.Duration {
	d := retryBackoffBase << attempt
	if d > retryBackoffCap || d <= 0 {
		d = retryBackoffCap
	}
	return d - time.Duration(rand.Int63n(int64(d)/4))
}

var (
	// 402 报文：「requested up to N ... can only afford M」（OpenRouter 风格）
	reAffordable = regexp.MustCompile(`(?is)requested up to (\d+)[^>]*?can only afford (\d+)`)
	// 400 报文：「input length and max_tokens exceed context limit: X + Y > Z」
	reCtxOverflow = regexp.MustCompile(`(?is)input length and max_tokens exceed context limit:\s*(\d+)\s*\+\s*(\d+)\s*>\s*(\d+)`)
)

// adaptiveMaxTokens 从 402/400 错误报文反解可行的 max_tokens；不匹配返回 0。
func adaptiveMaxTokens(le *LLMError) int {
	if le == nil || le.Body == "" || (le.Status != 402 && le.Status != 400) {
		return 0
	}
	if m := reAffordable.FindStringSubmatch(le.Body); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil && n > 0 {
			return n
		}
	}
	if m := reCtxOverflow.FindStringSubmatch(le.Body); m != nil {
		input, e1 := strconv.Atoi(m[1])
		limit, e3 := strconv.Atoi(m[3])
		if e1 == nil && e3 == nil {
			// 反解上下文上限，扣 1000 安全边距；下限 3000（openclaude 同值）
			mt := limit - input - 1000
			if mt < 3000 {
				mt = 3000
			}
			return mt
		}
	}
	return 0
}
