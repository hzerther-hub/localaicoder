// web_search 执行器：多引擎自动回退（Bing → DuckDuckGo → 百度），零第三方依赖。
package tools

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func stripTags(s string) string {
	tagRe := regexp.MustCompile(`<[^>]+>`)
	s = tagRe.ReplaceAllString(s, "")
	return unescapeHTML(strings.TrimSpace(s))
}

// unescapeHTML 还原常见 HTML 实体（标准库 html.UnescapeString 的轻量替代）。
func unescapeHTML(s string) string {
	repl := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&#x27;", "'", "&#x2F;", "/", "&nbsp;", " ",
	)
	return repl.Replace(s)
}

func fetchPage(u string) string {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
		"(KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ""
	}
	return string(b)
}

type searchResult struct{ title, url, snippet string }

var (
	bingBlockRe = regexp.MustCompile(`(?s)<li class="b_algo".*?</li>`)
	bingHeadRe  = regexp.MustCompile(`(?s)<h2[^>]*><a[^>]+href="([^"]+)"[^>]*>(.*?)</a></h2>`)
	bingSnipRe  = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)

	baiduBlockRe = regexp.MustCompile(`(?s)<div class="result[^"]*"[^>]*>.*?</div>\s*</div>`)
	baiduHeadRe  = regexp.MustCompile(`(?s)<h3[^>]*>\s*<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	baiduSnipRe  = regexp.MustCompile(`(?s)class="c-abstract[^"]*"[^>]*>(.*?)(?:</span>|</div>)`)

	ddgLinkRe   = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnipRe   = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
	ddgLkLinkRe = regexp.MustCompile(`(?s)<a[^>]*class='result-link'[^>]*href="([^" )]+)"[^>]*>(.*?)</a>`)
	ddgLkSnipRe = regexp.MustCompile(`(?s)class='result-snippet'[^>]*>(.*?)</td>`)
	uddgRe      = regexp.MustCompile(`[?&]uddg=([^&]+)`)
)

func parseBing(page string) []searchResult {
	var results []searchResult
	for _, block := range bingBlockRe.FindAllString(page, -1) {
		m := bingHeadRe.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		u, title := m[1], stripTags(m[2])
		if !strings.HasPrefix(u, "http") {
			continue
		}
		snip := ""
		if sm := bingSnipRe.FindStringSubmatch(block); sm != nil {
			snip = stripTags(sm[1])
		}
		results = append(results, searchResult{title, u, snip})
	}
	return results
}

func parseBaidu(page string) []searchResult {
	var results []searchResult
	for _, block := range baiduBlockRe.FindAllString(page, -1) {
		m := baiduHeadRe.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		u, title := m[1], stripTags(m[2])
		snip := ""
		if sm := baiduSnipRe.FindStringSubmatch(block); sm != nil {
			snip = stripTags(sm[1])
		}
		results = append(results, searchResult{title, u, snip})
		if len(results) >= 10 {
			break
		}
	}
	return results
}

func parseDuckDuckGo(page string) []searchResult {
	var results []searchResult
	blocks := ddgLinkRe.FindAllStringSubmatch(page, -1)
	snips := ddgSnipRe.FindAllString(page, -1)
	for i, m := range blocks {
		href, title := m[1], stripTags(m[2])
		u := href
		if um := uddgRe.FindStringSubmatch(href); um != nil {
			if dec, err := url.QueryUnescape(um[1]); err == nil {
				u = dec
			}
		}
		snip := ""
		if i < len(snips) {
			snip = stripTags(snips[i])
		}
		results = append(results, searchResult{title, u, snip})
	}
	if len(results) > 0 {
		return results
	}
	// lite 版备用布局
	blocks = ddgLkLinkRe.FindAllStringSubmatch(page, -1)
	snips = ddgLkSnipRe.FindAllString(page, -1)
	for i, m := range blocks {
		u := m[1]
		if um := uddgRe.FindStringSubmatch(u); um != nil {
			if dec, err := url.QueryUnescape(um[1]); err == nil {
				u = dec
			}
		}
		snip := ""
		if i < len(snips) {
			snip = stripTags(snips[i])
		}
		results = append(results, searchResult{stripTags(m[2]), u, snip})
	}
	return results
}

func execWebSearch(args map[string]any) string {
	query := strings.TrimSpace(strOf(args["query"]))
	if query == "" {
		return "错误：query 不能为空"
	}
	maxResults := args["max_results"]
	n := 8
	if v, ok := maxResults.(float64); ok && int(v) > 0 {
		n = int(v)
	}
	if n > 10 {
		n = 10
	}
	q := url.QueryEscape(query)
	engines := []struct {
		name  string
		u     string
		parse func(string) []searchResult
	}{
		{"Bing", "https://www.bing.com/search?q=" + q + "&setmkt=zh-CN&count=10", parseBing},
		{"DuckDuckGo", "https://html.duckduckgo.com/html/?q=" + q, parseDuckDuckGo},
		{"百度", "https://www.baidu.com/s?wd=" + q + "&rn=10", parseBaidu},
	}
	var tried []string
	var results []searchResult
	used := ""
	for _, e := range engines {
		tried = append(tried, e.name)
		page := fetchPage(e.u)
		if page == "" {
			continue
		}
		results = e.parse(page)
		if len(results) > 0 {
			used = e.name
			break
		}
	}
	if len(results) == 0 {
		return fmt.Sprintf("搜索失败或无结果：%s\n（已尝试 %s；可稍后重试或换关键词）",
			query, strings.Join(tried, "、"))
	}
	var lines []string
	for i, r := range results {
		if i >= n {
			break
		}
		snip := r.snippet
		if len(snip) > 200 {
			snip = snip[:200]
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s\n   %s", i+1, r.title, r.url, snip))
	}
	return fmt.Sprintf("（搜索引擎：%s，共 %d 条）\n\n%s", used, min(len(results), n),
		strings.Join(lines, "\n\n"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
