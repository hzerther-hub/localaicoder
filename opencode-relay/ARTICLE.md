[English](ARTICLE.en.md) | 中文

# 远程驱动家里 PC 的 opencode：中继、桥与 Nginx 的技术实现

> **最终目的：人在外面，用手机浏览器远程调用自己家里 PC 上的 opencode 干开发活。**
> 代码、运行环境、git 仓库、模型账号都在家里那台机器上——在外面也能让它改代码、
> 跑命令、看 diff、审批写操作、传截图识图、切模型切项目，和坐在 PC 前一样。
>
> 本文讲这套链路的实现：`service/`（部署到公网 VPS 的中继，前面挂 Nginx）与
> `pc/`（留在家里 PC 跑的桥）。使用与配置速查见 `README-opencode.md`、`config.md`。

---

## 一、要解决的问题

opencode 是"本地优先"的 AI 编码 agent：它要在项目目录里读写文件、跑 shell、走 git，
模型 key 也存在这台机器上。这些东西搬不进云端沙箱，所以**干活的那一端必须是你自己
的机器**。问题在于：

1. 家里 PC 在 NAT/防火墙后面，没有公网 IP，外面的设备连不进来；
2. 你也不该把它暴露到公网——公网扫描、弱口令爆破都是现实威胁；
3. 手机上没有得体的终端，更跑不了 opencode 本身。

解法是一个经典折中：**租一台便宜的 VPS 当"传话站"，家里 PC 主动出站连上去，
手机连 VPS，VPS 按凭据把两边对号转交**。这样家里 PC 保持零入站端口（路由器不用
做任何映射），公网上只有一台你完全掌控的服务器。

## 二、总体架构

```
┌──────────┐  HTTPS/WSS   ┌──────── 公网 VPS ────────┐   出站 WS   ┌── 自己的服务器（家里 PC）──┐
│ 手机浏览器 │ ──────────> │ Nginx(443) ──> 中继       │ <───────── │ 桥 pc/opencode_bridge.py │
│ page.html │             │             service/main.py│            │   │ HTTP + SSE           │
└──────────┘              │             (127.0.0.1:9000)│           │   ▼                      │
                          └──────────────────────────┘            │ opencode serve           │
                                                                   │ (127.0.0.1:9001)         │
                                                                   └──────────────────────────┘
```

四类角色、四段链路：

| 角色 | 跑在哪 | 职责 |
|---|---|---|
| 手机浏览器 | 外网 | 手机控制台（`service/page.html`，由中继直接提供） |
| Nginx | VPS | 唯一公网入口：终结 TLS、反代、透传 WebSocket |
| 中继 `service/main.py` | VPS（只绑 127.0.0.1:9000） | **哑管道**：校验 `device_token` 并双向转发 JSON 帧，不理解内容 |
| 桥 `pc/opencode_bridge.py` + `opencode serve` | **自己的服务器（家里 PC）** | 桥：协议翻译（手机帧 ↔ opencode HTTP/SSE）；opencode：真正干活（LLM、工具、文件） |

一次典型交互的完整路径：

```
手机发"帮我重构 foo.py"
  → WSS /s/ws?d=<token>        （经 Nginx 到中继）
  → 中继按 token 单转给该设备的桥（WS /client）
  → 桥翻译成 POST /session/:id/prompt_async，发给自己 PC 上的 opencode
  → opencode 流式产出 → 桥订阅 GET /event（SSE）逐条收到
  → 桥翻译成手机帧（text / tool_start / todo / usage …）
  → 中继按 token 广播给所有手机 → 手机逐字渲染
```

关键点：**家里 PC 对公网是零入站**——桥启动时主动连 VPS，这条出站 WebSocket 就是
控制信道；家里路由器不需要任何端口映射。**VPS 上没有业务逻辑**——它只认识 token，
不认识 opencode。

## 三、两个核心概念：中继与哑管道

**中继（relay）**：架在公网上的"传话站"。手机与家里 PC 谁也直接找不到谁，于是双方
都主动连到这台中间服务器，由它按 `device_token` 把消息对号转交。一个 token 代表一台
受控设备（家里 PC），持同一 token 的手机都能看到这台设备的屏幕；token 即控制权。

**哑管道（dumb pipe）**：形容这个中继"哑"在哪——对转发的内容**不解析、不缓存、
不产生**：

