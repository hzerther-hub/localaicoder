# Repository Guidelines（仓库指南）

## 项目一句话概述

Local AI Studio —— 本地优先的 AI 编码助手：一个纯 Go 内核（`internal/**`，模块名 `localai`）被三种前端消费——CLI（`cmd/localai`）、Bubbletea TUI（`internal/tui`）、Wails 桌面应用（`desktop/`，独立 Go 模块 `localai/desktop` + React/TS 前端）。核心是流式 function-calling 的 agent 循环，支持 readonly/ask/always 三态权限、智能路由、本地→云端模型自动回退、TF-IDF 工作区/知识库 RAG、MCP、技能沉淀。代码是 Python 内核的文档化 1:1 Go 移植——注释与用户可见字符串用**中文**；线上消息格式与 SQLite schema 保持字节级兼容（遗留迁移路径依赖它们，勿随意改动）。

## 架构与数据流

一个内核，三种前端。所有前端共享 `internal/config` 状态与 `internal/sessions` SQLite 库；桌面端只是 `internal/*` 之上的绑定/bridge 层（`desktop/app.go` 约 131 个 Wails 绑定方法），从不重新实现内核逻辑。

**每轮数据流**（`(*agent.Agent).Run`，`internal/agent/agent.go`）：

1. `routeTurn` 智能路由分类并**整轮固定模型**（原地改写 `*a.Model`，不改 history）；`weblinks.Process` 取材消息中的链接（≤3 个）；附件经 `attach`/`media` 变多模态 content part（图片 → `image_url` data URL，≤1568px）；可选知识库注入 `codera.RetrieveContext`（`internal/agent/kbhook.go`）。
2. 循环 ≤ `config.MaxToolRounds`（**32**）：`OnPause`/`OnStop` 检查 → 工具 schema 集（内置 `EnabledSchemas(readonly)` + 已连 MCP schema，**按名称排序产生字节稳定的请求前缀**——服务器 prompt-cache 优化，务必保留 `sortToolSchemas`）→ `ctxcompact.MaybeCompact`（上下文预算强制）→ LLM 缓存回放（`cache.GetLLM`，命中发 `cache_hit`）→ `llm.StreamChat`（手写 SSE）。
3. 流式回调消费内部事件契约 `text`/`reasoning`/`tool_calls`/`usage`（`finish` 仅存在于 transport 契约，agent 侧忽略）；回调返回 `llm.ErrStop` 即协作式中止（保留部分文本）。
4. 错误分流：simple 路由 + 可重试错误（连接失败/404/429/5xx）→ `escalateToStrong`（每轮一次）；`gpulocal*` 模型 503/加载/连接失败 + 开启自动回退 → 切 `dispatch_flash` → `dispatch_pro`（每轮一次），发 `model_switch`。
5. 工具调用：ask 模式下写工具先 `OnApproval`（写集合 fail-closed：`!ReadOnly` 的内置工具 + 非 `readonly` 的 MCP server）；执行经 `tools.ExecuteTool`——`mcp_` 前缀走 `mcp.GetManager()`（永不缓存）、只读走工具缓存、写直连；结果以 `role:"tool"` 消息追加。工具失败是**纯文本结果回喂模型**（`错误：` 前缀），panic 也被恢复成文本——循环从不在工具错误上中断。
6. 无工具调用且 todo 未完 → 注入"任务尚未完成"nudge（≤2 次，中间轮不入缓存）；否则输出最终文本，按请求时消息 `cache.PutLLM` 响应。

**agent→UI 事件**（`msg.Event`，11 种发射类型）：`text` `reasoning` `tool_start` `tool_result` `tool_denied` `usage` `round` `media` `cache_hit` `routing` `model_switch`。

**关键子系统**：

