# Local AI Studio — 全项目设计文档（供 AI 智能体重建）

> **文档目的**：任何 AI 智能体读完本文档后，应能**从零重新设计并实现**出与本仓库等价的项目。
> **读者**：AI 智能体（无人类上下文）。**语言**：正文中文、标识符英文（与代码库一致）。
> **基准**：基于当前代码库实测（`git log HEAD=11559b1`，约 18,700 行 Python，36 个顶层模块，5 个产品变体，24+ 测试文件）。

---

## 0. 一句话定义

**Local AI Studio** 是一个**本地优先的桌面 AI 编码/知识助手**：Tkinter GUI + OpenAI 兼容 LLM 后端 + 流式 function-calling + MCP 工具 + 多产品形态。一个内核，多个产品——通过 `LOCAL_AI_PRODUCT` 环境变量或 `products/<name>/run.py` 切换变体（`devtool_local`、`devtool`、`novelwriter`、`quant`、`devrag`）。

**核心设计原则**（重建时必须遵守）：
1. **本地优先**：默认本地模型（免费、离线）；仅在识图/超强推理时才委派云端。
2. **零重依赖内核**：核心全部 stdlib（`urllib.request` 手写 LLM SSE 客户端与 web 抓取，**无** openai/requests/web 框架）。可选依赖（Pillow、numpy、faster-whisper、psutil、tiktoken）缺失时**优雅降级**。
3. **单线程 UI + 每消息一个 daemon 线程**：Tkinter mainloop 单线程；worker 线程**绝不**直接碰 widget，一律 `root.after(0, fn)`。
4. **错误即字符串**：工具错误返回中文字符串（如 `"错误：文件不存在 …"`）给模型看；只有传输层失败才 raise（`LLMError`/`MCPError`）。
5. **中文注释/docstring + 英文标识符**；模块级 docstring 说明用途；`# ---- 标题` 分节。
6. **配置与代码分离**：所有用户配置在平台配置目录（`~/.config/local-ai-studio/`），仓库根 `models.json` 只是首跑种子。

---

## 1. 总体架构

```
main.py ──► ui.launch() ──► App (Tkinter mainloop, 单线程)
              │  每条消息 spawn 一个 daemon 线程
              ▼
   chatrun.ChatRun（每个会话一份运行态：消息/渲染op缓冲/todo/运行标志）
              │
              ▼
        agent.Agent.run()            # 同步 function-calling 主循环
              │
   ┌────┬───┼────────┬──────────┐
   ▼    ▼   ▼        ▼          ▼
 llm.py tools.py mcp.py  context.py  cache.py
 (SSE)  (9工具) (MCP)   (预算/压缩)  (SQLite/内存缓存)
              │
   ┌──────────┼─────────────┐
   ▼          ▼             ▼
 codeindex.py codera.py   lsp.py
 (工作区索引) (企业KB RAG) (LSP 客户端)
```

**事件流**：`Agent.run()` 每产生一个事件（text/reasoning/tool_start/tool_result/media/usage/todo…）→ `App._on_event` 是**唯一汇聚点** → 写入该会话 `ChatRun.ops` 渲染缓冲；当前显示的会话（`App.cur`）实时渲染，后台会话静默累积，切回时**重放** ops。

---

## 2. 目录与模块清单