| "哑"体现 | 具体行为 | 换来的好处 |
|---|---|---|
| 不解析 | 收到 JSON 帧原样转发，不理解帧里是聊天文本、模型列表还是权限审批 | 换被控端（opencode 换别的 CLI）只改桥，中继零改动；公网组件没有可攻击的业务面 |
| 不缓存 | 帧转出即忘，桥断线窗口内的帧直接丢 | 无状态，进程随便重启；丢帧由桥端"断线补偿"兜底（见第六节） |
| 不产生 | 只做三件事：校验 token → 查设备注册表 → 转发 | 逻辑越少越不会坏；整个服务端只有一个 ~300 行的 `main.py` |

所以分工是：**中继只认识 token，不认识 opencode**；所有与 opencode 有关的"翻译"
都发生在家里 PC 的桥上。这也是为什么手机帧协议能与另一套实现（Local AI Studio
桌面端 `desktop/relay.go`）保持同源——两端共享同一份帧协议，服务端却是完全独立的
两份代码。

## 四、仓库结构：service/ 与 pc/

代码按**部署位置**一分为二，部署时各拷各的：

```
opencode-relay/
├── service/                  # ── 服务端：整个目录 scp 到 VPS ──
│   ├── main.py               # 中继本体（FastAPI + uvicorn，哑管道）
│   ├── page.html             # 手机控制台（无构建单页）
│   ├── css/ js/              # 控制台静态资源（中继白名单直服）
│   ├── config.json.example   # 中继配置模板（listen + device_tokens）
│   ├── requirements.txt      # fastapi / uvicorn / websockets
│   └── nginx.conf.example    # Nginx 反代配置（带逐条注释）
├── pc/                       # ── 本机端：留在家里 PC ──
│   ├── opencode_bridge.py    # 桥（协议翻译，~1200 行）
│   └── opencode_bridge.json.example  # 桥配置模板
├── README-opencode.md        # 使用与配置速查
├── config.md                 # 全部配置项 + 部署步骤
└── ARTICLE.md                # 本文
```

机器级配置（`service/config.json`、`pc/opencode_bridge.json`，都含 token）已 gitignore，
仓库里只有 `.example` 模板。

## 五、中继的实现（service/main.py）

### 5.1 设备注册表与转发语义

核心数据结构是"每 token 一条"的注册表，惰性创建：

```python
devices: dict[str, dict] = {}      # token -> {"client": WebSocket|None,
dev_lock = asyncio.Lock()          #         "phones": set[WebSocket], "lock": asyncio.Lock()}
```

转发规则只有两条，对偶地覆盖两个方向：

- **上行（桥 → 手机）：广播 1 → N。** `/client` 收到一帧，遍历发给该 token 下所有
  `/s/ws` 手机——同一台 PC，可以有多部手机同时围观/遥控；
- **下行（手机 → 桥）：单转 N → 1。** `/s/ws` 收到一帧，只发给该 token 当前的唯一桥。

两个方向都**不 parse JSON**：转发语义就是"这台设备的屏幕，所有持 token 的手机都看
得到；任何手机的操作，都直达这台设备"。每台设备一把 `asyncio.Lock` 保护 `client`
槽位与 `phones` 集合，广播时 `list()` 拷贝再迭代，避免发送失败清理集合时踩迭代器。

### 5.2 槽位管理：把错误码变成可读提示

WebSocket 的 close code 被当成轻量信令用，手机端据此显示人话：

| 场景 | 动作 | 手机端表现 |
|---|---|---|
| token 不在白名单 | close **1008**（policy violation） | 红条「连接断开 (code 1008)」→ 检查 URL 里的 `?d=` |
| 桥未上线，手机先连 | close **1008**（拒绝而非挂起等待） | 提示先在家里 PC 把桥跑起来 |
| 新桥接入，同 token 旧桥还在 | 踢旧桥：close **1000** | 旧桥静默重连，用户无感 |

"踢旧桥"是踩过坑后的设计：两个桥进程互抢同一槽位会来回把对方踢下线，手机端表现为
反复 1006 重连（见 `config.md` 常见故障第 1 条）。与其放任互踢，不如把"一个 token
只保留一个客户端"定为语义——新连接赢，旧连接退出。清理时只 `dev["client"] is ws`
才置空，防止误清新桥的槽位。

### 5.3 静态资源：三层防护 + 现读现发

