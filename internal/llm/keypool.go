package llm

import (
	"strings"
	"sync"
	"time"
)

// 凭据池（对齐 openclaude credentialPool.ts 语义）：
//   - Next 轮询取可用 key，跳过禁用与冷却中的；
//   - auth 类失败（401/403）永久禁用，cooldown（402/429）冷却 30s；
//   - 全部冷却时返回最近失败最久的 key——部分可用优于完全拒绝；
//   - generation 计数防陈旧 lease：冷却报告来自过期 lease 时忽略，
//     但 auth 失败无论代数一律驱逐。
const keyCooldown = 30 * time.Second

type credState struct {
	value         string
	disabled      bool
	cooldownUntil time.Time
	lastFailure   time.Time
	generation    int
}

// CredentialPool 一个 provider 的 key 池；并发安全。
type CredentialPool struct {
	mu     sync.Mutex
	sig    string // key 集合指纹；配置变更时整个池重建
	creds  []*credState
	cursor int
}

var (
	poolsMu sync.Mutex
	pools   = map[string]*CredentialPool{}
)

// GetPool 取（或建）provider 的凭据池；keys 为空时放一个空串 key
// （本地无鉴权端点），保证单次尝试语义与旧版一致。
// 配置里的 key 集合变了（用户改了 models.json）就重建池。
func GetPool(providerKey string, keys []string) *CredentialPool {
	if len(keys) == 0 {
		keys = []string{""}
	}
	sig := strings.Join(keys, "\x00")
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[providerKey]; ok && p.signature() == sig {
		return p
	}
	p := &CredentialPool{sig: sig}
	for _, k := range keys {
		p.creds = append(p.creds, &credState{value: k})
	}
	pools[providerKey] = p
	return p
}

func (p *CredentialPool) signature() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sig
}

// Size 池内 key 数。
func (p *CredentialPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.creds)
}

// Next 轮询取下一个可用 key，返回 (key, leaseGeneration)。
// 全部禁用返回 ("", 0)（调用方按失败处理）；全部冷却返回冷却最早结束的。
func (p *CredentialPool) Next() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.creds)
	if n == 0 {
		return "", 0
	}
	now := time.Now()
	var fallback *credState
	for i := 0; i < n; i++ {
		c := p.creds[(p.cursor+i)%n]
		if c.disabled {
			continue
		}
		if now.Before(c.cooldownUntil) {
			if fallback == nil || c.lastFailure.Before(fallback.lastFailure) {
				fallback = c
			}
			continue
		}
		p.cursor = (p.cursor + i + 1) % n
		c.generation++
		return c.value, c.generation
	}
	if fallback != nil {
		fallback.generation++
		return fallback.value, fallback.generation
	}
	return "", 0
}

// ReportSuccess key 请求成功：清冷却与失败记录。
func (p *CredentialPool) ReportSuccess(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.find(key); c != nil {
		c.cooldownUntil = time.Time{}
		c.lastFailure = time.Time{}
	}
}

// ReportFailure 上报 lease 的失败：auth 永久禁用；cooldown 冷却 30s
// （仅当 generation 仍是当前 lease，防旧 lease 的迟到报告误冷却新 lease）。
func (p *CredentialPool) ReportFailure(key, kind string, generation int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.find(key)
	if c == nil {
		return
	}
	switch kind {
	case "auth":
		c.disabled = true
	case "cooldown":
		if generation == c.generation {
			c.cooldownUntil = time.Now().Add(keyCooldown)
			c.lastFailure = time.Now()
		}
	}
}

func (p *CredentialPool) find(key string) *credState {
	for _, c := range p.creds {
		if c.value == key {
			return c
		}
	}
	return nil
}
