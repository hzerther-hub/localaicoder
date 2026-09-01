[English](README.md) | 中文

# Switchboard（中文说明）

> **最终目的：人在外面，用手机浏览器远程调用自己家里 PC 上的 opencode 干开发活。**

代码、运行环境、git 仓库、模型账号都在家里那台机器上——所以"干活的那一端"必须是它。
Switchboard 让这台机器可以被任意浏览器遥控，而且**家里网络不用开任何入站端口**：
家里 PC 上的桥主动出站连接 VPS，NAT/防火墙零改动；VPS 上的中继只校验 `device_token`
并转发 JSON 帧——不解析、不缓存、不理解内容，是一个纯粹的**哑管道**。

手机端能做的：发需求、看流式回复与工具调用过程、传截图识图、审批写文件/命令、
切换模型与项目目录、跑斜杠命令、看 diff。

## 架构

```
┌────────────┐  HTTPS/WSS   ┌────── 公网 VPS ──────┐   出站 WS   ┌── 自己的服务器（家里 PC）──┐
│ 手机浏览器  │ ──────────> │  Nginx (443)          │ <───────── │ 桥 pc/opencode_bridge.py  │
│            │             │    └─> 中继 main.py    │            │    │ HTTP + SSE            │
└────────────┘             │    (127.0.0.1:9000)   │            │    ▼                      │
                            └──────────────────────┘            │ opencode serve            │
                                                                │ (127.0.0.1:9001)          │
                                                                └───────────────────────────┘
```

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

README.md / README.zh-CN.md            # 总览（英文 / 中文）
README-opencode.en.md / .md            # 使用与配置速查（英文 / 中文）
ARTICLE.en.md / ARTICLE.md             # 技术实现剖析（英文 / 中文）
config.en.md / config.md               # 全部配置项 + 部署步骤（英文 / 中文）
TUNNEL-3STEPS.en.md / .md              # SSH 隧道直出官方 UI（英文 / 中文）
```

每份文档都有中英两个版本（英文带 `.en.md` 后缀），文件顶部可互相跳转。

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

## 文档

- [ARTICLE.md](ARTICLE.md) —— 技术实现剖析：中继、哑管道、桥内部、流式/权限/断线补偿设计、Nginx 关键指令
- [README-opencode.md](README-opencode.md) —— 使用指南：方案 A（自建控制台）与方案 B（官方 UI）
- [config.md](config.md) —— 全部配置字段、systemd + Nginx 部署、常见故障
- [TUNNEL-3STEPS.md](TUNNEL-3STEPS.md) —— 变体：SSH 反向隧道直出官方 opencode UI

英文版：[ARTICLE.en.md](ARTICLE.en.md) · [README-opencode.en.md](README-opencode.en.md) ·
[config.en.md](config.en.md) · [TUNNEL-3STEPS.en.md](TUNNEL-3STEPS.en.md)