```
main.py            入口：Python 3.12 门槛检查 → ui.launch()
ui.py              Tk 主壳（~6000 行）：聊天流/工具展示/审批/目录/语音/会话/tab
chatrun.py         并行会话运行态：消息、渲染 op 缓冲、todo 步骤、运行/停止标志
agent.py           Agent 主循环 + 权限三模式 + MCP 路由 + 事件发射
llm.py             OpenAI 兼容 SSE 流式客户端（stdlib urllib 手写）→ LLMError
tools.py           9 个内置工具 + 执行器 + TOOL_SCHEMAS + 沙箱护栏
mcp.py             MCP 客户端：stdio 子进程 / streamable HTTP；工具名前缀 mcp_<server>_<tool>
context.py         token 估算 + 两阶段渐进压缩（对齐 DSH 思想）
cache.py           LLM/工具缓存：SQLite WAL 或内存；TTL 1h/5min
sessions.py        会话库：SQLite WAL；旧 JSON 迁移
config.py          ModelConfig dataclass、models.json 加载、CONFIG_DIR 单一事实源
codeindex.py       工作区代码索引：解析→分块→TF-IDF→SQLite（工具 index_search）
codera.py          企业多根知识库 RAG：TF-IDF + 可选 embedding（工具 kb_search）
embed.py           OpenAI 兼容 embeddings 客户端（codera 可选增强）
lsp.py             stdlib JSON-RPC LSP 客户端（pyright/tsserver/gopls/rust-analyzer/clangd）
lsp_install.py     LSP 服务器安装引导
skills.py          自学习技能库：可复用经验沉淀为 Markdown，按需注入 system prompt
skills_distill.py  会话结束蒸馏：成功会话→技能草稿→人工确认转正
theme.py           设计令牌（颜色/间距/字号）+ ttk clam 定制主题
icons.py           彩色 PNG 图标加载（纯 stdlib）：assets/icons/<name>_<size>.png
i18n.py            中英双语：i18n.t(key)，可切换并持久化
attach.py          附件预处理：docx/pdf/zip 内容就地喂给模型；file:// URI 提取
media.py           图片加载（含 GIF 动画）、音频播放、视频缩略图、外部打开
weblinks.py        消息里 URL 自动取材：图片→下载识图；网页→抓正文内联（6000字上限，≤3链接）
voice.py           语音录入（faster-whisper + PortAudio VAD，可选）
screenshot.py      多屏截屏 + 标注（PIL ImageGrab / XDG portal / 系统工具，按 OS 分方案）
localmodels.py     本地 GPU 模型桥：gpulocal 面板注册表 → models.json 的 gpulocal-<port> provider
ui_panel_*.py      对话面板：annotate(标注)/approval/cache/dispatch/help/kb/mcp/models/quant/sessions/skills
products/          产品变体：profile.json（title/features/exe_name）+ 可选 run.py/子代码
gpulocal/          内嵌本地模型管理面板（services/systemd 脚本/面板 UI）
packaging/         build.py — 产品感知的 PyInstaller 打包
tests/             pytest 套件（24+ 文件，~300 用例）
docs/ assets/ fonts/ examples/
```

---

## 3. 核心数据结构与状态

### 3.1 模型（config.py）
```python
@dataclass(frozen=True)
class ModelConfig:
    key: str              # "provider_id/model_id"
    provider_name: str
    model_id: str
    display_name: str
    base_url: str
    api_key: str
    vision: bool = False          # True 才能收图片附件
    reasoning: bool = False
    reasoning_effort: str = ""
    reasoning_choices: tuple = ()
    context_window: int = 0       # 0 = 未知
```
- `models.json`（配置目录）结构：`{"providers": [{"id","name","base_url","api_key","models":[{"id","display_name","vision","reasoning","reasoning_effort","context_window"}]}]}`
- 仓库根 `models.json` 只是种子；`config._ensure_models_file()` 首跑拷贝，并迁移旧配置目录（`qwen-coder`/`wellfuture-coder`）。
- `localmodels.py` 启动时把 gpulocal 面板的模型注册表同步为 `gpulocal-<port>` provider。
- **上下文预算**（context.effective_budget）：本地(gpulocal) → 131072−输出余量；云端 → `min(CONTEXT_BUDGET, context_window−margin)`；当前 `CONTEXT_BUDGET=1_000_000`（对齐 DSH）、`MAX_TOKENS=256_000`（本地输出单独钳 16384）。

### 3.2 会话运行态（chatrun.py）
每个并行会话一个 `ChatRun`：
- `messages`：OpenAI 消息列表（system/user/assistant/tool）
- `ops`：渲染意图 op 缓冲（切会话时重放，不扰动后台会话）
- `todo`：任务步骤清单（工具调用驱动：tool_start→添加、tool_result→完成打勾）
- 运行/停止标志；会话列表用绿 `▶` 标记运行中
- worker 通过 `tools.bind_workspace`（thread-local）绑定自己的工作目录 → **并行会话可跑在不同目录互不干扰**

### 3.3 配置目录（单一事实源）
```
~/.config/local-ai-studio/        (Windows: %APPDATA%/local-ai-studio/)
  models.json   模型 providers
  mcp.json      MCP servers（readonly 标记 → 免审批可缓存）
  cache.json    设置/连接/记忆
  state.json    最近工作目录、语言等
  sessions/     （旧版 JSON；现入 SQLite）
  sessions.db   会话 SQLite（WAL）
  index/        codeindex 的 <hash>.db
  media/        下载的图片/附件
  skills/       技能库 Markdown
```
测试隔离：conftest 把 `HOME`/`APPDATA` 重定向到 `tmp_path` 并清 `LOCAL_AI_PRODUCT`；`cache.reset()`、`mcp.reset_manager()`、`tools.push_workspace(path)` 三个可重置单点。

