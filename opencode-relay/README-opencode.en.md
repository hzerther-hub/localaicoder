English | [中文](README-opencode.md)

# Remotely driving opencode on your home PC for development (Plan A + B)

> **Ultimate goal: while away from home, use a phone / any browser to remotely drive opencode on your home PC to do development work.**
> The code, runtime, git repositories, and model accounts all live on that home PC — from outside you can still have it
> edit code, run commands, review diffs, approve write operations, upload screenshots for image recognition, and switch
> models or projects — using opencode as if you were sitting in front of the PC.
>
> The home PC sits behind NAT with no public IP, so the link is "**the home PC makes an outbound connection to a VPS relay**":
> this directory wires "a self-hosted relay + phone web console" into opencode (Plan A), and also provides a way to
> use opencode's own service + an SSH tunnel to expose the official UI directly (Plan B).
> The code is split by deployment location into two directories: **`service/` (the relay server, deployed to a public VPS, with Nginx in front)**
> and **`pc/` (the bridge, which stays running on the home PC)**; for a technical deep-dive see `ARTICLE.md`.
>
> Port conventions: **Plan A → 8999** (relay + bridge), **Plan B → 9001** (opencode serve).

## 1. Architecture Overview

```
┌──────── Phone browser ────────┐     ┌──── Public VPS (relay server) ──────┐
│ Plan A: page.html console     │     │ FastAPI dumb pipe, routes only by    │
│ Plan B: opencode built-in web │     │   device_token                       │
└───────────┬───────────────────┘     │   . Static page GET /s/?d=<token>    │
            │  (ws/wss, TLS)          │   . Phone WS  /s/ws?d=<token>        │
            └──────────── Nginx(443) ─┼─ . Client WS /client?d=<token> ──────┘
                                      │ Bridge connects in, purely outbound
                 ┌────────────────────┴─────────────────────────┐
                 │  Your own server (home PC running opencode)  │
                 ▼ Plan A: bridge (pc/opencode_bridge.py)   ▼ Plan B: SSH tunnel direct out
        relay:/client  <—— the bridge has no business logic, only protocol translation ——>          opencode serve(9001)
                 │                                             HTTP API + /event SSE
                 ▼
        opencode serve :9001   (HTTP OpenAPI + SSE event stream; all the work happens on this machine)
```

- **Verified in practice, August 2026**: the four frame types of Plan A's phone page — `state` (session list),
  `models`, `send` (streamed replies), and `messages` (history) — all work; Plan B's `opencode serve` health check,
  session creation, and `prompt_async` have also been verified.
- opencode is only the "doing the work" end: the actual LLM, tools, and permissions all live inside it. The bridge/relay
  merely ferry and translate.

### Key terms: what is a "relay", what is a "dumb pipe"

**Relay**: a "message station" hosted on the public internet. The phone is on the outside network and the home PC is
behind NAT, so neither can reach the other directly; instead, both sides **actively** connect to this middleman server
(the VPS), which matches up and hands over messages by `device_token` — frames sent by the phone go to the PC with the
same token, and frames sent by the PC go to all phones with the same token. It is neutral: it does no work and stores
no data, it only "delivers frames to where they should go". Analogy: the relay is the receptionist relaying messages,
while the home PC is the one actually doing the work.

**Dumb pipe**: describes where this relay is "dumb" — toward the content it forwards it does **not parse, not cache,
and not generate**; there is no business logic anywhere in the chain:

| How it is "dumb" | Concrete behavior | The benefit it buys |
|---|---|---|
| Does not parse | JSON frames received are forwarded verbatim, with no understanding of whether a frame carries chat text, a model list, or a permission approval (see `client_ws`/`phone_ws` in `main.py`: straight from `receive_text()` to `send_text()`) | Swapping the controlled end (replacing opencode with another CLI/agent) only requires changing the bridge — zero relay changes; the public-facing component contains no attackable business logic |
| Does not cache | A frame is forgotten as soon as it is forwarded; frames during a bridge disconnect window are simply dropped, with no persistent replay | Stateless; the process can be restarted freely; dropped frames are covered by the bridge-side "reconnect compensation" (after reconnecting, the phone re-pulls messages) |
| Does not generate | It only does three things: validate the token → look up the device registry → forward (upstream broadcast 1→N, downstream one-to-one N→1) | The less logic, the less can break; the entire server side is a single `main.py` (about 300 lines) |

