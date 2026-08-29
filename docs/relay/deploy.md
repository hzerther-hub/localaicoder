# 中继服务器部署指南

服务器是 **FastAPI（Python 3.10+）** 应用（`relay-server/main.py`），无数据库、
无静态前端构建（手机页面内嵌 `page.html`）。推荐"uvicorn + systemd + Caddy 自动 TLS"
三件套；最小 1C/512M 任意 VPS 即可。

## 1. 依赖

```bash
cd relay-server
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
# 或系统直装（需 --break-system-packages，见 PEP 668）
python3 -m pip install -r requirements.txt
```

`/etc/relay-server/config.json`：

```json
{
  "listen": "127.0.0.1:9000",
  "device_tokens": ["<64位随机hex，从桌面端『生成token』复制而来>"]
}
```

- `listen` 只绑本机回环，对外 TLS 一律交给 Caddy
- `device_tokens`：允许接入的设备；面板一键重置后同步替换这里

## 2. systemd 单元

`/etc/systemd/system/relay-server.service`：

```ini
[Unit]
Description=Local AI Studio relay server
After=network-online.target

[Service]
User=relay
WorkingDirectory=/opt/relay-server
ExecStart=/opt/relay-server/.venv/bin/python /opt/relay-server/main.py -config /etc/relay-server/config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /usr/sbin/nologin relay
sudo systemctl daemon-reload && sudo systemctl enable --now relay-server
```

## 3. Caddy 终结 TLS（自动签发续期）

`/etc/caddy/Caddyfile`：

```
relay.example.com {
    reverse_proxy 127.0.0.1:9000
}
```

Caddy 会自动申请/续期 Let's Encrypt 证书，客户端与手机统一走 `wss/https`。

## 4. Docker（可选）

```dockerfile
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY main.py page.html ./
EXPOSE 9000
CMD ["python", "main.py", "-config", "/etc/relay-server/config.json"]
```

```bash
docker run -d -p 127.0.0.1:9000:9000 \
  -v /etc/relay-server:/etc/relay-server relay-server
```

## 5. 桌面端对接步骤

1. 面板切换到「自建中继」→ 点「生成 token」→ 复制 64 位 hex
2. 把 token 填进服务器 `device_tokens`，`systemctl reload relay-server`
3. 面板填服务器地址（`wss://relay.example.com`）→ 连接；状态点变绿后二维码即为公网链接

## 6. 安全清单

- [ ] `listen` 仅 `127.0.0.1`，公网入口只留 Caddy 的 443
- [ ] 服务器防火墙：仅 80/443 入站
- [ ] token 定期轮换（面板重置 → 服务器同步替换）
- [ ] 不要使用不受信任的第三方中继：聊天内容对服务器可见（详见 README 安全模型）
