# 服务器搭建指南：biancheng.mei.biz（手机控制台）/ openi.mei.biz（opencode web）

> **本文位置**：`relay-server/DEPLOY.md` —— 与被部署代码同目录。
> **本文写给谁**：跑在服务器（154.9.25.70）上的智能体。按顺序执行，每步带验证；
> 任何一步不通过就停下排查，不要跳过安全项。标注「👤 用户本机」的步骤**不由你执行**，
> 汇总到第 7 步转告用户即可（本机侧也可直接跑仓库 `scripts/uplink.sh` 一键完成）。
>
> **参考基线**：与同目录 `INSTRUCTIONS-AGENT.md`（面向智能体的官方部署说明）
> 同构；验证用同目录 `SELF-CHECK.md` + `selftest.py`。冲突时以那两份为准。

## 0. 角色铁律（先读 this）

- **服务器（154.9.25.70）上只跑两样东西**：① `relay-server` 哑管道中继（含手机静态页）；② nginx/Tengine 反代。
- **opencode 绝不装在服务器上**。opencode、桥（opencode_bridge.py）都只跑在**用户的个人电脑**上；
  桥从个人电脑**主动出站**连服务器（NAT 友好，服务器无需知道用户 IP）。
- 服务器是"哑管道"：只校验 token、转发 JSON 帧、发静态页，**不解析业务、不存消息**。

```
用户个人电脑(NAT内)                                            本服务器 154.9.25.70
├─ opencode serve :9001  ←──────────────────────────────────┐
├─ 桥 opencode_bridge.py ──出站WS──> wss://biancheng.mei.biz/client ──> 中继 :9000 (relay-server,
└─ autossh -R 19001:127.0.0.1:9001 ─────────────────────────────> :19001    部署于 /www/wwwroot/biancheng.mei.biz)
手机浏览器 ──HTTPS──> https://biancheng.mei.biz/s/?d=<token>（手机控制台；open.mei.biz 同效）
                     https://openi.mei.biz（opencode 官方 web，带密码）
```

| 域名 | 服务 | 实现 |
|---|---|---|
| `biancheng.mei.biz`（主）`open.mei.biz`（别名） | 手机控制台 | 服务器部署 `relay-server` 哑管道，目录 **`/www/wwwroot/biancheng.mei.biz/`**（用户本机的桥出站接入） |
| `openi.mei.biz` | opencode 官方 web | nginx → `127.0.0.1:19001` ← SSH 反向隧道 ← 用户本机 9001 |
| `op.mei.biz` | （预留，本指南未使用） | 如需作控制台别名：加进 server_name 并签进证书即可 |

## 1. 前置检查（逐条执行并记录）

```bash
cat /etc/os-release | head -2
nginx -v 2>&1 || tengine -v 2>&1
ss -ltnp | grep -E ':80 |:443 '
python3 --version                                  # ≥3.10

# --- 宝塔环境 ---
ls -d /www/server/panel /www/wwwroot/biancheng.mei.biz /www/wwwroot/openi.mei.biz
ls /www/server/panel/vhost/nginx/ | grep -E "biancheng|openi|open\."
grep -nE "include.*vhost" /www/server/nginx/conf/nginx.conf   # vhost 目录已被主配置包含
```

**检查点**：`biancheng.mei.biz.conf`、`openi.mei.biz.conf` 存在且被 include。
**改这两个 conf 前必须备份**（备份到 `/root/opencode-secrets/backup/`）。

## 2. 生成两把密钥（现在生成，第 7 步交付用户）

```bash
TOKEN=$(openssl rand -hex 32)                                     # 中继设备令牌（64位hex）
OCPW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 16) # opencode web 密码
mkdir -p /root/opencode-secrets/backup
echo -e "TOKEN=$TOKEN\nOCPW=$OCPW" > /root/opencode-secrets/keys.txt && chmod 600 /root/opencode-secrets/keys.txt
echo "TOKEN=$TOKEN"; echo "OCPW=$OCPW"
```