So the division of labor is: **the relay only knows tokens, it does not know opencode**; all opencode-related
"translation" (phone frames ↔ opencode HTTP API/SSE) happens in the bridge on the home PC.

## 2. Plan A (keep your own phone console, port 8999)

The phone page is still `service/page.html`, and the relay is still a dumb pipe; only the "client" is replaced with
`opencode_bridge.py`, which translates the Local AI Studio protocol into opencode's HTTP API.

### Components and roles

| Component | Runs on | Port | Description |
|---|---|---|---|
| `service/main.py` (relay) | Public VPS | 8999 | Dumb pipe: `device_token` whitelist + bidirectional forwarding of JSON frames |
| `service/page.html` (phone console) | Public VPS (served by the relay) | —— | Phone console (unchanged) |
| `pc/opencode_bridge.py` (bridge) | **Your own server (home PC)** | none (pure outbound) | Connects to `/client`, maps `send/state/messages/models/...` onto opencode HTTP + `/event` SSE |
| `opencode serve` | **Your own server (home PC)** | 9001 | The backend that actually does the work (shared by Plans A and B): LLM calls, tool execution, and file modifications all happen on this machine |

> In short: **the VPS holds only two things — the relay + Nginx**; on the home PC run "bridge + opencode serve", with
> the bridge making purely outbound connections to the VPS, so the home router needs no ports opened at all.

### Configuration

1. `service/config.json` (gitignored; see `config.json.example` in the same directory for a template):
   ```json
   { "listen": "127.0.0.1:8999", "device_tokens": ["<64-char token, or testtoken123 for testing>"] }
   ```
2. `pc/opencode_bridge.json` (gitignored; see `opencode_bridge.json.example` in the same directory for a template):
   ```json
   {
     "relay":   { "server_url": "ws://127.0.0.1:8999", "device_token": "testtoken123",
                  "workspace": "/home/wellfuture/build/localaicoder", "mode": "always" },
     "opencode":{ "base_url": "http://127.0.0.1:9001", "default_model": "providerID/modelID", "password": "" }
   }
   ```
   - Leaving `default_model` empty = use the default model from opencode's own configuration; setting `provider/model` overrides it.
   - `server_url` may be set to `https://your-domain`; the bridge automatically converts it to `wss://`.
   - `opencode.password` = `OPENCODE_SERVER_PASSWORD` (fill it in only if `opencode serve` was started with a password; otherwise leave empty for no login;
     the bridge uses HTTP Basic auth with the fixed username `opencode`). You can also use the environment variable `OPENCODE_SERVER_PASSWORD`.
3. **(Out-of-the-box configuration) make the phone-side readonly/ask actually block writes/edits** — set the tools to `ask` in opencode's own configuration,
   so that opencode will send `permission.asked` to the bridge. Paste into `~/.config/opencode/opencode.jsonc`:
   ```jsonc
   {
     "$schema": "https://opencode.ai/config.json",
     "permission": { "edit": "ask", "bash": "ask", "webfetch": "ask" }
   }
   ```
   > ❗ The key must be a **top-level `permission`** (not `agent.permission`). Verified in practice: after setting `permission.edit="ask"`,
   > an attempted edit genuinely emits `permission.asked` and hands it to the bridge.
   > It also works without this configuration: opencode's default `build` agent is `{permission:"*", action:"allow"}` — writes/edits are
   > allowed automatically, and the phone-side `readonly/ask` cannot block them (only `external_directory`/`doom_loop` default to `ask`).
   > The snippet above is also included in the `_opencode_agent_permission_ref` section of `opencode_bridge.json.example` for reference.

### Startup

```bash
scripts/opencode-remote.sh start      # starts 9001 + 8999 + the bridge
# or separately:
opencode serve --hostname 127.0.0.1 --port 9001          # run in the project directory
cd opencode-relay/service && python3 main.py -config config.json   # 8999
python3 opencode-relay/pc/opencode_bridge.py --config opencode-relay/pc/opencode_bridge.json
```