`GET /s/?d=<token>` 返回 `page.html`（token 校验失败一律 403，页面本身不含 token）。
静态资源路由 `GET /s/{sub}/{name}` 做了三层防护：

1. `sub` 只允许 `css/js/images` 三个白名单目录（单层，无子目录）；
2. 文件名内禁止 `/ .. \`（防路径穿越，如 `css/../../main.py`）；
3. 后缀必须在 `STATIC_TYPES` 白名单内（css/js/图片，拒绝 `.py` `.json` 等被下载）。

每次请求现读磁盘、带 `Cache-Control: no-cache`——改前端文件刷新即生效，部署迭代
不需要重启中继。

### 5.4 其他工程细节

- **单帧上限**：uvicorn `ws_max_size` 默认 5MB（环境变量 `RELAY_WS_MAX` 调整），
  是为手机传图（base64 后膨胀 1.3 倍）留的量；调小可降低滥用风险。
- **安全缺省**：`listen` 缺省 `127.0.0.1:9000`，`device_tokens` 缺省空列表
  （空白名单 = 拒绝一切接入）——配置缺失时宁可起不来，不可裸奔。
- **日志克制**：只打接入/断开事件，且 token 只留前 6 位；uvicorn 访问日志关闭。
- `/docs` `/redoc` 显式关闭——公网入口不暴露任何多余的面。

## 六、桥的实现（pc/opencode_bridge.py）

桥是全链路里唯一的"聪明"组件：一头对中继说手机帧协议，一头对 opencode 说 HTTP/SSE。
它不跑 agent——会话管理、工具执行、权限裁决全部由 opencode 提供，桥只做翻译。

### 6.1 并发模型：三条流

```
asyncio 事件循环（主协程）
 ├─ _relay_loop   与中继的 WS 收发 + 指数退避重连（1s 起，30s 封顶，连上即复位）
 └─ _event_pump   消费 SSE 队列 → translate_event 翻译 → 下行
SSE 守护线程 _sse_thread
 └─ requests 流式读 GET /event（阻塞 IO，不能进事件循环）
    → 每帧 json.loads → loop.call_soon_threadsafe(evq.put_nowait, ev) 投回主循环
asyncio.to_thread 线程池
 └─ 所有对 opencode 的阻塞 HTTP 调用（requests）都丢这里，不卡事件循环