---

## 4. Agent 主循环（agent.py）

```
run():
  maybe_compact(messages)                       # 每轮前上下文压缩
  for round in range(MAX_TOOL_ROUNDS=12):
      stream = llm.stream_chat(model, messages, TOOL_SCHEMAS+MCP工具)
      for event in stream:                      # text/reasoning/tool_calls/usage/finish
          emit(event)                           # → App._on_event → ChatRun.ops
      if tool_calls:
          for tc in tool_calls:
              审批（ask 模式：写工具/非只读 MCP 需批准；readonly/always 跳过）
              result = tools.execute(...) 或 mcp.call(...)
              emit(tool_result)                 # todo 步骤打勾
              messages.append(tool 消息)
          continue                              # 下一轮
      else:
          纯文本回复 → 缓存 → 结束
```

**权限三模式**：`readonly`（只读工具） / `ask`（写工具与非只读 MCP 弹审批） / `always`。两个权威判定：`tools.is_write_tool(name)`、`MCPManager.is_write_tool(server)`。

**模型派发**（config：`model_dispatch`/`dispatch_smart`/`dispatch_model`）：本地优先；识图走 vision 路由（本地优先，缺则 `dispatch_vision`）；超强推理→云端 pro。工具 `call_model` 实现委派。

**沙箱**（`config.SANDBOX` 默认开，`LAS_SANDBOX=off` 关）：`write_file` 限制在工作区内（`tools.path_in_workspace`）；`run_shell` 拒绝破坏性命令（`rm -rf /`、`mkfs`、fork 炸弹、格式化、关机——见 `_BLOCKED_SHELL_PATTERNS`）。是护栏，非 OS 级隔离。

---

## 5. 内置工具（tools.py，9 个）

| 工具 | 作用 | 要点 |
|---|---|---|
| `read_file` | 读文件（offset/limit 分页） | 结果可能被压缩阶段截断 |
| `write_file` | 写文件 | **仅限工作区内**（沙箱）；是写工具 |
| `grep_search` | 正则搜索工作区 | 预编译 rx；跳过 >5MB 二进制 |
| `run_shell` | 执行 shell（cwd=workspace） | 破坏性模式黑名单；是写工具 |
| `glob_search` | 通配找文件 | — |
| `index_search` | 工作区 TF-IDF 语义检索 | codeindex 提供 |
| `kb_search` | 企业知识库检索 | codera 提供（可选 embedding 混合） |
| `call_model` | 委派其它模型（识图/重推理） | 事件里醒目显示“派发给 X” |
| `lsp_diagnostics` | LSP 诊断 | 多语言 server 预热 |

**注册模式**：静态 `TOOL_SCHEMAS` 列表 + `_EXECUTORS` dict；MCP 工具动态注册为 `mcp_<server>_<tool>`。`is_write_tool(name)` 是审批的权威之一。

---

## 6. LLM 客户端（llm.py）

- **手写 SSE**：`urllib.request` POST `/chat/completions`（`stream:true` + `stream_options.include_usage`），逐行解析 `data:` JSON。
- delta 聚合：`content`→text 事件；`reasoning_content`→reasoning 事件；`tool_calls` 按 index 累积 arguments 分片，finish 时组装完整 tool_calls。
- `reasoning_effort` 仅当模型配置了才发送；`max_tokens` 按模型钳制（本地 16K / 云端 256K）。
- 失败统一 raise `LLMError`（带响应体摘要）。**缓存键用请求时消息列表**（不能把回复 append 后再算 key，见 agent.py 注释）。

---

## 7. 上下文管理（context.py，对齐 DSH）

两阶段：
1. **就地收缩**：超长工具结果截断（保留头部+尾部提示）、剥离旧图片 data URL；
2. **仍超预算**：折叠中段轮次为单行摘要（保留首尾轮）。
- token 估算：有 tiktoken 用 `cl100k_base` 精确；否则 CJK 启发式（中文≈1 token/字，ASCII≈1/4 字）。
- `effective_budget(model)` 决定触发线（见 3.1）。

---

## 8. 缓存（cache.py）

- 后端：SQLite WAL（默认 `auto`，不可用回退内存）。
- LLM 回复按 `sha256(model+messages+tool_schemas)`；**只读工具**结果才缓存。
- TTL：LLM 1h / 工具 5min。`cache.reset()` 公开重置（测试隔离）。

