package weblinks

// 白盒同包测试：stdlib testing only，httptest 起本地服务器，无真实网络、无 sleep。
// 隔离模式对齐 internal/agent：config.SetDir(t.TempDir()) + t.Cleanup 复位。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"localai/internal/config"
)

// setup 测试隔离：配置目录指到临时目录，结束时恢复默认解析。
func setup(t *testing.T) {
	t.Helper()
	config.SetDir(t.TempDir())
	t.Cleanup(func() { config.SetDir("") })
}

// fakeTimeout 实现 net.Error 且 Timeout()=true，用于白盒验证超时文案折算。
type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "deadline exceeded" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return false }

// ---------------- 链接提取 ----------------

func TestExtractURLs(t *testing.T) {
	// 空白截断、右括号截断、去重保序、≤3 上限
	in := "开头 https://a.com/1.png 然后 (https://b.com/2?q=1) 加重复 https://a.com/1.png " +
		"再 http://c.com/3 第四 https://d.com/4 第五 https://e.com/5 结尾"
	got := extractURLs(in)
	want := []string{"https://a.com/1.png", "https://b.com/2?q=1", "http://c.com/3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("提取结果不符: got %v want %v", got, want)
	}
	if r := extractURLs("没有链接的一段话，http 也不是链接"); len(r) != 0 {
		t.Fatalf("无链接应返回空, got %v", r)
	}
}

// ---------------- 图片下载落盘 ----------------

func TestProcessImage(t *testing.T) {
	setup(t)
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13} // PNG 魔数开头的假字节
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	u := srv.URL + "/pic.png?x=1"
	res := Process("看这张图 " + u)
	if len(res.Images) != 1 {
		t.Fatalf("应下载 1 张图片, got %+v", res)
	}
	if len(res.Inline) != 0 || len(res.Notes) != 0 {
		t.Fatalf("图片链接不应产生 Inline/Notes: %+v", res)
	}
	// 文件名 = url sha256 前 16 位 + 按扩展名
	sum := sha256.Sum256([]byte(u))
	wantName := hex.EncodeToString(sum[:])[:16] + ".png"
	if filepath.Base(res.Images[0]) != wantName {
		t.Fatalf("文件名不符: got %s want %s", filepath.Base(res.Images[0]), wantName)
	}
	if !strings.HasPrefix(res.Images[0], filepath.Join(config.Dir(), "media")) {
		t.Fatalf("应落在 media 目录下: %s", res.Images[0])
	}
	data, err := os.ReadFile(res.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(png) {
		t.Fatalf("落盘内容不符: got %d 字节 want %d 字节", len(data), len(png))
	}
}

// ---------------- 网页剥标签 ----------------