```

`run()` 里 `asyncio.gather(_relay_loop(), _event_pump())`：任一主协程真正崩溃就让
进程退出，交给 systemd 拉起——不做内部静默重生，故障要显性化。

### 6.2 上行：指令分发表

手机帧进入 `_on_phone_frame`，一张 if/el 分发表映射到 opencode 端点（节选）：

| 手机帧 | opencode 调用 |
|---|---|
| `send {session,text,atts}` | 确认/建会话 → `POST /session/:id/prompt_async`（核心路径，见 6.3） |
| `stop` | `POST /session/:id/abort` + 本地补发 `run:finished` 立即收尾 UI |
| `state` | `GET /session` + git 分支 + 会话列表 → `state` 帧 |
| `messages {id}` | `GET /session/:id/message`，拼成 `{role,text,images}` |
| `models` / `model` | `GET /config/providers` 铺平 / 记录当前模型 |
| `mode {value}` | 切权限模式（只改桥侧状态） |
| `permission_response` | 映射 `allow/deny/always → once/reject/always` → `POST /session/:sid/permissions/:pid` |
| `workspace` / `dir_list` / `new_session` | 切工作区 / 浏览子目录（`GET /file?directory=`）/ 按目录建会话 |
| `commands` / `command` | `GET /command` 下发真实命令清单 / 执行（见 6.5） |
| `question_reply` / `question_reject` | `POST /question/:id/reply|reject`（回答 opencode 的提问） |

任何分支抛异常都不许弄断连接：捕获后转 `error` 帧回手机。

### 6.3 `send`：核心翻译路径

1. **附件分流**（对齐桌面端 `desktop/relay.go`）：`data:image` 开头的附件 → opencode
   原生多模态 `FilePartInput {type:file,mime,filename,url}` 直传；其他文件 → base64
   解码落盘到 `<工作区>/media/<纳秒时间戳>-<文件名>`（`basename` 防穿越），把绝对路径
   附进 prompt 正文，让 agent 自己用工具去读；
2. **无会话兜底**：`sid` 为空或已被删 → 先在当前工作区建会话（标题取消息前 40 字）；
3. **先回显后提交**：先发 `user_message` 帧让手机端立即显示（显示文本带 📎 附件名，
   发给模型的 prompt 不带），再 POST `prompt_async`——它是异步端点，提交即返回，
   后续产出全部经 `/event` SSE 流回来。

### 6.4 下行：SSE 事件 → 手机帧

`translate_event` 把一条 SSE 事件翻成 0..n 条手机帧。两个值得说的细节：

- **"沿"检测**。`session.status` 事件在 agent 每一步都会触发，但手机端只需要
  "开跑/跑完"两个沿：`running_map[sid]` 记住忙/闲状态，只在 空闲→忙 发一次
  `run:started`，忙→空闲 发 `run:finished` + `done`，避免多步执行反复重置 UI；
- **工具 part 去重**。opencode 对同一个工具 part 会连发多次 `message.part.updated`
  （pending/running/completed/error），桥用 `_tool_seen` 表按 part id 记账：
  首次见发 `tool_start`，completed/error 发 `tool_result`（输出截到末尾 900 字），
  中间态一律吞掉。

其余映射：`message.part.delta(field=text)` → `text`（流式增量）；`session.updated`
的 tokens → `usage`；`session.todo` → `todo`（真实任务清单直通手机端步骤栏）；
`file/storage/lsp` 等无关事件直接丢弃。

### 6.5 权限三态与 fail-closed

opencode 在执行写操作前发 `permission.asked`（前提：opencode 配置里把对应工具设为
顶层 `permission: ask`）。桥按手机端权限模式分流：

| 模式 | 桥的动作 |
|---|---|
| `readonly` | 自动 `POST …/permissions/:pid → {response:"reject"}`，不打扰手机 |
| `always` | 自动应答 `{response:"always"}` |
| `ask` | 转发 `permission_request` 给手机；`PERM_TIMEOUT`（默认 120s）无应答**自动拒绝** |

三处 fail-closed 原则：超时默认拒；手机回传未知响应值一律映射 `reject`；所有应答
（自动/超时/人工）汇入唯一出口 `_answer_permission`，天然幂等。**遥控场景的铁律：
宁可停下来等确认，绝不替用户默认放行。**

### 6.6 断线补偿：哑管道的代价与兜底

中继不缓存帧，桥断线窗口里产生的下行帧就永久丢了。桥的兜底不是重放（它也没存），
而是**通知手机重拉全量**：

```python
self._missed: set[str]   # 断线期间错过帧的会话 id
# _send() 发现 ws 为空/发送失败：把该帧所属会话记入 _missed
# _relay_loop() 重连成功、hello 握手之后：
for msid in missed:
    await self._send({"type": "session:opened", "id": msid})  # 手机端收到即重拉该会话消息