---

## 9. RAG 双系统

| | codeindex.py（工作区） | codera.py（企业 KB） |
|---|---|---|
| 范围 | 当前 workspace | N 个配置根目录（代码+文档） |
| 管线 | 解析→按代码结构分块→TF-IDF→SQLite | 多根→分块→TF-IDF + 可选 embedding 混合检索 |
| 工具 | `index_search` | `kb_search` |
| 增量 | mtime 增量构建 | 根目录管理面板（ui_panel_kb） |

embed.py：OpenAI 兼容 `/embeddings` 客户端（模型可配），供 codera 语义增强。

---

## 10. LSP（lsp.py）

- stdlib JSON-RPC over stdio 的轻量客户端；支持 pyright / typescript-language-server / gopls / rust-analyzer / clangd。
- 启动时预热常用 server；工具 `lsp_diagnostics` 把诊断作为中文工具结果返回。
- `lsp_install.py` 提供缺失 server 的安装引导。

---

## 11. 技能系统（skills.py + skills_distill.py）

- **skills.py**：可复用经验沉淀为 `skills/` 下 Markdown；按相关性**注入 system prompt**（模型“会做”这件事）。
- **skills_distill.py**：会话成功结束后**蒸馏**为技能草稿 → 人工在 `ui_panel_skills` 确认/编辑 → 转正入库（“载入编辑再确认”）。

---

## 12. UI（ui.py ~6000 行 + ui_panel_*）

### 12.1 布局
```
┌ 顶部菜单 ctrl：模型按钮(名+图标) 🧠思考等级 会话 续上下文 派发 知识库 目录 master 语言 ? ┐
├ 工作区侧栏(会话列表,绿▶=运行中) │ 中央聊天区(流式 md+工具卡片) │ 文件树/编辑器 tabs ┤
├ todo 步骤条（📋 任务步骤 N，工具驱动，◐进行中/✅完成，整轮结束消失）                  ┤
├ 统计条：Tokens(输入/输出/思考/缓存命中%/请求) 附件/识图 空间                          ┤
└ 底部控制条：＋新建 🕐队列 🛡权限 🤖模型 🧠思考 📸截图 🔄刷新 | 输入框(Enter发/Shift换行) ┘
```

### 12.2 关键机制
- **流式渲染**：Markdown 增量重排（`_md_reset_block`/`_md_render`）；思考(reasoning)折叠显示。
- **并行会话**：每会话一个 ChatRun；后台运行不丢 op；切换=重放；每会话独立工作目录。
- **todo 步骤条**：`tool_start`→`_todo_start`（添加/置进行中）、`tool_result`→`_todo_done`（打✅）；发送时立即可见（“处理中…”占位）；手动 `＋` 可加；`▾/▴` 折叠；整轮结束 `_clear_todo`。
- **模型名展示**：顶部 `model_btn`（图标+display_name，宽度自适应）＋ **窗口标题** `产品名 · 模型名`（`_title_with_model`，切模型/切语言同步刷新）。
- **审批**：ask 模式弹 `ui_panel_approval`，批准/拒绝写回工具结果。
- **截屏+标注**（ui + screenshot.py + ui_panel_annotate.py，见 §13）。
- **主题**（theme.py）：集中色板/间距/字号 + ttk clam；图标（icons.py）PNG 彩色，缺失自动降级 emoji。

### 12.3 面板模式约定
每个 `ui_panel_x.py` 暴露 `show(app, ...)`；**函数内部**才 `import ui`（取 FONT_* 全局与 `_make_modal`）；App 方法做薄委托。模态统一 `ui._make_modal(win, parent)`（等可见后再 grab，规避 Windows 下静默失效）。

---

## 13. 截图 + 标注（screenshot.py + ui_panel_annotate.py）

### 13.1 按 OS 分方案抓屏（screenshot.py）
| OS | 抓屏方案 |
|---|---|
| Windows/macOS | PIL `ImageGrab.grab(all_screens=win32)`（`grab_fullscreen()` 全屏存临时 PNG） |
| Linux | 优先系统工具（`import`/`scrot`/`maim`/`gnome-screenshot`/`grim`）→ 回退 PIL → 兜底空图（保证覆盖层不崩） |
| Linux(推荐) | **XDG Desktop Portal**（`org.freedesktop.portal.Screenshot`，dbus + GLib mainloop 等 Response，`unquote` 解码 file URI）——能截到真实画面 |

