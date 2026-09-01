[English](TUNNEL-3STEPS.en.md) | 中文

# 隧道三步 · 把官方 opencode UI 搬上公网（服务器 AI 执行手册）

> 目标：手机/外网打开 **https://oc.mei.biz** 看到与本机 `http://127.0.0.1:9001` **一模一样**的官方 opencode 网页界面（全部功能：会话、diff、终端、命令面板）。
>
> 数据流：
> ```
> 手机浏览器 ──HTTPS──> VPS nginx(oc.mei.biz) ──> 127.0.0.1:19001（隧道洞）
>                     ≈ SSH 反向隧道 ≈>  本机 127.0.0.1:9001 opencode serve（官方 UI + API）
> ```
>
> 分工：**第 0 步和第 3 步在 VPS 上做（服务器 AI）**；**第 1 步和第 2 步在用户的本机做**（用户粘贴两条命令，或由本机 AI 执行）。第 0 步结束时会产出两段"交付物"，请完整发给用户。

---

## 第 0 步（VPS · 服务器 AI）：准备密钥、DNS、站点

### 0.1 生成隧道专用密钥对并授权

```bash
mkdir -p /root/.opencode-tunnel
[ -f /root/.opencode-tunnel/id_ed25519 ] || ssh-keygen -t ed25519 -N "" -f /root/.opencode-tunnel/id_ed25519 -C "opencode-tunnel"
# 公钥进授权表（去重）
grep -qF "$(cat /root/.opencode-tunnel/id_ed25519.pub)" /root/.ssh/authorized_keys 2>/dev/null || \
  cat /root/.opencode-tunnel/id_ed25519.pub >> /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
# 生成一个强密码给 opencode serve 用（同时交付给用户）
openssl rand -base64 12 | tr -d '=+/' | head -c 12
```

### 0.2 DNS

在域名管理加一条 A 记录：**`oc.mei.biz` → 本 VPS 公网 IP**。
（若无法加 DNS：备选方案是走端口 `http://oc.mei.biz:9090`，需宝塔安全组放行 9090，且无 TLS，仅调试用。）

### 0.3 站点反代（宝塔或手写 nginx 均可）

宝塔：添加站点 `oc.mei.biz`（纯静态即可）→ 设置 → 反向代理 → 目标 `http://127.0.0.1:19001`，并开启 WebSocket 支持。
手写等价 nginx 配置：

```nginx
server {
    listen 80;
    server_name oc.mei.biz;
    location / {
        proxy_pass         http://127.0.0.1:19001;
        proxy_http_version 1.1;
        proxy_set_header   Upgrade $http_upgrade;      # WebSocket 必需（PTY 终端用）
        proxy_set_header   Connection "upgrade";
        proxy_set_header   Host $host;
        proxy_buffering    off;                        # SSE /event 流式必需
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

配置好后在 VPS 上先自检一遍（此时隧道还没建，应返回 502，属预期）：

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:19001/   # 预期 000/502（洞还没人接）
```

### 0.4 交付物（发还给用户/本机 AI 的两段内容）

1. **私钥全文**（`cat /root/.opencode-tunnel/id_ed25519`，PEM 三行）——用户存为本机 `/home/wellfuture/.ssh/opencode_tunnel`
2. **opencode 密码**（0.1 生成的 12 位串）

---

## 第 1 步（本机）：opencode 带密码重启

> 官方 UI 与 API 都由本机的 `opencode serve` 提供；上公网必须带密码。

```bash
pkill -f "opencode serve"; sleep 1
cd /home/wellfuture/build/localaicoder
OPENCODE_SERVER_PASSWORD='<第0步交付的密码>' nohup opencode serve \
  --hostname 127.0.0.1 --port 9001 > .ocdata/opencode-serve.log 2>&1 &
echo $! > .ocdata/serve.pid
sleep 2 && curl -sf http://127.0.0.1:9001/global/health && echo " 9001 OK"
```

