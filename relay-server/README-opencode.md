# opencode 远程控制（方案 A + B）

> 让手机像控制 Local AI Studio 一样远程驱动 **opencode**。本目录把"自建中继 + 手机
> 网页控制台"这套东西接到 opencode 上，并同时给出直接用 opencode 自身服务的方式。
>
> 端口约定：**方案 A → 8999**（中继 + 桥），**方案 B → 9001**（opencode serve）。

## 一、架构总览

```
┌────────── 手机浏览器 ──────────┐        ┌────── 中继服务器(relay-server) ──────┐
│ 方案A：page.html 控制台        │        │  FastAPI 哑管道，只按 device_token 路由 │
│ 方案B：opencode 自带 web 客户端 │        │   . 静态页 GET /s/?d=<token>        │
└───────────┬───────────────────┘        │   . 手机WS  /s/ws?d=<token>          │
            │  (ws/wss, TLS)             │   . 客户端WS /client?d=<token>        │
            └────────────────────────────┼──────────────────────────────────────┘
                                        │
                 ┌──────────────────────┴───────────────────────┐
                 ▼ 方案A: r 桥(opencode_bridge.py)                ▼ 方案B: 直接用
        relay:/client  <—— 桥本身无业务，只做协议翻译 ——>          opencode serve(9001)
                 │                                             HTTP API + /event SSE
                 ▼
        opencode serve :9001   （HTTP OpenAPI + SSE 事件流）
```

- **2026-08 实测**：方案 A 的手机页 `state`（会话列表）/`models`/`send`（流式回复）/`messages`（历史）四类
  帧已跑通；方案 B 的 `opencode serve` 健康检查、建会话、`prompt_async` 均已验证。
- opencode 只做"干活"的那一端：真正的 LLM、工具、权限都在它内部。桥/中继只是搬运 + 翻译。

## 二、方案 A（保留自己的手机控制台，端口 8999）

手机页还是 `relay-server/page.html`，中继还是哑管道，只是把"客户端"换成
`opencode_bridge.py`，它把 Local AI Studio 的协议翻译成 opencode 的 HTTP API。

### 组件与角色

| 组件 | 端口 | 说明 |
|---|---|---|
| `relay-server/main.py` | 8999 | 哑管道：`device_token` 白名单 + 双向转发 JSON 帧 |
| `relay-server/opencode_bridge.py` | —— | 桥：连 `/client`，把 `send/state/messages/models/...` 映射到 opencode HTTP + `/event` SSE |
| `relay-server/page.html` | 由 main.py 提供 | 手机控制台（不改动） |
| `opencode serve` | 9001 | 真正干活的后端（方案 A、B 共用） |

### 配置

1. `relay-server/config.json`（已 gitignore）：
   ```json
   { "listen": "127.0.0.1:8999", "device_tokens": ["<64位token 或测试用 testtoken123>"] }
   ```
2. `relay-server/opencode_bridge.json`（已 gitignore）：
   ```json
   {
     "relay":   { "server_url": "ws://127.0.0.1:8999", "device_token": "testtoken123",
                  "workspace": "/home/wellfuture/build/localaicoder", "mode": "always" },
     "opencode":{ "base_url": "http://127.0.0.1:9001", "default_model": "providerID/modelID", "password": "" }
   }
   ```
   - `default_model` 留空 = 用 opencode 配置里的默认模型；填 `provider/model` 会覆盖。
   - `server_url` 填 `https://你的域名` 也行，桥自动转 `wss://`。
   - `opencode.password` = `OPENCODE_SERVER_PASSWORD`（opencode serve 设了密码才填，否则留空免登陆；
     桥走 HTTP Basic 认证，用户名固定 `opencode`）。也可用环境变量 `OPENCODE_SERVER_PASSWORD`。
3. **（开箱配置）让手机端 readonly/ask 拦住写/改** —— 在 opencode 自身配置里把工具设为 `ask`，
   这样 opencode 才会发 `permission.asked` 给桥。粘贴到 `~/.config/opencode/opencode.jsonc`：
   ```jsonc
   {
     "$schema": "https://opencode.ai/config.json",
     "permission": { "edit": "ask", "bash": "ask", "webfetch": "ask" }
   }
   ```
   > ❗ 键必须是**顶层 `permission`**（不是 `agent.permission`）。已实测：配上 `permission.edit="ask"` 后，
   > 触发编辑会真正发出 `permission.asked` 交给桥。
   > 不配也行：opencode 默认 `build` agent 是 `{permission:"*", action:"allow"}`，写/改自动放行，
   > 手机端 `readonly/ask` 就拦不住（只有 `external_directory`/`doom_loop` 默认 `ask`）。
   > 上面的写法也一并放进 `opencode_bridge.json.example` 的 `_opencode_agent_permission_ref` 段作为参考。

