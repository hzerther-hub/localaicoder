English | [中文](ARTICLE.md)

# Remotely Driving opencode on Your Home PC: A Technical Deep Dive into the Relay, the Bridge, and Nginx

> **The end goal: you are out and about, and you use your phone's browser to remotely drive opencode on your home PC to do development work.**
> The code, the runtime, the git repositories, and the model accounts all live on the machine at home — yet from anywhere you can have it edit code,
> run commands, inspect diffs, approve write operations, upload screenshots for vision, and switch models or projects, just as if you were sitting in front of the PC.
>
> This article explains how this pipeline is implemented: `service/` (the relay deployed to a public VPS, fronted by Nginx) and
> `pc/` (the bridge that stays running on the home PC). For a quick usage and configuration reference, see `README-opencode.md` and `config.md`.

---

## 1. The Problem to Solve

opencode is a "local-first" AI coding agent: it reads and writes files in the project directory, runs shell commands, uses git, and the model API keys are stored on that same machine. None of this can be moved into a cloud sandbox, so **the machine doing the work must be your own machine**. The problems are:

1. The home PC sits behind NAT/firewalls with no public IP, so outside devices cannot connect to it;
2. You should not expose it to the public internet anyway — port scanning and brute-forcing of weak credentials are real threats;
3. Phones have no decent terminal, let alone the ability to run opencode itself.

The solution is a classic trade-off: **rent a cheap VPS as a "message station"; the home PC actively makes an outbound connection to it, the phone connects to the VPS, and the VPS routes both sides to each other by credentials**. This way the home PC keeps zero inbound ports (the router needs no port mapping at all), and the only thing exposed on the public internet is one server you fully control.

## 2. Overall Architecture

```
┌──────────┐  HTTPS/WSS   ┌──────── Public VPS ──────┐   Outbound WS   ┌── Your own server (home PC) ──┐
│ Phone     │ ──────────> │ Nginx(443) ──> Relay     │ <───────── │ Bridge pc/opencode_bridge.py │
│ page.html │             │             service/main.py│            │   │ HTTP + SSE           │
└──────────┘              │             (127.0.0.1:9000)│           │   ▼                      │
                          └──────────────────────────┘            │ opencode serve           │
                                                                   │ (127.0.0.1:9001)         │
                                                                   └──────────────────────────┘
```

Four roles, four link segments:

| Role | Runs on | Responsibility |
|---|---|---|
| Phone browser | External network | Phone console (`service/page.html`, served directly by the relay) |
| Nginx | VPS | The only public entry point: terminates TLS, reverse-proxies, passes through WebSockets |
| Relay `service/main.py` | VPS (bound only to 127.0.0.1:9000) | **Dumb pipe**: validates `device_token` and forwards JSON frames both ways without understanding their content |
| Bridge `pc/opencode_bridge.py` + `opencode serve` | **Your own server (home PC)** | Bridge: protocol translation (phone frames ↔ opencode HTTP/SSE); opencode: does the real work (LLM, tools, files) |

The full path of a typical interaction:

```
Phone sends "help me refactor foo.py"
  → WSS /s/ws?d=<token>        (via Nginx to the relay)
  → relay unicasts by token to that device's bridge (WS /client)
  → bridge translates to POST /session/:id/prompt_async, sent to opencode on the home PC
  → opencode streams output → bridge subscribes to GET /event (SSE) and receives items one by one
  → bridge translates into phone frames (text / tool_start / todo / usage …)
  → relay broadcasts by token to all phones → phone renders character by character
```

Key point: **the home PC has zero inbound exposure to the public internet** — when the bridge starts it actively connects to the VPS, and that outbound WebSocket is the control channel; the home router needs no port mapping. **There is no business logic on the VPS** — it knows tokens, not opencode.

## 3. Two Core Concepts: the Relay and the Dumb Pipe

**Relay**: the "message station" hosted on the public internet. The phone and the home PC cannot find each other directly, so both sides actively connect to this intermediate server, which routes messages to the right counterpart by `device_token`. One token represents one controlled device (the home PC); any phone holding that token can watch that device's screen. The token is the control channel.

**Dumb pipe**: a description of exactly where this relay is "dumb" — toward the content it forwards, it does **not parse, not cache, and not generate**:

| Aspect of "dumb" | Concrete behavior | Benefit gained |
|---|---|---|
| Does not parse | JSON frames are forwarded verbatim; the relay does not know whether a frame contains chat text, a model list, or a permission approval | Swapping the controlled backend (opencode replaced by another CLI) only requires changing the bridge — zero relay changes; the public-facing component has no attackable business surface |
| Does not cache | A frame is forgotten as soon as it is forwarded; frames produced while the bridge is disconnected are dropped outright | Stateless — the process can be restarted at will; frame loss is covered by "reconnect compensation" on the bridge side (see Section 6) |
| Does not generate | It does exactly three things: validate the token → look up the device registry → forward | The less logic, the fewer failure modes; the entire server side is a single ~300-line `main.py` |

So the division of labor is: **the relay knows tokens, not opencode**; all opencode-related "translation" happens in the bridge on the home PC. This is also why the phone frame protocol stays identical to another independent implementation (the Local AI Studio desktop's `desktop/relay.go`) — both ends share the same frame protocol, while the server-side codebases are two fully independent implementations.

## 4. Repository Layout: service/ and pc/

The code is split in two by **deployment location**; at deploy time you copy each half where it belongs:

```
opencode-relay/
├── service/                  # ── Server side: scp the whole directory to the VPS ──
│   ├── main.py               # The relay itself (FastAPI + uvicorn, dumb pipe)
│   ├── page.html             # Phone console (build-free single page)
│   ├── css/ js/              # Console static assets (served by the relay's whitelist)
│   ├── config.json.example   # Relay config template (listen + device_tokens)
│   ├── requirements.txt      # fastapi / uvicorn / websockets
│   └── nginx.conf.example    # Nginx reverse-proxy config (with per-line comments)
├── pc/                       # ── Local side: stays on the home PC ──
│   ├── opencode_bridge.py    # The bridge (protocol translation, ~1200 lines)
│   └── opencode_bridge.json.example  # Bridge config template
├── README-opencode.md        # Usage and configuration quick reference
├── config.md                 # All config options + deployment steps
└── ARTICLE.md                # This article
```

Machine-level configs (`service/config.json`, `pc/opencode_bridge.json`, both containing tokens) are gitignored; only the `.example` templates are in the repository.

## 5. The Relay's Implementation (service/main.py)

### 5.1 Device Registry and Forwarding Semantics

The core data structure is a "one entry per token" registry, created lazily:

```python
devices: dict[str, dict] = {}      # token -> {"client": WebSocket|None,
dev_lock = asyncio.Lock()          #         "phones": set[WebSocket], "lock": asyncio.Lock()}
```

There are only two forwarding rules, covering the two directions symmetrically:

- **Uplink (bridge → phones): broadcast 1 → N.** When `/client` receives a frame, it iterates and sends it to all `/s/ws` phones under that token — the same PC can be watched/controlled by multiple phones at once;
- **Downlink (phone → bridge): unicast N → 1.** When `/s/ws` receives a frame, it is sent only to that token's single current bridge.

Neither direction **parses JSON**: the forwarding semantics are simply "this device's screen is visible to every phone holding the token; any phone's actions go straight to this device." Each device has an `asyncio.Lock` guarding its `client` slot and `phones` set; when broadcasting, the set is copied with `list()` before iteration to avoid stepping on the iterator while cleanup removes failed senders.

### 5.2 Slot Management: Turning Error Codes into Readable Messages

WebSocket close codes are used as lightweight signaling, and the phone side turns them into human-readable messages:

| Scenario | Action | What the phone shows |
|---|---|---|
| Token not in the whitelist | close **1008** (policy violation) | Red banner "Connection closed (code 1008)" → check the `?d=` in the URL |
| Phone connects before the bridge is up | close **1008** (rejected rather than left hanging) | Prompt to first start the bridge on the home PC |
| A new bridge connects while an old bridge with the same token is still online | Kick the old bridge: close **1000** | The old bridge silently reconnects; the user never notices |

"Kicking the old bridge" is a design born from a real pitfall: two bridge processes fighting over the same slot keep kicking each other offline, which shows up on the phone as endless 1006 reconnects (see common failure #1 in `config.md`). Rather than tolerate mutual kicking, "one token keeps exactly one client" was made the semantic — the new connection wins, the old connection exits. During cleanup, the slot is only cleared when `dev["client"] is ws`, so the new bridge's slot is never mistakenly wiped.

### 5.3 Static Assets: Three Layers of Protection + Read-and-Serve

`GET /s/?d=<token>` returns `page.html` (token validation failure is always 403; the page itself contains no token). The static asset route `GET /s/{sub}/{name}` has three layers of protection:

1. `sub` is restricted to the three whitelist directories `css/js/images` (single level, no subdirectories);
2. `/ .. \` are forbidden inside filenames (prevents path traversal, e.g. `css/../../main.py`);
3. The extension must be in the `STATIC_TYPES` whitelist (css/js/images; `.py`, `.json`, etc. cannot be downloaded).

Every request reads from disk on the fly with `Cache-Control: no-cache` — change a frontend file, refresh, and it takes effect; iterating on deployments requires no relay restart.

### 5.4 Other Engineering Details

- **Per-frame size cap**: uvicorn's `ws_max_size` defaults to 5MB (tunable via the `RELAY_WS_MAX` env var), sized for phones uploading images (base64 inflates them by 1.3x); shrinking it reduces abuse risk.
- **Secure defaults**: `listen` defaults to `127.0.0.1:9000`, and `device_tokens` defaults to an empty list (empty whitelist = reject all connections) — if the config is missing, the service would rather fail to start than run naked.
- **Restrained logging**: only connect/disconnect events are logged, and tokens are truncated to their first 6 characters; uvicorn access logging is disabled.
- `/docs` and `/redoc` are explicitly disabled — the public entry point exposes no extra surface.

## 6. The Bridge's Implementation (pc/opencode_bridge.py)

The bridge is the only "smart" component in the whole pipeline: toward the relay it speaks the phone frame protocol; toward opencode it speaks HTTP/SSE. It does not run the agent — session management, tool execution, and permission adjudication are all provided by opencode; the bridge only translates.

### 6.1 Concurrency Model: Three Flows

```
asyncio event loop (main coroutines)
 ├─ _relay_loop   WS send/recv with the relay + exponential backoff reconnect (starts at 1s, caps at 30s, resets on success)
 └─ _event_pump   Consumes the SSE queue → translate_event translation → downlink
SSE daemon thread _sse_thread
 └─ requests streams GET /event (blocking IO, cannot run in the event loop)
    → json.loads per frame → loop.call_soon_threadsafe(evq.put_nowait, ev) hands it back to the main loop
asyncio.to_thread thread pool
 └─ All blocking HTTP calls to opencode (requests) are offloaded here, never blocking the event loop
```

In `run()`, `asyncio.gather(_relay_loop(), _event_pump())`: if either main coroutine genuinely crashes, the process exits and systemd brings it back up — no silent internal respawn; failures must be visible.

### 6.2 Uplink: the Command Dispatch Table

Phone frames enter `_on_phone_frame`, where an if/elif dispatch table maps them to opencode endpoints (excerpt):

| Phone frame | opencode call |
|---|---|
| `send {session,text,atts}` | Confirm/create session → `POST /session/:id/prompt_async` (core path, see 6.3) |
| `stop` | `POST /session/:id/abort` + locally emit a supplementary `run:finished` to immediately finalize the UI |
| `state` | `GET /session` + git branch + session list → `state` frame |
| `messages {id}` | `GET /session/:id/message`, assembled into `{role,text,images}` |
| `models` / `model` | `GET /config/providers` flattened / record the current model |
| `mode {value}` | Switch permission mode (only changes bridge-side state) |
| `permission_response` | Map `allow/deny/always → once/reject/always` → `POST /session/:sid/permissions/:pid` |
| `workspace` / `dir_list` / `new_session` | Switch workspace / browse subdirectories (`GET /file?directory=`) / create a session per directory |
| `commands` / `command` | `GET /command` returns the real command list / execute one (see 6.5) |
| `question_reply` / `question_reject` | `POST /question/:id/reply|reject` (answering opencode's questions) |

No branch may break the connection by throwing: exceptions are caught and converted into an `error` frame back to the phone.

### 6.3 `send`: the Core Translation Path

1. **Attachment triage** (aligned with the desktop's `desktop/relay.go`): attachments starting with `data:image` are passed straight through as opencode's native multimodal `FilePartInput {type:file,mime,filename,url}`; other files are base64-decoded and written to disk at `<workspace>/media/<nanosecond-timestamp>-<filename>` (`basename` guards against traversal), and their absolute paths are appended into the prompt body so the agent reads them with its own tools;
2. **No-session fallback**: if `sid` is empty or the session was deleted, a session is first created in the current workspace (title taken from the first 40 characters of the message);
3. **Echo before submit**: a `user_message` frame is sent first so the phone displays immediately (the displayed text includes 📎 attachment names; the prompt sent to the model does not), then `prompt_async` is POSTed — it is an async endpoint that returns upon submission, with all subsequent output streaming back over the `/event` SSE stream.

### 6.4 Downlink: SSE Events → Phone Frames

`translate_event` turns one SSE event into 0..n phone frames. Two details worth noting:

- **"Edge" detection.** `session.status` events fire on every agent step, but the phone only needs the two edges "started/finished": `running_map[sid]` remembers the busy/idle state and emits `run:started` only on idle→busy, and `run:finished` + `done` on busy→idle, preventing the UI from being reset repeatedly during multi-step execution;
- **Tool part deduplication.** opencode emits `message.part.updated` multiple times for the same tool part (pending/running/completed/error); the bridge keeps accounts in a `_tool_seen` table keyed by part id: the first sighting emits `tool_start`, completed/error emits `tool_result` (output truncated to the last 900 characters), and intermediate states are swallowed.

Other mappings: `message.part.delta(field=text)` → `text` (streaming deltas); `session.updated`'s tokens → `usage`; `session.todo` → `todo` (the real task list feeds the phone's step bar directly); irrelevant events like `file/storage/lsp` are dropped.

### 6.5 Three Permission Modes and Fail-Closed

opencode emits `permission.asked` before executing a write operation (provided the corresponding tool is set to top-level `permission: ask` in opencode's config). The bridge branches on the phone's permission mode:

| Mode | The bridge's action |
|---|---|
| `readonly` | Automatically `POST …/permissions/:pid → {response:"reject"}`, without bothering the phone |
| `always` | Automatically answers `{response:"always"}` |
| `ask` | Forwards a `permission_request` to the phone; if `PERM_TIMEOUT` (default 120s) elapses without an answer, **auto-reject** |

Fail-closed applies in three places: timeout defaults to rejection; any unknown response value from the phone is mapped to `reject`; and all answers (automatic / timeout / human) funnel through the single exit `_answer_permission`, which is naturally idempotent. **The iron rule of remote control: rather stop and wait for confirmation than ever default to allowing on the user's behalf.**

### 6.6 Reconnect Compensation: the Cost and the Safety Net of a Dumb Pipe

The relay caches nothing, so downlink frames produced while the bridge is disconnected are lost forever. The bridge's safety net is not replay (it does not store frames either) but **telling the phone to re-pull full state**:

```python
self._missed: set[str]   # session ids whose frames were missed while disconnected
# _send() notices ws is empty/send failed: record the frame's session into _missed
# _relay_loop() after a successful reconnect and the hello handshake:
for msid in missed:
    await self._send({"type": "session:opened", "id": msid})  # phone re-pulls that session's messages on receipt
```

The price of weak consistency is that both ends stay stateless: the relay needs no persistence, the bridge needs to remember no frames, and eventual consistency is achieved by "re-pulling." Combined with the 1s→30s exponential backoff reconnect, 20s WS heartbeats, the phone's 3s auto-reconnect, and 8s `state` polling, the whole pipeline self-heals after a jitter in any segment.

### 6.7 Directory Scoping: One `serve` Serving Multiple Projects

opencode 1.x namespaces sessions/files/commands under the "project directory." The bridge appends `?directory=` to all session-level calls via `scoped()`, and uses `_dir_by_sid` to remember each session's birth directory — so switching the workspace on the phone never harms sessions in other directories. The phone can also use `dir_list` to browse subdirectories and `new_session` to start a chat in a chosen directory with one tap (matching the remote "switch to another project" need).

## 7. The Phone Console (service/page.html + js/main.js)

The frontend is deliberately "build-free, single-file, auditable": vanilla JS, one WS, one DOM — no framework, no bundler. A few implementation highlights:

- **Request-response pairing**: every frame carries an auto-incrementing `rid`; `req()` returns a Promise stored in the `pend` table with a 9s timeout; event frames (no `rid`) go through a separate `onmessage` dispatch chain.
- **Streaming rendering**: `text` frame deltas accumulate into a buffer, and the whole buffer is re-run through `mdRender()` (HTML-escape first, then render a whitelist of syntax: headings/lists/inline code/fenced code blocks/tables) — **escape first, render second**, eliminating model-output HTML/XSS injection at the root.
- **Process visualization**: `tool_start/tool_result` render as expandable cards (key fields like `path/cmd/query` are extracted into a one-line summary); `todo` frames drive the task step bar (four states: done/run/wait/deny); when multiple sessions run in parallel, a "running" banner appears at the top — tap it to switch which session you watch.
- **Images**: images selected/pasted/dragged in are compressed via canvas to 1280px, JPEG 0.8, then converted to a dataURL for upload (reachable directly via the `/file` slash command); images in messages show as thumbnails with a lightbox zoom.
- **Connection self-healing**: `onclose` triggers an automatic reconnect after 3 seconds, clearing the screen and re-pulling; `state` is polled every 8 seconds as a fallback.

## 8. Nginx: the Only Public Entry Point

The relay binds only to `127.0.0.1:9000`; only Nginx's 80/443 are exposed to the public internet. The full annotated config is in `service/nginx.conf.example`; the core is a set of directives where missing any single one produces a classic failure:

```nginx
location / {
    proxy_pass         http://127.0.0.1:9000;
    proxy_http_version 1.1;
    proxy_set_header   Upgrade    $http_upgrade;   # ① WebSocket upgrade
    proxy_set_header   Connection "upgrade";
    proxy_set_header   Host       $host;
    proxy_buffering    off;                        # ② Streaming
    proxy_read_timeout 3600s;                      # ③ Long-lived connections
    proxy_send_timeout 3600s;
}
```

| Directive | What happens without it |
|---|---|
| ① `Upgrade/Connection` + HTTP/1.1 | The WS upgrade handshake fails, and the phone page endlessly shows "Connection closed (code 1006)"; both WS routes, `/s/ws` and `/client`, go through here |
| ② `proxy_buffering off` | Nginx only flushes to the browser once its buffer fills; on the phone this looks like "spinning forever, then the reply pops out all at once." Same story for `/event` SSE in Option B, which serves the official UI directly |
| ③ `proxy_read_timeout 3600s` | The 60s default kills every quiet long-lived connection: idle phone pages and the WS between bridge and relay all get disconnected periodically |

Supporting deployment pieces: a 301 redirect from port 80 to HTTPS; `client_max_body_size` to bound ordinary request bodies (WS frame size is governed separately by the relay's `RELAY_WS_MAX`); the relay runs persistently under systemd (`Restart=always` + `NoNewPrivileges/PrivateTmp`). Option B's variant — an SSH reverse tunnel serving the official opencode UI directly — reuses the same Nginx directives; see `TUNNEL-3STEPS.md`.

## 9. Security Model: Three Credential Layers, Each Guarding Its Own Segment

| Layer | Credential | Where | What it guards |
|---|---|---|---|
| ① Model account | provider API key | Home PC: `~/.local/share/opencode/auth.json` | The wallet that pays for LLM calls, **never leaves the intranet** — the relay/phone see the conversation, never the key |
| ② opencode server password | `OPENCODE_SERVER_PASSWORD` | Home PC env var + bridge config | Protects the HTTP API on 9001; held on behalf of the bridge (HTTP Basic, username fixed as `opencode`). Unnecessary if not exposed externally |
| ③ Device token | `device_token` (`openssl rand -hex 32`) | VPS whitelist + bridge config + phone URL `?d=` | The relay's sole authentication; **the token is the control channel** — rotate immediately if leaked |

Layered on top are several deny-by-default measures: an empty whitelist rejects all connections, `listen` binds only to loopback, static assets have three whitelist layers, and permission answers are fail-closed. One boundary must be stated explicitly: **the relay can see chat frames in plaintext** (the dumb pipe is only "dumb" about business logic — it is not blind) — so it must be deployed on your own VPS; do not use any third-party tunneling service.

## 10. Limitations and Evolution

- **Frames are not replayed**: reconnect compensation is "re-pull," not "replay," so reconnecting to a very large session history incurs one full fetch;
- **One token, one device**: one PC, one token; to control multiple machines, just add entries to the whitelist (the protocol is already isolated per token — the desktop's `desktop/relay.go` is the second consumer);
- **Reasoning effort levels**: opencode has no equivalent concept, so bridge v1 ignores it; reasoning text has no display slot on the phone page and is not forwarded;
- **Demo-grade vs production-grade**: the entire server side has no database and no persistence — a restart returns it to its initial state. This is the direct consequence of the "dumb pipe + stateless bridge" design choice, and also the reason its operational cost is nearly zero.

---

*Code: `service/` (VPS) + `pc/` (home PC); getting started: `README-opencode.md`; config details: `config.md`;
the direct-serve official UI option: `TUNNEL-3STEPS.md`.*
