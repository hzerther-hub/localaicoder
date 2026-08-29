#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""relay-server 一键自检（AI/运维可直接跑）：自动检查 域名→TLS→页面→WS→桌面是否在线。

用法：python3 selftest.py <域名> <device_token> [域名2 …]
例：  python3 selftest.py <你的域名> 6e2631…2a
说明：域名可给多个（如 .biz 和 .bz），脚本逐一探测并给出判定。
"""
import socket
import ssl
import sys
import urllib.request

def dns(host):
    try:
        return sorted({a[4][0] for a in socket.getaddrinfo(host, 443, proto=socket.IPPROTO_TCP)})
    except Exception as e:
        return f"DNS 失败: {e}"

def https(host, token):
    url = f"https://{host}/s/?d={token}"
    try:
        ctx = ssl.create_default_context()
        with urllib.request.urlopen(url, timeout=10, context=ctx) as r:
            return r.status, ""
    except urllib.error.HTTPError as e:
        return e.code, ""          # 403 = token 不在白名单
    except Exception as e:
        return None, f"{type(e).__name__}: {e}"

async def ws_probe(host, token):
    import asyncio, websockets, json
    async def one(path, expect_state=False):
        u = f"wss://{host}{path}?d={token}"
        try:
            ws = await asyncio.wait_for(websockets.connect(u, open_timeout=10), 10)
        except Exception as e:
            return {"ok": False, "detail": f"{type(e).__name__}: {str(e)[:60]}"}
        if expect_state:
            try:
                await ws.send(json.dumps({"type": "state", "rid": 1}))
                m = json.loads(await asyncio.wait_for(ws.recv(), 6))
                await ws.close()
                if m.get("type") == "state":
                    return {"ok": True, "detail": f"desktop_on={m.get('mode')} sessions={len(m.get('sessions', []))}"}
                return {"ok": True, "detail": f"got_type={m.get('type')}"}
            except asyncio.TimeoutError:
                return {"ok": True, "detail": "connected, no desktop reply"}
            except Exception as e:
                return {"ok": False, "detail": f"{type(e).__name__}: {str(e)[:50]}"}
        await ws.close()
        return {"ok": True, "detail": "handshake ok"}
    return {
        "/s/ws": await one("/s/ws", expect_state=True),
        "/client": await one("/client"),
    }

async def main(host, token):
    print(f"\n===== 自检: {host} (token {token[:6]}…) =====")
    print(f"[1] DNS        : {dns(host)}")
    code, err = https(host, token)
    if code is None:
        print(f"[2] HTTPS /s/  : ❌ {err}")
    else:
        print(f"[2] HTTPS /s/  : HTTP {code}  {'✅' if code == 200 else ('（403=token 不在白名单）' if code == 403 else '⚠️')}")
    w = await ws_probe(host, token)
    sw, cl = w["/s/ws"], w["/client"]
    print(f"[3] WS /s/ws   : {'✅ ' + sw['detail'] if sw['ok'] else '❌ ' + sw['detail']}")
    print(f"[4] WS /client : {'✅ ' + cl['detail'] if cl['ok'] else '❌ ' + cl['detail']}")
    # 判定
    verdict = "✅ 全部通过"
    if code is None or code != 200:
        verdict = "❌ HTTPS 页面不通：检查 Caddy / 443 / relay-server 是否在跑"
    elif "1008" in sw["detail"] or (not sw["ok"] and "1008" in sw["detail"]):
        verdict = "⚠️ 手机 WS 被拒(1008)= 桌面端未上线：重启桌面端或点「连接」；token 是否正确在白名单"
    elif sw.get("detail", "").startswith("desktop_on"):
        verdict = "✅ 桌面已在线，手机端可用"
    print(f"判定: {verdict}\n")

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    token = sys.argv[2]
    import asyncio
    hosts = [sys.argv[1]] + sys.argv[3:]
    for host in hosts:
        asyncio.run(main(host, token))
