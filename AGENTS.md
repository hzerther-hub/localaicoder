# 仓库指南（AGENTS.md）

## 项目一句话概述

Local AI Studio —— 本地优先的 AI 编码助手：一个纯 Go 内核（`internal/**`，模块名 `localai`）被三种前端消费——CLI（`cmd/localai`）、Bubbletea TUI（`internal/tui`）、Wails 桌面应用（`desktop/`，独立 Go 模块 + React/TS 前端）。核心是流式 function-calling 的 agent 循环，支持 readonly/ask/always 三态权限、本地→云端模型自动回退、TF-IDF 工作区 RAG、MCP。注释与用户可见字符串用**中文**；代码是 Python 内核的文档化 1:1 Go 移植——线上消息格式与 SQLite schema 保持字节级兼容。

## 架构与数据流

一个内核，三种前端。所有前端共享 `internal/config` 状态与 `internal/sessions` SQLite 库。桌面端只是 `internal/*` 之上的纯绑定/bridge 层——从不重新实现内核逻辑。

**每轮数据流**（`(*agent.Agent).Run`，位于 `internal/agent/agent.go`）：

1. 附件 → 多模态内容 part（`internal/attach`、`internal/media` 图片变 `image_url` data URL）；可选知识库自动注入 `codera.RetrieveContext`（`internal/agent/kbhook.go`）。
2. 循环 ≤ `config.MaxToolRounds`（12）：`OnStop` 检查 → `ctxcompact.MaybeCompact`（预算强制）→ LLM 缓存回放（`cache.GetLLM`）→ `llm.StreamChat`（SSE）发出 `text`/`reasoning`/`tool_calls`/`usage` 事件。
3. 遇到 `gpulocal*` 模型 503/加载错误 + 开启了自动回退：切到 `dispatch_flash` → `dispatch_pro`（云端），发出 `model_switch`，重试一次。
4. 工具调用：ask 模式的写工具先走 `OnApproval`；执行经 `tools.ExecuteTool`（`mcp_*` → MCP Manager，只读 → 工具缓存，写 → 直连）；结果以 `role:"tool"` 消息追加。工具失败以**字符串回喂给模型**——循环从不在工具错误上中断。
5. 无工具调用 → 输出最终文本，按请求时消息缓存响应。

**关键模块**：`internal/msg`（线缆契约：`Msg`/`Event` = `map[string]any` + 访问器 `S/B/F/I/M/L`——不引入 struct，不要建）、`internal/llm`（shim 式协议适配层：以 `model.APIFormat` 为键的 transport 注册表——`chat_completions` / `anthropic_messages` / `responses` / `gemini`；每个 transport 说同一套内部事件契约 `text`/`reasoning`/`tool_calls`/`finish`/`usage`；`keypool.go` 是 provider 级凭据池，round-robin 轮换，401/403 驱逐、402/429 冷却 30s——`StreamChat` 跨 key 透明重试；`ErrStop` 协作式停止）、`internal/tools`（注册表模式：每个工具一个文件、`init()` 自注册的 `Tool` struct——fail-closed `ReadOnly=false`，可选 `Enabled()` 条件暴露取代了旧的分开 schema map；`registry.go` 是唯一事实来源；`sandbox.go` 做路径/shell 防护）、`internal/config`（所有设置在 `models.json`，含 `api_format`/`api_keys`/`auth_header`/`prompt_addendum` provider 字段与 `smart_routing` 块）、`internal/routing`（智能路由：纯启发式分类器 + 可选本地大脑仲裁拿不准的边界轮，每用户轮一次分类并整轮固定）、`internal/prompt`（分区系统提示词，含 `SYSTEM_PROMPT_DYNAMIC_BOUNDARY` 静态/动态缓存边界，以及 gpulocal/≤32K 模型的极简模式）、`internal/mcp`（stdio + streamable HTTP，协议 2024-11-05）、`internal/cache` / `internal/sessions` / `internal/codeindex` / `internal/codera`（纯 Go SQLite）、`internal/products`（来自 `products/*/profile.json` 的白标功能开关）、`internal/weblinks`（消息中的 http(s) 链接自动取材：图片下载做视觉附件、网页剥正文内联、失败折成注释行）、`internal/skills`（技能系统：把成功会话经验沉淀为带 frontmatter 的 Markdown 技能，按触发词注入 system prompt；LLM 只产草稿、人握转正权）、`internal/embed`（OpenAI 兼容 embeddings 客户端，失败返回 nil 让 codera 自动回退纯 TF-IDF）、`internal/localmodels`（本地 GPU 模型桥：约定式健康三态 active/loading/stopped + 启停命令）、`internal/ctxcompact`（上下文压缩预算强制）。