注意：本机的 opencode 中继桥（`opencode_bridge.py`）若配置了 `opencode.password`，需同步填写；未配置则忽略。9001 带密码后，桥连它会 401——修复：

```bash
python3 - <<'PY'
import json
p='/home/wellfuture/build/localaicoder/opencode-relay/pc/opencode_bridge.json'
c=json.load(open(p)); c['opencode']['password']='<同一个密码>'
json.dump(c,open(p,'w'),ensure_ascii=False,indent=2); print('bridge password updated')
PY
pkill -f 'opencode_bridg[e]\.py'; sleep 1
cd /home/wellfuture/build/localaicoder/opencode-relay/pc && setsid nohup python3 opencode_bridge.py \
  --config opencode_bridge.json > /home/wellfuture/build/localaicoder/.ocdata/bridge.log 2>&1 < /dev/null &
```

---

## 第 2 步（本机）：起反向隧道

```bash
# 存私钥（内容=第 0 步交付物 1，权限必须 600）
mkdir -p ~/.ssh && chmod 700 ~/.ssh
cat > ~/.ssh/opencode_tunnel <<'EOF'
<粘贴私钥全文>
EOF
chmod 600 ~/.ssh/opencode_tunnel

# 安装 autossh（没有的话）
command -v autossh >/dev/null || (sudo apt-get install -y autossh || brew install autossh)

# 起隧道：VPS 的 127.0.0.1:19001 ←→ 本机 127.0.0.1:9001，断线自动重连
export AUTOSSH_GATETIME=0
autossh -M 0 -f -N \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=accept-new \
  -i ~/.ssh/opencode_tunnel -R 127.0.0.1:19001:127.0.0.1:9001 root@154.9.25.70

# 自检：隧道通不通（本机视角）
sleep 2; pgrep -f "ssh.*19001" >/dev/null && echo "隧道进程 OK" || echo "隧道未起"
```

---

## 第 3 步（VPS · 服务器 AI）：验收

```bash
# 1) 洞已接上：应返回官方 UI 的 HTML（含 opencode 字样）
curl -s http://127.0.0.1:19001/ | grep -o "<title>[^<]*" | head -1

# 2) 域名+反代+TLS 全链路：应 200/30x 且标题同上
curl -s -o /dev/null -w "%{http_code}\n" https://oc.mei.biz/

# 3) SSE 流不被缓冲：应看到持续 data: 帧或长连接挂起（Ctrl+C 退出）
timeout 4 curl -s -N http://127.0.0.1:19001/event | head -3

# 4) 密码生效：无密码应 401
curl -s -o /dev/null -w "%{http_code}\n" https://oc.mei.biz/session   # 预期 401
```

四项通过后告诉用户：**手机打开 https://oc.mei.biz，登录密码 = 第 0 步交付的密码。**

---

## 常见故障

| 现象 | 原因 / 处理 |
|---|---|
| 本机 autossh 起不来 `Permission denied` | 私钥没存对/权限不是 600；或 0.1 的公钥没进 `authorized_keys` |
| 页面 502 | 隧道没起（回本机看 `pgrep -f 19001`）或 19001 被占用（VPS `ss -tlnp | grep 19001`） |
| 页面开了但一直转圈/无流式 | nginx 少了 `proxy_buffering off`；或没开 WebSocket 升级头（终端功能不可用） |
| 域名打不开 | DNS A 记录没生效：`dig +short oc.mei.biz` 核对 |
| 提示密码错误 | 浏览器 Basic 认证用户名固定填 `opencode`，密码=交付密码 |
| 手机上终端(PTY)连不上 | 反代必须透传 WebSocket（`Upgrade/Connection` 头），逐一核对 |

## 与现有服务的关系

- 不动 `op.mei.biz/s/`（轻量控制台 + 新版官方风格界面）——两者并存。
- 本机 9001 现在带密码：本地开发若直连 9001，用密码 `opencode` 用户名登录。
- 隧道只绑 VPS 的 `127.0.0.1:19001`，公网无法绕过 nginx 直连该端口。
