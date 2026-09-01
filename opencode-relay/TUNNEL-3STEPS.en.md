English | [中文](TUNNEL-3STEPS.md)

# Three-Step Tunnel · Put the Official opencode UI on the Public Internet (Server-Side AI Execution Manual)

> Goal: open **https://oc.mei.biz** on a phone/from the external network and see the official opencode web UI **exactly identical** to the home PC's `http://127.0.0.1:9001` (all features: sessions, diff, terminal, command palette).
>
> Data flow:
> ```
> Phone browser ──HTTPS──> VPS nginx(oc.mei.biz) ──> 127.0.0.1:19001 (tunnel hole)
>                     ≈ SSH reverse tunnel ≈>  home PC 127.0.0.1:9001 opencode serve (official UI + API)
> ```
>
> Division of labor: **Step 0 and Step 3 are done on the VPS (server-side AI)**; **Step 1 and Step 2 are done on the user's home PC** (the user pastes two commands, or the home PC AI executes them). At the end of Step 0 you will produce two "deliverables" — send them to the user in full.

---

## Step 0 (VPS · server-side AI): Prepare keys, DNS, and the site

### 0.1 Generate a dedicated tunnel key pair and authorize it

```bash
mkdir -p /root/.opencode-tunnel
[ -f /root/.opencode-tunnel/id_ed25519 ] || ssh-keygen -t ed25519 -N "" -f /root/.opencode-tunnel/id_ed25519 -C "opencode-tunnel"
# Add the public key to the authorized list (deduplicated)
grep -qF "$(cat /root/.opencode-tunnel/id_ed25519.pub)" /root/.ssh/authorized_keys 2>/dev/null || \
  cat /root/.opencode-tunnel/id_ed25519.pub >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
# Generate a strong password for opencode serve (also deliver it to the user)
openssl rand -base64 12 | tr -d '=+/' | head -c 12
```

### 0.2 DNS

In the domain manager, add an A record: **`oc.mei.biz` → this VPS's public IP**.
(If DNS cannot be added: the fallback is to use port `http://oc.mei.biz:9090`, which requires allowing 9090 in the BaoTa security group, has no TLS, and is for debugging only.)

### 0.3 Site reverse proxy (BaoTa or hand-written nginx, either works)

BaoTa: add site `oc.mei.biz` (pure static is fine) → Settings → Reverse Proxy → target `http://127.0.0.1:19001`, and enable WebSocket support.
Equivalent hand-written nginx config:

```nginx
server {
    listen 80;
    server_name oc.mei.biz;
    location / {
        proxy_pass         http://127.0.0.1:19001;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;      # Required for WebSocket (PTY terminal)
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;                        # Required for the SSE /event stream
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

After configuring, self-check on the VPS first (the tunnel isn't up yet, so a 502 is expected):

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:19001/   # Expect 000/502 (nobody is answering the hole yet)
```

### 0.4 Deliverables (the two items to send back to the user / home PC AI)

1. **Full private key text** (`cat /root/.opencode-tunnel/id_ed25519`, three PEM lines) — the user saves it on the home PC as `/home/wellfuture/.ssh/opencode_tunnel`
2. **The opencode password** (the 12-character string generated in 0.1)

---

## Step 1 (home PC): Restart opencode with a password

> Both the official UI and API are served by the home PC's `opencode serve`; going on the public internet requires a password.

```bash
pkill -f "opencode serve"; sleep 1
cd /home/wellfuture/build/localaicoder
OPENCODE_SERVER_PASSWORD='<the password delivered in Step 0>' nohup opencode serve \
  --hostname 127.0.0.1 --port 9001 > .ocdata/opencode-serve.log 2>&1 &
echo $! > .ocdata/serve.pid
sleep 2 && curl -sf http://127.0.0.1:9001/global/health && echo " 9001 OK"
```

Note: if the home PC's opencode relay bridge (`opencode_bridge.py`) has `opencode.password` configured, it must be updated to match; if not configured, ignore this. Once 9001 has a password, the bridge will get 401 when connecting to it — fix:

```bash
python3 - <<'PY'
import json
p='/home/wellfuture/build/localaicoder/opencode-relay/pc/opencode_bridge.json'
c=json.load(open(p)); c['opencode']['password']='<the same password>'
json.dump(c,open(p,'w'),ensure_ascii=False,indent=2); print('bridge password updated')
PY
pkill -f 'opencode_bridg[e]\.py'; sleep 1
cd /home/wellfuture/build/localaicoder/opencode-relay/pc && setsid nohup python3 opencode_bridge.py \
  --config opencode_bridge.json > /home/wellfuture/build/localaicoder/.ocdata/bridge.log 2>&1 < /dev/null &
```

---

## Step 2 (home PC): Start the reverse tunnel

```bash
# Save the private key (content = Step 0 deliverable 1; permissions must be 600)
mkdir -p ~/.ssh && chmod 700 ~/.ssh
cat > ~/.ssh/opencode_tunnel <<'EOF'
<paste the full private key text>
EOF
chmod 600 ~/.ssh/opencode_tunnel

# Install autossh (if not present)
command -v autossh >/dev/null || (sudo apt-get install -y autossh || brew install autossh)

# Start the tunnel: VPS 127.0.0.1:19001 ←→ home PC 127.0.0.1:9001, auto-reconnect on disconnect
export AUTOSSH_GATETIME=0
autossh -M 0 -f -N \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new \
  -i ~/.ssh/opencode_tunnel -R 127.0.0.1:19001:127.0.0.1:9001 root@154.9.25.70

# Self-check: is the tunnel up (from the home PC's perspective)
sleep 2; pgrep -f "ssh.*19001" >/dev/null && echo "tunnel process OK" || echo "tunnel not started"
```

---

## Step 3 (VPS · server-side AI): Acceptance checks

```bash
# 1) The hole is connected: should return the official UI's HTML (containing the string "opencode")
curl -s http://127.0.0.1:19001/ | grep -o "<title>[^<]*" | head -1

# 2) Full chain of domain + reverse proxy + TLS: should be 200/30x with the same title as above
curl -s -o /dev/null -w "%{http_code}\n" https://oc.mei.biz/

# 3) SSE stream is not buffered: you should see continuous data: frames or a long-lived connection hanging (exit with Ctrl+C)
timeout 4 curl -s -N http://127.0.0.1:19001/event | head -3

# 4) Password is in effect: without a password you should get 401
curl -s -o /dev/null -w "%{http_code}\n" https://oc.mei.biz/session   # Expect 401
```

Once all four checks pass, tell the user: **open https://oc.mei.biz on the phone; the login password = the password delivered in Step 0.**

---

## Common Troubleshooting

| Symptom | Cause / Fix |
|---|---|
| Home PC autossh fails with `Permission denied` | Private key saved incorrectly / permissions not 600; or the 0.1 public key was not added to `authorized_keys` |
| Page returns 502 | Tunnel not started (check `pgrep -f 19001` on the home PC) or 19001 is already occupied (on the VPS: `ss -tlnp | grep 19001`) |
| Page opens but keeps spinning / no streaming | nginx is missing `proxy_buffering off`; or the WebSocket upgrade headers are not enabled (terminal feature unavailable) |
| Domain doesn't open | DNS A record not effective yet: verify with `dig +short oc.mei.biz` |
| Password error message | In the browser's Basic auth, the username must be `opencode`, and the password = the delivered password |
| Terminal (PTY) won't connect on the phone | The reverse proxy must pass WebSocket through (`Upgrade/Connection` headers) — check each one |

## Relationship to Existing Services

- `op.mei.biz/s/` (lightweight console + new official-style UI) is untouched — the two coexist.
- The home PC's 9001 now requires a password: for local development connecting directly to 9001, log in with username `opencode` and the password.
- The tunnel is bound only to the VPS's `127.0.0.1:19001`; the public internet cannot bypass nginx and connect to that port directly.