Open `http://127.0.0.1:8999/s/?d=testtoken123` (local) or `https://your-domain/s/?d=<token>` (public, with Nginx) on the phone.

### Public exposure (Nginx terminating TLS)

Point the Nginx site at the relay port; all three routes `/s/`, `/s/ws`, and `/client` are served by main.py
(for the full annotated configuration see `service/nginx.conf.example`):

```nginx
server {
    listen 443 ssl;
    server_name your-domain;
    # Fill in ssl_certificate / ssl_certificate_key with the actual issued paths
    location / {
        proxy_pass         http://127.0.0.1:8999;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;   # Required for WebSocket
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;                     # Required for streamed replies
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

> ⚠️ When the phone goes through the public internet, chat content is visible to the relay server — make sure you
> trust only your own VPS (same as the existing self-hosted relay model).

### Protocol translation done by the bridge (see `docs/relay/protocol.md` and `desktop/relay.go`)

| Phone→bridge | opencode counterpart |
|---|---|
| `state` | `GET /session` + computing `workspace/mode/current/branch` |
| `messages {id}` | `GET /session/:id/message` (text/image/tool parts assembled into `{role,text,images}`) |
| `models` | `GET /config/providers` (flattened into `{key,model_id,display_name,is_current,vision,reasoning}`) |
| `send {session,text,atts}` | Confirm/create session → `POST /session/:id/prompt_async` (body contains `parts[text]`, images → `{type:file,url}`, `model` must be passed as an object `{providerID,modelID}`) |
| `stop` | `POST /session/:id/abort` |
| `delete/rename/open_session` | `DELETE|PATCH /session/:id` / set the current session and reply with `session:opened` |

| opencode event (`/event` SSE) → phone frame | Description |
|---|---|
| `message.part.delta`(field=text) | → `text {delta}` (streaming) |
| `message.part.updated`(type=tool) | → `tool {delta: tool name}` (deduplicated by part id) |
| `session.status` busy / `session.idle` | → `run:started` / `run:finished` + `done` |
| `session.updated`(tokens) | → `usage` (best effort) |
| `permission.asked` | See "Permission approval" below |

### Permission approval (readonly / ask / always → `/permissions/:id`)

When approval is needed, opencode emits `permission.asked` (containing `id`/`sessionID`/`permission`/`patterns`/`metadata`/`tool`). The bridge handles it according to the phone-side permission mode:

| Phone permission mode | Bridge action |
|---|---|
| `readonly` | Automatically `POST /session/:sid/permissions/:pid` → `{response:"reject"}` (does not disturb the phone) |
| `always` | Automatically answers → `{response:"always"}` (remembered; always allow) |
| `ask` | Forwards a `permission_request` to the phone; the phone taps "Allow / Deny / Always allow" and replies with `permission_response`, which the bridge maps to `{response:"once"/"reject"/"always"}`; on timeout `OPENCODE_BRIDGE_PERM_TIMEOUT` (default 120s) it automatically denies |

> ⚠️ opencode's default `build` agent has permissions `{permission:"*", action:"allow"}` (writes/edits allowed automatically;
> only `external_directory`/`doom_loop` default to `ask`). Therefore, for `readonly/ask` to genuinely block writes/edits,
> the corresponding tools must be set to `ask` in the opencode configuration — **top-level `permission`** (see the
> "out-of-the-box configuration" above; not `agent.permission`), so that opencode emits `permission.asked` for the bridge
> to auto-answer or forward to the phone.

### Credentials and security (what about "username and password"?)

This link has three layers of credentials; do not confuse them:

| Layer | Name | Where it is configured | Description |
|---|---|---|---|
| ① AI model account | provider API key | `~/.local/share/opencode/auth.json` / opencode configuration | Used for model calls; already configured — no action needed for local testing |
| ② opencode server password | `OPENCODE_SERVER_PASSWORD` | Environment variable when starting `opencode serve` + the bridge's `opencode.password` | Protects opencode's HTTP API/web. **Not set = no authentication** (local `127.0.0.1` only); **must be set for public/phone access**. Clients use **HTTP Basic auth**: fixed username `opencode`, password = this value |
| ③ Relay device_token | `device_tokens` / `d=` | `service/config.json` + the phone URL `?d=` | The phone page's "passphrase"; use `testtoken123` for local testing; the token is control |

- **Local testing**: none of the three needs extra configuration — `opencode serve` runs without a password (listening on
  127.0.0.1 only, which is safe), the relay uses `testtoken123`, and the model key is already in place. Just run it.
- **Going public / cross-network phone access**: opencode **must** have `OPENCODE_SERVER_PASSWORD` set, with the bridge's
  `opencode.password` kept in sync (or the environment variable `OPENCODE_SERVER_PASSWORD`); otherwise anyone could call your models.

## 3. Plan B (use opencode's own service directly, port 9001)

No custom console is written; use opencode's built-in CLI service + browser UI directly.

```bash
# Start the headless service in the project directory
opencode serve --hostname 127.0.0.1 --port 9001
# Or start the service and open its built-in web interface
opencode web --port 9001
```

- Health check: `curl http://127.0.0.1:9001/global/health` → `{"healthy":true,...}`
- Once the service is up, you get a ready-made HTTP API (`/session`, `/session/:id/message`, `/event` SSE, `/tui/*`, etc.)
  and `opencode attach <url>` can connect to any running server.
