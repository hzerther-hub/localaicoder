// Package routing 简单/复杂轮次智能路由（对齐 openclaude smartModelRouting
// 的设计：纯启发式分类 + 薄外壳；分类每次用户提问做一次，本轮内钉住）。
//
// 本地扩展：启发式拿不准（unsure 弱信号区）时，可选让本地大脑
// （dispatch_model）仲裁一次，仲裁结果按文本哈希缓存。
package routing

import (
	"context"
	"crypto/sha1"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"localai/internal/config"
	"localai/internal/llm"
	"localai/internal/msg"
)

// Decision 一轮的分类结果。
type Decision string

const (
	DecisionSimple = "simple" // 走轻量模型
	DecisionStrong = "strong" // 走强力模型
	DecisionUnsure = "unsure" // 弱信号区，需仲裁（无仲裁时归 simple）
)

// Input 分类输入（纯数据；不读任何全局状态）。
type Input struct {
	UserText          string // 用户本轮提问文本
	HasNonTextContent bool   // 本轮带非文本附件（图片/文档等）
	TurnNumber        int    // 会话内第几轮（从 1 计）
}

// Config 路由配置（从 config.GetSmartRouting() 取）。
type Config struct {
	Enabled        bool
	SimpleModel    string
	StrongModel    string
	SimpleMaxChars int
	SimpleMaxWords int
	Arbitrate      bool
}

// strongKeywords 强信号关键词：英文用词边界正则，中文直接包含匹配。
var (
	strongEn = regexp.MustCompile(`(?i)\b(plan|planning|design|architect|architecture|refactor|debug|investigate|analyze|analyse|analysis|optimize|optimi[sz]e|implement|security|performance|benchmark|migrate|migration|concurrency|deadlock|race|memory leak|root cause|troubleshoot|evaluate|audit|threat model|why does|why is|why do|how should|how do|how to|step by step)\b`)
	strongZh = []string{
		"规划", "设计", "架构", "重构", "调试", "排查", "分析", "根因", "为什么",
		"怎么会", "如何实现", "怎么实现", "怎么解决", "如何解决", "优化", "性能",
		"安全", "漏洞", "内存泄漏", "死锁", "并发问题", "迁移", "评估", "审计",
		"调研", "对比方案", "深入", "仔细考虑", "逐步", "步骤", "实现一个", "完整实现",
	}
)

// Classify 纯函数启发式分类；判定序对齐 openclaude routeModel，
// 末尾追加弱信号区（unsure）判定。任何强信号命中 → strong。
func Classify(in Input, cfg Config) Decision {
	text := strings.TrimSpace(in.UserText)
	// 1) 非文本内容（图片/文档附件）：一定走强模型
	if in.HasNonTextContent {
		return DecisionStrong
	}
	// 2) 空文本：视为工具链延续（便宜侧默认）
	if text == "" {
		return DecisionSimple
	}
	maxChars := cfg.SimpleMaxChars
	if maxChars <= 0 {
		maxChars = config.SmartRouteDefaultMaxChars
	}
	maxWords := cfg.SimpleMaxWords
	if maxWords <= 0 {
		maxWords = config.SmartRouteDefaultMaxWords
	}
	// 3) 代码块：任务型（行内单反引号易在中文里误判，不再单独作为代码信号）
	if strings.Contains(text, "```") {
		return DecisionStrong
	}
	// 4) 强关键词
	if strongEn.MatchString(text) {
		return DecisionStrong
	}
	for _, kw := range strongZh {
		if strings.Contains(text, kw) {
			return DecisionStrong
		}
	}
	// 5) 多段落：说明描述较复杂
	if paragraphs(text) >= 2 {
		return DecisionStrong
	}
	// 6) 长度硬阈值：英文按字符数/词数；中文无空格分词（words 恒≈1），
	// 改用「词数×2」折算汉字阈值，避免复杂中文任务被漏判为简单。
	charLen := len([]rune(text))
	words := len(strings.Fields(text))
	if containsHan(text) {
		zhLimit := maxWords * 2
		if charLen > zhLimit {
			return DecisionStrong
		}
		if charLen >= zhLimit*3/4 {
			return DecisionUnsure
		}
		return DecisionSimple
	}
	if charLen > maxChars || words > maxWords {
		return DecisionStrong
	}
	// 7) 弱信号区：接近阈值但无任何强信号 → 拿不准
	if charLen >= maxChars*3/4 || words >= maxWords*3/4 {
		return DecisionUnsure
	}
	return DecisionSimple
}