- `internal/llm` —— shim 式协议适配层：transport 注册表以 `config.NormalizedFormat(model.APIFormat)` 为键——`chat_completions` / `anthropic_messages` / `responses` / `gemini`（未知回落 chat_completions）；每个 transport 翻译同一套 5 事件内部契约。`keypool.go` 是 provider 级凭据池：round-robin 轮换、401/403 永久驱逐、402/429/529 冷却 30s、≤8 key 尝试、90s 空闲看门狗；`ErrStop` 哨兵用 `errors.Is` 检查。gpulocal 模型 `max_tokens` 钳到 16384。
- `internal/routing` —— 纯启发式 `Classify`（附件/代码块/中英强关键词/多段/长度 → strong；边界弱信号 → 可选 `Arbitrate` 派发 `dispatch_model` 8s 仲裁，sha1 缓存结果）。**`smart_routing` 默认禁用**（`dispatch_smart` 开关门控；旧标志故意不启用它）。
- `internal/tools` —— 注册表模式：每工具一个文件、`init()` 自注册 `Tool{Schema, ReadOnly, Enabled, Exec, Describe}`；`registry.go` 是唯一事实来源，`register()` 对缺失/重复/坏 schema 直接 panic；`sandbox.go` 做路径/shell 防护（`LAS_SANDBOX=off` 关闭；`ToolExecTimeout`=60s）。
- `internal/cache` / `internal/sessions` / `internal/codeindex` / `internal/codera` —— 纯 Go SQLite（`modernc.org/sqlite`，单连接 `SetMaxOpenConns(1)` + WAL）。
- `internal/products` —— 白标功能开关：`products/*/profile.json` 的 `{title, features, exe_name}`，`LOCAL_AI_PRODUCT` 选择（默认 `devtool_local`）；11 个 feature 键（`gpulocal/dispatch/editor/voice/mcp/attachments/sessions/rag/zh_only/quant/quant_tools`）门控工具 schema 与 UI；`zh_only` 强制中文。
- `internal/skills` —— frontmatter Markdown 技能按触发词排序注入 system prompt（≤6 条）；LLM 只产草稿（`distill.go` 会话后蒸馏），人握转正权（`AcceptDraft`）。
- `internal/weblinks` / `internal/embed` / `internal/localmodels` / `internal/attach` / `internal/media` / `internal/lsp` —— 链接取材（永不返回 error）、embeddings（失败返回 nil → 回退纯 TF-IDF）、本地 GPU 模型桥（健康三态 200=active/503=loading/其他=stopped）、附件分析、图片缩放、LSP stdio 客户端。

## 目录结构导览

- `cmd/localai/` —— CLI 入口：手写 `os.Args` 分发，子命令 `chat`（默认）、`tui`、`models`、`sessions`、`version/-v`、`help`，无 flag 框架。
- `internal/` —— 内核，包清单：`agent` `attach` `cache` `codeindex` `codera` `config` `ctxcompact` `embed` `llm` `localmodels` `lsp` `mcp` `media` `msg` `products` `prompt` `routing` `sessions` `skills` `tools` `tui` `weblinks`。
- `desktop/` —— 独立 Go 模块（`replace localai => ../`）：`main.go`（Wails 引导，`go:embed frontend/dist` + models.json）、`app.go`（绑定方法）、`runner.go`（`RunManager`：每会话串行跑 agent，同会话再发返回 `errBusy`；事件 `run:*`/`agent:event`，`approval:request` + `RespondApproval` 600s 超时自动拒绝）、`term*.go`（内置终端，Windows ConPTY / 其他 piped shell）、`shot*.go`（截图，Wayland 走 XDG portal）。
- `desktop/frontend/` —— React 18 + TS + Vite 6 + zustand 5；`bridge.ts`（类型化 api → `window.go.main.App`，无 Wails 时 mock 模式）、`store.ts`（单一 zustand store，90ms 流式节流）、`components/`（24 个面板/组件）。
- `products/` —— 5 个产品 profile：`devrag` `devtool` `devtool_local` `novelwriter` `quant`。
- `relay-server/` —— Python 移动端中继（不属于 Go 构建）。`docs/DESIGN.md` 描述的是**旧 Python/Tkinter 实现，已过时**。

## 开发命令

```bash
# 内核 + CLI/TUI（纯 Go，无 CGO；仅根模块，不含 desktop/）
go build ./... && go vet ./... && go test ./...   # = CI go job 三步；vet 是唯一 lint
go run ./cmd/localai chat                          # 流式 CLI REPL
go build -o bin/localai ./cmd/localai              # make build 产出 bin/localai.exe（Makefile 硬编码 .exe，即使 Linux）

# 桌面（独立模块；需 CGO + WebView2/WebKitGTK；production 标签必须带！）
cd desktop/frontend && pnpm install && pnpm build  # build = tsc --noEmit && vite build（含类型检查；build:fast 跳过）
cd desktop && go build -tags desktop,production -ldflags "-s -w" -o bin/LocalAIStudio .
# Linux 需追加 webkit2_41 标签——直接用 make build-desktop（已按 uname 处理）
cd desktop && wails dev                            # 热重载；wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 单个测试
go test ./internal/agent/ -run TestAgentToolLoop
```

Makefile 目标：`build` `build-all` `test` `vet` `run` `frontend` `build-desktop` `dev-desktop` `package-desktop`（wails installers）`clean` `help`。

