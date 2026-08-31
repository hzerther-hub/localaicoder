#!/usr/bin/env bash
#
# opencode 远端一键起停（方案 A + B 的组合入口）
#
#   方案 A（自建中继 + 桥，端口 8999）：
#       手机页 page.html <-> relay-server(8999) <-> opencode_bridge <-> opencode serve(9001)
#   方案 B（opencode 自身 web/serve，端口 9001）：
#       直接用 opencode 自带的 HTTP API/OpenAI web 客户端，手机经公网 TLS 访问
#
# 用法：
#   scripts/opencode-remote.sh start            # 起 9001(opencode) + 8999(relay) + 桥
#   scripts/opencode-remote.sh stop             # 停全部
#   scripts/opencode-remote.sh status           # 看各端口与进程
#   scripts/opencode-remote.sh serve            # 只起 opencode serve(9001)
#   scripts/opencode-remote.sh relay            # 只起 relay-server(8999)
#   scripts/opencode-remote.sh bridge           # 只起 桥
#
# 依赖：
#   opencode 在 PATH；python3 已装 fastapi/uvicorn 与 websockets/requests。
# 本机数据与日志写到 .ocdata/（已 gitignore）。

set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
STATE_DIR="$ROOT/.ocdata"
mkdir -p "$STATE_DIR"

PIDFILE="$STATE_DIR/opencode-remote.pids"
WORKSPACE="${OPCODE_WORKSPACE:-$ROOT}"          # opencode 的项目工作目录
RELAY_PORT=8999
OPENCODE_PORT=9001

# relay/bridge 的 token 白名单（本机默认 testtoken123，生产改 config 文件）
RELAY_TOKEN="${OPCODE_RELAY_TOKEN:-testtoken123}"

write_pid() { echo "$1" > "$STATE_DIR/$2.pid"; }
read_pid()  { [ -f "$STATE_DIR/$1.pid" ] && cat "$STATE_DIR/$1.pid" || true; }
alive()     { kill -0 "$1" 2>/dev/null; }

start_serve() {
  if curl -sf "http://127.0.0.1:$OPENCODE_PORT/global/health" >/dev/null 2>&1; then
    echo "opencode serve 已在 $OPENCODE_PORT"
  else
    ( cd "$WORKSPACE" && nohup opencode serve --hostname 127.0.0.1 --port "$OPENCODE_PORT" \
        > "$STATE_DIR/opencode-serve.log" 2>&1 & echo $! > "$STATE_DIR/serve.pid" )
    echo "opencode serve -> http://127.0.0.1:$OPENCODE_PORT  (日志 .ocdata/opencode-serve.log)"
  fi
}

start_relay() {
  if curl -sf "http://127.0.0.1:$RELAY_PORT/s/?d=${RELAY_TOKEN}" >/dev/null 2>&1; then
    echo "relay-server 已在 $RELAY_PORT"
  else
    ( cd "$ROOT/relay-server" && nohup python3 main.py -config config.json \
        > "$STATE_DIR/relay.log" 2>&1 & echo $! > "$STATE_DIR/relay.pid" )
    echo "relay-server -> http://127.0.0.1:$RELAY_PORT  (日志 .ocdata/relay.log)"
  fi
}

start_bridge() {
  if [ -f "$STATE_DIR/bridge.pid" ] && alive "$(read_pid bridge)"; then
    echo "opencode 桥已在运行"
  else
    # 先清理可能残留的孤儿桥：多个桥会互抢同一个 /client 槽位，导致中继反复"接入→断开"，
    # 进而让手机页 WebSocket 报 code 1006。只保留一个桥。
    pkill -f "opencode_bridge.py" 2>/dev/null || true
    sleep 0.3
    ( cd "$ROOT/relay-server" && nohup python3 opencode_bridge.py --config opencode_bridge.json \
        > "$STATE_DIR/bridge.log" 2>&1 & echo $! > "$STATE_DIR/bridge.pid" )
    echo "opencode 桥已启动  (日志 .ocdata/bridge.log)"
  fi
}

stop_all() {
  for n in serve relay bridge; do
    local p=$(read_pid "$n")
    if [ -n "$p" ] && alive "$p"; then kill "$p" 2>/dev/null || true; echo "已停 $n (pid $p)"; fi
    rm -f "$STATE_DIR/$n.pid"
  done
  # 兜底：按端口清进程
  for port in "$RELAY_PORT" "$OPENCODE_PORT"; do
    fuser -k "$port/tcp" 2>/dev/null || true
  done
}

status() {
  echo "== opencode serve :$OPENCODE_PORT =="
  curl -sf "http://127.0.0.1:$OPENCODE_PORT/global/health" && echo "  (健康)" || echo "  (未运行)"
  echo "== relay-server :$RELAY_PORT =="
  curl -sf -o /dev/null -w "  手机页 token 校验 http=%{http_code}\n" "http://127.0.0.1:$RELAY_PORT/s/?d=${RELAY_TOKEN}" || echo "  (未运行)"
  echo "== bridge =="
  local bp=$(read_pid bridge); [ -n "$bp" ] && alive "$bp" && echo "  运行中 (pid $bp)" || echo "  (未运行)"
}

case "${1:-}" in
  start) start_serve; start_relay; start_bridge; status ;;
  serve)  start_serve ;;
  relay)  start_relay ;;
  bridge) start_bridge ;;
  stop)   stop_all ;;
  status) status ;;
  *) echo "用法: $0 {start|stop|status|serve|relay|bridge}"; exit 1 ;;
esac
