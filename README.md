# Local AI Studio

> 本地优先的 AI 编码助手 —— 纯 Go 内核 + CLI/TUI + Wails 桌面（React + TS）+ 手机远程控制台 + 机器人/定时任务。

一个内核，多形态：纯 Go 内核（无 CGO，可静态编译）、命令行（CLI / TUI）、Wails 桌面应用（Go 逻辑 + React 界面 + WebView），以及远程联动（手机网页控制台、飞书/Lark/Telegram/企业微信等 IM 频道、定时任务）。

## 特性

### 核心 Agent
- **Agent 循环**：流式 function-calling，权限三态（readonly / ask / always），本地→云端自动回退。
- **Shim 式协议适配层**：内核只说一种内部消息语言，按 `api_format` 自动切换 `chat_completions` / `anthropic_messages`（Claude、GLM/Kimi 等兼容端点）/ `responses`（OpenAI Responses API）/ `gemini`（Google 原生），含工具调用、多模态、推理流的双向翻译。
- **凭据池轮换**：provider 级 `api_keys` 多 key 池，401/403 永久驱逐、402/429 冷却 30s 自动换 key 重试。
- **智能路由**：本地启发式分类（首轮/代码块/关键词/长度→复杂，其余→简单），拿不准可让本地大脑仲裁；简单轮走轻量模型省钱、复杂轮走强力模型保质量。
- **分区式系统提示词**：静态/动态边界（prompt cache 稳定），按工具面生成使用政策，环境信息（git/平台/日期），小上下文模型极简模式，provider/模型级 `prompt_addendum`。
- **工具集**：`read_file` / `write_file` / `list_dir` / `glob_search` / `grep_search` / `lsp_diagnostics` / `index_search` / `run_shell` / `web_search` / `todo_write`，外加条件暴露的 `call_model`（模型派发）与 `kb_search`（知识库）。工具 schema 每轮按名排序保证请求前缀字节稳定。
- **多模型**：`models.json` 配置（provider 分组），DeepSeek 官方定价费用统计。
- **LSP 智能提示**：多语言语义补全 + 诊断；语言服务器可自动安装。
- **双 RAG**：工作区 `index_search`（TF-IDF）+ 公司多根知识库 `kb_search`（TF-IDF + 可选 embedding）。
- **MCP**：stdio 子进程 + streamable HTTP 客户端。
- **会话管理**：SQLite 持久化，按项目(工作目录)分组，支持改名/删除/自动新会话/垃圾箱。
- **技能系统**：把成功会话经验沉淀为带 frontmatter 的 Markdown 技能，按触发词注入 system prompt。
- **链接取材（weblinks）**：消息中的 http(s) 链接自动取材（图片下载做视觉附件、网页剥正文内联）。

### 手机远程 & 双向同步（relay-server）
- **`relay-server`**（FastAPI 哑管道）：只按 `device_token` 路由，桥接桌面端出站 WS `/client` 与手机页 WS `/s/ws`，不做业务解析（业务在桌面端）。
- 桌面端**出站连接** `wss://<你的域名>/client?d=<token>`（自动绕过系统代理）；手机端打开 `https://<你的域名>/s/?d=<token>` 即可远程控制。
- **双向同步**：模型/推理等级/权限、会话改名/删除/新建、当前打开会话（刷新互相跟随）、会话/项目增删均桌面↔手机同源。
- 手机控制台：项目/会话列表（改名/删除）、运行标记、模型/推理/权限选择、`/` 斜杠命令、任务步骤栏（`任务步骤 N · 运行中 · 第 X 轮`）、用量状态栏（工作目录/分支/命中/tokens/费用/上下文/压缩阈值）、图片缩略图、`/file` 选文件/图片上传识别（图片压缩降尺寸、base64 传 PC）。
- **局域网移动端**（`desktop/mobile.go`）：手机连本机 `mobileStart` 起的本地页，功能大致同远程页。
- **也支持远程驱动 opencode**：`opencode-relay/`（独立目录，中继代码自包含、不复用 `relay-server/`）把手机控制台接到 `opencode serve`
  （方案 A 走中继端口 8999；方案 B 直接用 opencode 自带 web/serve 端口 9001）。
  架构、配置与用法见 `opencode-relay/README-opencode.md`；起停用 `scripts/opencode-remote.sh`。

