package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"localai/internal/msg"
)

// FetchModelIDs 探测 OpenAI 兼容端点的 GET /models（添加模型时自动填充 ID）。
// 失败返回空切片（界面提示手动输入）。
func FetchModelIDs(baseURL, apiKey string) []string {
	url := ""
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	url = baseURL + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil {
		return nil
	}
	out := make([]string, 0, len(data.Data))
	for _, m := range data.Data {
		if id := msg.S(map[string]any{"x": m.ID}, "x"); id != "" {
			out = append(out, id)
		}
	}
	return out
}
