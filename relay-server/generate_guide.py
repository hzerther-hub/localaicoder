#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成中继服务器配置说明（面向智能体 / AGENT 的部署手册）。

用法：python3 generate_guide.py [域名]
默认域名 <你的域名>。产出文件：INSTRUCTIONS-AGENT.md
"""
import os
import sys

DOMAIN = sys.argv[1] if len(sys.argv) > 1 else "<你的域名>"

GUIDE = f"""# 自建中继 · 面向智能体的配置说明

> 目标域名：**{DOMAIN}**。请**严格按本文件顺序**在服务器上执行；
> 每条命令可直接复制。任何一步出错先停，勿跳过安全项。

## 0. 背景（30 秒看懂）

- **客户端**：用户电脑上的 Local AI Studio（Go，出站 WS 连 `wss://{DOMAIN}/client?d=<token>`）
- **服务器**：`relay-server/`（FastAPI），只按 token 路由，不解析业务
- **手机**：浏览器打开 `https://{DOMAIN}/s/?d=<token>` 即得网页控制台
- **一个 token 两用**：桌面连服务器的凭证 + 手机链接的 `d=`。

## 1. 前置：DNS 与防火墙

1. 把 `{DOMAIN}` 解析到本服务器公网 IP（A 记录）。
2. 放行 80/443：
   ```bash
   ufw allow 80/tcp && ufw allow 443/tcp && ufw reload
   ```

## 2. 安装服务器

```bash
# 放代码（把本地 relay-server/ 拷到 /opt/relay-server）
mkdir -p /opt/relay-server && cd /opt/relay-server

# 依赖（venv，勿动系统 Python）
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
```

## 3. 服务器配置（config.json 字段说明）

```bash
cat > /opt/relay-server/config.json <<'EOF'
{{
  "listen": "127.0.0.1:9000",
  "device_tokens": ["<设备A的64位token>", "<设备B的64位token>"]
}}
EOF
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `listen` | string | 只绑本机回环（`127.0.0.1:9000`）；公网 TLS 由 Caddy 承担，**不要**改绑 `0.0.0.0` 裸暴露 |
| `device_tokens` | string[] | **白名单**：只有列出的 token 才被允许（桌面 `/client` 与手机 `/s/ws` 都要校验）。每个 token = 一台设备身份；加多个 = 多人各自控制自己的机器；一个 token 被多人分享 = 多人共用同一台 AI |

> 一个 token 两用：桌面连服务器的凭证（`/client?d=<token>`）+ 手机链接的 `d=<token>`。**三处必须完全一致**：服务器 `device_tokens[]`、桌面面板 token、手机 URL。改 token = 作废旧链接（换配置需重启）。

- ⚠️ 全文的 `wss://<你的域名>` / `https://<你的域名>` 均需替换为**你自己的域名**（能解析到本服务器公网 IP 的一个域名，Caddy 会自动签证书）。

## 4. Caddy 终结 TLS

```bash
# 若未装 Caddy：apt install -y caddy   （Debian/Ubuntu）
cat > /etc/caddy/Caddyfile <<'EOF'
{DOMAIN} {{
    reverse_proxy 127.0.0.1:9000
}}
EOF
systemctl reload caddy
```

Caddy 会自动申请并续期 Let's Encrypt 证书。桌面端与手机走 `wss://`/`https://`，需 Caddy 的 443。

## 5. systemd 常驻

```bash
cat > /etc/systemd/system/relay-server.service <<'EOF'
[Unit]
Description=Local AI Studio relay
After=network-online.target

[Service]
User=root
WorkingDirectory=/opt/relay-server
ExecStart=/opt/relay-server/.venv/bin/python /opt/relay-server/main.py -config /opt/relay-server/config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now relay-server
systemctl restart relay-server   # 改 config.json 后必须重启（配置启动时读一次）
```

## 6. 验证（能通过再去找用户配置桌面端）

```bash
# 正确 token（先往 config.json 写一个如 testtoken123 再重启）→ 应 200
curl -s -o /dev/null -w "%{{http_code}}\\n" "http://127.0.0.1:9000/s/?d=testtoken123"
# 错误 token → 应 403
curl -s -o /dev/null -w "%{{http_code}}\\n" "http://127.0.0.1:9000/s/?d=wrong"
# 页面含手机控制台标记
curl -s "http://127.0.0.1:9000/s/?d=testtoken123" | grep -c "项目会话"
```

## 7. 桌面端配置（用户侧）

1. 用户打开 📱 面板底部 → 🌐 自建中继（跨网）。
2. **服务器地址** 填：`wss://{DOMAIN}`（填 `https://{DOMAIN}` 也可，桌面端自动转 `wss://`）。
3. 点「**生成**」得到 **64 位 token** → 写入服务器 `config.json` 的 `device_tokens[]` → `systemctl restart relay-server`。
4. 用户点「**连接**」，状态点变绿 = `已连接`（出站 WS 直连，自动绕过本机 `HTTPS_PROXY`）。
5. 手机打开：`https://{DOMAIN}/s/?d=<同token>` → 即为手机控制台（模型/推理/权限切换 + 会话 + 快捷命令）。

> 桌面端配置存于本机 `models.json` 顶层 `relay` 块（`server_url`/`device_token`），改动后**重启应用**才会重新自动连接。

## 8. 安全清单（勿省）

- [ ] 只信任自己服务器：**别**把 `ws://` 裸端口暴露到公网，务必走 Caddy 的 443
- [ ] token 只在 `device_tokens` 白名单内生效；换 token = 作废旧链接
- [ ] 手机/中继触发的 agent 仍走本机权限模式（写工具需审批），无需额外放开
- [ ] 该链接即控制权：不要发给不可信的人

## 9. 常见故障

| 现象 | 排查 |
|---|---|
| 桌面「连接」后状态常红 | `curl -k https://{DOMAIN}/s/?d=<token>` 看是否 403；token 是否在服务器白名单；Caddy 是否已 reload |
| 手机打开 403 | 链接里 `d=<token>` 与白名单不一致 |
| 手机能开但没反应 | 桌面端是否已「连接」且 `Running`；设备未上线时服务器会拒绝新手机 |
| 域名连不通 | `dig {DOMAIN}` 是否解析到本机；80/443 是否放行 |
"""

DEST = os.path.join(os.path.dirname(os.path.abspath(__file__)), "INSTRUCTIONS-AGENT.md")
with open(DEST, "w", encoding="utf-8") as f:
    f.write(GUIDE)
print(f"已生成: {DEST}（域名 {DOMAIN}）")