### 并发与编辑
- **并发聊天/编程**：不同会话可同时后台运行（`runs` 列表），切换会话不阻塞；`running` 按会话实时派生。
- **编辑器不再锁定**：AI 运行时仅提示横幅，可并行编辑；`write_file` 完成后**同频编辑**（打开标签实时刷新 + 运行中周期重读，覆盖 write_file 与 run_shell）。
- **工作区约束**：`run_shell` 禁止 `cd` 到工作区外（`LAS_SANDBOX` 沙箱），agent 以“当前打开的项目”为准。
- **图片/附件双端显示**：图片以 84px 气泡内缩略图展示；附件只显示文件名、不内联正文；实时 `user_message` 广播携带缩略图。

### 机器人 & 定时
- **飞书 / 企业微信**：把消息推送到 IM，agent 运行结果可回传（`desktop/feishu.go`、`wecom.go`）。
- **Lark（国际版，域名切换）**：独立 `larkB` bot，`WithDomain` 切换游廊/自建应用。
- **Telegram**：getUpdates/sendMessage 长轮询（`desktop/telegram.go`）。
- **定时任务**：`desktop/schedule.go` 调度循环，按时执行任务、切换工作区、串行单任务。

### 桌面体验
- CodeMirror 编辑器（多语言高亮/LSP/选区加聊天/拖放/粘贴图片附件）、内置多标签终端（ConPTY）、截图+标注、图片缩略图、多面板（模型/知识库/缓存/MCP/派发/移动端/定时/垃圾箱）、底部单行用量状态栏。
- **中文/英文** 双语界面。

## 目录结构

```
cmd/localai/        CLI 入口（chat / tui / models / sessions）
internal/           内核包（agent/llm/tools/mcp/lsp/config/cache/sessions/codeindex/codera/...）
desktop/            独立 Go 模块：Wails 桌面（app/runner/term/shot/relay/mobile/feishu/telegram/wecom/schedule）
desktop/frontend/   React + TS + Vite 界面
relay-server/       FastAPI 手机远程中继（哑管道；page.html 手机控制台；generate_guide.py 生成部署指引）
opencode-relay/     opencode 远程控制（自包含中继副本 + opencode_bridge.py 协议翻译桥）
products/           产品 profile（devtool_local/devtool/novelwriter/quant/devrag）
docs/relay/         远程架构/协议/部署文档
```

## 构建

### 内核 + CLI/TUI（纯 Go，无 CGO）

```bash
go build ./...
go test ./...
go build -o bin/localai ./cmd/localai

bin/localai chat        # 流式 CLI
bin/localai tui         # 终端界面
```

### Wails 桌面

桌面是独立模块 `desktop/`（需 CGO + WebView2/WebKit）；**必须带 production 构建标签**。

```bash
cd desktop/frontend && pnpm install && pnpm build
cd desktop && go build -tags desktop,production -ldflags "-s -w" -o bin/LocalAIStudio .
```

或用根目录 `make`：`make build-desktop`（桌面）、`make test`（测试）。

### 手机远程中继（relay-server）

```bash
cd relay-server && pip install -r requirements.txt
python main.py          # 按 config.json 监听，绑 device_tokens 白名单
# 然后由 generate_guide.py 生成部署指引（TLS/systemd/桌面端连接等）。
# 桌面端连：https://<你的域名>/client?d=<token>；手机打开 https://<你的域名>/s/?d=<token>
```

## 配置

- 模型/端点：`~/.config/local-ai-studio/models.json`（Linux/macOS）或 `%APPDATA%\local-ai-studio\models.json`（Windows）。
- provider 可选字段：`api_key` / `api_keys`（凭据池）/ `api_format` / `auth_header` / `pricing`（USD/1M）/ `start_command` / `prompt_addendum`；模型级可加 `vision` / `reasoning` / `context_window`。
- 智能路由：顶层 `smart_routing` 块 `{enabled, simple_model, strong_model, simple_max_chars=160, simple_max_words=28, arbitrate}`。
- 会话库：`sessions.db`；缓存：`cache.db`；代码索引：`index/`；知识库：`kb/`。
- 手机远程：`models.json` 顶层 `relay` 块（`server_url` / `device_token`，64 位 hex），桌面端与手机 URL 的 `d=` 一致。
- 机器人/定时：`models.json` 里 `feishu` / `lark` / `telegram` / `wecom` / `scheduled_tasks` 配置。
- 沙箱：默认开启（`write_file` 限工作目录、`run_shell` 拦高危 + 禁 cd 出工作区）；关闭设 `LAS_SANDBOX=off`。手机中继 WS 消息上限默认 `RELAY_WS_MAX=5`（MB）。

## 许可

MIT
