# Local AI Studio

> 本地优先的 AI 编码助手 —— Go 内核 + CLI/TUI + Wails 桌面（React + TS）。

一个内核，多形态：纯 Go 内核（无 CGO，可静默静态编译）、命令行（CLI / TUI）、以及 Wails 桌面应用（Go 逻辑 + React 界面 + WebView）。

## 特性

- **Agent 循环**：流式 function-calling，权限三态（readonly / ask / always），本地→云端自动回退。
- **9 个内置工具**：read_file / write_file / list_dir / glob_search / grep_search / lsp_diagnostics / index_search / run_shell / web_search，外加按条件暴露的 `call_model`（模型派发）与 `kb_search`（公司知识库）。
- **多模型**：models.json 配置（provider 分组），DeepSeek 官方定价费用统计。
- **LSP 智能提示**：多语言语义补全 + 诊断；语言服务器可自动安装到应用自包含目录。
- **双 RAG**：工作区 `index_search`（TF-IDF）+ 公司多根知识库 `kb_search`（TF-IDF + 可选 embedding 混合检索）。
- **MCP**：stdio 子进程 + streamable HTTP 客户端。
- **会话管理**：SQLite 持久化，按项目(工作目录)分组，支持改名/删除/自动新会话。
- **桌面体验**：CodeMirror 编辑器（多语言高亮/LSP/选区加聊天）、内置多标签 PowerShell 终端（ConPTY）、微信式截图+标注、图片缩略图、多面板（模型/知识库/缓存/MCP/派发）。
- **中文/英文** 双语界面。

## 目录结构

```
cmd/localai/        CLI 入口（chat / tui / models / sessions）
internal/           内核包（agent/llm/tools/mcp/lsp/config/cache/sessions/codeindex/codera/...）
desktop/            独立 Go 模块：Wails 桌面（go.mod + app/runner/term/shot）
desktop/frontend/   React + TS + Vite 界面
products/           产品 profile（devtool_local/devtool/novelwriter/quant/devrag）
```

## 构建

### 内核 + CLI/TUI（纯 Go，无 CGO）

```bash
go build ./...
go test ./...
go build -o bin/localai.exe ./cmd/localai

# 使用
bin/localai.exe chat        # 流式 CLI
bin/localai.exe tui         # 终端界面
```

### Wails 桌面

桌面是独立模块 `desktop/`（需要 CGO + WebView2/WebKit）；**必须带上 production 构建标签**。

```bash
cd desktop/frontend && pnpm install && pnpm build
cd ../ && go build -tags desktop,production -ldflags "-s -w" -o bin/LocalAIStudio.exe .
```

或在项目根用 `make`：

```bash
make            # CLI
make build-desktop   # 桌面 exe
make test       # 全部 Go 测试
```

## 配置

- 模型/端点：`%APPDATA%\local-ai-studio\models.json`（Windows）或 `~/.config/local-ai-studio/models.json`。
- 模型按 provider 分组，provider 级可选字段：`api_key` / `api_format` / `pricing`（`input_hit`/`input`/`output`，USD/1M）/ `start_command`（本地模型启停）。
- 会话库：同目录 `sessions.db`；缓存：`cache.db`；代码索引：`index/`；知识库：`kb/`。
- 沙箱：默认开启（`write_file` 限工作目录内、`run_shell` 拦截高危命令），关闭设 `LAS_SANDBOX=off`。

## 许可

MIT