func TestProcessHTML(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>页面标题</title>`+
			`<style>body { color: red; }</style>`+
			`<script>alert("不该出现")</script></head>`+
			`<body><h1>Hello&nbsp;World</h1><p>第一段正文</p>`+
			`<noscript>无脚本回退</noscript></body></html>`)
	}))
	defer srv.Close()

	res := Process("帮我读 " + srv.URL)
	if len(res.Inline) != 1 {
		t.Fatalf("应有 1 个网页文本块, got %+v", res)
	}
	block := res.Inline[0]
	if !strings.HasPrefix(block, "> 来自 "+srv.URL+" 的网页内容：\n") {
		t.Fatalf("缺少来源标注行: %q", block)
	}
	for _, bad := range []string{"页面标题", "color: red", "不该出现", "alert", "无脚本回退", "<"} {
		if strings.Contains(block, bad) {
			t.Errorf("正文不应包含 %q: %s", bad, block)
		}
	}
	for _, good := range []string{"Hello World", "第一段正文"} {
		if !strings.Contains(block, good) {
			t.Errorf("正文应包含 %q: %s", good, block)
		}
	}
}

func TestProcessHTMLTruncate(t *testing.T) {
	setup(t)
	long := strings.Repeat("甲", 7000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body><p>%s</p></body></html>", long)
	}))
	defer srv.Close()

	res := Process(srv.URL)
	if len(res.Inline) != 1 {
		t.Fatalf("应有 1 个文本块, got %+v", res)
	}
	body := res.Inline[0][strings.IndexByte(res.Inline[0], '\n')+1:]
	if n := utf8.RuneCountInString(body); n != 6000 {
		t.Fatalf("正文应截到 6000 字, got %d", n)
	}
}

// ---------------- 非图片文件保存 ----------------

func TestProcessFile(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello, plain text")
	}))
	defer srv.Close()

	u := srv.URL + "/notes.txt"
	res := Process("取这个文件 " + u)
	if len(res.Notes) != 1 {
		t.Fatalf("应有 1 行说明, got %+v", res)
	}
	if len(res.Inline) != 0 || len(res.Images) != 0 {
		t.Fatalf("普通文件不应产生 Inline/Images: %+v", res)
	}
	note := res.Notes[0]
	if !strings.HasPrefix(note, "> ") {
		t.Fatalf("说明行应以 \"> \" 开头: %q", note)
	}
	if !strings.Contains(note, "text/plain") {
		t.Errorf("说明行应含类型: %q", note)
	}
	// 预算落盘路径并验证内容（与图片同一套 sha256 命名规则）
	sum := sha256.Sum256([]byte(u))
	want := filepath.Join(config.Dir(), "media", hex.EncodeToString(sum[:])[:16]+".txt")
	if !strings.Contains(note, want) {
		t.Errorf("说明行应含保存路径 %s: %q", want, note)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello, plain text" {
		t.Fatalf("落盘内容不符: %q", string(data))
	}
}

// ---------------- 失败注释行 ----------------

func TestProcessFailure(t *testing.T) {
	setup(t)
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	srv500URL := srv500.URL
	defer srv500.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 关掉再用其地址 → 立即连接失败

	res := Process("两个都失败 " + srv500URL + " " + deadURL + "/x")
	if len(res.Notes) != 2 {
		t.Fatalf("失败链接应有 2 行注释, got %+v", res)
	}
	if len(res.Inline) != 0 || len(res.Images) != 0 {
		t.Fatalf("失败链接不应有其它产物: %+v", res)
	}
	if !strings.Contains(res.Notes[0], "链接取材失败") || !strings.Contains(res.Notes[0], "500") {
		t.Errorf("非 200 应折算成失败注释行: %q", res.Notes[0])
	}
	if !strings.Contains(res.Notes[1], "链接取材失败") {
		t.Errorf("连接失败应折算成失败注释行: %q", res.Notes[1])
	}
}

func TestErrReasonTimeout(t *testing.T) {
	if got := errReason(fakeTimeout{}); got != "超时" {
		t.Fatalf("超时错误应折算为「超时」, got %q", got)
	}
}

// ---------------- 并发与顺序 ----------------

func TestProcessOrderAndCap(t *testing.T) {
	setup(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<html><body>内容%s</body></html>", r.URL.Path)
	}))
	defer srv.Close()

	text := srv.URL + "/a " + srv.URL + "/b " + srv.URL + "/c " + srv.URL + "/d " + srv.URL + "/a"
	res := Process(text)
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Fatalf("超过 3 个链接应只抓前 3 个, 实抓 %d", n)
	}
	if len(res.Inline) != 3 {
		t.Fatalf("应有 3 个文本块, got %+v", res)
	}
	// 结果按 URL 出现顺序：a、b、c
	for i, p := range []string{"/a", "/b", "/c"} {
		if !strings.Contains(res.Inline[i], "内容"+p) {
			t.Errorf("第 %d 块应来自 %s: %q", i+1, p, res.Inline[i])
		}
	}
}

// ---------------- 无链接 ----------------

func TestProcessNoLinks(t *testing.T) {
	setup(t)
	res := Process("今天天气不错，适合写代码。ftp://example.com/file.zip 也不是 http(s) 链接。")
	if len(res.Inline)+len(res.Images)+len(res.Notes) != 0 {
		t.Fatalf("无链接应全空, got %+v", res)
	}
}