**工具**：11 个内置（`internal/tools/` 下每工具一个文件、`init()` 自注册；`registry_test.go` 对 schema 输出做 golden 锁定）：`read_file`、`write_file`、`list_dir`、`glob_search`、`grep_search`、`lsp_diagnostics`、`index_search`、`run_shell`、`web_search`、`todo_write`（任务步骤清单，只读，渲染为任务条）；条件性 `call_model`（派发：`Enabled()` = 派发开启 + 本地大脑健康）与 `kb_search`（RAG 功能）；MCP 工具命名为 `mcp_<server>_<tool>`。写集合 = `write_file` + `run_shell`（外加非 `readonly` 的 MCP server）。工具 schema 每轮按名称排序以产生字节稳定的请求前缀（服务器 prompt-cache 优化）——务必保留 `sortToolSchemas`。新增工具 = 新文件 + `register(&Tool{...})`（schema JSON 原样照抄、`Exec`、可选 `ReadOnly`/`Enabled`/`Describe`）。

**智能路由**（`internal/routing` + `models.json` 的 `smart_routing` 块）：每用户轮，`agent.routeTurn` 分类（首轮 / 代码块 / 强关键词中英 / 多段 / 长度 → strong；边界弱信号区 → 可选 `dispatch_model` 仲裁，结果按 hash 缓存）并整轮固定模型；`routing` 事件（decision：simple/strong/escalate）流向 UI；简单模型可重试错误（404/429/5xx/连接）每轮升级到 strong 一次。除非显式设置 `smart_routing.enabled`，否则禁用（旧 `dispatch_smart` 标志**故意不**启用它）。

## 目录结构导览

- `cmd/localai/` —— CLI 入口（`chat`、`tui`、`models`、`sessions`、`version`、`help`）；手写 `os.Args` 分发，无 flag 框架。
- `internal/` —— 内核；一个包一个关注点，多为每包单文件（`tools/`、`llm/`、`config/`、`mcp/` 为多文件）。包清单：`agent` `attach` `cache` `codeindex` `codera` `config` `ctxcompact` `embed` `llm` `localmodels` `lsp` `mcp` `media` `msg` `products` `prompt` `routing` `sessions` `skills` `tui` `weblinks` `tools`。
- `desktop/` —— **独立 Go 模块**（`localai/desktop`，`replace localai => ../`）：`main.go`（Wails 引导，`go:embed frontend/dist` + `models.json`）、`app.go`（约 50 个 Wails 绑定方法）、`runner.go`（`RunManager`：一次跑一个 agent run，`agent:event` 发事件、`approval:request` + `RespondApproval` 走审批）、`term*.go`（内置终端）、`shot*.go`（截图标注）。
- `desktop/frontend/` —— React 18 + TypeScript + Vite + zustand；`bridge.ts` 包装 Wails 绑定，`store.ts` 是 zustand store，`components/` 存面板。
- `products/` —— 产品 profile：`profile.json` = `{title, features, exe_name}`；由 `LOCAL_AI_PRODUCT` 环境变量选择，默认 `devtool_local`。feature 键（如 `gpulocal`、`rag`、`zh_only`、`quant`）门控工具 schema 与 UI。

## 开发命令

