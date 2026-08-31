# Local AI Studio · 自建中继服务器（FastAPI 版）
#
# 哑管道：只按 device_token 路由，桥接「桌面端出站 WS /client」与「手机页面
# WS /s/ws」。不做任何业务解析——业务在桌面端（agent 执行、写保护、状态管理）。
#
# 运行：python3 main.py（或 uvicorn main:app --host 0.0.0.0 --port 9000）
# 建议用 Caddy/nginx 反代 127.0.0.1:9000 终结 TLS（见部署文档）。

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

# 心跳间隔（秒）：保活空闲连接，避免中间设备/nginx 掐断
HEARTBEAT = 25

app = FastAPI(title="Local AI Studio relay", docs_url=None, redoc_url=None)

# hub: token -> {"client": WebSocket|None, "phones": set[WebSocket], "lock": asyncio.Lock}
devices: dict = {}
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
    return bool(d) and d in CFG["device_tokens"]


async def get_device(token: str) -> dict:
    async with dev_lock:
        if token not in devices:
            devices[token] = {"client": None, "phones": set(), "lock": asyncio.Lock()}
        return devices[token]


async def phone_heartbeat(dev: dict) -> None:
    """定期向手机页发心跳，保持连接活跃。"""
    try:
        while True:
            await asyncio.sleep(HEARTBEAT)
            async with dev["lock"]:
                dead = set()
                for p in list(dev["phones"]):
                    try:
                        await p.send_text(json.dumps({"type": "ping"}))
                    except Exception:
                        dead.add(p)
                dev["phones"] -= dead
    except asyncio.CancelledError:
        pass


def missing_token_page() -> HTMLResponse:
    """访问 /s/ 不带 ?d= 时返回的友好提示页（避免 422 JSON 甩给用户）。"""
    html = (
        "<!doctype html><html lang=\"zh\"><head><meta charset=\"utf-8\">"
        "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"
        "<title>Local AI Studio · 中继</title>"
        "<style>"
        " body{margin:0;background:#0d0e12;color:#e8e8ef;font:15px/1.6 -apple-system,\"PingFang SC\",\"Microsoft YaHei\",system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100dvh;padding:24px}"
        " .card{max-width:460px;background:#16171f;border:1px solid #23242f;border-radius:16px;padding:28px 26px}"
        " h1{font-size:18px;margin:0 0 12px;color:#c5bcff}"
        " p{margin:8px 0;color:#b9b9c6}"
        " code{background:#1d1d27;padding:2px 7px;border-radius:7px;color:#3ddc97;font-size:13px;word-break:break-all}"
        " .err{color:#ff6b81}"
        "</style></head><body><div class=\"card\">"
        "<h1>🔌 缺少设备令牌</h1>"
        "<p>手机控制台需要通过 <code>?d=&lt;token&gt;</code> 参数携带设备令牌才能打开。</p>"
        "<p>正确打开方式：</p>"
        "<p><code>https://biancheng.mei.biz/s/?d=你的64位token</code></p>"
        "<p class=\"err\">直接打开 <code>/s/</code> 不带 token 会被拒绝。</p>"
        "<p style=\"color:#8b8b98;font-size:13px\">令牌在你的服务器 <code>config.json</code> 的 device_tokens 里，由桌面端 Local AI Studio 生成。</p>"
        "</div></body></html>"
    )
    return HTMLResponse(html)


@app.get("/s/")
async def page(d: str = Query(None)) -> Response:
    if not token_ok(d):
        resp = missing_token_page()
        resp.status_code = 403 if d is not None else 200
        return resp
    with open(PAGE_FILE, encoding="utf-8") as f:
        return HTMLResponse(f.read())


STATIC_TYPES = {".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
                ".png": "image/png", ".jpg": "image/jpeg", ".svg": "image/svg+xml", ".ico": "image/x-icon"}


@app.get("/s/{sub}/{name}")
async def static_file(sub: str, name: str) -> Response:
    """页面静态资源（css/、js/）。仅允许单层目录 + 白名单后缀，拒绝路径穿越。"""
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
async def client_ws(ws: WebSocket, d: str = Query(None)) -> None:
    """桌面客户端出站连接：注册为该设备客户端，转发其帧给该设备所有手机页。

    注意：不主动 close 旧连接。旧 socket 会在桌面端自己放弃后由其 disconnect
    处理器清理，避免“服务器 1000 踢掉旧连接 → 桌面端误判断线 → 重连风暴”。
    """
    if not token_ok(d):
        await ws.close(code=1008)
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        # 仅替换引用，不主动关闭旧连接（让其自然退出）
        dev["client"] = ws
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
async def phone_ws(ws: WebSocket, d: str = Query(None)) -> None:
    """手机页面连接：转发其帧给设备客户端（设备未上线则等待，不再立即拒绝）。"""
    if not token_ok(d):
        await ws.close(code=1008)
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        dev["phones"].add(ws)
        client = dev["client"]
    log.info("手机接入: %s…", d[:6])

    hb = asyncio.create_task(phone_heartbeat(dev))

    try:
        if client is None:
            await ws.send_text(json.dumps({"type": "state", "device_online": False}))
        else:
            await ws.send_text(json.dumps({"type": "state", "device_online": True}))

        while True:
            raw = await ws.receive_text()
            async with dev["lock"]:
                client = dev["client"]
            if client is None:
                # 设备未上线：忽略手机帧，保持连接等待桌面上线
                continue
            try:
                await client.send_text(raw)
            except Exception:
                await ws.send_text(json.dumps({"type": "state", "device_online": False}))
                break
    except WebSocketDisconnect:
        pass
    finally:
        async with dev["lock"]:
            dev["phones"].discard(ws)
        hb.cancel()
        log.info("手机断开: %s…", d[:6])


def parse_bind(listen: str):
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
