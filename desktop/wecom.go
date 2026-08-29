package main

// desktop/wecom.go 企业微信群机器人推送（单向快赢渠道）：
// 任务结束时把结果摘要推到群里。配置一个 webhook URL 即用，纯 stdlib。
// webhook 存 models.json 顶层 "wecom_webhook"（config.Get/SetWeComWebhook）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"localai/internal/config"
)

// wecomPushTimeout 推送超时（尽力而为，不阻塞主流程）。
const wecomPushTimeout = 5 * time.Second

// PushWecom 推送 markdown 消息到企业微信群；未配置 webhook 时静默跳过。
// 失败只返回错误不外抛——推送是锦上添花，绝不影响聊天主流程。
func PushWecom(text string) error {
	webhook := config.GetWeComWebhook()
	if webhook == "" {
		return nil
	}
	// 企业微信 markdown 消息上限 4096 字节，超长截断
	if len(text) > 3600 {
		text = text[:3600] + "\n…（已截断）"
	}
	body, _ := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": text},
	})
	client := &http.Client{Timeout: wecomPushTimeout}
	resp, err := client.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("企业微信推送 HTTP %d", resp.StatusCode)
	}
	return nil
}

// SaveWeComWebhook 保存 webhook 地址（桌面绑定）。
func (a *App) SaveWeComWebhook(url string) bool {
	config.SetWeComWebhook(strings.TrimSpace(url))
	return true
}

// GetWeComWebhook 读取已配置的 webhook（桌面绑定，面板回显用）。
func (a *App) GetWeComWebhook() string { return config.GetWeComWebhook() }

// TestWeComWebhook 发送一条测试消息验证 webhook（桌面绑定）。
func (a *App) TestWeComWebhook() map[string]any {
	if config.GetWeComWebhook() == "" {
		return map[string]any{"ok": false, "msg": "未配置 webhook 地址"}
	}
	if err := PushWecom("**Local AI Studio** 渠道测试：连接成功 ✅"); err != nil {
		return map[string]any{"ok": false, "msg": err.Error()}
	}
	return map[string]any{"ok": true, "msg": "测试消息已发送，请在群里查看"}
}
