#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
opencode 远程控制 · 中继服务器（FastAPI 版，方案 A）

════════════════════════════════════════════════════════════════════
一、角色与架构

本进程部署在公网 VPS（如 op.mei.biz），是整个远程控制链路里唯一的
公网入口。它是一个"哑管道"（dumb pipe）：

    ┌─ 手机浏览器 ────────┐
    │  page.html + js     │     ② WS /s/ws?d=<token>
    └─────────┬───────────┘──────────────┐
              │        ① HTTPS           ▼
              │              ┌──────────────────────┐
              │              │  本文件（中继 main.py）│   ← 只转发 JSON 帧，
              │              │  FastAPI 哑管道       │     不解析任何业务
              │              └──────────┬───────────┘
              │                         │ ③ WS /client?d=<token>
              │              ┌──────────┴───────────┐
              └──────────────│ 桥 opencode_bridge.py │（跑在用户本机，纯出站连接）
                             └──────────┬───────────┘
                                        │ ④ HTTP + SSE
                             ┌──────────▼───────────┐
                             │ opencode serve:9001  │（本机，官方后端）
                             └──────────────────────┘

设计要点：
  * 中继不解析业务帧——上行（桥→手机）原样广播，下行（手机→桥）原样单转。
    因此更换被控端（opencode 换成别的 CLI）只需要改桥，中继零改动。
  * 桥是"纯出站"连接：用户本机无需对公网开放任何端口，穿透 NAT/防火墙。
  * 一个 device_token 代表一台受控设备；token 即控制权，泄露立即轮换。
  * 协议（帧格式）与 relay-server/（Local AI Studio 自用中继）完全一致，
    但代码各自独立、互不复用；页面与桥的另一份实现在桌面端 desktop/relay.go。

二、路由一览

    GET  /s/?d=<token>              手机控制台页面（page.html）
    GET  /?d=<token>                同 /s/（根路径别名，token 同源）
    GET  /s/{css|js|images}/{name}  /s/ 页面的静态资源（白名单后缀，拒绝穿越）
    GET  /css/{name}、/js/{name}    根路径页面的静态资源（同上）
    WS   /s/ws?d=<token>            手机端 WebSocket 接入
    WS   /client?d=<token>          桥（受控设备客户端）WebSocket 接入

转发规则：
  * /client 收到的帧  → 广播给同一 token 下所有 /s/ws 手机（1 对 N）
  * /s/ws  收到的帧   → 单转给该 token 当前唯一 /client 桥（N 对 1）
  * 桥重复接入        → 踢掉旧桥（close 1000），一个 token 只保留一个客户端槽位
  * 桥未上线时手机接入 → 拒绝（close 1008），手机页显示"连接断开 (code 1008)"

三、配置与运行

    配置文件 config.json（与本文件同目录，-config 可指定）：
        { "listen": "127.0.0.1:9000",          # VPS 必须 127.0.0.1，公网入口只留反代
          "device_tokens": ["<64位随机hex>"] }  # openssl rand -hex 32

    启动：python3 main.py -config config.json
    生产：systemd 常驻（Restart=always）+ Nginx 反代 443 → 本端口
          （见 nginx.conf.example）；反代须透传 WebSocket（/s/ws、/client）。

四、安全清单

  * listen 只绑 127.0.0.1；公网仅暴露反代的 80/443。
  * token 用 64 位随机 hex，禁用弱 token；三处（白名单/桥/手机 URL）保持一致。
  * 中继能看到聊天明文帧——必须部署在自己的服务器上。
  * 静态路由做了目录穿越防护与后缀白名单（见 static_file 注释）。

五、相关文件

    本目录（service/）         中继服务端：main.py + page.html + css/ + js/
                               + config.json.example + requirements.txt + nginx.conf.example
    ../pc/opencode_bridge.py   桥：把中继协议翻译成 opencode HTTP API（本机跑）
    ../pc/opencode_bridge.json 桥配置（server_url / device_token / workspace …）
    ../config.md               部署说明（VPS + Nginx + systemd 全流程）
    ../TUNNEL-3STEPS.md        隧道三步：把官方 opencode UI 也搬上公网的手册
    ../../relay-server/        姊妹项目：Local AI Studio 自用中继（协议同源）
    ../ARTICLE.md              技术文章：整套实现的技术剖析