> ⚠️ 旧 `testtoken123` 是本机测试值，公网一律作废。这两个密钥**只在第 7 步交付给用户**。

## 3. 部署 open.mei.biz 控制台：`relay-server` 哑管道 → `/www/wwwroot/biancheng.mei.biz/`

> 与同目录 `INSTRUCTIONS-AGENT.md` 完全同构；差异仅三点：反代用宝塔 Tengine+certbot
> （该文档写 Caddy；本机 80 已被 Tengine 占用）、部署目录用宝塔站点目录、新增静态资源路由验证。

### 3.1 获取代码（来自用户 GitHub 仓库）

```bash
git clone --depth 1 https://github.com/hzerther-hub/localaicoder.git /tmp/lai
cp -r /tmp/lai/relay-server/{main.py,page.html,css,js,requirements.txt,selftest.py,SELF-CHECK.md,INSTRUCTIONS-AGENT.md} \
   /www/wwwroot/biancheng.mei.biz/
rm -rf /tmp/lai
ls /www/wwwroot/biancheng.mei.biz/    # 应含 main.py page.html css js requirements.txt selftest.py
```

> clone 失败则请用户本机执行：
> `scp -r ~/build/localaicoder/relay-server/{main.py,page.html,css,js,requirements.txt,selftest.py,SELF-CHECK.md} root@154.9.25.70:/www/wwwroot/biancheng.mei.biz/`

### 3.2 依赖与配置

```bash
cd /www/wwwroot/biancheng.mei.biz
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
cat > config.json <<EOF
{ "listen": "127.0.0.1:9000", "device_tokens": ["$TOKEN"] }
EOF
```

### 3.3 systemd 常驻

```bash
cat > /etc/systemd/system/relay-server.service <<EOF
[Unit]
Description=relay-server dumb pipe (biancheng.mei.biz)
After=network-online.target
[Service]
WorkingDirectory=/www/wwwroot/biancheng.mei.biz
ExecStart=/www/wwwroot/biancheng.mei.biz/.venv/bin/python main.py -config /www/wwwroot/biancheng.mei.biz/config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now relay-server
sleep 1 && systemctl is-active relay-server                                                # active
curl -s -o /dev/null -w "local /s/ http=%{http_code}\n" "http://127.0.0.1:9000/s/?d=$TOKEN" # 200
```

## 4. 宝塔站点 conf 改为反代（先备份，再整体替换）

> 改 `/www/server/panel/vhost/nginx/biancheng.mei.biz.conf` 与 `openi.mei.biz.conf`。
> biancheng 的 `server_name` 同时写两个域名（biancheng + open 均可访问控制台）。

```bash
VH=/www/server/panel/vhost/nginx
cp -a $VH/biancheng.mei.biz.conf /root/opencode-secrets/backup/ 2>/dev/null
cp -a $VH/openi.mei.biz.conf     /root/opencode-secrets/backup/ 2>/dev/null

# ---- biancheng.mei.biz.conf：中继（页面 + WebSocket /client /s/ws）；open.mei.biz 为别名 ----
cat > $VH/biancheng.mei.biz.conf <<'EOF'
server {
    listen 80;
    server_name biancheng.mei.biz open.mei.biz;
    root /www/wwwroot/biancheng.mei.biz;
    client_max_body_size 20m;                    # 手机传图 base64 可达 5MB
    location ^~ /.well-known/acme-challenge/ { root /www/wwwroot/biancheng.mei.biz; }
    location / {
        proxy_pass http://127.0.0.1:9000;        # relay-server 哑管道
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;  # WebSocket(/client /s/ws) 必需
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
EOF

# ---- openi.mei.biz.conf：opencode 官方 web（隧道→用户本机 9001；敏感站点）----
cat > $VH/openi.mei.biz.conf <<'EOF'
server {
    listen 80;
    server_name openi.mei.biz;
    root /www/wwwroot/openi.mei.biz;
    client_max_body_size 100m;
    location ^~ /.well-known/acme-challenge/ { root /www/wwwroot/openi.mei.biz; }
    location / {
        proxy_pass http://127.0.0.1:19001;       # SSH 反向隧道 → 用户本机 9001
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_buffering off;                     # SSE 流式必须关缓冲
        proxy_cache off;
    }
}
EOF

nginx -t && nginx -s reload
```

