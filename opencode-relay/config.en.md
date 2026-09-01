English | [中文](config.md)

# opencode-relay Configuration Guide (config.md)

> Complete reference for every configuration item needed to remotely drive the local opencode instance through the relay over the public internet (e.g. `op.mei.biz`).
> Each of the three components has its own config file; after any change you must **restart the corresponding process** for it to take effect.

## 1. Components and Ports Overview

```
Phone browser ──HTTPS──> Relay (dumb pipe) on op.mei.biz <──outbound WS── Local bridge ──HTTP/SSE──> Local opencode serve
```

| Component | Runs on | Port | How to start | Config file |
|---|---|---|---|---|
| `opencode serve` | Local machine | 9001 | `opencode serve --hostname 127.0.0.1 --port 9001` (run inside the target project directory) | `~/.config/opencode/opencode.jsonc` + `~/.local/share/opencode/auth.json` |
| Relay `main.py` | Public VPS (or local machine for testing) | VPS binds `127.0.0.1:9000`; local testing binds `127.0.0.1:8999` | `python3 main.py -config config.json` | `config.json` in the same directory |
| Bridge `opencode_bridge.py` | Local machine | None (outbound-only connection) | `python3 opencode_bridge.py --config opencode_bridge.json` | `opencode_bridge.json` in the same directory |

- The relay is a **dumb pipe**: it only validates `device_token` and forwards JSON frames without parsing business payloads; it also serves the phone page `page.html` and the static `css/ js/` assets.
- The bridge uses **outbound connections**: the local machine never needs any port open to the public internet; it connects to the relay at `/client` on one end and calls the local opencode HTTP API on the other.

## 2. Relay Configuration `config.json`

Start with: `python3 main.py -config config.json` (reads `config.json` from the same directory by default).

```json
{
  "listen": "127.0.0.1:9000",
  "device_tokens": ["<64-char random hex>"]
}
```

| Field | Type | Description |
|---|---|---|
| `listen` | string | Bind address. **On a VPS this must be `127.0.0.1:9000`** (the only public entry point is Nginx on 443); use `127.0.0.1:8999` for local testing. A bare `0.0.0.0` is equivalent to having no protection and is forbidden |
| `device_tokens` | string[] | **Whitelist**: the relay only accepts listed tokens. One token = one device; the desktop (bridge `/client`) and the phone (`/s/?d=`, `/s/ws`) use the **same token**. Generate with: `openssl rand -hex 32` |

Environment variables:

| Variable | Default | Description |
|---|---|---|
| `RELAY_WS_MAX` | `5` | Max size of a single WS frame (MB); increase to send larger base64 images |

Routes (provided by the relay; both phone and bridge use these):

| Path | Purpose |
|---|---|
| `GET /s/?d=<token>` | Phone console page |
| `GET /s/css/*`, `GET /s/js/*`, `GET /s/images/*` | Static page assets (suffix whitelist + path traversal rejected) |
| `WS /s/ws?d=<token>` | Phone-side WebSocket |
| `WS /client?d=<token>` | Bridge (client) outbound connection |

## 3. Bridge Configuration `opencode_bridge.json`

Start with: `python3 opencode_bridge.py --config opencode_bridge.json` (reads that file from the same directory by default).

```json
{
  "relay": {
    "server_url": "https://op.mei.biz",
    "device_token": "<64-char token matching the relay whitelist>",
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

### `relay` section

| Field | Default | Description |
|---|---|---|
| `server_url` | (required) | Relay address. `https://` is automatically converted to `wss://`; for local testing use `http://127.0.0.1:8999` (converted to `ws://`). For public use enter `https://op.mei.biz` |
| `device_token` | (required) | Must be **exactly identical** to a token in the relay's `device_tokens` whitelist |
| `workspace` | Current directory | Initial workspace. "New session" and "workspace switch" initiated from the phone update it in real time |
| `mode` | `always` | Initial permission mode `readonly / ask / always`; can be switched from the phone at any time |
| `insecure` | `false` | When `true`, skips TLS certificate verification (only for self-signed certificate debugging) |

### `opencode` section

| Field | Default | Description |
|---|---|---|
| `base_url` | `http://127.0.0.1:9001` | Address of the local opencode serve |
| `default_model` | empty | Empty = use opencode's own default model; entering `providerID/modelID` (e.g. `deepseek/deepseek-v4-pro`) overrides it. Switching models on the phone updates it in real time |
| `password` | empty | Only needed if opencode has `OPENCODE_SERVER_PASSWORD` set; the bridge uses HTTP Basic (username is fixed to `opencode`). Can also be provided via the `OPENCODE_SERVER_PASSWORD` environment variable |

### Command-line flags (override the matching config file entries)

```
--config  --relay  --token  --workspace  --mode
--opencode  --model  --password  --insecure
```

## 4. The opencode serve Side (Local Machine)