════════════════════════════════════════════════════════════════════
"""

# 兼容 Python 3.8：注解延迟求值（dict[str, dict] / tuple[str, int] 等写法在 3.8 需要它）
from __future__ import annotations

import argparse  # 命令行解析：-config 指定配置文件路径
import asyncio   # 事件循环与异步锁：WS 收发、devices 字典的并发保护
import json      # 读取 config.json
import logging   # 接入/断开日志（排障核心依据，见 config.md 常见故障表）
import os        # 路径拼接、文件存在性判断

from fastapi import FastAPI, Query, WebSocket, WebSocketDisconnect, Response
from fastapi.responses import HTMLResponse  # 页面响应（text/html）
import uvicorn                              # ASGI 服务器：真正监听端口的是它

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("relay")

# 运行时配置，由 load_config() 从 config.json 填充；两个键都有缺省值
CFG = {"listen": "127.0.0.1:9000", "device_tokens": []}
BASE = os.path.dirname(os.path.abspath(__file__))      # 本文件所在目录（静态资源根）
PAGE_FILE = os.path.join(BASE, "page.html")            # 手机控制台页面

# FastAPI 应用本体。关闭 /docs /redoc——公网入口不暴露任何多余面
app = FastAPI(title="Local AI Studio relay", docs_url=None, redoc_url=None)

# ── 设备注册表 ────────────────────────────────────────────────────────
# token -> {"client": WebSocket|None,   该设备的桥（受控端）连接，最多一个
#           "phones": set[WebSocket],  该设备的所有手机页连接，1 对 N 广播
#           "lock":   asyncio.Lock}    该设备内部状态的无竞争保护
# 惰性创建：首个该 token 的连接到来时才建条目（get_device）
devices: dict[str, dict] = {}
dev_lock = asyncio.Lock()  # 保护 devices 字典本身的并发读写


def load_config(path: str) -> None:
    """读取配置文件并填入全局 CFG。文件不存在时用缺省值（方便空目录冒烟启动）。"""
    global CFG
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            CFG = json.load(f)
    CFG.setdefault("listen", "127.0.0.1:9000")   # 缺省只绑回环：安全缺省
    CFG.setdefault("device_tokens", [])          # 空白名单 = 拒绝一切接入
    log.info("已加载 %s，%d 台设备白名单", path, len(CFG["device_tokens"]))


def token_ok(d: str) -> bool:
    """设备 token 校验。白名单命中才放行；这是中继唯一的鉴权手段。"""
    return d in CFG["device_tokens"]


async def get_device(token: str) -> dict:
    """取（或惰性创建）某 token 的设备条目。持 dev_lock 保证并发下不重复创建。"""
    async with dev_lock:
        if token not in devices:
            devices[token] = {"client": None, "phones": set(), "lock": asyncio.Lock()}
        return devices[token]


@app.get("/s/")
async def page(d: str = Query(...)) -> Response:
    """手机控制台页面。token 校验失败返回 403；页面本身不含 token，
    后续 WS 由 js 拼接 ?d= 发起——token 只在 URL 上，注意别泄露分享链接。"""
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
    """根路径页面引用的相对静态资源 /css/*、/js/*（与 /s/ 同一套文件）。"""
    return await static_file("css", name)


@app.get("/js/{name}")
async def js_root(name: str) -> Response:
    return await static_file("js", name)


# 静态资源后缀白名单：只服文本/图片，拒脚本之外的一切可执行类型
STATIC_TYPES = {".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
                ".png": "image/png", ".jpg": "image/jpeg", ".svg": "image/svg+xml", ".ico": "image/x-icon"}


@app.get("/s/{sub}/{name}")
async def static_file(sub: str, name: str) -> Response:
    """页面静态资源（css/、js/、images/）。安全约束：
      * sub 只允许三个白名单目录（单层，杜绝路径穿越）；
      * name 内禁止 / .. \\（再次防穿越，如 css/../../etc/passwd）；
      * 后缀必须在 STATIC_TYPES 白名单内（拒绝 .py .json 等敏感文件被下载）。
    每次请求现读磁盘：改前端文件无需重启本进程（部署友好）。"""
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
                        headers={"Cache-Control": "no-cache"})  # no-cache：前端迭代时刷新即生效


@app.websocket("/client")
async def client_ws(ws: WebSocket, d: str = Query()) -> None:
    """桥（受控设备客户端）出站接入。这是整条链路的"上行源"：

    * token 不在白名单 → close 1008（手机端表现为 code 1008 红条）；
    * 接入即抢占：同 token 旧桥被 close 1000 踢掉——防止两个桥进程
      互抢槽位来回踢（历史上排障踩过：config.md 常见故障第 1 条）；
    * 收到帧原样广播给该 token 所有手机。中继不 parse JSON：
      转发语义 = "这台设备的屏幕，所有持 token 的手机都看得到"。
    """
    if not token_ok(d):
        await ws.close(code=1008)   # 1008 policy violation：token 无效
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        old = dev["client"]
        dev["client"] = ws
    if old is not None:
        try:
            await old.close(code=1000)  # 正常关闭：旧桥静默重连即可
        except Exception:
            pass  # 旧桥可能已死，忽略
    log.info("客户端接入: %s…", d[:6])   # 日志只留 token 前 6 位，避免泄露完整凭据
    try:
        while True:
            raw = await ws.receive_text()
            async with dev["lock"]:
                for p in list(dev["phones"]):   # list() 拷贝：迭代中可能 discard
                    try:
                        await p.send_text(raw)   # 原样广播，不解析业务
                    except Exception:
                        dev["phones"].discard(p)  # 手机已断：静默清理，帧不补发
    except WebSocketDisconnect:
        pass
    finally:
        async with dev["lock"]:
            if dev["client"] is ws:               # 只清自己：避免误清新桥的槽位
                dev["client"] = None
        log.info("客户端断开: %s…", d[:6])


@app.websocket("/s/ws")
async def phone_ws(ws: WebSocket, d: str = Query()) -> None:
    """手机页 WebSocket 接入（下行屏 + 上行指令）：

    * token 无效 → close 1008；
    * 桥未上线   → close 1008（手机页提示 code 1008：先在本机把桥跑起来）；
    * 收到帧单转给当前桥；桥不在（断连窗口）时 send 抛异常 → break 断开手机。
      注意：中继不缓存帧——断连窗口内的帧永久丢失，由桥端"断线补偿"
      （opencode_bridge.py 的 _missed 机制）通知手机重拉消息来兜底。
    * 心跳保活：每 25 秒发 ping 帧，防止 Nginx/服务器掐断空闲连接。
    """
    if not token_ok(d):
        await ws.close(code=1008)
        return
    await ws.accept()
    dev = await get_device(d)
    async with dev["lock"]:
        if dev["client"] is None:
            await ws.close(code=1008)   # 没有桥在线：拒绝（而非挂起等待）
            return
        client = dev["client"]
        dev["phones"].add(ws)
    log.info("手机接入: %s…", d[:6])
    
    # 心跳保活：每 25 秒发 ping 帧，防止 Nginx/服务器掐断空闲连接
    async def phone_heartbeat():
        try:
            while True:
                await asyncio.sleep(25)
                if ws.closed:
                    break
                try:
                    await ws.send_text(json.dumps({"type": "ping"}))
                except Exception:
                    break
        except asyncio.CancelledError:
            pass
    
    hb_task = asyncio.create_task(phone_heartbeat())
    try:
        while True:
            raw = await ws.receive_text()
            try:
                await client.send_text(raw)   # 单转：所有手机共享同一个桥槽位
            except Exception:
                break                          # 桥不在了：结束本手机连接（页面会重连）
    except WebSocketDisconnect:
        pass
    finally:
        hb_task.cancel()
        async with dev["lock"]:
            dev["phones"].discard(ws)
        log.info("手机断开: %s…", d[:6])


def parse_bind(listen: str) -> tuple[str, int]:
    """'host:port' → (host, port)；纯数字视为纯端口（绑定 0.0.0.0，生产勿用）。"""
    if ":" in listen:
        host, port = listen.rsplit(":", 1)
        return host, int(port)
    return "0.0.0.0", int(listen)


def main() -> None:
    """入口：加载配置 → 解析监听地址 → uvicorn 起服务。

    ws_max_size：单帧 WS 上限（默认 5MB，环境变量 RELAY_WS_MAX 可调，单位 MB）。
    调大它才能传更大的图片 base64；调小可降低滥用风险。
    log_level=warning：接入/断开日志由本文件自己的 log 打印（INFO 级），
    uvicorn 的访问日志对排障没有价值，关掉保持输出干净。
    """
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
