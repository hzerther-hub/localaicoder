# 服务器搭建指南：open.mei.biz / openi.mei.biz

> **本文写给谁**：跑在服务器（154.9.25.70）上的智能体。按顺序执行，每步带验证；
> 任何一步不通过就停下排查，不要跳过安全项。标注「👤 用户本机」的步骤**不由你执行**，
> 汇总到第 7 步转告用户即可。
>
> **参考基线**：本指南与仓库 `relay-server/INSTRUCTIONS-AGENT.md`（面向智能体的官方部署说明）
> 同构；验证用 `relay-server/SELF-CHECK.md` + `relay-server/selftest.py`。冲突时以那两份为准。

## 0. 角色铁律（先读 this）

- **服务器（154.9.25.70）上只跑两样东西**：① `relay-server` 哑管道中继（含手机静态页）；② nginx/Tengine 反代。
- **opencode 绝不装在服务器上**。opencode、桥（opencode_bridge.py）都只跑在**用户的个人电脑**上；
  桥从个人电脑**主动出站**连服务器（NAT 友好，服务器无需知道用户 IP）。
- 服务器是"哑管道"：只校验 token、转发 JSON 帧、发静态页，**不解析业务、不存消息**。

```
用户个人电脑(NAT内)                                    本服务器 154.9.25.70
├─ opencode serve :9001  ←──────────────────────────┐
├─ 桥 opencode_bridge.py ──出站WS──> wss://open.mei.biz/client ──> 中继 :9000 (relay-server)
└─ autossh -R 19001:127.0.0.1:9001 ─────────────────────> :19001 <── openi.mei.biz (nginx)
手机浏览器 ──HTTPS──> https://open.mei.biz/s/?d=<token>（手机控制台）
                     https://openi.mei.biz（opencode 官方 web，带密码）
```

| 域名 | 服务 | 实现 |
|---|---|---|
| `open.mei.biz` | 手机控制台 | 服务器部署 `relay-server` 哑管道（用户本机的桥出站接入） |
| `openi.mei.biz` | opencode 官方 web | nginx → `127.0.0.1:19001` ← SSH 反向隧道 ← 用户本机 9001 |

两域名已解析到本服务器；80 端口已有 Tengine 在跑默认页（443 尚未配置）。

## 1. 前置检查（逐条执行并记录）

```bash
cat /etc/os-release | head -2
nginx -v 2>&1 || tengine -v 2>&1                 # Tengine 兼容 nginx 配置语法
ss -ltnp | grep -E ':80 |:443 '
nginx -V 2>&1 | tr ' ' '\n' | grep conf-path     # 记下配置目录
python3 --version                                 # ≥3.10
ufw status 2>/dev/null || firewall-cmd --list-all 2>/dev/null || echo "无防火墙管理器"
```

## 2. 生成两把密钥（现在生成，第 7 步交付用户）

```bash
TOKEN=$(openssl rand -hex 32)                                    # 中继设备令牌（64位hex）
OCPW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 16) # opencode web 密码
mkdir -p /root/opencode-secrets
echo -e "TOKEN=$TOKEN\nOCPW=$OCPW" > /root/opencode-secrets/keys.txt && chmod 600 /root/opencode-secrets/keys.txt
echo "TOKEN=$TOKEN"; echo "OCPW=$OCPW"
```

> ⚠️ 旧 `testtoken123` 是本机测试值，公网一律作废。这两个密钥**只在第 7 步交付给用户**，
> 不要写进日志或其他文件。

## 3. 部署 open.mei.biz：`relay-server` 哑管道

> 与 `relay-server/INSTRUCTIONS-AGENT.md` 完全同构；唯一差异：反代用本机已有的
> Tengine+certbot（该文档写的是 Caddy；本机 80 已被 Tengine 占用，不重复装 Caddy）。

### 3.1 获取代码（来自用户 GitHub 仓库）

```bash
git clone --depth 1 https://github.com/hzerther-hub/localaicoder.git /tmp/lai
mkdir -p /opt/relay-server
cp -r /tmp/lai/relay-server/{main.py,page.html,css,js,requirements.txt,selftest.py,SELF-CHECK.md,INSTRUCTIONS-AGENT.md} /opt/relay-server/
rm -rf /tmp/lai
ls /opt/relay-server/     # 应含 main.py page.html css js requirements.txt selftest.py
```

> clone 失败则请用户本机执行：
> `scp -r ~/build/localaicoder/relay-server/{main.py,page.html,css,js,requirements.txt,selftest.py,SELF-CHECK.md} root@154.9.25.70:/opt/relay-server/`

### 3.2 依赖与配置

```bash
cd /opt/relay-server
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
cat > config.json <<EOF
{ "listen": "127.0.0.1:9000", "device_tokens": ["$TOKEN"] }
EOF
```

### 3.3 systemd 常驻

```bash
cat > /etc/systemd/system/relay-server.service <<EOF
[Unit]
Description=relay-server dumb pipe (open.mei.biz)
After=network-online.target
[Service]
WorkingDirectory=/opt/relay-server
ExecStart=/opt/relay-server/.venv/bin/python main.py -config /opt/relay-server/config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now relay-server
sleep 1 && systemctl is-active relay-server      # active
curl -s -o /dev/null -w "local /s/ http=%{http_code}\n" "http://127.0.0.1:9000/s/?d=$TOKEN"   # 200
```

