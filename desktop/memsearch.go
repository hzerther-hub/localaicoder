package main

// memsearch 项目知识检索（PyPI 工具，用法见 docs/memsearch.md）的检测、安装与调用。
//
// 路线优先级（全平台自动路由，见 MemsearchRun）：
//   - Linux/macOS：原生 memsearch（uv tool install，milvus-lite 本地向量库）；
//   - Windows：pykb（自研 Python 语义检索服务，desktop/pykb.go + desktop/pykb/，
//     唯一依赖 fastembed，应用启动自动拉起）→ WSL 内 memsearch → 内置知识库（前端回退）；
//   - pykb 同时是所有平台的兜底路线（原生缺失时自动顶上）。
//
// 进度与结果经 memsearch:install:log / memsearch:install:done 事件推送（安装为异步）。

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"localai/internal/tools"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// memsearchInstallCmd 展示给用户/复制用的安装命令（与 docs/memsearch.md 保持一致）。
const memsearchInstallCmd = "uv tool install memsearch --force --with onnxruntime --with tokenizers"

// WSL 内安装/运行时注入的国内镜像（PyPI 走清华、HF 走镜像站，避免 GitHub/HF 直连超时）。
const wslEnvLine = "export HF_ENDPOINT=https://hf-mirror.com UV_DEFAULT_INDEX=https://pypi.tuna.tsinghua.edu.cn/simple"

// 安装互斥：双开窗口/重复点击时只允许一个安装流程。
var (
	memsearchMu         sync.Mutex
	memsearchInstalling bool
)

// ---------------- 探测 ----------------

// uvShimPath 返回 ~/.local/bin/<name>（uv tool install 的默认 shim 位置；Windows 带 .exe）。
func uvShimPath(name string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(home, ".local", "bin", name)
	_, err = os.Stat(p)
	return p, err == nil
}

// findExecutable 探测可执行文件：PATH 优先，shim 目录兜底。
// 除 PATH 外还要查 shim——uv tool install 成功后本进程 PATH 不会刷新，
// 只查 LookPath 会出现「装完引导条不消失」，直到重启应用。
func findExecutable(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	return uvShimPath(name)
}

// memsearchInstalled 原生 memsearch 是否可用（Linux/macOS 直接可用；
// Windows 仅代表 CLI 在，没有向量库仍跑不起来）。
func memsearchInstalled() (bool, string) {
	p, ok := findExecutable("memsearch")
	return ok, p
}

// wslOn 是否装有 WSL（仅 Windows 关心）。
func wslOn() bool {
	if goruntime.GOOS != "windows" {
		return false
	}
	_, err := exec.LookPath("wsl.exe")
	return err == nil
}

// wslMemsearchReady 默认发行版内是否有可用的 memsearch（登录 shell 探测）。
func wslMemsearchReady() bool {
	if !wslOn() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wsl.exe", "-e", "sh", "-lc", "command -v memsearch >/dev/null 2>&1")
	return cmd.Run() == nil
}

// ---------------- 状态 ----------------

// MemsearchStatus 给前端引导条用。usable=true 表示「装了且能用」，引导条不出现。
// Windows 路线：pykb（自研 Python 语义检索，首选）/ WSL 内 memsearch / 内置知识库（前端回退）。
func (a *App) MemsearchStatus() map[string]any {
	installed, path := memsearchInstalled()
	_, uvOK := findExecutable("uv")
	nativeOK := goruntime.GOOS != "windows"

	pykbDeps, pykbRunning := false, false
	wslAvailable, wslReady := false, false
	if goruntime.GOOS == "windows" && !nativeOK {
		pykbRunning = pykbHealthy()
		pykbDeps = pykbDepsProbe()
		wslAvailable = wslOn()
		if wslAvailable {
			wslReady = wslMemsearchReady()
		}
	}
	usable := (installed && nativeOK) || wslReady || pykbDeps
	return map[string]any{
		"installed":     installed,
		"path":          path,
		"uv_available":  uvOK,
		"native_ok":     nativeOK,
		"pykb_deps":     pykbDeps,
		"pykb_running":  pykbRunning,
		"wsl_available": wslAvailable,
		"wsl_memsearch": wslReady,
		"usable":        usable,
		"install_cmd":   memsearchInstallCmd,
	}
}

