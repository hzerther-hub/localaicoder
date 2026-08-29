// Package weblinks 消息链接自动取材（对译 Python 版 weblinks.py，见 docs/DESIGN.md §15）。
//
// 扫描用户消息里的 http(s) 链接（按出现顺序最多 3 个），并发抓取后按类型归类：
//   - 图片（image/*）→ 下载到配置目录 media/，返回本地路径作视觉附件；
//   - 网页（text/html）→ 剥除 script/style/noscript 取正文，截 6000 字内联进消息；
//   - 其它类型（pdf/zip 等）→ 下载存 media/，返回路径 + 一行说明，交模型用工具处理；
//   - 任何失败（超时/非 200/解不开）→ 一条中文注释行，绝不向上抛错中断发送。
//
// 纯 Go 标准库实现（net/http + regexp 手写剥标签），不引入第三方依赖。
// 调用方是 internal/agent：Images 作视觉附件、Inline 并入消息文本、Notes 原样附在消息里。
package weblinks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
)

// ---------------- 常量与包级单例 ----------------

const (
	maxLinks     = 3               // 最多处理的链接数（按出现顺序取前 3 个）
	fetchTimeout = 8 * time.Second // 单请求超时
	maxBodyBytes = 5 << 20         // 响应体读取上限 5MB
	maxTextRunes = 6000            // 网页正文最大字符数（按 rune 计）
	userAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

var (
	httpClient = &http.Client{Timeout: fetchTimeout}
	urlRe      = regexp.MustCompile(`https?://[^\s)]+`)
)

// ---------------- 链接提取 ----------------

// extractURLs 抽取文本中的 http(s) 链接：每个 URL 截到首个空白或右括号为止，
// 去重保序，最多返回 maxLinks 个。
func extractURLs(text string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, u := range urlRe.FindAllString(text, -1) {
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if len(out) >= maxLinks {
			break
		}
	}
	return out
}

// ---------------- 抓取与分类 ----------------

// fetchResult 单个链接的取材产物（三选一：inline / image / note）。
type fetchResult struct {
	inline string // 网页正文文本块
	image  string // 图片本地路径
	note   string // 非图片文件说明行 / 失败注释行
}

// fetchOne 抓取单个链接并按 Content-Type 分类处理；任何失败都折算成
// note 注释行，不返回 error，不中断其它链接。
func fetchOne(rawURL string) fetchResult {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchResult{note: failNote(rawURL, "无效链接")}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fetchResult{note: failNote(rawURL, errReason(err))}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fetchResult{note: failNote(rawURL, fmt.Sprintf("HTTP %d", resp.StatusCode))}
	}
	mime := mimeType(resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fetchResult{note: failNote(rawURL, "下载失败")}
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
		p, err := saveMedia(rawURL, body, imageExt(mime, rawURL))
		if err != nil {
			return fetchResult{note: failNote(rawURL, "保存失败")}
		}
		return fetchResult{image: p}
	case strings.HasPrefix(mime, "text/html"):
		text := truncateRunes(htmlToText(string(body)), maxTextRunes)
		return fetchResult{inline: "> 来自 " + rawURL + " 的网页内容：\n" + text}
	default:
		p, err := saveMedia(rawURL, body, fileExt(mime, rawURL))
		if err != nil {
			return fetchResult{note: failNote(rawURL, "保存失败")}
		}
		return fetchResult{note: fmt.Sprintf("> 链接文件已保存：%s（%s，%s）", p, mimeLabel(mime), fmtSize(len(body)))}
	}
}

// failNote 生成失败注释行（"> " 前缀，随消息发出，不中断发送流程）。
func failNote(rawURL, reason string) string {
	return fmt.Sprintf("> （链接取材失败：%s ：%s）", rawURL, reason)
}

// errReason 把网络错误折算成简短原因；超时统一叫「超时」，其余透传错误文本。
func errReason(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "超时"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	return err.Error()
}

// mimeType 从 Content-Type 头取 mime 主值（去参数、转小写；缺失按通用二进制处理）。
func mimeType(ct string) string {
	m := strings.TrimSpace(ct)
	if i := strings.Index(m, ";"); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	if m == "" {
		m = "application/octet-stream"
	}
	return strings.ToLower(m)
}

// mimeSub 取 mime 的子类型部分（"/" 之后）。
func mimeSub(mime string) string {
	if i := strings.Index(mime, "/"); i >= 0 {
		return mime[i+1:]
	}
	return mime
}

