#!/usr/bin/env bash
#
# opencode 远端一键起停（方案 A + B 的组合入口）
#
#   方案 A（自建中继 + 桥，端口 8999）：
#       手机页 page.html <-> opencode-relay(8999) <-> opencode_bridge <-> opencode serve(9001)
#   方案 B（opencode 自身 web/serve，端口 9001）：
#       直接用 opencode 自带的 HTTP API/OpenAI web 客户端，手机经公网 TLS 访问
#
# 用法：
#   scripts/opencode-remote.sh start            # 起 9001(opencode) + 8999(relay) + 桥
#   scripts/opencode-remote.sh stop             # 停全部
#   scripts/opencode-remote.sh status           # 看各端口与进程
#   scripts/opencode-remote.sh serve            # 只起 opencode serve(9001)
#   scripts/opencode-remote.sh relay            # 只起 opencode-relay(8999)
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

# python 解析：优先 .ocdata/venv（README「环境要求」的 miniconda venv，无需激活）；
# 其次 Windows 的 python3 常是 Microsoft Store 占位 stub（WindowsApps 下），指向它时
# 改用 python；Linux/macOS 用 python3。
resolve_python() {
  for vp in "$STATE_DIR/venv/Scripts/python.exe" "$STATE_DIR/venv/bin/python"; do
    if [ -x "$vp" ]; then echo "$vp"; return; fi
  done
  local p3; p3=$(command -v python3 2>/dev/null || true)
  case "$p3" in
    *WindowsApps*|'') command -v python 2>/dev/null || command -v python3 ;;
    *) echo "$p3" ;;
  esac
}
PYTHON=$(resolve_python)

# Git Bash 无 pkill：先试 pkill；wmic 在部分 Windows 上输出鬼 PID 且 taskkill 报
# 「无效查询」，改用 PowerShell CIM 按命令行精确清杀（等价 pkill -f）。
# 必须限定 Name=python.exe：否则 PS 会匹配到自身/调用方 bash 的命令行（都含本关键字），
# 把自己杀掉导致清理半途而废。
kill_bridge_orphans() {
  if command -v pkill >/dev/null 2>&1; then pkill -f "opencode_bridge.py" 2>/dev/null || true; return; fi
  powershell -NoProfile -Command 'Get-CimInstance Win32_Process | Where-Object { $_.Name -eq "python.exe" -and $_.CommandLine -like "*opencode_bridge.py*" } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }' 2>/dev/null || true
}

# Git Bash 无 fuser：按 netstat 找监听端口的 PID，用 taskkill 兜底
kill_port() {
  local p; p=$(netstat -ano | awk -v p=":$1" '$1=="TCP" && $4=="LISTENING" && $2 ~ p"$" {print $5; exit}')
  [ -n "$p" ] && taskkill //F //PID "$p" >/dev/null 2>&1 || true
}

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
  # 有任何 HTTP 响应即视为存活（旧实例 /s/ 可能 500，但服务仍在转发，不重复起）
  if [ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$RELAY_PORT/s/?d=${RELAY_TOKEN}")" != "000" ]; then
    echo "opencode-relay 已在 $RELAY_PORT"
  else
    ( cd "$ROOT/opencode-relay/service" && nohup "$PYTHON" main.py -config config.json \
        > "$STATE_DIR/relay.log" 2>&1 & echo $! > "$STATE_DIR/relay.pid" )
    echo "opencode-relay -> http://127.0.0.1:$RELAY_PORT  (日志 .ocdata/relay.log)"
  fi
}

start_bridge() {
  if [ -f "$STATE_DIR/bridge.pid" ] && alive "$(read_pid bridge)"; then
    echo "opencode 桥已在运行"
  else
    # 先清理可能残留的孤儿桥：多个桥会互抢同一个 /client 槽位，导致中继反复"接入→断开"，
    # 进而让手机页 WebSocket 报 code 1006。只保留一个桥。
    kill_bridge_orphans
    sleep 0.3
    ( cd "$ROOT/opencode-relay/pc" && nohup "$PYTHON" opencode_bridge.py --config opencode_bridge.json \
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
  # 兜底：按端口清进程（fuser 缺失时走 netstat+taskkill）
  for port in "$RELAY_PORT" "$OPENCODE_PORT"; do
    if command -v fuser >/dev/null 2>&1; then fuser -k "$port/tcp" 2>/dev/null || true; else kill_port "$port"; fi
  done
  kill_bridge_orphans
}

status() {
  echo "== opencode serve :$OPENCODE_PORT =="
  curl -sf "http://127.0.0.1:$OPENCODE_PORT/global/health" && echo "  (健康)" || echo "  (未运行)"
  echo "== opencode-relay :$RELAY_PORT =="
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