- Cross-network phone access: reverse-proxy `9001` to public port 443 via Nginx (as above), then open it in the browser.

```nginx
server {
    listen 443 ssl;
    server_name your-domain;
    # Fill in ssl_certificate / ssl_certificate_key with the actual issued paths
    location / {
        proxy_pass         http://127.0.0.1:9001;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

> Plan B advantages: nearly zero work, official UI included. Disadvantages: you lose the phone console's in-house details
> such as "two-way project/session sync, task step bar, usage status bar, permission switching, /file upload"; Plan A
> preserves these to the greatest extent.

## 4. Known limitations / future work

- **Permission modes are wired up**: on `permission.asked` the bridge auto-answers or forwards to the phone for approval
  according to `readonly/always/ask`, landing via `POST /session/:id/permissions/:permissionID` (see above). But **to
  genuinely block writes/edits**, the opencode agent's corresponding tools must be set to `ask` (the default is
  allow-all, otherwise no permission request is triggered).
- **Session model**: an opencode session = project + session (parent/child possible); the bridge directly uses the
  opencode session id as the phone session id; project (workspace) switching is not hot-switched.
- **Reasoning effort levels**: opencode has no effort level that maps one-to-one to the phone page; the bridge ignores that field.
- **Images**: images uploaded from the phone are passed to opencode as `FilePartInput {type:file,url}`; display-side image
  detection is by `data:image`.
- **reasoning/token counts**: the phone page has no reasoning display slot, so the bridge does not forward that part.

## 5. File list

| File | Purpose |
|---|---|
| `pc/opencode_bridge.py` | Plan A bridge (protocol translation) |
| `service/config.json.example` | Relay 8999 configuration example |
| `pc/opencode_bridge.json.example` | Bridge configuration example |
| `service/nginx.conf.example` | Nginx reverse-proxy configuration (TLS + WebSocket + SSE) |
| `scripts/opencode-remote.sh` | One-shot start/stop (serve/relay/bridge) |
| `service/config.json`, `pc/opencode_bridge.json` | Local configuration (gitignored) |

## 6. Common troubleshooting

| Symptom | Cause / fix |
|---|---|
| Phone page shows a red banner "Connection lost (code 1006)" | **Multiple bridge processes** fighting over the same `/client` slot on the relay (each new bridge kicks the old one off → flapping → the phone WS gets 1006). Fix: `scripts/opencode-remote.sh stop` then `start` (the script now cleans up leftover bridges before startup to prevent recurrence); or manually `pkill -f opencode_bridge.py` and start only one. |
| Phone page stuck on "Connecting to desktop…" / model list empty | The three processes — relay/bridge/opencode — are not all up: run `scripts/opencode-remote.sh status` to check health; start whichever is missing. |
| `opencode serve` reports `ServeError` | Port already in use (commonly: the previous instance was not stopped). Clear it with `scripts/opencode-remote.sh stop`, or confirm `9001` is free before starting. |
| `opencode serve` keeps warning that no password is set | Safe to ignore for local `127.0.0.1` only; for external access set `OPENCODE_SERVER_PASSWORD` in the environment and configure `opencode.password` in the bridge. |