## 4. nginx 两个虚拟主机（先 HTTP；证书第 5 步自动补）

```bash
NGCONF=$(dirname $(nginx -V 2>&1 | grep -o 'conf-path=[^ ]*' | cut -d= -f2))
mkdir -p $NGCONF/conf.d
cat > $NGCONF/conf.d/opencode-meibiz.conf <<'EOF'
# open.mei.biz : relay-server 哑管道（页面 + WebSocket /client /s/ws）
server {
    listen 80;
    server_name open.mei.biz;
    client_max_body_size 20m;                     # 手机传图 base64 可达 5MB
    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;    # WebSocket 必需
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
# openi.mei.biz : opencode 官方 web（SSH 反向隧道 → 用户本机 9001；敏感站点）
server {
    listen 80;
    server_name openi.mei.biz;
    client_max_body_size 100m;
    location / {
        proxy_pass http://127.0.0.1:19001;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_buffering off;                       # SSE 流式必须关缓冲
        proxy_cache off;
    }
}
EOF
nginx -t && systemctl reload nginx
```

> `:19001` 现在还没人监听 → `openi.mei.biz` 暂时 502/504 属预期，等用户本机起隧道。

## 5. TLS 证书（两域名一起签）

```bash
apt install -y certbot 2>/dev/null || yum install -y certbot
certbot --nginx -d open.mei.biz -d openi.mei.biz --non-interactive --agree-tos -m admin@mei.biz \
  || echo "nginx 插件不兼容 Tengine 时，改用 webroot：见 INSTRUCTIONS-AGENT.md 第 4 步兜底"
systemctl list-timers | grep -q certbot && echo "自动续期 OK"
curl -sI https://open.mei.biz | head -3
```

## 6. 服务器侧验证（官方自检工具 + 补充两条）

```bash
# 6.1 官方一键自检（relay-server/SELF-CHECK.md 的标准姿势）
cd /opt/relay-server && .venv/bin/python selftest.py open.mei.biz "$TOKEN"
# 期望：[1]DNS ['154.9.25.70']  [2]HTTPS /s/ 200  [3]WS /s/ws ✅  [4]WS /client ✅
#       （桌面未上线时 [3]/[4] 显示等待用户本机桥接入，属预期）

# 6.2 静态资源（旧进程缺该路由会导致"无样式"，必须验证）
curl -s -o /dev/null -w "style.css %{http_code} %{content_type}\n" "https://open.mei.biz/s/css/style.css"  # 200 text/css
curl -s -o /dev/null -w "main.js   %{http_code} %{content_type}\n" "https://open.mei.biz/s/js/main.js"     # 200 text/javascript
# 6.3 错误 token 必须 403
curl -s -o /dev/null -w "wrong-token %{http_code}\n" "https://open.mei.biz/s/?d=wrong"                     # 403
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
中继地址   https://open.mei.biz
设备令牌   <keys.txt 里的 TOKEN>
web 密码   <keys.txt 里的 OCPW>
手机入口   https://open.mei.biz/s/?d=<设备令牌>
官方 web   https://openi.mei.biz（密码=<OCPW>）
```

**👤 用户本机三步**（在 `~/build/localaicoder`）：

```bash
# ① opencode 带密码重启（openi 暴露公网，无密码=任何人可操作你电脑！）
pkill -f "opencode serve" ; sleep 1
cd /home/wellfuture/build/localaicoder
OPENCODE_SERVER_PASSWORD='<OCPW>' nohup opencode serve --hostname 127.0.0.1 --port 9001 >/dev/null 2>&1 &

# ② 桥改为出站连公网中继
python3 - <<'PY'
import json
p='opencode-relay/opencode_bridge.json'
c=json.load(open(p)); c['relay']['server_url']='https://open.mei.biz'; c['relay']['device_token']='<TOKEN>'
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

**最终验收**：手机开 `https://open.mei.biz/s/?d=<TOKEN>` 可聊天；浏览器开 `https://openi.mei.biz` 输 `<OCPW>` 进官方 web；服务器 `ss -ltnp | grep 19001` 有 sshd 监听。

> 说明：桥连到公网中继后，本机 `127.0.0.1:8999` 入口默认不再有桥接入（一个桥连一条中继）。
> 若局域网内还想用 8999，可再跑一个桥实例指向 `ws://127.0.0.1:8999`（可选）。

## 8. 服务器智能体注意事项

1. **服务器上不装 opencode、不跑桥**——opencode 与桥只在用户个人电脑（本指南第 0 节铁律）。
2. `/root/opencode-secrets/keys.txt` 内容不要写入日志或额外文件。
3. 中继是哑管道但能看到聊天明文：不得加装任何记录/转发/分析逻辑。
4. 用户本机的桥带自动重连：重启 relay-server 服务后桥会自行恢复，无需通知用户。
5. 仓库代码若与本文不符（用户可能已更新），以仓库内 `relay-server/INSTRUCTIONS-AGENT.md`、
   `relay-server/SELF-CHECK.md`、`opencode-relay/config.md` 为准。
6. Tengine 与 INSTRUCTIONS-AGENT.md 里的 Caddy 二选一即可；本机已占 80 的是 Tengine，故用 nginx vhost + certbot。
