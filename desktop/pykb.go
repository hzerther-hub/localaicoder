package main

// pykb —— 自研的 Windows 原生语义检索服务（memsearch 替代，见 desktop/pykb/kb_service.py）。
// 脚本经 go:embed 打进 exe，运行时释放到配置目录，按需拉起为 127.0.0.1 常驻 HTTP 服务
// （模型懒加载；依赖 fastembed，由引导条「安装语义检索」一键 pip install）。
// /memsearch 在 Windows 上的调用顺序：pykb 服务 → WSL 内 memsearch（若装）→ 原生报错引导。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"localai/internal/config"
)

//go:embed pykb/kb_service.py
var pykbScriptFS embed.FS

const (
	pykbPort      = "19587"
	pykbScriptVer = "1" // 脚本内容变更时递增，触发重新释放
)

var (
	pykbMu       sync.Mutex
	pykbStarting bool
	pykbDepsOnce sync.Once
	pykbDepsOK   bool
)

func pykbBaseURL() string { return "http://127.0.0.1:" + pykbPort }

// pykbEnsureScript 释放内嵌脚本到配置目录（内容一致则复用），返回脚本路径。
func pykbEnsureScript() (string, error) {
	src, err := pykbScriptFS.ReadFile("pykb/kb_service.py")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(config.Dir(), "pykb")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "kb_service.py")
	if old, err := os.ReadFile(dst); err == nil && sha256.Sum256(old) == sha256.Sum256(src) {
		return dst, nil
	}
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// pykbPickPython 探测可用的 Python（>=3.9，取版本最高者）。
// 默认 python 可能是老版本（如 3.8），所以扫 PATH + 常见安装位置后按版本择优。
func pykbPickPython() (string, error) {
	var cands []string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			cands = append(cands, p)
		}
	}
	if apps, _ := filepath.Glob(filepath.Join(os.Getenv("LocalAppData"), "Programs", "Python", "Python*", "python.exe")); apps != nil {
		cands = append(cands, apps...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if local, _ := filepath.Glob(filepath.Join(home, "py*", "python.exe")); local != nil {
			cands = append(cands, local...)
		}
	}
	best, bestMaj, bestMin := "", 0, 0
	for _, py := range cands {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out, err := exec.CommandContext(ctx, py, "-c", "import sys;print(sys.version_info[0], sys.version_info[1])").Output()
		cancel()
		if err != nil {
			continue
		}
		var maj, min int
		if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &maj, &min); err != nil || maj < 3 || min < 9 {
			continue
		}
		if maj > bestMaj || (maj == bestMaj && min > bestMin) {
			best, bestMaj, bestMin = py, maj, min
		}
	}
	if best == "" {
		return "", fmt.Errorf("未找到 Python>=3.9：请安装 Python 后重试")
	}
	return best, nil
}