```bash
# 内核 + CLI/TUI（纯 Go，无 CGO）
go build ./...            # 编译全部（仅根模块）
go test ./...             # 全部测试（= make test）
go vet ./...              # 唯一配置的 lint
go run ./cmd/localai chat # 流式 CLI REPL
go build -o bin/localai ./cmd/localai

# 桌面（独立模块；需 CGO + WebView2/WebKit；production 标签必须带！）
cd desktop/frontend && pnpm install && pnpm build   # tsc --noEmit && vite build
cd desktop && go build -tags desktop,production -ldflags "-s -w" -o bin/LocalAIStudio.exe .
cd desktop && wails dev     # 热重载（需: go install github.com/wailsapp/wails/v2/cmd/wails@latest）

# Makefile 快捷方式
make build | test | vet | run | frontend | build-desktop | dev-desktop | package-desktop | clean
```

单个测试：`go test ./internal/agent/ -run TestAgentToolLoop`。

## 代码约定与常见模式

- **语言**：文档注释、错误字符串、UI 文案用中文（如 `"高危命令未被拦截"`）；工具结果失败以 `错误：` 开头。
- **线缆格式优先**：消息/事件保持 `map[string]any`（`msg.Msg`/`msg.Event`）与 OpenAI JSON 1:1 对应；用 `msg.S/B/F/I/M/L` 访问器，绝不再建模成 struct。类型化 struct 只出现在边界：`config.ModelConfig`、`sessions.Session`、`agent.Usage`。
- **回调 DI，而非接口**：`agent.Agent` 接收函数字段 `OnEvent` / `OnApproval` / `OnStop`；前端通过构造 `&agent.Agent{...}` 接入。唯一显著接口：`mcp.transport`。
- **包级惰性状态**：`sync.Once` 单例（`mcp.GetManager()`、`tools.ToolSchemas()`）与互斥锁保护的包级变量（workspace、DB handle）。每个有状态包都暴露测试重置钩子：`config.SetDir`、`cache.Reset`、`mcp.ResetManager`、`tools.PushWorkspace`（返回 restore 函数）、`codeindex.CloseAll`、`sessions.Close`、`products.ResetForTest`。
- **错误处理分流**：transport 错误是类型化的（`llm.LLMError`、`mcp.MCPError`）且中断该轮；协作式停止是 `llm.ErrStop` 哨兵，用 `errors.Is` 检查。工具层的失败是纯文本结果；`ExecuteTool` 把 panic 恢复成文本。尽力而为的 IO 用 `_ = err`。
- **并发**：每个长时间运行单元配 goroutine + channel（MCP `readLoop` + 每请求 `pending` chans、TUI 事件 channel、桌面 run goroutine）；shell 执行用 `context.WithTimeout`（`ToolExecTimeout` 60s）；CLI 里用 `signal.NotifyContext`。SQLite 连接串行化（`SetMaxOpenConns(1)`、WAL）。
- **缓存纪律**：sha256-of-JSON 键；LLM 缓存以请求时消息为键（排除 usage）；写工具与 MCP 调用永不缓存；任何新缓存键都要有确定性 marshalling。
- **移植保真**：不要随便改动事件名、SQLite schema 或工具 schema JSON——它们镜像 Python 前身，且遗留迁移路径（`sessions/*.json`、旧配置目录）依赖它们。
- **命名**：`exec<Tool>` 非导出执行器；config 里用 `Get*/Set*` 成对；条件工具 schema 用 `*Schema()` 函数；类似 `ModeReadonly/ModeAsk/ModeAlways` 的常量。

## 重要文件