```bash
cd /home/wellfuture/build/localaicoder     # ← the project directory determines opencode's workspace
opencode serve --hostname 127.0.0.1 --port 9001
```

- `~/.config/opencode/opencode.jsonc`:
  ```jsonc
  {
    "permission": { "edit": "ask", "bash": "ask" }   // top-level permission (not agent.permission)
  }
  ```
  Only with `ask` set will opencode emit `permission.asked`, allowing the bridge to auto-answer according to `readonly/ask/always` or forward approvals to the phone.
- Model accounts: `~/.local/share/opencode/auth.json` (managed by `opencode providers`).
- `OPENCODE_SERVER_PASSWORD`: sets a password for the opencode HTTP API; if set, the bridge's `opencode.password` must be filled in to match.

## 5. Deploying to op.mei.biz (Option 1 · With a Public VPS)

The code is split by side into directories: `service/` (the relay server deployed to the VPS) and `pc/` (the bridge that stays on the local machine).

```bash
# Local machine: generate a token and copy the server directory to the VPS
openssl rand -hex 32
scp -r opencode-relay/service root@VPS_IP:/opt/opencode-relay
```

```bash
# VPS: dependencies + config
cd /opt/opencode-relay && python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
cat > config.json <<'EOF'
{ "listen": "127.0.0.1:9000", "device_tokens": ["<64-char token>"] }
EOF
```

Nginx (`/etc/nginx/conf.d/opencode-relay.conf`, terminating TLS + reverse proxy + WebSocket/SSE passthrough;
see `service/nginx.conf.example` for the fully commented version):

```nginx
server {
    listen 443 ssl;
    server_name op.mei.biz;
    ssl_certificate     /etc/ssl/certs/op.mei.biz.pem;        # use your actual certificate path
    ssl_certificate_key /etc/ssl/private/op.mei.biz.key;
    location / {
        proxy_pass         http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;   # required for WebSocket (phone page + bridge, two WS connections)
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;                     # required for streaming replies; buffering swallows the stream
        proxy_read_timeout 3600s;                   # the default 60s would cut off long-lived connections
        proxy_send_timeout 3600s;
    }
}
```

> Caddy also works: `op.mei.biz { reverse_proxy 127.0.0.1:9000 }`, which likewise passes WebSocket through automatically.

systemd (`/etc/systemd/system/opencode-relay.service`):

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

Finally, on the local machine change the bridge's `server_url` to `https://op.mei.biz` and restart the bridge; open `https://op.mei.biz/s/?d=<token>` on the phone.

## 6. One-Shot Start/Stop on the Local Machine (for testing)

```bash
scripts/opencode-remote.sh start    # opencode:9001 + opencode-relay:8999 + bridge (idempotent; cleans up leftover bridges before starting)
scripts/opencode-remote.sh status   # health check
scripts/opencode-remote.sh stop
```

## 7. Address Quick Reference

| Scenario | Address |
|---|---|
| Phone console (local testing) | `http://127.0.0.1:8999/s/?d=<token>` |
| Phone console (public internet) | `https://op.mei.biz/s/?d=<token>` |
| Official opencode web (local) | `http://127.0.0.1:9001` (`/server/<base64>/session/<id>` is the session detail page) |

## 8. Security Checklist

- [ ] **Disable** weak tokens like `testtoken123` on the public internet; use `openssl rand -hex 32`
- [ ] A token is control: keep it identical in all three places (VPS whitelist / bridge / phone URL) and rotate it immediately if leaked
- [ ] On the VPS only open 80/443, and bind the relay to `127.0.0.1`; the relay can see chat plaintext, so it must run on your own server
- [ ] If opencode is exposed externally, `OPENCODE_SERVER_PASSWORD` must be set

## 9. Troubleshooting

| Symptom | Cause / Fix |
|---|---|
| Phone page shows a red bar "connection lost (code 1006)" | Multiple bridge processes fighting over the `/client` slot. Run `scripts/opencode-remote.sh stop && start` (the script automatically cleans up leftover bridges before starting) |
| Page opens but has **no styling** | The relay process is running old code (no `/s/{sub}/{name}` static route). Restart the relay process and hard-refresh the browser |
| `opencode serve` reports `ServeError` | The port is occupied, or two opencode instances share the same data directory and lock each other. Run `pkill -f "opencode serve"`, confirm 9001 is free, then start again |
| Messages get no response | No session selected (the frontend ignores sends outright when `sid` is empty). Open a session from the session list first |
| Phone-side `readonly/ask` fails to block write operations | opencode config's top-level `permission.edit/bash = "ask"` is not set (everything is allowed by default, so no permission requests are produced) |
| Bridge connects to the relay but keeps reporting `/event` disconnect/reconnect | opencode (9001) is not running or was just restarted; the bridge re-subscribes automatically every 3s; verify with `curl 127.0.0.1:9001/global/health` |
