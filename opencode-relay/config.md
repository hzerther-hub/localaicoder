# opencode-relay 配置说明（config.md）

> 通过中继在公网（如 `op.mei.biz`）远程驱动本机 opencode 的**全部配置项说明**。
> 三个组件各有一份配置，改完都要**重启对应进程**才生效。

## 一、组件与端口总览

```
手机浏览器 ──HTTPS──> op.mei.biz 上的中继(哑管道) <──出站WS── 本机桥 ──HTTP/SSE──> 本机 opencode serve
```

| 组件 | 跑在哪 | 端口 | 启动方式 | 配置文件 |
|---|---|---|---|---|
| `opencode serve` | 本机 | 9001 | `opencode serve --hostname 127.0.0.1 --port 9001`（在目标项目目录下跑） | `~/.config/opencode/opencode.jsonc` + `~/.local/share/opencode/auth.json` |
| 中继 `main.py` | 公网 VPS（或本机测试） | VPS 绑 `127.0.0.1:9000`；本机测试绑 `127.0.0.1:8999` | `python3 main.py -config config.json` | 同目录 `config.json` |
| 桥 `opencode_bridge.py` | 本机 | 无（纯出站连接） | `python3 opencode_bridge.py --config opencode_bridge.json` | 同目录 `opencode_bridge.json` |

- 中继是**哑管道**：只校验 `device_token` 并转发 JSON 帧，不解析业务；手机页 `page.html`、静态 `css/ js/` 由它提供。
- 桥是**出站连接**：本机无需对公网开任何端口；它一头连中继 `/client`，一头调本机 opencode HTTP API。

## 二、中继配置 `config.json`

启动：`python3 main.py -config config.json`（默认读同目录 `config.json`）。