### 13.2 统一标注编辑器（ui_panel_annotate.py）
无论哪个 OS，抓到图后统一弹**居中模态标注窗**：
- 工具：**选择/调整**（8 手柄改大小）/ 框形 / 椭圆 / 直线 / 箭头（画完自动提示补说明文字）/ 画笔 / 马赛克 / **文字**（每条独立字号，可选中改字号）
- 颜色彩色 swatch（8 色）、笔画宽度 1–10、字号 6–48、撤销/重做、Delete 删除、`Enter`=完成 `Esc`=取消 `Ctrl+Z/Y`
- 画布实时绘制（tag="shape"），**✔完成**时按缩放比把矢量标注烧录进原图（PIL），保存临时 PNG → 回调 `on_done(path)`
- 中文字体 `_font()`：依次试 应用自带 NotoSansCJK / 系统 Noto/uming/wqy / Windows msyh / macOS PingFang / `fc-match` 兜底

### 13.3 入口与流转
- 入口：底部控制条 📸 按钮、快捷键 `Ctrl+Shift+F` / `Ctrl+F2`
- Linux：portal 区域抓屏 → 标注窗 → 确认进附件；Windows/macOS：全屏抓取 → 同一标注窗
- 标注图进 `_pending_attachments` → 随消息发给**视觉模型**（模型在系统提示词里已被告知“红色框/箭头/文字=用户标出的重点”）

---

## 14. 语音（voice.py，可选）

faster-whisper 转录 + PortAudio VAD；按住说话/自动转写；缺依赖优雅降级为不可用。ffmpeg 用于音视频探针（`ffprobe` 提取时长/编码后再分析）。

---

## 15. 链接取材（weblinks.py）

消息里的 http(s) URL（≤3 个）后台线程自动处理：
- `image/*` → 下载到 `media/` → 视觉附件（与文件附件同一 vision 门控）
- `text/html` → 剥脚本/样式取正文，截 6000 字内联进消息
- 其它类型 → 存 `media/` + 路径说明交给模型工具
- 失败 → **注释行**，绝不抛异常中断发送
- 细节：`_html_to_text` 只取 `<body>`（防 head 标题混入）；`_decode` 容错 charset；`_normalize_image` 对已是标准 PNG/JPEG 的图跳过重解码

---

## 16. 本地模型（gpulocal/ + localmodels.py）

- `gpulocal/` 内嵌面板：模型注册表 `MODELS`（端口、启动命令、服务管理 svc_*，**串行**——启动一个停其它）、systemd/ps1/sh 安装脚本。
- `localmodels.py` 按文件路径 mtime 缓存导入该注册表（无 UI 副作用），轮询健康检查 `/v1/models`，通过后自动选中；同步进 models.json 成为 `gpulocal-<port>` provider。
- 模型下拉显示 ●运行/◐启动中/○停止 + 启停/重启。

---

## 17. 产品变体（products/）

```python
# products/__init__.py
LOCAL_AI_PRODUCT = os.environ.get("LOCAL_AI_PRODUCT", "devtool_local")
profile = json.load(products/<name>/profile.json)
# profile: { "title": str, "features": {gpulocal,dispatch,editor,voice,mcp,attachments,sessions,rag,quant,zh_only...}, "exe_name": str }
feature(key, default=False)   # 各处 UI/工具按 feature 开关裁剪
```

| 产品 | title | 特点 |
|---|---|---|
| devtool_local | Local AI Studio | 默认全功能本地 |
| devtool | Local AI Studio · Dev | 云端可用 |
| quant | 量化开发 | 策略翻译 IR（JoinQuant↔PTrade↔gm↔QMT）、微调语料管线（QLoRA）、回测/sim、api_notes/api_maps |
| novelwriter | 短剧网文创作 | 写作向 |
| devrag | 宁波易到救援开发平台 | 企业多根 KB RAG；gpulocal/dispatch 关、zh_only |

运行：`LOCAL_AI_PRODUCT=quant python3 main.py` 或 `python3 products/quant/run.py`。

---

## 18. i18n 与主题

- `i18n.t(key)` 双语；界面可切中/英并持久化；系统提示词随语言追加回复语言指令。
- `theme.py`：颜色/间距/字号令牌集中；ttk clam 定制；组件从令牌取值（不散落硬编码色值——历史遗留逐步迁移）。

---

## 19. 测试与 CI