### 启动

```bash
scripts/opencode-remote.sh start      # 起 9001 + 8999 + 桥
# 或分别：
opencode serve --hostname 127.0.0.1 --port 9001          # 在项目目录下跑
cd relay-server && python3 main.py -config config.json   # 8999
python3 relay-server/opencode_bridge.py --config relay-server/opencode_bridge.json
```

手机打开 `http://127.0.0.1:8999/s/?d=testtoken123`（本地）或 `https://你的域名/s/?d=<token>`（公网，配 Caddy）。

### 公网暴露（Caddy 终结 TLS）

把 Caddyfile 指向中继端口即可，`/s/`、`/s/ws`、`/client` 三个路由都由 main.py 提供：

```
你的域名 {
    reverse_proxy 127.0.0.1:8999
}
```

> ⚠️ 手机访问走了公网时，聊天内容对中继服务器可见——务必只信任自己的 VPS（同现有自建中继模型）。

### 桥做的协议翻译（对应 `docs/relay/protocol.md` 与 `desktop/relay.go`）

| 手机→桥 | opencode 对应 |
|---|---|
| `state` | `GET /session` + 计算 `workspace/mode/current/branch` |
| `messages {id}` | `GET /session/:id/message`（text/image/tool 部分拼成 `{role,text,images}`） |
| `models` | `GET /config/providers`（铺平成 `{key,model_id,display_name,is_current,vision,reasoning}`） |
| `send {session,text,atts}` | 确认/建会话 → `POST /session/:id/prompt_async`（body 含 `parts[text]`，图→`{type:file,url}`，`model` 必须传对象 `{providerID,modelID}`） |
| `stop` | `POST /session/:id/abort` |
| `delete/rename/open_session` | `DELETE|PATCH /session/:id` / 设当前会话并回发 `session:opened` |

| opencode 事件（`/event` SSE）→ 手机帧 | 说明 |
|---|---|
| `message.part.delta`(field=text) | → `text {delta}`（流式） |
| `message.part.updated`(type=tool) | → `tool {delta: 工具名}`（按 part id 去重） |
| `session.status` busy / `session.idle` | → `run:started` / `run:finished` + `done` |
| `session.updated`(tokens) | → `usage`（尽力而为） |
| `permission.asked` | 见下方「权限审批」 |

### 权限审批（readonly / ask / always → `/permissions/:id`）

opencode 在需要审批时发 `permission.asked`（含 `id`/`sessionID`/`permission`/`patterns`/`metadata`/`tool`）。桥按手机端权限模式处理：

| 手机权限模式 | 桥的动作 |
|---|---|
| `readonly` | 自动 `POST /session/:sid/permissions/:pid` → `{response:"reject"}`（不打扰手机） |
| `always` | 自动应答 → `{response:"always"}`（记住，总是允许） |
| `ask` | 转发 `permission_request` 给手机；手机点“允许/拒绝/总是允许”回 `permission_response`，桥映射为 `{response:"once"/"reject"/"always"}`；超时 `OPENCODE_BRIDGE_PERM_TIMEOUT`(默认 120s) 自动拒绝 |

> ⚠️ opencode 默认 `build` agent 的权限是 `{permission:"*", action:"allow"}`（写/改自动放行，
> 只有 `external_directory`/`doom_loop` 默认 `ask`）。因此 `readonly/ask` 要真正拦住写/改，
> 需在 opencode 配置里把对应工具设为 `ask`——**顶层 `permission`**（见上文「开箱配置」，不是
> `agent.permission`），让 opencode 发出 `permission.asked` 交给桥自动应答或转发手机。

### 凭证与安全（"账密"怎么办？）

这套链路有三层凭证，别混淆：

| 层 | 名称 | 在哪配 | 说明 |
|---|---|---|---|
| ① AI 模型账号 | provider API key | `~/.local/share/opencode/auth.json` / opencode 配置 | 调模型用的，你已配好，本机测试不用管 |
| ② opencode 服务密码 | `OPENCODE_SERVER_PASSWORD` | 启动 `opencode serve` 的环境变量 + 桥的 `opencode.password` | 保护 opencode 的 HTTP API/web。**不设=无鉴权**（仅限本地 `127.0.0.1`）；**公网/手机访问必须设**。客户端用 **HTTP Basic 认证**：用户名固定 `opencode`，密码=该值 |
| ③ 中继 device_token | `device_tokens` / `d=` | `relay-server/config.json` + 手机 URL `?d=` | 手机页的"口令"，本机测试用 `testtoken123`；token 即控制权 |

