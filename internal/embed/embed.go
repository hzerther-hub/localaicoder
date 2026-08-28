// Package embed OpenAI-compatible embeddings 客户端（企业知识库的可选增强）。
// 对译 Python embed.py：POST {base_url}/embeddings，任何失败返回 nil，
// codera 自动退回纯 TF-IDF，不影响主流程。
package embed

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"localai/internal/config"
)

const (
	defaultTimeout = 30 * time.Second
	maxText        = 6000 // 单条文本截断字符数（防超长 token 上限）
)

// Embed 把 texts 编码为向量；任何失败（网络/端点/格式）返回 nil。
func Embed(modelCfg config.ModelConfig, texts []string) [][]float64 {
	if len(texts) == 0 {
		return [][]float64{}
	}
	base := modelCfg.BaseURL
	if base == "" {
		return nil
	}
	input := make([]string, len(texts))
	for i, t := range texts {
		if len(t) > maxText {
			t = t[:maxText]
		}
		input[i] = t
	}
	payload, _ := json.Marshal(map[string]any{
		"model": modelCfg.ModelID,
		"input": input,
	})
	req, err := http.NewRequest("POST", trimRightSlash(base)+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	if modelCfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+modelCfg.APIKey)
	}
	client := &http.Client{
		Timeout: defaultTimeout,
		Transport: &http.Transport{Proxy: nil},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil
	}
	var data struct {
		Data []struct {
			Index     int     `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil {
		return nil
	}
	items := data.Data
	sort.SliceStable(items, func(a, b int) bool { return items[a].Index < items[b].Index })
	if len(items) != len(texts) {
		return nil
	}
	out := make([][]float64, len(items))
	for i, it := range items {
		out[i] = it.Embedding
	}
	return out
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
