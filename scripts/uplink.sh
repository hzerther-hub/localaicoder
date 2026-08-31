#!/usr/bin/env bash
#
# 本机上联脚本：把本机 opencode / 桥 接到公网中继（biancheng.mei.biz / openi.mei.biz）
#
# 对应 relay-server/DEPLOY.md 第 7 步「👤 用户本机三步」：
#   ① 本机 opencode serve 带密码重启（openi.mei.biz 暴露公网，必须设密码）
#   ② 桥改为出站连公网中继（biancheng.mei.biz）并重启
#   ③ SSH 反向隧道：本机 9001 → 服务器 19001（autossh 断线自动重连）
#
# 用法：
#   scripts/uplink.sh <TOKEN> <OCPW> [用户@服务器IP]
# 例：
#   scripts/uplink.sh 6e26313990758a35a4fc28868e9dd8aa319164cb3740128f1dc36d5d9f5f3e2a 'Ab3xk9Qm' root@154.9.25.70
#
# 幂等：可重复执行；每步先停旧进程/配置再落地。TOKEN/OCPW 由服务器侧智能体交付。

set -euo pipefail

TOKEN="${1:-}"
OCPW="${2:-}"
SSH_TARGET="${3:-root@154.9.25.70}"
TUNNEL_PORT=19001          # 服务器侧反代指向的端口（nginx openi.mei.biz → 127.0.0.1:19001）

cd "$(dirname "$0")/.."
ROOT=$(pwd)
BRIDGE_DIR="$ROOT/opencode-relay"
BRIDGE_CONF="$BRIDGE_DIR/opencode_bridge.json"
PUBLIC_RELAY="https://biancheng.mei.biz"

[ -n "$TOKEN" ] || { echo "用法: $0 <TOKEN> <OCPW> [user@host]"; exit 1; }
[ -n "$OCPW" ]  || { echo "缺少 OCPW（opencode web 密码，服务器智能体会交付）"; exit 1; }

echo "== ① opencode serve 带密码重启（:9001） =="
pkill -f "opencode serve" 2>/dev/null || true
sleep 1
WORKSPACE="${OPCODE_WORKSPACE:-$(pwd)}"
( cd "$WORKSPACE" && OPENCODE_SERVER_PASSWORD="$OCPW" nohup opencode serve \
    --hostname 127.0.0.1 --port 9001 > "$ROOT/.ocdata/opencode-serve.log" 2>&1 \
    & echo $! > "$ROOT/.ocdata/serve.pid" )
sleep 2
curl -sf "http://127.0.0.1:9001/global/health" >/dev/null \
  && echo "  opencode:9001 ✅（已带密码，本机桥与官方 web 均需该密码）" \
  || echo "  ⚠️ opencode:9001 未响应，查看 .ocdata/opencode-serve.log"

echo "== ② 桥出站连公网中继（$PUBLIC_RELAY） =="
pkill -f "opencode_bridge.py" 2>/dev/null || true
sleep 1
python3 - "$BRIDGE_CONF" "$TOKEN" <<'PY'
import json, sys
p, token = sys.argv[1], sys.argv[2]
c = json.load(open(p))
c['relay']['server_url'] = 'https://biancheng.mei.biz'
c['relay']['device_token'] = token
json.dump(c, open(p, 'w'), ensure_ascii=False, indent=2)
print('  bridge config updated:', p)
PY
mkdir -p "$ROOT/.ocdata"
( cd "$BRIDGE_DIR" && nohup python3 opencode_bridge.py --config opencode_bridge.json \
    > "$ROOT/.ocdata/bridge.log" 2>&1 & echo $! > "$ROOT/.ocdata/bridge.pid" )
sleep 2
grep -q "已接入中继" "$ROOT/.ocdata/bridge.log" && echo "  桥 ✅ 已接入中继" || echo "  ⚠️ 桥未接入，查看 .ocdata/bridge.log"

echo "== ③ SSH 反向隧道（本机 9001 → 服务器 $TUNNEL_PORT） =="
if ! command -v autossh >/dev/null 2>&1; then
  ( apt-get install -y autossh 2>/dev/null || brew install autossh ) || {
    echo "  ⚠️ 请手动安装 autossh 后重跑本脚本"; exit 1; }
fi
pkill -f "autossh.*$TUNNEL_PORT" 2>/dev/null || true
sleep 0.5
# 首次需要 ssh-copy-id "$SSH_TARGET" 装公钥（会要输一次密码）
export AUTOSSH_GATETIME=0
autossh -M 0 -f -N \
  -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes \
  -o StrictHostKeyChecking=accept-new \
  -R "127.0.0.1:$TUNNEL_PORT:127.0.0.1:9001" "$SSH_TARGET"
sleep 2
pgrep -f "autossh.*$TUNNEL_PORT" >/dev/null && echo "  autossh 隧道 ✅（$SSH_TARGET:$TUNNEL_PORT → 本机 9001）" \
  || echo "  ⚠️ 隧道未起：先 ssh-copy-id $SSH_TARGET 再重跑"

echo
echo "== 完成。入口地址 =="
echo "  手机控制台   https://biancheng.mei.biz/s/?d=$TOKEN   （别名 open.mei.biz 同效）"
echo "  opencode web https://openi.mei.biz   （密码：$OCPW）"
echo "  服务器自检   ssh $SSH_TARGET 'cd /www/wwwroot/biancheng.mei.biz && .venv/bin/python selftest.py biancheng.mei.biz $TOKEN'"