- **pytest**（24+ 文件，~300 用例）：`python -m pytest tests/ -q`
- conftest 隔离：重定向 HOME/APPDATA、清 LOCAL_AI_PRODUCT；单例可 reset。
- LLM 传输全 mock（test_agent/test_llm）；sessions 用真 SQLite(tmp_path)；`test_products.py` 参数化跑全部 5 产品。
- **CI**（.github/workflows/test.yml）：`py_compile *.py` + `pytest -q` → PyInstaller 打包；矩阵 ubuntu/windows/macos × Python 3.12。
- 本地无额外 lint 门（pyrightconfig 是 stub，pyright 手动跑）。

---

## 20. 打包（packaging/build.py）

- 产品感知：`python packaging/build.py [product] [--clean]` → 按该产品 profile 裁剪特性、命名 exe（`exe_name`）。
- spec 排除 torch/faster-whisper/onnx（AI 重栈不进包）；可选依赖缺失时运行期降级。

---

## 21. 重建路线图（给智能体的实施顺序）

1. **config.py**：CONFIG_DIR、ModelConfig、models.json 加载/迁移、SANDBOX、上下文预算。
2. **llm.py**：手写 SSE 流式 + tool_calls 聚合 + LLMError。（先写测试 mock 传输）
3. **tools.py**：9 工具 + schema + 执行器 + 沙箱 + `is_write_tool`。
4. **agent.py**：主循环 + 权限三模式 + 事件发射 + 缓存接入。
5. **context.py + cache.py**：压缩与缓存（含“请求时键”约定）。
6. **chatrun.py + ui.py 最小壳**：单会话流式聊天 → 工具卡片 → 审批弹窗。
7. **sessions.py + 并行会话**：SQLite + ChatRun ops 重放 + 绿▶。
8. **mcp.py**：stdio + HTTP 两客户端 + 动态注册 + readonly 审批/缓存语义。
9. **codeindex.py / codera.py / embed.py**：两套 RAG + index_search/kb_search。
10. **lsp.py**：诊断工具；**attach/media/weblinks/voice**：附件与多媒体。
11. **theme/icons/i18n + ui_panel_***：面板体系、双语、截屏标注。
12. **skills + skills_distill**：技能沉淀/蒸馏。
13. **products/**：profile 裁剪 + quant/novelwriter/devrag 变体。
14. **localmodels + gpulocal/**：本地模型桥。
15. **packaging + CI**：build.py + GitHub Actions 矩阵。

每步都以 `py_compile + pytest` 验证；UI 改动必须 `root.after(0,…)`；错误返回中文字符串。

---

## 22. 已知约定与坑（重建必读）

- **不要**在 worker 线程碰 widget；一切 UI 更新走 `root.after`。
- **缓存键**=请求时消息列表；追加回复后再算 key 是错的。
- `write_file` 必须**真正落盘**（不要只把新内容输出在回复里）；写完 `read_file` 抽查——该纪律已写入系统提示词。
- 系统提示词含“完成纪律”：列 todo→逐项做→lsp/测试验证→验证通过才收尾。
- 推理模型（如 deepseek-v4-pro）思考消耗输出预算：`MAX_TOKENS` 必须给足（256K），否则截断成“空回复”。
- 事件里 `usage` 不进渲染缓冲（只累计）；其余事件都进 ops 以便会话切换重放。
- 截屏在**无桌面/远程会话**抓不到真实帧（黑帧）——portal/GNOME 才有真帧；Windows 用 ImageGrab 原生可用。
- Python **3.12+** 硬门槛（main.py 检查）。

---

---

## 附录 A：Quant IR —— 策略互转体系（products/quant/）

### A.1 设计思想：IR 星型架构（不做 N×N 直译）

4 个平台（`joinquant` 聚宽 / `ptrade` / `gm` 掘金 / `qmt`）互转若两两直译要 12 条通路。本项目改为**星型**：
所有平台代码先**解析成平台无关的 StrategyIR**，再从 IR **确定性发射**成目标平台代码：

```
聚宽代码 ─┐                          ┌→ PTrade 代码
PTrade  ─┤   parsers.py   ir.py   emitters.py   掘金代码
掘金    ─┤   (解析→IR)  (中间表示)  (模板发射)  →  QMT 代码
QMT    ─┘          ↓ 校验 validate.py（语法+API白名单）
```

- **确定性优先**：模板发射不经过 LLM，同 IR 永远同输出，可基准化（benchmark.py 信号一致率矩阵）。
- **LLM 只做兜底**：模板覆盖不了的手写策略才走 LLM（见 A.4），且 LLM 也以 IR/KB 为约束。

### A.2 StrategyIR 数据结构（ir.py，可 JSON 序列化）

```python
@dataclass
class Schedule:            # 触发节奏
    freq: str = "daily"    # daily | minute | tick
    time: str = ""         # daily 触发时刻，如 "09:35" / "open"

@dataclass
class Signal:              # 一个信号：指标+参数+条件
    kind: str              # ma_cross / grid / threshold …
    params: dict
    condition: str = ""    # 人类可读，如 "fast crosses above slow"

@dataclass
class OrderSpec:           # 下单语义
    style: str = "market"  # market | limit | target_value | target_pct
    pct: float = 0.0       # target_pct 仓位比例 0-1

@dataclass
class StrategyIR:
    name: str
    universe: list[str]        # 标的列表（IR 内部统一聚宽格式为规范形）
    schedule: Schedule
    signals: list[Signal]
    order: OrderSpec
    risk: dict                 # 止损止盈/仓位约束
    source_platform: str       # 解析来源标记
    to_json() / from_json()    # asdict 序列化
```
内置样例 IR：`demo_ma_cross()`（双均线）、`demo_etf_rotation()`（ETF 轮动）。

### A.3 主流程（translate.py）与配套模块

```python
translate(source, src, dst, chat_fn=None) -> {"ir","code","validation"}
  1) _parse(source, src, chat_fn)   # parsers.py；失败→ llm_parse 兜底（LLM 代码→IR）
  2) emit(ir, dst)                  # emitters.py 确定性模板（emit_joinquant/emit_ptrade/…）
  3) validate(code, dst)            # 静态校验：AST 语法 + 平台 API 白名单（沙箱约束）
  4) 头部备注 api_notes.py          # 注入“本文件是 X 平台代码、用到哪些平台函数”
```
- **parsers.py**：`parse_code(source, platform)`，`_PLATFORMS=("joinquant","ptrade","gm","qmt")`，AST 级解析可识别的策略模式，不可解析抛 `ParseError`（触发 LLM 兜底）。
- **emitters.py**：每平台一个 `emit_<platform>(ir)`，纯模板拼装。
- **symbols.py**：标的代码互转，**IR 内部统一聚宽格式为规范形**（`000001.XSHE` 风格）。
- **api_notes.py**：转换产物头部生成“平台 API 说明”（本文件是什么平台、用到哪些函数）。**动机**：各平台同名函数参数不同（`get_history`/`order_target_percent`），不标清楚，修 bug 的 AI 会把聚宽/QMT/PTrade 函数混用。`translate()` 与 `llm_direct()` 共用；还会把策略名里的平台字样改写为目标平台（“早小市值—聚宽版”→“—PTrade版”）。
- **api_maps/**：人工维护的平台 API 映射与已知差异 Markdown（`joinquant_ptrade.md`、`gm_myquant.md`、`qmt_xtquant.md`），供 LLM 直译时检索、也做微调语料源。
- **validate.py**：发射后必须过静态校验（语法 + API 白名单），不通过即失败——**宁可失败不出错代码**。
- **sim.py**：桩运行时模拟器——把发射出的平台代码在 mocked API 上**真实 exec** 跑通，验证语义可运行。
- **benchmark.py**：基准策略 × 4 平台互转矩阵的**信号一致率**报告（回归保护）。
- **kb.py / kb/**：量化知识库（RAG 的 R）：加载 + 轻量检索，LLM 直译前先查。
- **qmt.py**：QMT/miniQMT 终端探活（复用 gpulocal 本地服务探测思路）。

### A.4 LLM 兜底两条路
1. **llm_parse.py**（任意手写策略 → StrategyIR）：parsers 解析不了时，LLM 按 IR schema 产出 JSON，`StrategyIR.from_json` 收敛，失败仍可回退错误字符串。
2. **llm_direct.py**（模板搞不定 → 整段直译）：LLM **先查量化知识库（kb）与 api_maps**，再整段翻译成目标平台代码；产物同样过 validate/api_notes。

### A.5 微调管线（products/quant/finetune/，QLoRA）
- **prepare_data.py**：把三类知识生成 **alpaca JSONL** 语料（`{"instruction","input","output"}`）：
  1. `api_maps/*.md` 的差异行 → `“聚宽/PTrade/掘金/QMT 互转注意：<语义>”`
  2. `api_notes`/平台对照文档 → `“量化平台对照：<title> 的 API 映射与已知差异”`
  3. `kb/` 知识文档 → `“量化平台知识：<标题>”`
  4. `skills/` 技能经验 → `“作为量化开发助手，遵循技能经验：<name>”`
- **train_lora.py**：QLoRA 训练（unsloth，FP16 + 4bit）；宿主机指南见 `HOST-GUIDE.md`。
- 目的：把“平台差异知识”内化进模型，减少运行时 RAG 依赖。

---

## 附录 B：Skills 技能系统与蒸馏三阶段（skills.py + skills_distill.py）

### B.0 目标
让助手**越用越懂这个用户/这个领域**：把成功会话里的可复用经验（流程、坑点、平台 API 映射）沉淀成结构化技能，之后**按需注入 system prompt**——模型“天生会做”做过的事。

### B.1 技能载体（skills.py）
- **Skill** = Markdown 文件，严格 frontmatter：
  ```markdown
  ---
  name: quant-joinquant-to-qmt
  description: 一句话说明这条经验
  when: 触发词1,触发词2
  ---
  正文：精炼流程/坑/映射表（≤400 字，只要可执行结论）
  ```
- **三个作用域目录**：`user_dir()`（用户级，配置目录下）、`project_dir(workspace)`（项目级，随工作区）、`drafts_dir()`（蒸馏草稿，待确认）。
- `load_all(workspace)` 扫描+解析（mtime 快照缓存，`_invalidate()` 失效）；`save(sk, scope)` 落盘；`clean_name()` 规范名为小写连字符。
- **注入**：按触发词/相关性挑选技能追加进 system prompt（`skills.enabled()` 总开关）。

### B.2 三阶段
```
阶段一（会话中）：正常干活 —— 工具调用、写文件成功……（无额外动作，只积累 transcript）
阶段二（会话结束）：蒸馏 should_distill? → distill() → 技能【草稿】
阶段三（人工确认）：ui_panel_skills 列草稿 → 载入编辑再确认 → accept 转正 / discard 丢弃
```

### B.3 阶段二触发判定（should_distill）
全部满足才蒸馏（宁缺毋滥）：
1. 有 `final`（会话正常收尾，非中途停止）
2. `skills.enabled()` 开关开
3. 配了蒸馏模型（`_resolve_model_key()`：本地或云端）
4. 工具调用次数 ≥ `MIN_TOOL_CALLS`（工作量足够，过滤闲聊）
5. `_had_write_success(messages)`：**确实写成功过文件**（证明任务真完成了，而非只读闲逛）

### B.4 蒸馏执行（distill）——并并发安全与成本控制
```
with _distill_lock:
    sid 已蒸馏过 → 跳过（_session_done 先占名额，并发收尾不重复）
本地模型要求健康检查（未运行只跳过，不自动拉起）；云端直接调，传输失败静默放弃
草稿数 ≥ MAX_DRAFTS → 跳过（防堆积）
```
- **提示词 DISTILL_PROMPT**（严格约束输出）：读完整对话记录，判断是否有【下次可复用】的流程/坑点/API 映射；
  **有** → 只输出一个 Markdown 片段（首行必须 `---`，字段 name/description/when，正文 ≤400 字可执行结论）；
  **没有** → 只回 `NO`。不输出任何其它内容。
- 序列化 transcript：`_serialize_messages(deepcopy(messages))`（截图等大对象就地瘦身）。
- 后处理：`_extract_skill_text` 截取片段 → `skills._parse_text` 结构化 → `clean_name` → body 截 2000 字 → **`_similar_existing` 查重**（已有近似技能则不重复入库）→ 写入 `drafts_dir()` 草稿。
- 返回草稿路径；任何一环不满足返回 None（**绝不打扰用户**）。

### B.5 阶段三：人工确认（ui_panel_skills）
- `list_drafts()` 列草稿；`load_draft(name)` 载入**可编辑**；用户改完 → `accept_draft(name)` 转正（进 user/project 作用域，立即参与注入）或 `discard_draft(name)` 丢弃。
- 设计要点：**LLM 只产草稿，人握转正权**——防止错误经验污染技能库。

### B.6 闭环
```
成功会话 → 蒸馏草稿 → 人工转正 → 注入 system prompt → 下次同类任务更强 → 再蒸馏更好的经验 …
```
（quant 微调管线 A.5 的第 4 类语料正是来自 skills——技能经验同时服务于「运行时注入」与「离线训练」两条路。）

---

*本文档由代码库实测生成；若代码演进，请以 `git log` 与各模块 docstring 为准更新本文。*