// Resolve 分类 + 可选仲裁 + key 解析，返回 (本轮模型 key, decision)。
// key 为空串 = 不路由（保持当前模型）。强模型缺失视为配置不完整，
// 整体禁用（对齐 openclaude：缺 strong 直接关）；弱模型缺失塌缩到 strong。
func Resolve(in Input, cfg Config) (string, Decision) {
	if !cfg.Enabled || cfg.StrongModel == "" {
		return "", ""
	}
	d := Classify(in, cfg)
	if d == DecisionUnsure {
		if cfg.Arbitrate {
			d = Arbitrate(in.UserText)
		} else {
			d = DecisionSimple
		}
	}
	key := cfg.SimpleModel
	if d == DecisionStrong {
		key = cfg.StrongModel
	}
	// simple 未配置 → 塌缩到 strong（宁可保守也不丢路由）
	if key == "" && d == DecisionSimple {
		key, d = cfg.StrongModel, DecisionStrong
	}
	// 目标模型不存在：simple 缺失塌缩到 strong；strong 也缺失则放弃路由
	if key != "" && config.FindModel(key) == nil {
		if key != cfg.StrongModel {
			key, d = cfg.StrongModel, DecisionStrong
		}
		if key == "" || config.FindModel(key) == nil {
			return "", ""
		}
	}
	return key, d
}

// containsHan 文本是否含 CJK 汉字（用于中英文阈值/段落分流）。
func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func paragraphs(s string) int {
	sep := "\n\n"
	if containsHan(s) {
		// 中文常以单个换行分隔段落；英文则以空行分隔
		sep = "\n"
	}
	n := 0
	for _, p := range strings.Split(s, sep) {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}

// ---------------- 本地大脑仲裁 ----------------

const (
	arbitrateTimeout = 8 * time.Second
	arbitrateMaxTok  = 512
	cacheMax         = 1024
)

const arbitratePrompt = "你是路由器。判断用户请求属于哪类：简单（一句话直接能答、或继续刚才的操作）还是复杂（需要写代码、多步骤处理或深度思考）。" +
	"只回答一个词：简单 或 复杂。"

var (
	arbMu    sync.Mutex
	arbCache = map[string]Decision{}
)

// Arbitrate 用本地大脑（config.GetDispatchModel()）仲裁；任何失败都
// 归 simple（宁可便宜，不打断对话）。结果按文本哈希缓存。
func Arbitrate(text string) Decision {
	// 缓存键纳入本地大脑 key：切换 dispatch_model 后旧结果自然失效
	sum := sha1.Sum([]byte(config.GetDispatchModel() + "\x00" + text))
	cacheKey := string(sum[:])
	arbMu.Lock()
	if d, ok := arbCache[cacheKey]; ok {
		arbMu.Unlock()
		return d
	}
	arbMu.Unlock()

	d := arbitrateCall(text)
	arbMu.Lock()
	if len(arbCache) >= cacheMax {
		arbCache = map[string]Decision{} // 简单整体重置，够用
	}
	arbCache[cacheKey] = d
	arbMu.Unlock()
	return d
}

func arbitrateCall(text string) Decision {
	m := config.FindModel(config.GetDispatchModel())
	if m == nil {
		return DecisionSimple
	}
	// 轻量请求：无工具、无推理等级、小输出上限
	light := *m
	light.ReasoningEffort = ""
	light.ContextWindow = 0
	type result struct{ text string }
	// 后台 goroutine 不随 8s 超时取消（StreamChat 不接外部 ctx）；但 ch 有缓冲
	// 不会阻塞，且 StreamChat 内部 90s 空闲看门狗兜底，goroutine 最终自然退出，非泄漏。
	ch := make(chan result, 1)
	go func() {
		var sb strings.Builder
		_ = llm.StreamChat(light, []msg.Msg{
			{"role": "system", "content": arbitratePrompt},
			{"role": "user", "content": text},
		}, nil, func(e msg.Event) error {
			if msg.S(e, "type") == "text" {
				sb.WriteString(msg.S(e, "delta"))
			}
			return nil
		})
		ch <- result{text: sb.String()}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), arbitrateTimeout)
	defer cancel()
	select {
	case r := <-ch:
		if strings.Contains(r.text, "复杂") {
			return DecisionStrong
		}
		return DecisionSimple
	case <-ctx.Done():
		return DecisionSimple
	}
}