// urlPath 取 URL 的路径部分；解析失败返回空串。
func urlPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Path
}

// imageExt 决定图片扩展名：优先 Content-Type 子类型，退回 URL 路径扩展名。
func imageExt(mime, rawURL string) string {
	switch mimeSub(mime) {
	case "png":
		return ".png"
	case "jpeg", "jpg":
		return ".jpg"
	case "gif":
		return ".gif"
	case "webp":
		return ".webp"
	case "bmp":
		return ".bmp"
	case "svg+xml":
		return ".svg"
	}
	if ext := strings.ToLower(path.Ext(urlPath(rawURL))); len(ext) > 1 && len(ext) <= 8 {
		return ext
	}
	return ".img"
}

// fileExt 决定普通文件扩展名：优先 URL 路径扩展名，退回 mime 对照表。
func fileExt(mime, rawURL string) string {
	if ext := strings.ToLower(path.Ext(urlPath(rawURL))); len(ext) > 1 && len(ext) <= 8 {
		return ext
	}
	switch mimeSub(mime) {
	case "pdf":
		return ".pdf"
	case "zip":
		return ".zip"
	case "json":
		return ".json"
	case "xml":
		return ".xml"
	case "plain":
		return ".txt"
	case "html":
		return ".html"
	}
	return ".bin"
}

// mimeLabel 面向说明行的类型标签（无有效类型信息时给「未知类型」）。
func mimeLabel(mime string) string {
	if mime == "" || mime == "application/octet-stream" {
		return "未知类型"
	}
	return mime
}

// fmtSize 人类可读的文件大小。
func fmtSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ---------------- HTML 转文本 ----------------

var (
	scriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	styleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	noscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	bodyRe     = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body>`)
	bodyOpenRe = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)$`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

// htmlToText 网页 HTML 转纯文本：剥除 script/style/noscript、只取 body
// （防 head 标题混入，对齐 Python 版 _html_to_text）、剥剩余标签、实体解码、
// 空白收敛。手写 regexp 实现，不要求完备，够取材用即可。
func htmlToText(raw string) string {
	s := scriptRe.ReplaceAllString(raw, " ")
	s = styleRe.ReplaceAllString(s, " ")
	s = noscriptRe.ReplaceAllString(s, " ")
	if m := bodyRe.FindStringSubmatch(s); m != nil {
		s = m[1]
	} else if m := bodyOpenRe.FindStringSubmatch(s); m != nil {
		s = m[1] // 只有 <body> 开标签的残缺页面
	}
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ") // &nbsp; 解码出的不换行空格折算成普通空格
	s = spaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// truncateRunes 按字符（rune）截断到 n 个以内。
func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// ---------------- 落盘 ----------------

// saveMedia 把内容写到配置目录 media/ 下，文件名 = url 的 sha256 前 16 位 + 扩展名。
func saveMedia(rawURL string, data []byte, ext string) (string, error) {
	dir := filepath.Join(config.Dir(), "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	p := filepath.Join(dir, hex.EncodeToString(sum[:])[:16]+ext)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// ---------------- 对外入口 ----------------

// Result 一次链接取材的结果。
type Result struct {
	Inline []string // 网页正文文本块（已含来源标注行，如 "> 来自 https://... 的网页内容："）
	Images []string // 已下载的图片本地路径（作视觉附件）
	Notes  []string // 非图片文件说明行 / 失败注释行（每行以 "> " 前缀）
}

// Process 抽取 text 中的 http(s) 链接（≤3 个，按出现顺序）并并发抓取取材；
// 结果按链接出现顺序归入 Inline / Images / Notes。永不返回 error，
// 单个链接的任何失败都折算成一条注释行，绝不中断调用方的发送流程。
func Process(text string) Result {
	urls := extractURLs(text)
	if len(urls) == 0 {
		return Result{}
	}
	results := make([]fetchResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = fetchOne(u) // 各 goroutine 只写自己的下标，无需加锁
		}()
	}
	wg.Wait()
	var res Result
	for _, r := range results {
		switch {
		case r.inline != "":
			res.Inline = append(res.Inline, r.inline)
		case r.image != "":
			res.Images = append(res.Images, r.image)
		case r.note != "":
			res.Notes = append(res.Notes, r.note)
		}
	}
	return res
}