// ---------------- 配置 ----------------

// memsearchEnsureConfig 首次安装后写入 ONNX 嵌入配置；已有配置文件则不碰。
func memsearchEnsureConfig() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".memsearch")
	cfg := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfg); err == nil {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(cfg, []byte("[embedding]\nprovider = \"onnx\"\n"), 0o644)
}

// ---------------- 安装 ----------------

// InstallMemsearch 启动一键安装（幂等、防重入），立即返回；进度经事件推送：
//
//	memsearch:install:log  {line}   安装输出逐行
//	memsearch:install:done {ok, msg?, output?}  终态
//
// via："native"（Linux/macOS 直装）/"wsl"（Windows→WSL2）/"pykb"（Windows→自研 Python
// 语义检索，首选）；空串自动选（非 Windows=native，Windows=pykb）。
func (a *App) InstallMemsearch(via string) map[string]any {
	memsearchMu.Lock()
	if memsearchInstalling {
		memsearchMu.Unlock()
		return map[string]any{"ok": false, "error": "安装已在进行中"}
	}
	memsearchInstalling = true
	memsearchMu.Unlock()

	// 快速终态：所选路线已就绪
	if quick, msg := memsearchRouteReady(via); quick {
		memsearchMu.Lock()
		memsearchInstalling = false
		memsearchMu.Unlock()
		runtime.EventsEmit(a.ctx, "memsearch:install:done", map[string]any{"ok": true, "msg": msg})
		return map[string]any{"ok": true, "msg": msg}
	}
	if via == "" {
		if goruntime.GOOS != "windows" {
			via = "native"
		} else {
			via = "pykb"
		}
	}
	if via == "wsl" && goruntime.GOOS != "windows" {
		memsearchMu.Lock()
		memsearchInstalling = false
		memsearchMu.Unlock()
		return map[string]any{"ok": false, "error": "WSL 路线仅用于 Windows"}
	}
	if via == "native" && goruntime.GOOS == "windows" {
		memsearchMu.Lock()
		memsearchInstalling = false
		memsearchMu.Unlock()
		return map[string]any{"ok": false, "error": "Windows 原生直装不受支持（milvus-lite 无 Windows 轮子）：请选 pykb（Python 语义检索）或 WSL 路线"}
	}

	go func() {
		var ok bool
		var output string
		switch via {
		case "wsl":
			ok, output = a.memsearchInstallWSL()
		case "pykb":
			ok, output = a.pykbInstallDeps()
		default:
			ok, output = a.memsearchInstallNative()
		}
		memsearchMu.Lock()
		memsearchInstalling = false
		memsearchMu.Unlock()
		done := map[string]any{"ok": ok, "output": output}
		if ok {
			done["msg"] = memsearchDoneMsg(via)
		}
		runtime.EventsEmit(a.ctx, "memsearch:install:done", done)
	}()
	return map[string]any{"ok": true, "msg": "安装已开始"}
}

// memsearchRouteReady 所选路线是否已经就绪（就绪则无需再装）。
func memsearchRouteReady(via string) (bool, string) {
	switch via {
	case "wsl":
		if wslMemsearchReady() {
			return true, "WSL2 内已有可用的 memsearch"
		}
	case "pykb":
		if pykbHealthy() {
			return true, "pykb 语义检索服务已在运行"
		}
		return false, ""
	}
	if installed, path := memsearchInstalled(); installed && goruntime.GOOS != "windows" {
		return true, "已安装：" + path
	}
	return false, ""
}