> `:19001` 还没人监听 → `openi.mei.biz` 暂时 502/504 属预期，等用户本机起隧道（第 7 步③）。

## 5. TLS 证书（webroot 签发 + 追加 443 块 + 80 跳转）

### 5.1 签发（用站点目录当 webroot，不停 nginx；biancheng 一张证书同时覆盖 open 别名）

```bash
apt install -y certbot 2>/dev/null || yum install -y certbot
certbot certonly --webroot -w /www/wwwroot/biancheng.mei.biz \
  -d biancheng.mei.biz -d open.mei.biz --non-interactive --agree-tos -m admin@mei.biz
certbot certonly --webroot -w /www/wwwroot/openi.mei.biz \
  -d openi.mei.biz --non-interactive --agree-tos -m admin@mei.biz
ls /etc/letsencrypt/live/        # 应有 biancheng.mei.biz/ 与 openi.mei.biz/
```

### 5.2 追加 443 server 块（反代内容与 80 相同）

```bash
# --- biancheng.mei.biz：443（证书同时覆盖 open.mei.biz 别名）---
cat >> $VH/biancheng.mei.biz.conf <<'EOF'
server {
    listen 443 ssl;
    server_name biancheng.mei.biz open.mei.biz;
    ssl_certificate     /etc/letsencrypt/live/biancheng.mei.biz/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/biancheng.mei.biz/privkey.pem;
    client_max_body_size 20m;
    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
EOF

# --- openi.mei.biz：443 ---
cat >> $VH/openi.mei.biz.conf <<'EOF'
server {
    listen 443 ssl;
    server_name openi.mei.biz;
    ssl_certificate     /etc/letsencrypt/live/openi.mei.biz/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/openi.mei.biz/privkey.pem;
    client_max_body_size 100m;
    location / {
        proxy_pass http://127.0.0.1:19001;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_buffering off;
    }
}
EOF
```

### 5.3 两个 80 块改为跳转 HTTPS（⚠️ 必须在追加 443 块之后执行，否则会误伤）

```bash
sed -i 's|proxy_pass http://127.0.0.1:9000;|return 301 https://$host$request_uri;|'  $VH/biancheng.mei.biz.conf
sed -i 's|proxy_pass http://127.0.0.1:19001;|return 301 https://$host$request_uri;|' $VH/openi.mei.biz.conf
nginx -t && nginx -s reload
curl -sI https://biancheng.mei.biz | head -2        # TLS 正常
```

> 顺序说明：先追加 443（内含同样的 proxy_pass），再把 80 块的 proxy_pass 换成 301——
> 反过来做会把 443 的反代也误改成 301。acme 的 `^~` location 不受影响，续期照常。

## 6. 服务器侧验证（官方自检工具 + 补充）

```bash
# 6.1 官方一键自检（两个域名都跑）
cd /www/wwwroot/biancheng.mei.biz && .venv/bin/python selftest.py biancheng.mei.biz "$TOKEN"
.venv/bin/python selftest.py open.mei.biz "$TOKEN"
# 期望：[1]DNS ['154.9.25.70']  [2]HTTPS /s/ 200  [3]WS /s/ws ✅  [4]WS /client ✅
#       （用户本机桥未接入时 [3]/[4] 显示等待，属预期）

# 6.2 静态资源（旧进程缺该路由会导致"无样式"，必须验证）
curl -s -o /dev/null -w "style.css %{http_code} %{content_type}\n" "https://biancheng.mei.biz/s/css/style.css"  # 200 text/css
curl -s -o /dev/null -w "main.js   %{http_code} %{content_type}\n" "https://biancheng.mei.biz/s/js/main.js"     # 200 text/javascript
# 6.3 错误 token 必须 403
curl -s -o /dev/null -w "wrong-token %{http_code}\n" "https://biancheng.mei.biz/s/?d=wrong"                     # 403
# 6.4 openi：隧道未通前 502/504 属预期
curl -s -o /dev/null -w "openi %{http_code}\n" "https://openi.mei.biz/"
# 6.5 公网监听面收口：只应有 22/80/443
ss -ltnp | grep -vE "127.0.0.1|::1"
```

