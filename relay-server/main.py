# Local AI Studio · 自建中继服务器（FastAPI 版）
#
# 哑管道：只按 device_token 路由，桥接「桌面端出站 WS /client」与「手机页面
# WS /s/ws」。不做任何业务解析——业务在桌面端（agent 执行、写保护、状态管理）。
# 协议见 docs/relay/protocol.md；桌面端 Go 客户端与此版完全兼容。
#
# 运行：python3 main.py（或 uvicorn main:app --host 0.0.0.0 --port 9000）
# 建议用 Caddy 反代 127.0.0.1:9000 终结 TLS（见 docs/relay/deploy.md）。

import argparse
import asyncio
import json
import logging
import os

from fastapi import FastAPI, Query, WebSocket, WebSocketDisconnect, Response
from fastapi.responses import HTMLResponse
import uvicorn

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("relay")

CFG = {"listen": "127.0.0.1:9000", "device_tokens": []}
BASE = os.path.dirname(os.path.abspath(__file__))
PAGE_FILE = os.path.join(BASE, "page.html")

app = FastAPI(title="Local AI Studio relay", docs_url=None, redoc_url=None)

# hub: token -> {"client": WebSocket|None, "phones": set[WebSocket], "lock": asyncio.Lock}
devices: dict[str, dict] = {}
dev_lock = asyncio.Lock()


def load_config(path: str) -> None:
    global CFG
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            CFG = json.load(f)
    CFG.setdefault("listen", "127.0.0.1:9000")
    CFG.setdefault("device_tokens", [])
    log.info("已加载 %s，%d 台设备白名单", path, len(CFG["device_tokens"]))


def token_ok(d: str) -> bool:
    return d in CFG["device_tokens"]


async def get_device(token: str) -> dict:
    async with dev_lock:
        if token not in devices:
            devices[token] = {"client": None, "phones": set(), "lock": asyncio.Lock()}
        return devices[token]


@app.get("/s/")
async def page(d: str = Query(...)) -> Response:
    if not token_ok(d):
        return Response(status_code=403)
    with open(PAGE_FILE, encoding="utf-8") as f:
        return HTMLResponse(f.read())


@app.websocket("/client")
async def client_ws(ws: WebSocket, d: str = Query()) -> None:
    """桌面客户端出站连接：注册为该设备客户端，转发其帧给该设备所有手机页。"""
    if not token_ok(d):
        await ws.close(code=1008)
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        old = dev["client"]
        dev["client"] = ws
    if old is not None:
        try:
            await old.close(code=1000)
        except Exception:
            pass
    log.info("客户端接入: %s…", d[:6])
    try:
        while True:
            raw = await ws.receive_text()
            async with dev["lock"]:
                for p in list(dev["phones"]):
                    try:
                        await p.send_text(raw)
                    except Exception:
                        dev["phones"].discard(p)
    except WebSocketDisconnect:
        pass
    finally:
        async with dev["lock"]:
            if dev["client"] is ws:
                dev["client"] = None
        log.info("客户端断开: %s…", d[:6])


@app.websocket("/s/ws")
async def phone_ws(ws: WebSocket, d: str = Query()) -> None:
    """手机页面连接：转发其帧给设备客户端（设备未上线则拒绝）。"""
    if not token_ok(d):
        await ws.close(code=1008)
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        if dev["client"] is None:
            await ws.close(code=1008)
            return
        client = dev["client"]
        dev["phones"].add(ws)
    log.info("手机接入: %s…", d[:6])
    try:
        while True:
            raw = await ws.receive_text()
            try:
                await client.send_text(raw)
            except Exception:
                break
    except WebSocketDisconnect:
        pass
    finally:
        async with dev["lock"]:
            dev["phones"].discard(ws)
        log.info("手机断开: %s…", d[:6])


def parse_bind(listen: str) -> tuple[str, int]:
    if ":" in listen:
        host, port = listen.rsplit(":", 1)
        return host, int(port)
    return "0.0.0.0", int(listen)


def main() -> None:
    ap = argparse.ArgumentParser(description="Local AI Studio relay server (FastAPI)")
    ap.add_argument("-config", default="config.json", help="配置文件路径")
    args = ap.parse_args()
    load_config(args.config)
    host, port = parse_bind(CFG["listen"])
    log.info("relay-server 监听 %s:%d", host, port)
    ws_max = int(os.environ.get("RELAY_WS_MAX", "5")) * 1024 * 1024  # 默认 5MB，防大图 base64 超限断连
    uvicorn.run(app, host=host, port=port, log_level="warning", ws_max_size=ws_max)


if __name__ == "__main__":
    main()