func memsearchDoneMsg(via string) string {
	switch via {
	case "wsl":
		return "已安装到 WSL2（首次检索/索引时会自动下载约 570MB 向量模型）"
	case "pykb":
		return "pykb 语义检索已就绪（嵌入模型约 95MB 已随首次索引下载，服务 127.0.0.1:" + pykbPort + "）"
	default:
		return "已安装（首次检索/索引时会自动下载约 570MB 向量模型）"
	}
}

// memsearchStreamRun 跑命令并逐行推送 memsearch:install:log，返回 (退出成功?, 尾部输出)。
func (a *App) memsearchStreamRun(cmd *exec.Cmd) (bool, string) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return false, err.Error()
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}
	_ = pw.Close() // 父侧关闭写端；读端 EOF 即命令结束

	tail := []string{}
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		runtime.EventsEmit(a.ctx, "memsearch:install:log", map[string]any{"line": line})
		tail = append(tail, line)
		if len(tail) > 40 {
			tail = tail[1:]
		}
	}
	_ = pr.Close()
	return cmd.Wait() == nil, strings.TrimSpace(strings.Join(tail, "\n"))
}

// memsearchInstallNative Linux/macOS 直装：uv tool install + ONNX 配置。
func (a *App) memsearchInstallNative() (bool, string) {
	uvPath, uvOK := findExecutable("uv")
	if !uvOK {
		return false, "未找到 uv（memsearch 的安装器）。请先安装 uv：https://docs.astral.sh/uv/getting-started/installation/"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, uvPath, "tool", "install", "memsearch",
		"--force", "--with", "onnxruntime", "--with", "tokenizers")
	ok, tail := a.memsearchStreamRun(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return false, "安装超时（10 分钟），请检查网络后重试"
	}
	if !ok {
		return false, tail
	}
	if installed, _ := memsearchInstalled(); !installed {
		return false, strings.TrimSpace(tail + "\n安装命令已执行，但未找到 memsearch 可执行文件，请手动检查 uv tool list")
	}
	memsearchEnsureConfig()
	return true, tail
}

// memsearchInstallWSL Windows→WSL2 路线：发行版内装 uv（缺失时用 curl 引导安装）+ memsearch + ONNX 配置。
func (a *App) memsearchInstallWSL() (bool, string) {
	if !wslOn() {
		return false, "未检测到 WSL2：请先安装 WSL2（管理员 PowerShell 运行 wsl --install），或用内置知识库（零安装）替代"
	}
	line := wslEnvLine + "; command -v uv >/dev/null 2>&1 || curl -LsS https://astral.sh/uv/install.sh | sh; " +
		"~/.local/bin/uv tool install memsearch --force --with onnxruntime --with tokenizers; " +
		"mkdir -p ~/.memsearch; [ -f ~/.memsearch/config.toml ] || printf '[embedding]\\nprovider = \"onnx\"\\n' > ~/.memsearch/config.toml"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wsl.exe", "-e", "sh", "-lc", line)
	ok, tail := a.memsearchStreamRun(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		return false, "WSL2 内安装超时（20 分钟），请检查网络后重试"
	}
	if !ok {
		return false, tail
	}
	if !wslMemsearchReady() {
		return false, strings.TrimSpace(tail + "\n安装命令已执行，但 WSL 内未找到 memsearch，请进入 WSL 手动检查 uv tool list")
	}
	return true, tail
}

// ---------------- 调用 ----------------

// memsearchWSLArgv 把 argv 组装成经登录 shell 执行的 wsl 调用（单引号转义，无注入面）。
func memsearchWSLArgv(argv []string) []string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return []string{"-e", "sh", "-lc", wslEnvLine + "; " + strings.Join(quoted, " ")}
}