## 7. 交付物（原样发给用户；含👤用户本机三步）

服务器侧全部通过后，把下面整段发给用户：

---

### 📋 交付用户

```text
手机控制台   https://biancheng.mei.biz/s/?d=<keys.txt 里的 TOKEN>
            （别名 https://open.mei.biz/s/?d=<TOKEN> 同效）
设备令牌     <keys.txt 里的 TOKEN>
web 密码     <keys.txt 里的 OCPW>
官方 web     https://openi.mei.biz（密码=<OCPW>）
```

**👤 用户本机三步**（一键脚本：`scripts/uplink.sh <TOKEN> '<OCPW>'`，其内容即下面三步）：

```bash
# ① opencode 带密码重启（openi 暴露公网，无密码=任何人可操作你电脑！）
pkill -f "opencode serve" ; sleep 1
cd /home/wellfuture/build/localaicoder
OPENCODE_SERVER_PASSWORD='<OCPW>' nohup opencode serve --hostname 127.0.0.1 --port 9001 >/dev/null 2>&1 &

# ② 桥改为出站连公网中继（biancheng.mei.biz）
python3 - <<'PY'
import json
p='opencode-relay/opencode_bridge.json'
c=json.load(open(p)); c['relay']['server_url']='https://biancheng.mei.biz'; c['relay']['device_token']='<TOKEN>'
json.dump(c,open(p,'w'),ensure_ascii=False,indent=2); print('bridge config updated')
PY
pkill -f opencode_bridge.py ; sleep 1
( cd opencode-relay && nohup python3 opencode_bridge.py --config opencode_bridge.json >/dev/null 2>&1 & )

# ③ openi 反向隧道（断线自动重连）
apt-get install -y autossh 2>/dev/null || brew install autossh
ssh-copy-id root@154.9.25.70          # 首次输一次密码
autossh -M 0 -f -N -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -o ExitOnForwardFailure=yes -R 127.0.0.1:19001:127.0.0.1:9001 root@154.9.25.70
```

**最终验收**：手机开 `https://biancheng.mei.biz/s/?d=<TOKEN>` 可聊天、可选子目录；浏览器开 `https://openi.mei.biz` 输 `<OCPW>` 进官方 web；服务器 `ss -ltnp | grep 19001` 有 sshd 监听。

> 说明：桥连到公网中继后，本机 `127.0.0.1:8999` 入口默认不再有桥接入（一个桥连一条中继）。
> 若局域网内还想用 8999，可再跑一个桥实例指向 `ws://127.0.0.1:8999`（可选）。

## 8. 服务器智能体注意事项

1. **服务器上不装 opencode、不跑桥**——opencode 与桥只在用户个人电脑（第 0 节铁律）。
2. `/root/opencode-secrets/keys.txt` 内容不要写入日志或额外文件。
3. 中继是哑管道但能看到聊天明文：不得加装任何记录/转发/分析逻辑。
4. 用户本机的桥带自动重连：重启 relay-server 服务后桥会自行恢复，无需通知用户。
5. **宝塔面板**：面板里对站点做设置变更可能覆盖手改的 conf，改完面板后需核对反代与 443 配置仍在（备份在 `/root/opencode-secrets/backup/`）。
6. 部署目录 `/www/wwwroot/biancheng.mei.biz/` 内含 `config.json`（token）与 `.venv/`，
   **不要**在该站点开静态目录列举；nginx 对该站点是全量反代，静态文件不会直接暴露。
7. 仓库代码若与本文不符（用户可能已更新），以同目录 `INSTRUCTIONS-AGENT.md`、
   `SELF-CHECK.md` 与 `opencode-relay/config.md` 为准。