- `cmd/localai/main.go` —— CLI 接线：products → config → `tools.SetWorkspace` → MCP 连接 → `agent.Agent` 构造 → REPL。
- `internal/agent/agent.go` —— 循环、权限、云端回退、usage/费用核算。
- `internal/llm/llm.go` —— `StreamChat`、SSE 解析、工具调用 delta 累积、`max_tokens` 策略（gpulocal 上限 16384）。
- `internal/tools/tools.go`（+ `dispatch.go`、`sandbox.go`、`websearch.go`、`todo.go`）—— 注册表、执行器、沙箱、条件工具、步骤清单。
- `internal/config/config.go` —— 配置目录（`~/.config/local-ai-studio` / `%APPDATA%\local-ai-studio`，含遗留迁移）、`models.json` CRUD、全部 getter/setter、`GetSystemPrompt()`（双语）。
- `internal/msg/msg.go` —— 内核的数据契约。
- `internal/skills/skills.go` —— 技能加载/保存/注入（trigger 命中排序、外部源只读扫描）。
- `internal/weblinks/weblinks.go` —— 链接取材（≤3 个、并发、永不返回 error）。
- `desktop/main.go`、`desktop/app.go`、`desktop/runner.go` —— Wails 桥；前端只跟这些绑定通信。
- `models.json`（仓库根）—— seed/示例 provider 配置：`{default, providers:[{id, base_url, api_key, models:[{id, vision, reasoning}]}]}`；模型键 = `provider_id/model_id`。
- `products/*/profile.json` —— 功能开关；`rag` 门控 `kb_search`、`zh_only` 强制中文 UI。

## 运行时/工具偏好

- **Go 1.25**（两个模块）。内核依赖刻意精简：`modernc.org/sqlite`（纯 Go——保持 CLI 无 CGO/静态）+ charmbracelet TUI 库；其余全 stdlib。不新增 SDK/HTTP-client 依赖；手写 SSE/MCP/LSP 客户端是项目选择。
- **pnpm 10 专用于前端**（Node 20）；永不用 npm/yarn。Vite 6 + TS 5.6 strict；`pnpm build` 含类型检查（`build:fast` 跳过）。无 ESLint/Prettier 配置。
- 桌面构建需要 `desktop,production` 构建标签和 CGO；根模块命令从不碰 `desktop/`（独立模块）。
- 内核读取的环境变量：`LAS_SANDBOX=off`（关闭写/shell 沙箱）、`LAS_PRODUCTS_DIR`、`LOCAL_AI_PRODUCT`。API key 在 `models.json` 里按 provider 存放（本地 provider 用字面量 `"local-noauth"`），绝不放环境变量。
- CI（`.github/workflows/ci.yml`，push/PR 到 `master`）：`go build/vet/test ./...` + 前端 `pnpm install && pnpm build`。桌面 Go 模块和 wails 构建**不**被 CI 覆盖。

## 测试与 QA

- **仅用 stdlib `testing`** —— 无 testify/gomock；手工 `if` + `t.Fatal(t.Errorf)`。现有套件无表驱动测试或 `t.Run` 子测试；沿用每 `Test` 函数独立风格。
- 测试为**白盒**（同包）且通过公共 API（`ExecuteTool`、`StreamChat`、`MaybeCompact`）做集成式测试，断言可观察契约（事件序列、schema 计数、回退行为）。
- **隔离模式**（新测试照抄）：包内 `setup(t)` 辅助函数 → `t.TempDir()` + `config.SetDir(dir)` → 用 `os.WriteFile` 种 fixture → `t.Cleanup` 调该包重置钩子 + `config.SetDir("")`。
- 假 LLM 后端：`net/http/httptest` SSE 服务器（见 `internal/agent/agent_test.go` 的 `sseHandler`/`sse`/`toolCallJSON` 辅助函数）；用 `msg.S` 扫描 `Event["type"]` 断言事件。无真实网络、无 sleep、无 `testdata/` 目录。
- 无覆盖率阈值、CI/Makefile 里无 `-race`。测试在临时目录创建真实 SQLite 库——保持确定性。
- 未测包（`msg`、`mcp`、`lsp`、`tui`、`attach`、`media`、`embed`、`localmodels`、`products`、`codera`、`weblinks`、`skills`、`cmd/`、全部 `desktop/`）——若在此加测试，照 `internal/agent` 模式。