// MemsearchRun 运行 memsearch 风格子命令（argv 已由前端拆好），返回文本输出。典型 argv：
//
//	["search", "权限 模型", "-c", "localaicoder", "-k", "5"]
//	["index", "-c", "localaicoder", "AGENTS.md", "docs"]
//	["stats"]
//
// 路线：原生 memsearch（Linux/macOS 首选）→ pykb 服务（自研、全平台；Windows 首选）
// → WSL 内 memsearch（Windows 次选）。统一 15 分钟超时（首次运行含模型下载）。
func (a *App) MemsearchRun(argv []string) map[string]any {
	if len(argv) == 0 {
		return map[string]any{"ok": false, "error": "缺少参数"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// 1) 原生 memsearch（非 Windows）
	if goruntime.GOOS != "windows" {
		if _, found := findExecutable("memsearch"); found {
			return memsearchExecNative(ctx, argv)
		}
	}
	// 2) pykb 服务（自研 Python 语义检索）
	if err := pykbEnsureRunning(); err == nil {
		return pykbRun(argv, tools.GetWorkspace())
	}
	// 3) WSL 内 memsearch（仅 Windows）
	if goruntime.GOOS == "windows" && wslMemsearchReady() {
		cmd := exec.CommandContext(ctx, "wsl.exe", memsearchWSLArgv(argv)...)
		if ws := tools.GetWorkspace(); ws != "" {
			cmd.Dir = ws // wsl.exe 把 Windows 工作区映射为 /mnt/...，相对索引路径即可用
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return map[string]any{"ok": false, "error": "WSL 内 memsearch 失败", "output": memsearchClip(string(out))}
		}
		return map[string]any{"ok": true, "output": memsearchClip(string(out))}
	}
	// 全部不可用：给平台化的引导
	if goruntime.GOOS == "windows" {
		return map[string]any{"ok": false, "error": "语义检索不可用：点引导条「安装语义检索」一键装 pykb，或直接用内置知识库（/kb）。也可在 WSL 内运行 " + memsearchInstallCmd}
	}
	return map[string]any{"ok": false, "error": "未安装 memsearch：运行 " + memsearchInstallCmd + "，或 pip install fastembed 后走 pykb 路线（重启应用自动检测）"}
}

// memsearchExecNative 原生 memsearch 子命令执行（含常见报错的友好化）。
func memsearchExecNative(ctx context.Context, argv []string) map[string]any {
	bin, found := findExecutable("memsearch")
	if !found {
		return map[string]any{"ok": false, "error": "未安装 memsearch：运行 " + memsearchInstallCmd + "，或 pip install fastembed 后走 pykb 路线"}
	}
	out, err := exec.CommandContext(ctx, bin, argv...).CombinedOutput()
	tail := memsearchClip(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return map[string]any{"ok": false, "error": "运行超时（15 分钟）。首次运行需下载约 570MB 向量模型，稍后重试通常即可", "output": tail}
	}
	if err != nil {
		// 非零退出也把输出带回去（memsearch 的报错信息都在输出里）
		if strings.Contains(tail, "milvus-lite does not support Windows") {
			return map[string]any{"ok": false, "error": "Windows 原生 memsearch 缺向量库支持：请用 pykb 路线（引导条一键安装）或内置知识库（/kb）", "output": tail}
		}
		low := strings.ToLower(tail)
		if strings.Contains(tail, "拒绝访问") || strings.Contains(low, "access is denied") || strings.Contains(low, "permission denied") {
			return map[string]any{"ok": false, "error": "memsearch 可执行文件被安全软件拦截（360 等常拦 uv 的 shim）：请把 memsearch.exe 加入信任名单，或改用 pykb / 内置知识库", "output": tail}
		}
		return map[string]any{"ok": false, "error": "memsearch 退出码非 0", "output": tail}
	}
	return map[string]any{"ok": true, "output": tail}
}

// memsearchClip 统一截断超长输出（保尾部，报错信息多在末尾）。
func memsearchClip(s string) string {
	tail := strings.TrimSpace(s)
	if len(tail) > 4000 {
		tail = "…（输出过长已截断）\n" + tail[len(tail)-4000:]
	}
	return tail
}