// pykbHealthy 服务健康检查（1.5s 快速超时）。
func pykbHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pykbBaseURL()+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// pykbEnsureRunning 拉起常驻服务（已在跑则直接返回）；等 /health 就绪。
func pykbEnsureRunning() error {
	pykbMu.Lock()
	if pykbHealthy() {
		pykbMu.Unlock()
		return nil
	}
	if pykbStarting {
		pykbMu.Unlock()
		return fmt.Errorf("pykb 服务正在启动")
	}
	pykbStarting = true
	pykbMu.Unlock()
	defer func() { pykbMu.Lock(); pykbStarting = false; pykbMu.Unlock() }()

	py, err := pykbPickPython()
	if err != nil {
		return err
	}
	script, err := pykbEnsureScript()
	if err != nil {
		return err
	}
	logF, err := os.OpenFile(filepath.Join(config.Dir(), "pykb", "service.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer logF.Close()
	}
	cmd := exec.Command(py, script, "serve", "--port", pykbPort)
	if logF != nil {
		cmd.Stdout, cmd.Stderr = logF, logF
	}
	// 故意不 Wait：服务常驻；应用退出后残留实例会被下一次 health 检查复用
	if err := cmd.Start(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for pykbHealthy() == false {
		if ctx.Err() != nil {
			return fmt.Errorf("pykb 服务启动超时（详见 %s）", filepath.Join(config.Dir(), "pykb", "service.log"))
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

// pykbDepsProbe 检测 fastembed 是否已装（进程内只探一次）。
func pykbDepsProbe() bool {
	pykbDepsOnce.Do(func() {
		py, err := pykbPickPython()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pykbDepsOK = exec.CommandContext(ctx, py, "-c", "import fastembed").Run() == nil
	})
	return pykbDepsOK
}

// pykbPost 调用服务 JSON 接口。
func pykbPost(path string, req any, timeout time.Duration) (map[string]any, error) {
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, pykbBaseURL()+path, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// pykbRun 把 memsearch 风格 argv 翻译成 pykb 调用，返回 memsearch 风格文本
// （/memsearch 的渲染保持不变）。调用前需 pykbEnsureRunning 成功。
func pykbRun(argv []string, ws string) map[string]any {
	sub := argv[0]
	switch sub {
	case "search":
		query, col, k := "", "localaicoder", 5
		for i := 1; i < len(argv); i++ {
			switch argv[i] {
			case "-c":
				if i+1 < len(argv) {
					col = argv[i+1]
					i++
				}
			case "-k":
				if i+1 < len(argv) {
					k, _ = strconv.Atoi(argv[i+1])
					i++
				}
			default:
				if query == "" {
					query = argv[i]
				}
			}
		}
		if query == "" {
			return map[string]any{"ok": false, "error": "缺少检索关键词"}
		}
		out, err := pykbPost("/search", map[string]any{"collection": col, "query": query, "k": k}, 10*time.Minute)
		if err != nil {
			return map[string]any{"ok": false, "error": "pykb 服务调用失败：" + err.Error()}
		}
		if ok, _ := out["ok"].(bool); !ok {
			msg, _ := out["error"].(string)
			return map[string]any{"ok": false, "error": "pykb：" + msg}
		}
		var b strings.Builder
		hits, _ := out["hits"].([]any)
		for i, h := range hits {
			m, _ := h.(map[string]any)
			head, _ := m["heading"].(string)
			src, _ := m["source"].(string)
			line, _ := m["start_line"].(float64)
			score, _ := m["score"].(float64)
			text, _ := m["text"].(string)
			if head != "" {
				fmt.Fprintf(&b, "[%d] score=%.3f  Source: %s § %s (L%d)\n%s\n\n", i+1, score, src, head, int(line), text)
			} else {
				fmt.Fprintf(&b, "[%d] score=%.3f  Source: %s (L%d)\n%s\n\n", i+1, score, src, int(line), text)
			}
			if b.Len() > 3800 {
				break
			}
		}
		if b.Len() == 0 {
			return map[string]any{"ok": true, "output": "（无命中）"}
		}
		return map[string]any{"ok": true, "output": strings.TrimRight(b.String(), "\n")}

	case "index":
		col := "localaicoder"
		var paths []string
		for i := 1; i < len(argv); i++ {
			if argv[i] == "-c" && i+1 < len(argv) {
				col = argv[i+1]
				i++
				continue
			}
			p := argv[i]
			if !filepath.IsAbs(p) && ws != "" {
				p = filepath.Join(ws, p)
			}
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			return map[string]any{"ok": false, "error": "缺少索引路径"}
		}
		out, err := pykbPost("/index", map[string]any{"collection": col, "paths": paths}, 20*time.Minute)
		if err != nil {
			return map[string]any{"ok": false, "error": "pykb 服务调用失败：" + err.Error()}
		}
		if ok, _ := out["ok"].(bool); !ok {
			msg, _ := out["error"].(string)
			return map[string]any{"ok": false, "error": "pykb：" + msg}
		}
		return map[string]any{"ok": true, "output": fmt.Sprintf(
			"索引完成：%v 文件 / %v 块（新嵌入 %v，复用 %v），耗时 %vs",
			out["files"], out["chunks"], out["embedded"], out["reused"], out["seconds"])}

	case "stats":
		out, err := pykbPost("/stats", map[string]any{}, time.Minute)
		if err != nil {
			return map[string]any{"ok": false, "error": "pykb 服务调用失败：" + err.Error()}
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		return map[string]any{"ok": true, "output": string(raw)}
	}
	return map[string]any{"ok": false, "error": "pykb 不支持子命令：" + sub}
}

// pykbInstallDeps 一键安装 Python 依赖（fastembed，含 onnxruntime），流式进度走安装事件。
func (a *App) pykbInstallDeps() (bool, string) {
	py, err := pykbPickPython()
	if err != nil {
		return false, err.Error() + "；或改用 WSL2 / 内置知识库路线"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-m", "pip", "install", "--disable-pip-version-check", "fastembed")
	ok, tail := a.memsearchStreamRun(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return false, "pip 安装超时（15 分钟），请检查网络后重试"
	}
	if !ok {
		return false, tail
	}
	pykbDepsOnce = sync.Once{} // 允许重新探测
	if err := pykbEnsureRunning(); err != nil {
		return false, tail + "\n依赖已装，但服务启动失败：" + err.Error()
	}
	return true, tail
}

// pykbAutoStart 应用启动后的后台拉起（原生 memsearch 已装时无需它；静默失败不打扰）。
func pykbAutoStart() {
	if goruntime.GOOS != "windows" {
		return
	}
	if !pykbDepsProbe() {
		return
	}
	_ = pykbEnsureRunning()
}