```json
{
  "listen": "127.0.0.1:9000",
  "device_tokens": ["<64位随机hex>"]
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `listen` | string | 绑定地址。**VPS 上必须 `127.0.0.1:9000`**（公网入口只留 Nginx 443）；本机测试用 `127.0.0.1:8999`。裸 `0.0.0.0` 等于不设防，禁止 |
| `device_tokens` | string[] | **白名单**：中继只认列出的 token。一个 token = 一台设备；桌面（桥 `/client`）与手机（`/s/?d=`、`/s/ws`）用的是**同一个 token**。生成：`openssl rand -hex 32` |

环境变量：

| 变量 | 默认 | 说明 |
|---|---|---|
| `RELAY_WS_MAX` | `5` | 单帧 WS 上限（MB），调大可传更大的图片 base64 |

路由（由中继提供，手机/桥都走这些）：

| 路径 | 用途 |
|---|---|
| `GET /s/?d=<token>` | 手机控制台页面 |
| `GET /s/css/*`、`GET /s/js/*`、`GET /s/images/*` | 页面静态资源（白名单后缀 + 拒绝路径穿越） |
| `WS /s/ws?d=<token>` | 手机端 WebSocket |
| `WS /client?d=<token>` | 桥（客户端）出站接入 |

## 三、桥配置 `opencode_bridge.json`

启动：`python3 opencode_bridge.py --config opencode_bridge.json`（默认读同目录该文件）。

```json
{
  "relay": {
    "server_url": "https://op.mei.biz",
    "device_token": "<与中继白名单一致的64位token>",
    "workspace": "/home/wellfuture/build/localaicoder",
    "mode": "always",
    "insecure": false
  },
  "opencode": {
    "base_url": "http://127.0.0.1:9001",
    "default_model": "",
    "password": ""
  }
}
```

### `relay` 段

| 字段 | 默认 | 说明 |
|---|---|---|
| `server_url` | （必填） | 中继地址。`https://` 自动转 `wss://`；本机测试填 `http://127.0.0.1:8999`（转 `ws://`）。公网填 `https://op.mei.biz` |
| `device_token` | （必填） | 与中继 `device_tokens` 白名单**完全一致** |
| `workspace` | 当前目录 | 初始工作区。手机端发起的「新建会话」「工作区切换」会实时更新它 |
| `mode` | `always` | 初始权限模式 `readonly / ask / always`；手机端可随时切换 |
| `insecure` | `false` | `true` 时跳过 TLS 证书校验（仅自签证书调试用） |

### `opencode` 段

| 字段 | 默认 | 说明 |
|---|---|---|
| `base_url` | `http://127.0.0.1:9001` | 本机 opencode serve 地址 |
| `default_model` | 空 | 空=用 opencode 自己的默认模型；填 `providerID/modelID`（如 `deepseek/deepseek-v4-pro`）则覆盖。手机端切模型会实时更新 |
| `password` | 空 | opencode 设了 `OPENCODE_SERVER_PASSWORD` 才填；桥走 HTTP Basic（用户名固定 `opencode`）。也可用环境变量 `OPENCODE_SERVER_PASSWORD` |

### 命令行参数（可覆盖配置文件同名项）

```
--config  --relay  --token  --workspace  --mode
--opencode  --model  --password  --insecure
```

## 四、opencode serve 侧（本机）

```bash
cd /home/wellfuture/build/localaicoder     # ← 项目目录决定 opencode 的工作区
opencode serve --hostname 127.0.0.1 --port 9001
```

- `~/.config/opencode/opencode.jsonc`：
  ```jsonc
  {
    "permission": { "edit": "ask", "bash": "ask" }   // 顶层 permission（非 agent.permission）
  }
  ```
  设了 `ask`，opencode 才会发 `permission.asked`，桥才能按 `readonly/ask/always` 自动应答或转发手机审批。
- 模型账号：`~/.local/share/opencode/auth.json`（`opencode providers` 管理）。
- `OPENCODE_SERVER_PASSWORD`：给 opencode HTTP API 设密码；设了就必须同步填桥的 `opencode.password`。

## 五、部署到 op.mei.biz（方案一 · 有公网 VPS）

代码按两端分目录：`service/`（部署到 VPS 的中继服务端）与 `pc/`（留在本机的桥）。

```bash
# 本机：生成 token 并把服务端目录拷到 VPS
openssl rand -hex 32
scp -r opencode-relay/service root@VPS_IP:/opt/opencode-relay
```

```bash
# VPS：依赖 + 配置
cd /opt/opencode-relay && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
cat > config.json <<'EOF'
{ "listen": "127.0.0.1:9000", "device_tokens": ["<64位token>"] }
EOF
```

Nginx（`/etc/nginx/conf.d/opencode-relay.conf`，终结 TLS + 反代 + WebSocket/SSE 透传；
完整带注释版见 `service/nginx.conf.example`）：

```nginx
server {
    listen 443 ssl;
    server_name op.mei.biz;
    ssl_certificate     /etc/ssl/certs/op.mei.biz.pem;        # 证书按实际签发路径
    ssl_certificate_key /etc/ssl/private/op.mei.biz.key;
    location / {
        proxy_pass         http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;   # WebSocket 必需（手机页 + 桥两条 WS）
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;                     # 流式回复必需，缓冲会吞掉流式
        proxy_read_timeout 3600s;                   # 默认 60s 会掐断长连接
        proxy_send_timeout 3600s;
    }
}
```

> Caddy 亦可：`op.mei.biz { reverse_proxy 127.0.0.1:9000 }`，同样自动透传 WebSocket。

systemd（`/etc/systemd/system/opencode-relay.service`）：

```ini
[Unit]
Description=opencode relay (dumb pipe)
After=network-online.target
[Service]
WorkingDirectory=/opt/opencode-relay
ExecStart=/opt/opencode-relay/.venv/bin/python main.py -config config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
```

`systemctl daemon-reload && systemctl enable --now opencode-relay`

最后本机把桥的 `server_url` 改成 `https://op.mei.biz` 并重启桥，手机开 `https://op.mei.biz/s/?d=<token>`。

## 六、本机一键起停（测试用）

```bash
scripts/opencode-remote.sh start    # opencode:9001 + opencode-relay:8999 + 桥（幂等，起前清理残留桥）
scripts/opencode-remote.sh status   # 健康检查
scripts/opencode-remote.sh stop
```

## 七、地址速查

| 场景 | 地址 |
|---|---|
| 手机控制台（本机测试） | `http://127.0.0.1:8999/s/?d=<token>` |
| 手机控制台（公网） | `https://op.mei.biz/s/?d=<token>` |
| opencode 官方 web（本机） | `http://127.0.0.1:9001`（`/server/<base64>/session/<id>` 为会话详情页） |

## 八、安全清单

- [ ] 公网**禁用** `testtoken123` 之类弱 token；用 `openssl rand -hex 32`
- [ ] token 即控制权：三处（VPS 白名单 / 桥 / 手机 URL）一致，泄露立即轮换
- [ ] VPS 只开 80/443，中继绑 `127.0.0.1`；中继能看到聊天明文，必须是自己的服务器
- [ ] opencode 若对外开放，必须设 `OPENCODE_SERVER_PASSWORD`

## 九、常见故障

| 现象 | 原因 / 处理 |
|---|---|
| 手机页红条「连接断开 (code 1006)」 | 多个桥进程互抢 `/client` 槽位。`scripts/opencode-remote.sh stop && start`（脚本起前自动清残留桥） |
| 页面能开但**无样式** | 中继进程是旧代码（无 `/s/{sub}/{name}` 静态路由）。重启中继进程 + 浏览器强刷 |
| `opencode serve` 报 `ServeError` | 端口被占，或两个 opencode 共用同一数据目录互相锁。`pkill -f "opencode serve"` 后确认 9001 空闲再起 |
| 发消息没反应 | 未选中会话（前端 `sid` 为空直接忽略发送）。先在会话列表点开一个会话 |
| 手机端 `readonly/ask` 拦不住写操作 | opencode 配置顶层 `permission.edit/bash = "ask"` 未设置（默认全部 allow，不会产生权限请求） |
| 桥连上中继但一直报 `/event` 断开重连 | opencode(9001) 没起或刚重启，桥每 3s 自动重订阅；确认 `curl 127.0.0.1:9001/global/health` |
