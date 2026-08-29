package llm

import (
	"strings"
	"testing"
	"time"

	"localai/internal/msg"
)

func TestPoolRotationRoundRobin(t *testing.T) {
	p := GetPool("p1", []string{"a", "b", "c"})
	if p.Size() != 3 {
		t.Fatalf("池大小应为 3, got %d", p.Size())
	}
	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		k, gen := p.Next()
		if k == "" || gen == 0 {
			t.Fatalf("第 %d 次取 key 不应为空: %q gen=%d", i, k, gen)
		}
		got[k] = true
		p.ReportSuccess(k)
	}
	if len(got) != 3 {
		t.Fatalf("轮询应覆盖全部 key, got %v", got)
	}
}

func TestPoolAuthEvicts(t *testing.T) {
	p := GetPool("p2", []string{"bad", "good"})
	// 第一次取到 bad（轮询起点），报 auth → 永久禁用
	k1, gen1 := p.Next()
	p.ReportFailure(k1, "auth", gen1)
	var k2 string
	for i := 0; i < 5; i++ {
		k, _ := p.Next()
		if k == k1 {
			t.Fatalf("auth 后 key %q 仍被轮询到", k1)
		}
		k2 = k
	}
	if k2 == "" {
		t.Fatal("应还有可用 key")
	}
}

func TestPoolCooldownAndFallback(t *testing.T) {
	p := GetPool("p3", []string{"x"})
	k, gen := p.Next()
	p.ReportFailure(k, "cooldown", gen)
	// 冷却中：应回退返回同一个（部分可用优于拒绝）
	k2, _ := p.Next()
	if k2 != k {
		t.Fatalf("全部冷却时应回退最近失败的 key, got %q", k2)
	}
	// 陈旧 lease 的冷却报告应被忽略：先清冷却，再用旧 generation 上报
	p.ReportSuccess(k)
	_, g2 := p.Next()
	p.Next() // generation 再前进
	p.ReportFailure(k, "cooldown", g2)
	p.mu.Lock()
	zero := p.find(k).cooldownUntil.IsZero()
	p.mu.Unlock()
	if !zero {
		t.Fatal("陈旧 lease 的冷却报告不应生效")
	}
}

func TestPoolSuccessClearsCooldown(t *testing.T) {
	p := GetPool("p4", []string{"s"})
	k, gen := p.Next()
	p.ReportFailure(k, "cooldown", gen)
	p.ReportSuccess(k)
	p.mu.Lock()
	c := p.find(k)
	cooling := time.Now().Before(c.cooldownUntil)
	p.mu.Unlock()
	if cooling {
		t.Fatal("成功后应清除冷却")
	}
}

func TestPoolRebuildOnKeyChange(t *testing.T) {
	p1 := GetPool("p5", []string{"a"})
	GetPool("p5", []string{"a", "b"}) // key 集合变化 → 重建
	p3 := GetPool("p5", []string{"a", "b"})
	if p1 == p3 {
		t.Fatal("key 集合变化应重建池")
	}
	if p3.Size() != 2 {
		t.Fatalf("新池大小应为 2, got %d", p3.Size())
	}
}

func TestPoolEmptyKeysUsesSingleEmptyKey(t *testing.T) {
	p := GetPool("p6", nil)
	if p.Size() != 1 {
		t.Fatalf("空 key 列表应放一个空串 key, got %d", p.Size())
	}
	k, _ := p.Next()
	if k != "" {
		t.Fatalf("应返回空串 key, got %q", k)
	}
}

func TestFailureKindClassification(t *testing.T) {
	cases := map[int]string{401: "auth", 403: "auth", 402: "cooldown", 429: "cooldown", 500: "", 0: ""}
	for status, want := range cases {
		got := failureKind(&LLMError{Msg: "x", Status: status})
		if got != want {
			t.Fatalf("status %d: 期望 %q, got %q", status, want, got)
		}
	}
	if failureKind(nil) != "" || failureKind(&LLMError{Msg: "no status"}) != "" {
		t.Fatal("非 HTTP 错误应分类为空")
	}
}

func TestStreamChatRotatesKeysOn401(t *testing.T) {
	// 第一个 key 401，第二个成功 → StreamChat 应自动换 key 完成本轮
	var seen []string
	srv := newKeyServer(t, &seen)
	m := testModel(srv.URL)
	m.APIKeys = []string{"bad", "good"}
	var text string
	err := StreamChat(m, nil, nil, func(e msg.Event) error {
		if msg.S(e, "type") == "text" {
			text += msg.S(e, "delta")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "Bearer bad" || seen[1] != "Bearer good" {
		t.Fatalf("应先 bad 后 good, got %v", seen)
	}
	if text != "ok" {
		t.Fatalf("换 key 后应正常输出, got %q", text)
	}
	if !strings.Contains(text, "ok") {
		t.Fatal("unreachable")
	}
}