CI（`.github/workflows/ci.yml`，push/PR → `master`）：job `go`（build/vet/test `./...`，Go 1.25）+ job `frontend`（Node 20 / pnpm 10，`pnpm build`）。**desktop Go 模块与 wails 构建不被 CI 覆盖**——改 `desktop/*.go` 后必须本地编译验证。

## 代码约定与常见模式

- **语言**：文档注释、错误字符串、UI 文案用中文；工具失败结果以 `错误：` 开头。
- **线缆格式优先**：消息/事件保持 `map[string]any`（`msg.Msg`/`msg.Event`）与 OpenAI JSON 1:1 对应；用 `msg.S/B/F/I/M/L` 访问器（容忍 int/float64/json.Number），**绝不再建模成 struct**。类型化 struct 只出现在边界：`config.ModelConfig`、`sessions.Session`、`agent.Usage`。
- **回调 DI，而非接口**：`agent.Agent` 接收函数字段 `OnEvent` / `OnApproval` / `OnStop` / `OnPause`；前端构造 `&agent.Agent{...}` 接入。唯一显著接口：`mcp.transport`。
- **包级惰性状态**：`sync.Once` 单例（`mcp.GetManager()`、`tools.ToolSchemas()`、`config.Sandbox()`、`llm` 共享 http.Client）+ 互斥锁保护的包级变量。每个有状态包暴露测试重置钩子：`config.SetDir`、`cache.Reset`、`mcp.ResetManager`、`products.ResetForTest`、`skills.ResetForTest`、`tools.ResetFileHistory`、`codeindex.CloseAll`、`sessions.Close`、`tools.PushWorkspace`（返回 restore 函数）。
- **错误处理分流**：transport 错误是类型化的（`llm.LLMError`、`mcp.MCPError`）且中断该轮；工具层失败是纯文本结果；panic 在 `ExecuteTool`/KB 注入处 recover 成文本；协作停止是 `llm.ErrStop` 哨兵。尽力而为的 IO 用 `_ = err`。
- **缓存纪律**：键 = `"qwenc:llm:"+sha256(json([modelID, messages, schemas]))` / `"qwenc:tool:"+sha256(json([name, args, workspace]))`（`json.Marshal` 排序 map 键 → 确定性）；usage 事件、中止轮、nudge 中间轮、MCP 调用、写工具**永不缓存**；TTL llm 3600 / tool 300。
- **移植保真**：不要改事件名、SQLite schema、工具 schema JSON——它们镜像 Python 前身，遗留迁移路径（`sessions/*.json`、wellfuture-coder/qwen-coder 配置目录）依赖它们。
- **命名**：`exec<Tool>` 非导出执行器；config 用 `Get*/Set*` 成对；条件工具 schema 用 `*Schema()` 函数；权限常量 `ModeReadonly/ModeAsk/ModeAlways`（Agent 构造默认 `always`）。
- **新增内置工具** = 新文件 + `init(){register(&Tool{...})}`（schema JSON 原样照抄、`Exec`、可选 `ReadOnly`/`Enabled`/`Describe`），并同步更新 `internal/tools/registry_test.go` 的 golden。写集合 = `write_file` + `run_shell`（外加非 readonly MCP server）。当前 12 个内置工具，条件暴露 2 个：`kb_search`（KB 开启）、`call_model`（派发开启 + 本地大脑健康）。

## 重要文件

- `cmd/localai/main.go` —— CLI 接线：products → `config.LoadModels` → `tools.SetWorkspace` → `mcp.GetManager().Connect` → `signal.NotifyContext` → `agent.Agent` 构造 → REPL。
- `internal/agent/agent.go` —— 循环、权限、路由固定/升级、云端回退、usage/费用核算；`kbhook.go` 知识库注入。
- `internal/llm/llm.go` —— `StreamChat`、transport 注册表、`LLMError`/`ErrStop`；`keypool.go` 凭据池；`retry.go` 退避与自适应 `max_tokens`。
- `internal/tools/registry.go`（+ `sandbox.go`、`callmodel.go`、`todo.go`）—— 注册表、沙箱、派发、步骤清单。
- `internal/config/config.go` —— 配置目录与遗留迁移、models.json CRUD、全部 `Get*/Set*`、循环/上下文常量（`MaxToolRounds=32`、`ToolExecTimeout=60`、`AttachImageMaxPix=1568` 等）。
- `internal/msg/msg.go` —— 内核数据契约。`internal/prompt/prompt.go` —— 分区 system prompt + `SYSTEM_PROMPT_DYNAMIC_BOUNDARY` 静态/动态缓存边界 + 极简模式。
- `models.json`（仓库根）—— seed provider 配置：`{default, dispatch_model, providers:[{id, base_url, api_key, api_keys?, api_format?, auth_header?, prompt_addendum?, models:[{id, vision?, reasoning?}]}], smart_routing?:{simple_model, strong_model}}`；模型键 = `provider_id/model_id`（如 `gpulocal-8080/qwen3-coder-30b`）。
- `products/*/profile.json` —— 功能开关。`desktop/main.go`、`desktop/app.go`、`desktop/runner.go` —— Wails 桥，前端只跟这些绑定通信。

