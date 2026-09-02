#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
跨平台桥启动器 —— Ubuntu / macOS / Windows 通用
==================================================

把 `opencode_bridge.py` 当守护进程跑起来，并管理它的生命周期。
用法：

    python start.py [start|stop|restart|status|log]
        start    后台启动桥（默认）
        stop     停止
        restart  重启
        status   查看运行状态
        log      实时 tail 日志（Ctrl+C 退出）

文件布局（都相对于本脚本所在目录 pc/）：
    opencode_bridge.py        桥主体
    opencode_bridge.json      桥配置（同目录，机器级，gitignore）
    run/bridge.pid            进程号
    log/bridge.log            运行日志

跨平台要点：
  * 用 `sys.executable` 启动（在 venv 下也正确走 venv 的 Python）
  * 子进程完全脱离父进程：Linux/macOS 走 `start_new_session=True`；
    Windows 走 `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`
  * 进程探活用 `os.kill(pid, 0)`（Windows 也支持，进程不在抛 OSError）
  * 停止：Linux/macOS 用 SIGTERM；Windows 走 `taskkill /F /PID`
  * 日志 tail 用纯 Python 实现，不依赖 `tail`/`Get-Content`

依赖：纯标准库，无第三方。
"""
from __future__ import annotations

import argparse
import os
import signal
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
RUN_DIR = HERE / "run"
LOG_DIR = HERE / "log"
PID_FILE = RUN_DIR / "bridge.pid"
LOG_FILE = LOG_DIR / "bridge.log"
BRIDGE = HERE / "opencode_bridge.py"
CONFIG = HERE / "opencode_bridge.json"


def log(msg: str) -> None:
    print(f"[bridge-ctl {time.strftime('%H:%M:%S')}] {msg}", flush=True)


def is_alive(pid: int) -> bool:
    """进程探活：跨平台可用。Windows 上进程不存在时 os.kill 抛 OSError。"""
    if pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        # 进程存在但无权发信号，也算活着
        return True
    except OSError:
        return False


def read_pid() -> int | None:
    if not PID_FILE.exists():
        return None
    try:
        v = int(PID_FILE.read_text().strip())
        return v if v > 0 else None
    except Exception:
        return None


def write_pid(pid: int) -> None:
    RUN_DIR.mkdir(parents=True, exist_ok=True)
    PID_FILE.write_text(str(pid))


def clear_pid() -> None:
    try:
        PID_FILE.unlink(missing_ok=True)
    except Exception:
        pass


def kill_existing() -> int:
    """终止已存在的桥进程（若还活着），返回被停掉的 pid（0 = 没有进程要停）。"""
    pid = read_pid()
    if pid is None or not is_alive(pid):
        clear_pid()
        return 0
    if sys.platform == "win32":
        subprocess.run(
            ["taskkill", "/F", "/PID", str(pid)],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    else:
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    # 最多等 5s 让它正常退出
    for _ in range(25):
        if not is_alive(pid):
            break
        time.sleep(0.2)
    if is_alive(pid) and sys.platform != "win32":
        # 兜底：强杀
        try:
            os.kill(pid, signal.SIGKILL)
        except Exception:
            pass
    clear_pid()
    return pid


def do_start() -> int:
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    RUN_DIR.mkdir(parents=True, exist_ok=True)
    if not BRIDGE.exists():
        log(f"找不到桥主体：{BRIDGE}")
        return 2
    if not CONFIG.exists():
        log(f"找不到配置：{CONFIG}（参考 {CONFIG.name}.example 复制一份）")
        return 2

    # 先清残留（单桥不变量：同 token 多个桥互抢槽位）
    old = kill_existing()
    if old:
        log(f"已停残留桥 pid={old}")

    logf = open(LOG_FILE, "ab")
    kwargs: dict = {}
    if sys.platform == "win32":
        # Windows：脱离父进程 + 新进程组，避免父终端退出带走子进程
        kwargs["creationflags"] = (
            subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP
        )
    else:
        # Linux/macOS：新会话，父终端退出后子进程独立
        kwargs["start_new_session"] = True

    proc = subprocess.Popen(
        [sys.executable, str(BRIDGE), "--config", str(CONFIG)],
        cwd=str(HERE),
        stdin=subprocess.DEVNULL,
        stdout=logf,
        stderr=subprocess.STDOUT,
        close_fds=True,
        **kwargs,
    )
    write_pid(proc.pid)

    # 给桥 2s 起来，期间若退出就报告
    time.sleep(2)
    if proc.poll() is not None:
        log(f"桥启动后立即退出（rc={proc.returncode}），看日志：{LOG_FILE}")
        clear_pid()
        return 1
    log(f"桥已启动  pid={proc.pid}  日志：{LOG_FILE}")
    return 0


def do_stop() -> int:
    pid = kill_existing()
    if pid:
        log(f"已停  pid={pid}")
        return 0
    log("当前无桥进程")
    return 0


def do_status() -> int:
    pid = read_pid()
    if pid is None:
        log("无 PID 文件（桥未启动过）")
        return 1
    if is_alive(pid):
        log(f"运行中  pid={pid}  日志：{LOG_FILE}")
        return 0
    log(f"PID 文件存在（pid={pid}）但进程不在——残留文件，已清理")
    clear_pid()
    return 1


def do_log() -> int:
    """纯 Python 的 `tail -F`：打开日志 → 跳到尾部 → 轮询新行 → Ctrl+C 退出。"""
    if not LOG_FILE.exists():
        log(f"日志尚未生成：{LOG_FILE}")
        return 1
    with open(LOG_FILE, "rb") as f:
        # 从末尾前 4KB 起，确保最后一段完整
        f.seek(0, 2)
        size = f.tell()
        f.seek(max(0, size - 4096))
        # 对齐到行首，避免从半行开始打印
        if size > 4096:
            f.readline()
        for line in f:
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
        try:
            while True:
                line = f.readline()
                if line:
                    sys.stdout.buffer.write(line)
                    sys.stdout.buffer.flush()
                else:
                    time.sleep(0.3)
        except KeyboardInterrupt:
            return 0


def main() -> int:
    ap = argparse.ArgumentParser(
        description="跨平台桥启动器（Ubuntu / macOS / Windows 通用）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument(
        "action",
        nargs="?", default="start",
        choices=["start", "stop", "restart", "status", "log"],
        help="start=后台启动（默认） stop=停止 restart=重启 status=查看状态 log=tail 日志",
    )
    args = ap.parse_args()
    if args.action == "start":
        return do_start()
    if args.action == "stop":
        return do_stop()
    if args.action == "restart":
        kill_existing()
        return do_start()
    if args.action == "status":
        return do_status()
    if args.action == "log":
        return do_log()
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
