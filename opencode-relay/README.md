# Switchboard

**English** | [中文](#中文)

---

> Drive the **opencode** running on your home PC from your phone — from anywhere.

Switchboard is a tiny two-piece system for remote AI development. Your code, environment,
git repos and model keys all live on your home machine — so the agent that "does the work"
must run there. Switchboard makes that machine reachable from any browser, without opening
a single inbound port on your home network:

- **Remotely use opencode for real development** — send prompts, watch streaming replies
  and tool calls, upload screenshots, approve file writes, switch models/projects.
- **Zero inbound ports at home.** The bridge on your home PC makes an *outbound*
  WebSocket connection to your VPS; NAT and firewalls stay untouched.
- **A dumb-pipe relay on the VPS.** The relay only checks a device token and forwards
  JSON frames — it neither parses nor stores your traffic, and it knows nothing about
  opencode. Swap the controlled backend and only the bridge changes.

## Architecture

```
┌────────────┐  HTTPS/WSS   ┌───────── Public VPS ─────────┐   outbound WS   ┌──── Your server (home PC) ────┐
│   Phone    │ ──────────> │  Nginx (443)                  │ <───────────── │  bridge  pc/opencode_bridge.py │
│  browser   │             │    └─> relay service/main.py  │                │    │ HTTP + SSE                │
└────────────┘             │        (127.0.0.1:9000)       │                │    ▼                           │
                            └───────────────────────────────┘                │  opencode serve               │
                                                                              │  (127.0.0.1:9001)             │
                                                                              └───────────────────────────────┘
```

| Piece | Runs on | Role |
|---|---|---|
| Phone console (`service/page.html`) | Anywhere | Single-page console served by the relay — no build step |
| Nginx | VPS | The only public entry: TLS + reverse proxy + WebSocket/SSE passthrough |
| Relay (`service/main.py`) | VPS, loopback only | **Dumb pipe**: validates the device token and forwards frames (broadcast up, single-forward down) |
| Bridge (`pc/opencode_bridge.py`) + `opencode serve` | **Your server (home PC)** | Bridge translates phone frames ↔ opencode HTTP/SSE; opencode does the actual work |

Why "dumb pipe"? The relay is deliberately mute — it **never parses, never caches, never
produces** content. It only routes frames by `device_token`. That keeps the public attack
surface logic-free, makes the relay reusable for any backend, and pushes all translation
logic into the bridge on your own machine.

## Repository layout

```
switchboard/
├── service/            # deploy to the VPS
│   ├── main.py         # relay (FastAPI, ~300 lines)
│   ├── page.html, css/, js/   # phone console
│   ├── config.json.example
│   ├── requirements.txt
│   └── nginx.conf.example     # annotated Nginx site config
├── pc/                 # stays on your home PC
│   ├── opencode_bridge.py     # protocol-translating bridge
│   └── opencode_bridge.json.example
├── ARTICLE.md          # technical deep-dive (how it all works)
├── README-opencode.md  # usage guide (Chinese)
├── config.md           # every config field + deployment steps (Chinese)
└── TUNNEL-3STEPS.md    # variant: expose the official opencode UI via SSH tunnel
```

## Quick start

**Home PC** (needs [opencode](https://opencode.ai) installed):

```bash
opencode serve --hostname 127.0.0.1 --port 9001
cp pc/opencode_bridge.json.example pc/opencode_bridge.json   # fill in relay URL + token
python3 pc/opencode_bridge.py --config pc/opencode_bridge.json
```

**VPS**:

```bash
scp -r service/ root@YOUR_VPS:/opt/switchboard
cd /opt/switchboard && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
cp config.json.example config.json    # listen: 127.0.0.1:9000, token: openssl rand -hex 32
# install nginx.conf.example, then run under systemd (unit in config.md)
```

Open `https://your.domain/s/?d=<token>` on your phone. Full deployment walkthrough:
[config.md](config.md).

## Security model

Three credentials, each guarding one segment:

| Credential | Where it lives | What it protects |
|---|---|---|
| Provider API key | Home PC (`opencode` auth) | Your LLM billing — never leaves the home network |
| `OPENCODE_SERVER_PASSWORD` | Home PC + bridge config | opencode's HTTP API (bridge holds it via HTTP Basic) |
| Device token (`openssl rand -hex 32`) | VPS whitelist + bridge + phone URL | The relay's only authentication — **the token is the control channel**; rotate on leak |

Plus the ground rules: the relay binds to loopback only, an empty token whitelist rejects
everything, permission requests fail closed (timeout ⇒ deny), and — since the relay sees
plaintext frames — **run it on a VPS you trust**, i.e. your own.

## Documentation

- [ARTICLE.md](ARTICLE.md) — how and why it works: relay, dumb pipe, bridge internals,
  streaming/permission/reconnect design, Nginx directives that prevent classic failures
- [config.md](config.md) — all configuration fields, systemd + Nginx deployment, troubleshooting
- [TUNNEL-3STEPS.md](TUNNEL-3STEPS.md) — alternative: expose the official opencode web UI
  through an SSH reverse tunnel behind the same Nginx setup

---

<a id="中文"></a>

# Switchboard（中文说明）

> **最终目的：人在外面，用手机浏览器远程调用自己家里 PC 上的 opencode 干开发活。**

代码、运行环境、git 仓库、模型账号都在家里那台机器上——所以"干活的那一端"必须是它。
Switchboard 让这台机器可以被任意浏览器遥控，而且**家里网络不用开任何入站端口**：
家里 PC 上的桥主动出站连接 VPS，NAT/防火墙零改动；VPS 上的中继只校验 `device_token`
并转发 JSON 帧——不解析、不缓存、不理解内容，是一个纯粹的**哑管道**。

手机端能做的：发需求、看流式回复与工具调用过程、传截图识图、审批写文件/命令、
切换模型与项目目录、跑斜杠命令、看 diff。

## 架构

| 组件 | 跑在哪 | 职责 |
|---|---|---|
| 手机控制台（`service/page.html`） | 任意浏览器 | 中继直供的单页应用，无构建 |
| Nginx | 公网 VPS | 唯一公网入口：TLS + 反代 + WebSocket/SSE 透传 |
| 中继（`service/main.py`） | VPS（只绑 127.0.0.1） | **哑管道**：校验 token，上行广播 1→N、下行单转 N→1 |
| 桥（`pc/opencode_bridge.py`）+ `opencode serve` | **自己的服务器（家里 PC）** | 桥做协议翻译（手机帧 ↔ opencode HTTP/SSE）；opencode 真正干活 |

分工一句话：**中继只认识 token，不认识 opencode**；所有与 opencode 有关的翻译都发生
在家里 PC 的桥上。因此换掉被控端（opencode 换别的 CLI）只需改桥，中继零改动。

## 目录结构

```
service/   # 部署到 VPS：中继 main.py + 手机控制台 + Nginx 配置示例
pc/        # 留在家里 PC：桥 opencode_bridge.py + 配置模板
ARTICLE.md          # 技术实现剖析（推荐先读）
README-opencode.md  # 使用与配置速查
config.md           # 全部配置项 + VPS 部署步骤 + 常见故障
TUNNEL-3STEPS.md    # 变体：SSH 隧道直出官方 opencode UI
```

## 快速开始

```bash
# 家里 PC
opencode serve --hostname 127.0.0.1 --port 9001
python3 pc/opencode_bridge.py --config pc/opencode_bridge.json

# VPS
scp -r service/ root@VPS_IP:/opt/switchboard
cd /opt/switchboard && pip install -r requirements.txt
cp config.json.example config.json    # token 用 openssl rand -hex 32 生成
# 装 nginx.conf.example + systemd（见 config.md）
```

手机打开 `https://你的域名/s/?d=<token>` 即可。

## 安全模型：三层凭证

| 凭证 | 在哪 | 管什么 |
|---|---|---|
| Provider API key | 家里 PC（opencode auth） | 模型账单，**永不出内网** |
| `OPENCODE_SERVER_PASSWORD` | 家里 PC + 桥配置 | opencode HTTP API（桥代持，HTTP Basic） |
| 设备 token（`openssl rand -hex 32`） | VPS 白名单 + 桥 + 手机 URL | 中继唯一鉴权，**token 即控制权**，泄露立即轮换 |

底线：中继只绑回环、空白名单拒一切、权限审批超时默认拒绝；中继能看到聊天明文帧——
**必须部署在你自己的 VPS 上**。