## 运行时/工具偏好

- **Go 1.25**（两个模块，无 go.work）。内核依赖刻意精简：`modernc.org/sqlite`（纯 Go，保持 CLI 无 CGO/静态）+ charmbracelet TUI 三件套；其余全 stdlib。**不新增 SDK/HTTP-client 依赖**——手写 SSE/MCP/LSP 客户端是项目选择。
- **pnpm 10 + Node 20 专用于前端**；永不用 npm/yarn。Vite 6 + TS 5.6 strict + React 18 + zustand 5；无 ESLint/Prettier 配置。
- 桌面构建需 `desktop,production` 标签和 CGO；根模块命令从不碰 `desktop/`（独立模块，`go build ./...` 不会编译它）。
- 内核读取的环境变量：`LAS_SANDBOX`（`off|0|false|no` 关闭写/shell 沙箱）、`LOCAL_AI_PRODUCT`、`LAS_PRODUCTS_DIR`、`LAS_PORTAL_DEBUG`（Wayland 截图调试）。**API key 在 `models.json` 按 provider 存放**（本地 provider 用字面量 `"local-noauth"`），绝不放环境变量。
- 配置目录：`~/.config/local-ai-studio` / `%APPDATA%\local-ai-studio`；目标目录缺失时从 `wellfuture-coder`/`qwen-coder` 遗留目录 copyTree 迁移。

## 项目知识检索（memsearch，省 token）

本仓库文档（AGENTS/README/docs/opencode-relay/relay-server 等，448 块）已索引进本地向量库（milvus-lite + bge-m3 int8/ONNX，全本地零 API）。**回答架构/部署/协议/中继类问题前先检索，不要整篇读长文档**：

```bash
memsearch search "问题关键词" -c localaicoder -k 5   # 返回带 Source 路径+Heading 的相关小块，按需再读原文那一节
```

更新文档后重索引用 `memsearch index -c localaicoder <路径>`（增量）。跨项目共用一个库，靠 collection 区分。

## 测试与 QA

- **仅 stdlib `testing`**——无 testify/gomock（testify 只是 desktop/go.sum 里 wails 的传递依赖，从不 import）；手工 `if` + `t.Fatal/t.Errorf`。全部白盒同包测试；无 `t.Run` 子测试（表驱动数据用函数内字面量）、无 benchmark、无 `-race`、无覆盖率工具。
- **隔离惯例**（新测试照抄）：包内 `setup(t)` helper → `t.TempDir()` + `config.SetDir(dir)` → `os.WriteFile` 种 fixture → `t.Cleanup` 还原 `config.SetDir("")` + 该包 reset 钩子。注意各包 cleanup 顺序不一致（SetDir 与 reset 钩子先后都有）——照所在包的 exemplar 写。
- **假 LLM 后端**：`internal/agent/agent_test.go` 的 `sseHandler`（按请求计数脚本化多轮工具循环）/`sse`/`toolCallJSON`；`httptest.NewServer` 返回 OpenAI 式 `data:` SSE 帧；用 `msg.S(e, "type")` 扫事件断言。无真实网络、无 sleep、无 `testdata/` 目录。
- **Golden 锁定**：`internal/tools/registry_test.go` 的 `TestSchemasGold` 把工具 schema JSON 锁在内联字面量（schema 描述属于 prompt 工程，不许漂移）；`internal/routing` 的 `TestDefaultThresholdsMatchConfig` 锁分类阈值。
- 已测 12 包：agent/cache/codeindex/config/ctxcompact/llm/prompt/routing/sessions/skills/tools/weblinks。**零测试**：`mcp` `tui` `media` `msg` `products` `localmodels` `lsp` `embed` `codera` `attach` + `cmd/`；desktop 模块仅 Wayland 门控的 `desktop/zz_portal_test.go`。若在未测包加测试，照 `internal/agent` 模式。
- 缓存/索引/会话测试在临时目录创建**真实 SQLite 库**——确定性靠 tempdir 隔离，不 mock。