- **本机测试**：三者都不用额外配置——`opencode serve` 不设密码（只监听 127.0.0.1 安全），
  中继用 `testtoken123`，模型 key 已就绪。直接跑即可。
- **要对外/手机跨网**：opencode 端**必须**设 `OPENCODE_SERVER_PASSWORD`，并同步在桥配置
  `opencode.password`（或环境变量 `OPENCODE_SERVER_PASSWORD`），否则任何人都能调用你的模型。

## 三、方案 B（直接用 opencode 自身服务，端口 9001）

不写自定义控制台，直接用 opencode 自带的 CLI 服务 + 浏览器 UI。

```bash
# 在项目目录下起 headless 服务
opencode serve --hostname 127.0.0.1 --port 9001
# 或者起服务并打开自带 web 界面
opencode web --port 9001
```

- 健康检查：`curl http://127.0.0.1:9001/global/health` → `{"healthy":true,...}`
- 拿到服务后，就有现成的 HTTP API（`/session`、`/session/:id/message`、`/event` SSE、`/tui/*` 等）
  和 `opencode attach <url>` 可连任意已跑 server。
- 手机跨网访问：把 `9001` 用 Caddy 反代到公网 443（同上），再在浏览器打开即可。

```caddyfile
你的域名 {
    reverse_proxy 127.0.0.1:9001
}
```

> 方案 B 优点：几乎零改造，官方 UI 自带。缺点：失去手机控制台自研的"项目/会话双向同步、
> 任务步骤栏、用量状态栏、权限切换、/file 上传"等细节；这些在方案 A 里能最大程度保留。

## 四、已知限制 / 后续

- **权限模式已接入**：桥在 `permission.asked` 时按 `readonly/always/ask` 自动应答或转发手机审批，
  经 `POST /session/:id/permissions/:permissionID` 落地（见上）。但**要真正拦住写/改**，需把
  opencode agent 对应工具设成 `ask`（默认 allow-all，否则不会触发权限请求）。
- **会话模型**：opencode 会话=项目+会话(可父子)；桥直接把 opencode 会话 id 当手机会话 id，
  项目切换(workspace) 未做热切。
- **推理档位 effort**：opencode 没有与手机页一一对应的档位，桥忽略该字段。
- **图片**：手机上传的图按 `FilePartInput {type:file,url}` 给 opencode；展示按 `data:image` 判图。
- **reasoning/字数统计**：手机页没有 reasoning 展示槽，桥不转发该 part。

## 五、文件清单

| 文件 | 作用 |
|---|---|
| `relay-server/opencode_bridge.py` | 方案 A 桥（协议翻译） |
| `relay-server/config.8999.example.json` | 中继 8999 配置示例 |
| `relay-server/opencode_bridge.json.example` | 桥配置示例 |
| `scripts/opencode-remote.sh` | 一键起停（serve/relay/bridge） |
| `relay-server/config.json`、`opencode_bridge.json` | 本机配置（已 gitignore） |

## 六、常见故障

| 现象 | 原因 / 处理 |
|---|---|
| 手机页红条「连接断开 (code 1006)」 | **多个桥进程**在互抢中继同一个 `/client` 槽位（每次新桥一接入就把旧桥踢掉 → 抖动 → 手机 WS 被 1006）。处理：`scripts/opencode-remote.sh stop` 再 `start`（脚本已加"启动前清理残留桥"防复发）；或手动 `pkill -f opencode_bridge.py` 后只起一个。 |
| 手机页「正在连接桌面…」/ 模型为空 | 中继/桥/opencode 三个进程没齐：跑 `scripts/opencode-remote.sh status` 看健康；缺哪个补哪个。 |
| `opencode serve` 报 `ServeError` | 端口被占（常见：之前那个实例没停）。用 `scripts/opencode-remote.sh stop` 清掉，或确认 `9001` 空闲再起。 |
| `opencode serve` 一直提示未设密码 | 仅本机 `127.0.0.1` 可忽略；要对外请在环境变量设 `OPENCODE_SERVER_PASSWORD` 并在桥配 `opencode.password`。 |
