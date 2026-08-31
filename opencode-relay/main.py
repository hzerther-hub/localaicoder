# opencode 远程控制 · 中继服务器（FastAPI 版，方案 A，端口 8999）
#
# 哑管道：只按 device_token 路由，桥接「桥进程出站 WS /client」与「手机页面
# WS /s/ws」。不做任何业务解析——业务在 opencode_bridge.py（协议翻译）与
# opencode serve。与 relay-server/（Local AI Studio 自用中继）互不复用，代码各自独立。
# 协议见 docs/relay/protocol.md。
#
# 运行：python3 main.py（或 uvicorn main:app --host 0.0.0.0 --port 8999）
# 建议用 Caddy 反代 127.0.0.1:8999 终结 TLS（见 opencode-relay/README-opencode.md）。

# 兼容 Python 3.8：注解延迟求值（dict[str, dict] / tuple[str, int] 等）
from __future__ import annotations

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


@app.get("/")
async def root_page(d: str = Query(...)) -> Response:
    """根路径控制台（与 /s/ 同页同 token）：https://op.mei.biz/?d=<token>"""
    if not token_ok(d):
        return Response(status_code=403)
    with open(PAGE_FILE, encoding="utf-8") as f:
        return HTMLResponse(f.read())


@app.get("/css/{name}")
async def css_root(name: str) -> Response:
    """根路径页面引用的相对静态资源 /css/*、/js/*。"""
    return await static_file("css", name)


@app.get("/js/{name}")
async def js_root(name: str) -> Response:
    return await static_file("js", name)


STATIC_TYPES = {".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
                ".png": "image/png", ".jpg": "image/jpeg", ".svg": "image/svg+xml", ".ico": "image/x-icon"}


@app.get("/s/{sub}/{name}")
async def static_file(sub: str, name: str) -> Response:
    """页面静态资源（css/、images/）。仅允许单层目录 + 白名单后缀，拒绝路径穿越。"""
    if sub not in ("css", "js", "images") or "/" in name or ".." in name or "\\" in name:
        return Response(status_code=404)
    ext = os.path.splitext(name)[1].lower()
    if ext not in STATIC_TYPES:
        return Response(status_code=404)
    path = os.path.join(BASE, sub, name)
    if not os.path.isfile(path):
        return Response(status_code=404)
    with open(path, "rb") as f:
        return Response(content=f.read(), media_type=STATIC_TYPES[ext],
                        headers={"Cache-Control": "no-cache"})


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
