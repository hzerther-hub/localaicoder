package main

// relay_probe_live_test.go：以"手机端"身份直连中继，验证桌面端是否在线并应答 state。
// 手动诊断用：go test -run TestWsProbeLive -tags desktop,production . -v

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"localai/internal/config"
	"localai/internal/msg"
)

func TestWsProbeLive(t *testing.T) {
	cfg := config.GetRelayConfig()
	server := msg.S(cfg, "server_url")
	token := msg.S(cfg, "device_token")
	if server == "" || token == "" {
		t.Skip("未配置中继")
	}
	server = strings.Replace(server, "wss://", "https://", 1)
	wsURL := strings.Replace(server, "https://", "wss://", 1) + "/s/ws?d=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("手机端拨号失败（中继不可达或桌面未在线）: %v", err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"type": "state", "rid": float64(1)})
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("等待 state 应答超时/断开: %v", err)
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if mt, _ := m["type"].(string); mt == "state" || mt == "hello" {
			b, _ := json.Marshal(m)
			if len(b) > 400 {
				b = b[:400]
			}
			t.Logf("收到 %s: %s", mt, b)
			if mt == "state" {
				return // 桌面在线且应答正常
			}
		}
	}
}