```

弱一致性换来的是两端都无状态：中继不用持久化，桥不用记帧，最终一致由"重拉"达成。
配合 1s→30s 指数退避重连、20s WS 心跳，以及手机端 3s 自动重连 + 8s `state` 轮询，
整个链路在任何一段抖动后都能自愈。

### 6.7 目录作用域：一个 serve 服务多个项目

opencode 1.x 把会话/文件/命令都挂在"项目目录"命名空间下。桥用 `scoped()` 给所有
会话级调用追加 `?directory=`，并用 `_dir_by_sid` 记住每个会话的出生目录——手机切了
工作区，也不会误伤其他目录下的会话。手机端还能 `dir_list` 浏览子目录、`new_session`
按目录一键开聊（对应"换个项目开发"的遥控需求）。

## 七、手机控制台（service/page.html + js/main.js）

前端刻意保持"无构建、单文件可审计"：原生 JS 一个 WS + 一份 DOM，没有框架与打包。
几个实现要点：

- **请求-应答配对**：每帧带自增 `rid`，`req()` 返回 Promise 存进 `pend` 表，9s 超时；
  事件类帧（无 rid）走另一条 `onmessage` 分发链。
- **流式渲染**：`text` 帧的 delta 累积进缓冲，整段重新过 `mdRender()`（先 HTML 转义、
  再渲染白名单语法：标题/列表/行内代码/围栏代码块/表格）——**先转义再渲染**，从
  根上杜绝模型输出注入 HTML/XSS。
- **过程可视化**：`tool_start/tool_result` 渲染成可展开卡片（参数抽 `path/cmd/query`
  等关键字段做单行摘要）；`todo` 帧驱动任务步骤栏（done/run/wait/deny 四态）；
  多会话并行时顶部出现"运行中"横幅，点击即切换围观。
- **图片**：选择/粘贴/拖拽进来的图片经 canvas 压到 1280px、JPEG 0.8 再转 dataURL
  上传（`/file` 斜杠命令直达）；消息里的图片缩略图 + lightbox 放大。
- **连接自愈**：`onclose` 3 秒后自动重连并清屏重拉；每 8 秒轮询一次 `state` 兜底。

## 八、Nginx：公网的唯一入口

中继只绑 `127.0.0.1:9000`，公网只暴露 Nginx 的 80/443。完整带注释配置见
`service/nginx.conf.example`，核心是一组"少一条就出经典故障"的指令：

```nginx
location / {
    proxy_pass         http://127.0.0.1:9000;
    proxy_http_version 1.1;
    proxy_set_header   Upgrade    $http_upgrade;   # ① WebSocket 升级
    proxy_set_header   Connection "upgrade";
    proxy_set_header   Host       $host;
    proxy_buffering    off;                        # ② 流式
    proxy_read_timeout 3600s;                      # ③ 长连接
    proxy_send_timeout 3600s;
}
```

| 指令 | 缺了会怎样 |
|---|---|
| ① `Upgrade/Connection` + HTTP/1.1 | WS 升级握手失败，手机页反复「连接断开 (code 1006)」；`/s/ws` 与 `/client` 两条 WS 都走这里 |
| ② `proxy_buffering off` | Nginx 攒满 buffer 才吐给浏览器，手机端"转圈半天、回复一次性蹦出来"；方案 B 直出官方 UI 时 `/event` SSE 同理 |
| ③ `proxy_read_timeout 3600s` | 默认 60s 掐断一切安静的长连接：挂着不说话的手机页、桥与中继间的 WS 都会被周期性断开 |

配套部署件：80 端口 301 跳 HTTPS；`client_max_body_size` 管住普通请求体（WS 帧大小
另由中继 `RELAY_WS_MAX` 管）；中继用 systemd 常驻（`Restart=always` +
`NoNewPrivileges/PrivateTmp`）。方案 B 的变体——SSH 反向隧道直出官方 opencode UI——
复用同一套 Nginx 指令，见 `TUNNEL-3STEPS.md`。

## 九、安全模型：三层凭证，各管一段

| 层 | 凭证 | 在哪 | 管什么 |
|---|---|---|---|
| ① 模型账号 | provider API key | 家里 PC：`~/.local/share/opencode/auth.json` | 调 LLM 用的钱袋子，**永远不出内网**——中继/手机只见对话，不见 key |
| ② opencode 服务密码 | `OPENCODE_SERVER_PASSWORD` | 家里 PC 环境变量 + 桥配置 | 保护 9001 的 HTTP API；桥代持（HTTP Basic，用户名固定 `opencode`）。不对外就免设 |
| ③ 设备令牌 | `device_token`（`openssl rand -hex 32`） | VPS 白名单 + 桥配置 + 手机 URL `?d=` | 中继的唯一鉴权；**token 即控制权**，泄露立即轮换 |

再叠加几条默认拒绝：中继空白名单拒一切接入、`listen` 只绑回环、静态资源三层白名单、
权限应答 fail-closed。还有一条必须写明的边界：**中继能看到聊天明文帧**（哑管道只"哑"
在不懂业务，不"瞎"）——所以它必须部署在你自己的 VPS 上，别用任何第三方内网穿透。

## 十、边界与演进

- **帧不重放**：断线补偿是"重拉"不是"重放"，超大历史会话重连后有一次全量拉取成本；
- **单 token 单设备**：一台 PC 一个 token；要遥控多台机器，在白名单加条目即可
  （协议已按 token 隔离，`desktop/relay.go` 的桌面端就是第二个消费者）；
- **推理档位 effort**：opencode 无对应概念，桥 v1 忽略；reasoning 文本手机页无展示槽，
  不转发；
- **演示级 vs 生产级**：整套服务端无数据库、无持久化，重启即回到初始态——这是"哑
  管道 + 无状态桥"设计选择的直接结果，也是它运维成本几乎为零的原因。

---

*代码：`service/`（VPS）+ `pc/`（家里 PC）；上手：`README-opencode.md`；配置细则：`config.md`；
官方 UI 直出方案：`TUNNEL-3STEPS.md`。*
