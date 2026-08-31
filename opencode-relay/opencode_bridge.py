#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
opencode 中继桥（方案 A）：

把本地 AI Studio 的手机控制台页面（opencode-relay/page.html）连到 opencode serve，
让手机像原来控制本地 Agent 一样控制 opencode 干活。

                       ┌─ opencode-relay（FastAPI, 哑管道） ─┐
  手机浏览器(page.html) ──WS /s/ws ──>  [按 device_token 路由] ──WS /client──> 本脚本(桥)
                                          │                                    │
                                          │                          调用 opencode HTTP API
                                          └──────────── 只转发 JSON 帧 ◄──────┘
                                                              + 订阅 /event SSE 转成手机帧

- 协议（手机帧 / 下行帧）与 desktop/relay.go 完全一致，见 docs/relay/protocol.md。
- 桥本身不跑 agent：它把 Local AI Studio 的协议翻译成 opencode 的 HTTP API。
- 用法：
    python opencode_bridge.py --config opencode_bridge.json
  或直接给参数（覆盖同名字段）：
    python opencode_bridge.py --relay wss://yours.com --token <64hex> \
        --opencode http://127.0.0.1:9001 --workspace /path/to/project
"""

import argparse
import asyncio
import base64
import json
import os
import sys
import threading
import time
import traceback
from urllib.parse import quote

import requests
import websockets

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_CONFIG = os.path.join(HERE, "opencode_bridge.json")
PERM_TIMEOUT = int(os.environ.get("OPENCODE_BRIDGE_PERM_TIMEOUT", "120"))  # ask 模式手机未回，超时自动拒绝


def log(msg):
    print(f"[bridge {time.strftime('%H:%M:%S')}] {msg}", flush=True)


# ---------------------------------------------------------------------------
# opencode serve HTTP 封装（每个调用都在子线程跑，避免阻塞 asyncio 事件循环）
# ---------------------------------------------------------------------------
class OpenCodeClient:
    def __init__(self, base_url, password=""):
        self.base = (base_url or "").rstrip("/")
        self.s = requests.Session()
        # opencode serve 设了 OPENCODE_SERVER_PASSWORD 后走 HTTP Basic 认证（用户名固定 opencode）
        self.password = password or ""
        self.auth = ("opencode", self.password) if self.password else None
        if self.auth:
            self.s.auth = self.auth

    def _url(self, path):
        return self.base + path

    def get(self, path):
        r = self.s.get(self._url(path), timeout=20)
        r.raise_for_status()
        return r.json()

    def post(self, path, body=None):
        r = self.s.post(self._url(path), json=body, timeout=30)
        if r.status_code == 204:
            return None
        r.raise_for_status()
        return r.json() if r.content else None

    def delete(self, path):
        r = self.s.delete(self._url(path), timeout=20)
        r.raise_for_status()
        return r.json()

    def patch(self, path, body=None):
        r = self.s.patch(self._url(path), json=body, timeout=20)
        r.raise_for_status()
        return r.json()

    def session_exists(self, sid):
        try:
            self.get(f"/session/{sid}")
            return True
        except Exception:
            return False


# ---------------------------------------------------------------------------
# 手机帧 ↔ opencode 模型的翻译
# ---------------------------------------------------------------------------
def dataurl_of_image_part(part):
    """把 opencode file/image part 转成 dataURL（手机页用 <img src>）。"""
    url = part.get("url") or part.get("data") or part.get("value") or ""
    if url.startswith("data:image"):
        return url
    mime = part.get("mime") or part.get("mimeType") or "image/png"
    if url and not url.startswith("data:"):
        return f"data:{mime};base64,{url}"
    return url


def scoped(path, directory):
    """API 路径追加 ?directory=（opencode 1.x 按目录作用域；旧版本忽略该参数时行为同全局，安全降级）。"""
    if not directory:
        return path
    sep = "&" if "?" in path else "?"
    return f"{path}{sep}directory={quote(directory)}"


def oc_sessions_to_phone(sessions, running_map, trash_map):
    out = []
    for s in sessions:
        ws = s.get("directory") or s.get("path") or ""
        if ws in trash_map:
            continue  # 对应用户在桌面端"删到垃圾箱"的项目，手机端一并隐藏
        out.append({
            "id": s.get("id"),
            "title": s.get("title") or s.get("slug") or "",
            "updated": (s.get("time") or {}).get("updated") or 0,
            "workspace": ws,
            "running": bool(running_map.get(s.get("id"))),
        })
    return out


def translate_event(ev, running_map):
    """把一条 opencode /event SSE 消息转成若干手机帧。
    running_map: session_id -> bool，用来推断 run:started/run:finished。"""
    frames = []
    typ = ev.get("type")
    p = ev.get("properties") or {}
    sid = p.get("sessionID") or p.get("sessionId")

    if typ == "message.part.delta":
        field, delta = p.get("field"), p.get("delta")
        if field == "text" and delta:
            frames.append({"type": "text", "delta": delta})
        # reasoning / title 等字段手机页没有对应展示，先不发

    elif typ == "message.part.updated":
        part = p.get("part") or {}
        pt = part.get("type")
        if pt in ("tool", "tool-call", "tool-input", "tool-start"):
            st = part.get("state") or {}
            name = str(part.get("tool") or part.get("name") or st.get("title") or pt)
            # 同一 tool part 的 updated 会触发多次：pending/running 只发一次 tool_start，
            # completed/error 发 tool_result（带参数与输出），供手机端「编程过程」面板展示
            pid = part.get("id") or f"{pt}:{part.get('messageID')}"
            seen = running_map.setdefault("_tool_seen", {})
            status = st.get("status") or "pending"
            inp = st.get("input") if isinstance(st.get("input"), dict) else {}
            if status in ("completed", "error"):
                if seen.get(pid) == "start":
                    seen[pid] = "done"
                    out = str(st.get("output") or st.get("error") or "")
                    if len(out) > 1000:
                        out = "…（已截断）\n" + out[-900:]
                    frames.append({"type": "tool_result", "name": name, "args": inp, "result": out})
            elif seen.get(pid) != "start":
                seen[pid] = "start"
                frames.append({"type": "tool_start", "name": name, "args": inp})
        elif pt in ("agent", "agent-start"):
            frames.append({"type": "tool", "delta": f"agent: {part.get('name') or 'sub'}"})

    elif typ == "session.status":
        st = (p.get("status") or {}).get("type")
        if st == "busy":
            # 只在从空闲变为忙时发一次 run:started，避免多步 agent 每步重复重置手机端步骤栏
            if not running_map.get(sid):
                running_map[sid] = True
                frames.append({"type": "run:started", "sessionId": sid})
        elif st == "idle":
            running_map[sid] = False
            frames.append({"type": "run:finished", "sessionId": sid})
            frames.append({"type": "done"})

    elif typ == "session.idle":
        running_map[sid] = False
        frames.append({"type": "run:finished", "sessionId": sid})
        frames.append({"type": "done"})

    elif typ == "session.updated":
        info = p.get("info") or {}
        tok = info.get("tokens")
        if tok:
            frames.append({
                "type": "usage",
                "usage": {"input": tok.get("input", 0), "output": tok.get("output", 0)},
                "total": {"input": tok.get("input", 0), "output": tok.get("output", 0)},
            })

    elif typ == "session.todo":
        # opencode 真实任务清单（TodoTool 维护），手机端步骤栏直接吃
        todo = p.get("todo") or p.get("todos") or []
        if isinstance(todo, list) and todo:
            frames.append({"type": "todo", "sessionId": sid, "todos": todo})

    elif typ == "question.asked":
        # opencode Question 工具向用户提问：转手机审批条（选项按钮作答）
        frames.append({"type": "question_request", "id": p.get("id"),
                       "sessionID": sid, "questions": p.get("questions") or []})

    elif typ in ("question.replied", "question.rejected"):
        frames.append({"type": "question_done", "id": p.get("id")})

    return frames


# ---------------------------------------------------------------------------
# 桥主体
# ---------------------------------------------------------------------------
class Bridge:
    def __init__(self, cfg):
        self.cfg = cfg
        pw = cfg["opencode"].get("password") or os.environ.get("OPENCODE_SERVER_PASSWORD", "")
        self.oc = OpenCodeClient(cfg["opencode"].get("base_url"), password=pw)
        self.relay_url = cfg["relay"].get("server_url", "")
        self.token = cfg["relay"].get("device_token", "")
        self.workspace = cfg["relay"].get("workspace") or os.getcwd()
        self.mode = cfg["relay"].get("mode", "always")
        self.default_model = cfg["opencode"].get("default_model", "")

        self.ws = None
        self.wlock = asyncio.Lock()
        self.evq = asyncio.Queue()
        self.loop = None

        self.current = ""          # 当前打开的 opencode session id
        self.current_model = self.default_model
        self.running_map = {}      # session_id -> bool
        self._dir_by_sid = {}      # session_id -> 创建目录（session 级调用按目录作用域）
        self.pending_perms = {}    # permissionID -> Permission(ask 模式等待手机审批)
        self.perm_timers = {}      # permissionID -> TimerHandle(超时自动拒绝)
        self._provider_cache = None  # /config/providers 懒加载缓存
        self._missed = set()       # 断线期间错过帧的会话，重连后通知手机重拉
        self.insecure = cfg["relay"].get("insecure", False)

    # ---- 发送到手机（经中继广播给该设备所有手机页） ----
    async def _send(self, frame):
        ws = self.ws
        sid = frame.get("session") or frame.get("sessionID") or frame.get("sessionId")
        if ws is None:
            if sid:
                self._missed.add(sid)  # 断线窗口：记下会话，重连后补发刷新通知
            return False
        async with self.wlock:
            try:
                await ws.send(json.dumps(frame, ensure_ascii=False))
                return True
            except Exception as e:
                log(f"下行失败: {e}")
                if sid:
                    self._missed.add(sid)
                return False

    # ---- /event SSE 订阅线程：解析 data: 帧，丢进 asyncio 队列 ----
    def _sse_thread(self):
        url = self.oc.base + "/event"
        while True:
            try:
                with self.oc.s.get(url, stream=True, timeout=None) as r:
                    r.raise_for_status()
                    r.encoding = "utf-8"  # SSE 无 charset，requests 默认按 latin-1 解 → 中文乱码
                    for raw in r.iter_lines(decode_unicode=True):
                        if raw is None:
                            continue
                        s = raw.strip()
                        if s.startswith("data:"):
                            payload = s[5:].strip()
                            if not payload:
                                continue
                            try:
                                ev = json.loads(payload)
                            except Exception:
                                continue
                            if self.loop is not None:
                                self.loop.call_soon_threadsafe(self.evq.put_nowait, ev)
            except Exception as e:
                log(f"opencode /event 断开: {e}；3s 后重订阅")
                time.sleep(3)

    # ---- 事件泵：把 opencode 事件翻译成手机帧并下行 ----
    async def _event_pump(self):
        while True:
            ev = await self.evq.get()
            try:
                if ev.get("type") == "permission.asked":
                    # 权限请求：always/readonly 自动应答，ask 转发给手机审批
                    await self._handle_permission_asked(ev.get("properties") or {})
                    continue
                p = ev.get("properties") or {}
                psid = p.get("sessionID") or p.get("sessionId")
                for f in translate_event(ev, self.running_map):
                    # 补 sessionId：断线记账 + 手机端会话过滤都依赖它
                    if psid and not (f.get("session") or f.get("sessionID") or f.get("sessionId")):
                        f["sessionId"] = psid
                    await self._send(f)
            except Exception as e:
                log(f"事件翻译失败: {e}")

    # ---- 权限：opencode permission.asked -> /permissions/:id 应答 ----
    async def _handle_permission_asked(self, perm):
        pid = perm.get("id")
        sid = perm.get("sessionID")
        if not pid or not sid:
            return
        if self.mode == "readonly":
            await self._answer_permission(sid, pid, "reject")
        elif self.mode == "always":
            await self._answer_permission(sid, pid, "always")
        else:
            # ask：转发给手机等审批；超时自动拒绝
            self.pending_perms[pid] = perm
            self.perm_timers[pid] = self.loop.call_later(
                PERM_TIMEOUT, self._schedule_deny, sid, pid)
            await self._send({
                "type": "permission_request", "id": pid, "sessionID": sid,
                "permission": perm.get("permission"), "patterns": perm.get("patterns"),
                "metadata": perm.get("metadata") or {}, "tool": perm.get("tool") or {},
            })

    def _schedule_deny(self, sid, pid):
        # call_later 回调里创建协程，避免阻塞事件循环
        asyncio.ensure_future(self._answer_permission(sid, pid, "reject"))

    async def _answer_permission(self, sid, pid, response):
        """response: 'once' | 'always' | 'reject'（opencode 接受的三态）。"""
        self.pending_perms.pop(pid, None)
        t = self.perm_timers.pop(pid, None)
        if t is not None:
            t.cancel()
        try:
            await asyncio.to_thread(
                self.oc.post, f"/session/{sid}/permissions/{pid}", {"response": response})
            log(f"权限应答 {pid} -> {response}")
        except Exception as e:
            log(f"权限应答 {pid} 失败: {e}")

    async def _respond_phone_permission(self, frame):
        pid = frame.get("id")
        resp = (frame.get("response") or "").strip().lower()
        mapped = {"allow": "once", "deny": "reject", "always": "always"}.get(resp, "reject")
        sid = frame.get("sessionID") or (self.pending_perms.get(pid) or {}).get("sessionID")
        if pid and sid:
            await self._answer_permission(sid, pid, mapped)
        else:
            log(f"缺少权限应答上下文: id={pid} sessionID={sid}")

    # ---- 中继出入站循环（带重连） ----
    async def _relay_loop(self):
        server = self.relay_url
        server = server.replace("https://", "wss://").replace("http://", "ws://").rstrip("/")
        url = f"{server}/client?d={quote(self.token)}"
        backoff = 1
        while True:
            try:
                extra = {}
                if self.insecure:
                    import ssl
                    extra["ssl"] = ssl.create_default_context()
                    extra["ssl"].check_hostname = False
                    extra["ssl"].verify_mode = ssl.CERT_NONE
                conn = await websockets.connect(url, open_timeout=15,
                                                ping_interval=20, ping_timeout=10, **extra)
            except Exception as e:
                log(f"连接中继失败: {e}；{backoff}s 后重试")
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, 30)
                continue
            backoff = 1
            self.ws = conn
            log(f"已接入中继 {url}")
            await self._send({"type": "hello", "workspace": self.workspace,
                              "model": self.current_model, "mode": self.mode,
                              "version": "opencode-bridge/1.0"})
            # 断线补偿：哑管道不缓存帧，通知手机端重拉错过帧的会话（openS 会重拉全量消息）
            if self._missed:
                missed, self._missed = set(self._missed), set()
                for msid in missed:
                    await self._send({"type": "session:opened", "id": msid})
                log(f"断线补偿：通知手机重拉 {len(missed)} 个会话")
            try:
                async for raw in conn:
                    try:
                        frame = json.loads(raw)
                    except Exception:
                        continue
                    await self._on_phone_frame(frame)
            except Exception as e:
                log(f"中继连接断开: {e}")
            finally:
                self.ws = None
                await asyncio.sleep(min(backoff, 5))

    # ---- 手机下行的指令分发 ----
    async def _on_phone_frame(self, frame):
        t = frame.get("type")
        rid = frame.get("rid")
        try:
            if t == "send":
                await self._handle_send(frame)
            elif t == "stop":
                await self._handle_stop(frame)
            elif t == "state":
                await self._send(await self._state(rid))
            elif t == "messages":
                await self._send(await self._messages(frame.get("id"), rid))
            elif t == "models":
                await self._send(await self._models(rid))
            elif t == "model":
                if frame.get("key"):
                    self.current_model = frame["key"]
                await self._send({"type": "model:changed"})
            elif t == "effort":
                # opencode 没有与 phone 推理档位一一对应的概念；v1 记录但不下发
                pass
            elif t == "mode":
                v = (frame.get("value") or "").strip()
                if v:
                    self.mode = v
            elif t == "delete_session":
                await self._handle_delete(frame.get("id"))
                await self._send({"type": "sessions:changed"})
            elif t == "rename_session":
                await self._handle_rename(frame.get("id"), frame.get("title"))
                await self._send({"type": "sessions:changed"})
            elif t == "open_session":
                sid = frame.get("id")
                if sid:
                    self.current = sid
                    self.running_map.setdefault(sid, False)
                    await self._send({"type": "session:opened", "id": sid})
            elif t == "workspace":
                # 手机→PC 同步：切桥的活动工作区（影响 state 显示、git 分支、新会话归属）。
                # opencode serve 的执行目录由 ?directory= 按会话下发，见 scoped()。
                d = (frame.get("dir") or "").strip()
                if d and os.path.isdir(d):
                    self.workspace = d
                    log(f"工作区切换（手机端）-> {d}")
                    await self._send(await self._state(rid))
                else:
                    await self._send({"type": "error", "delta": f"切换失败：目录不存在 {d}", "rid": rid})
            elif t == "permission_response":
                # 手机对 ask 权限的审批结果：allow/deny/always -> once/reject/always
                await self._respond_phone_permission(frame)
            elif t == "dir_list":
                # 手机端目录浏览：列出 path 下的子目录（按目录作用域调 /file，任意绝对路径可浏览）
                p = (frame.get("path") or self.workspace or "/").rstrip("/") or "/"
                await self._send(await self._dir_list(p, rid))
            elif t == "new_session":
                # 在指定目录新建会话（手机端选子目录后一键开聊）
                await self._new_session((frame.get("dir") or "").strip(), rid)
            elif t == "commands":
                # opencode 真实命令列表（自定义命令/skill/mcp），供手机斜杠菜单
                await self._send(await self._commands(rid))
            elif t == "command":
                # 执行斜杠命令：内建映射 + 自定义 command 转发
                await self._handle_command(frame)
            elif t == "question_reply":
                # 手机端对 opencode 提问的作答：answers 与 questions 等长，每项为选中 label 列表
                qid = frame.get("id")
                answers = frame.get("answers") or []
                if qid:
                    await asyncio.to_thread(self.oc.post, f"/question/{qid}/reply",
                                            {"answers": answers})
            elif t == "question_reject":
                qid = frame.get("id")
                if qid:
                    await asyncio.to_thread(self.oc.post, f"/question/{qid}/reject", {})
        except Exception as e:
            log(f"处理 {t} 失败: {e}")
            traceback.print_exc()
            await self._send({"type": "error", "delta": f"桥接错误：{e}"})

    async def _session_list(self):
        return await asyncio.to_thread(self.oc.get, "/session")

    async def _dir_list(self, path, rid):
        """列出 path 下的子目录。/file 按目录作用域调用，任意绝对路径均可浏览；
        失败（如非项目目录）时回空列表并带 error，前端提示可直接粘贴路径。"""
        url = f"/file?path={quote(path)}&directory={quote(path)}"
        try:
            nodes = await asyncio.to_thread(self.oc.get, url)
            dirs = [
                {"name": n.get("name"), "path": n.get("absolute") or (path.rstrip("/") + "/" + n.get("name", ""))}
                for n in nodes
                if isinstance(n, dict) and n.get("type") == "directory" and n.get("name")
            ]
            dirs = [d for d in dirs if not d["name"].startswith(".")]
            dirs.sort(key=lambda d: d["name"].lower())
            return {"type": "dir_list", "rid": rid, "path": path, "dirs": dirs}
        except Exception as e:
            return {"type": "dir_list", "rid": rid, "path": path, "dirs": [],
                    "error": f"该目录无法浏览（可直接粘贴路径新建会话）：{e}"}

    async def _new_session(self, directory, rid):
        """在 directory 下新建会话并切换过去：POST /session?directory=…（按目录作用域）。"""
        directory = (directory or "").strip().rstrip("/") or "/"
        if not os.path.isdir(directory):
            await self._send({"type": "error", "delta": f"目录不存在：{directory}", "rid": rid})
            return
        try:
            s = await asyncio.to_thread(
                self.oc.post, scoped("/session", directory),
                {"title": os.path.basename(directory) or "新会话"})
            sid = (s or {}).get("id")
            self._dir_by_sid[sid] = directory
            self.workspace = directory
            self.current = sid
            log(f"新建会话 {sid} @ {directory}")
            await self._send({"type": "new_session", "rid": rid, "ok": True,
                              "session": sid, "dir": directory})
            await self._send({"type": "session:opened", "id": sid})
            await self._send({"type": "sessions:changed"})
        except Exception as e:
            log(f"新建会话失败 @ {directory}: {e}")
            await self._send({"type": "error", "delta": f"新建会话失败：{e}", "rid": rid})

    def _model_obj(self):
        """'providerID/modelID' 字符串转 opencode 期望的 model 对象（同步版，仅带前缀时可用）。"""
        k = self.current_model or ""
        if "/" in k:
            pid, mid = k.split("/", 1)
            return {"providerID": pid, "modelID": mid}
        return None

    async def _ensure_providers(self):
        """懒加载 provider 清单（用于解析无前缀的模型名）。"""
        if self._provider_cache is None:
            try:
                d = await asyncio.to_thread(self.oc.get, "/config/providers")
                self._provider_cache = (d or {}).get("providers") or []
            except Exception:
                self._provider_cache = []
        return self._provider_cache

    async def _resolve_model(self):
        """当前模型 → {providerID, modelID}；无前缀时在 provider 清单里找唯一归属。"""
        k = (self.current_model or "").strip()
        if not k:
            return None
        if "/" in k:
            pid, mid = k.split("/", 1)
            return {"providerID": pid, "modelID": mid}
        for prov in await self._ensure_providers():
            if isinstance(prov, dict) and k in (prov.get("models") or {}):
                return {"providerID": prov.get("id"), "modelID": k}
        return None

    async def _handle_send(self, frame):
        sid = frame.get("session")
        text = (frame.get("text") or "").strip()
        atts = [a for a in (frame.get("atts") or [])
                if isinstance(a, dict) and (a.get("name") or "").strip() and (a.get("data") or "")]
        if not text and not atts:
            return
        # 附件分流（对齐 desktop/relay.go）：图片→opencode 原生多模态 parts；
        # 其他文件→落盘工作区 media/，路径附进 prompt 让 agent 用工具读。
        imgs, names, paths = [], [], []
        for a in atts:
            name = (a.get("name") or "").strip()
            d = a.get("data") or ""
            names.append(name)
            if d.startswith("data:image"):
                imgs.append(d)
            else:
                p = await asyncio.to_thread(_save_upload, self.workspace, name, d)
                if p:
                    paths.append(p)
                else:
                    log(f"附件落盘失败: {name}")
        # 无会话直接发（空项目开聊）：先在当前工作区建会话
        if not sid:
            try:
                title = text[:40] or (names[0][:40] if names else "新会话")
                new = await asyncio.to_thread(
                    self.oc.post, scoped("/session", self.workspace), {"title": title})
            except Exception as e:
                await self._send({"type": "error", "delta": f"新建会话失败：{e}"})
                return
            sid = (new or {}).get("id", "")
            if not sid:
                await self._send({"type": "error", "delta": "新建会话失败"})
                return
            self._dir_by_sid[sid] = self.workspace
            log(f"新建会话 {sid} @ {self.workspace}")
            await self._send({"type": "sessions:changed"})
        # 回显带 📎 附件名（模式同 desktop/relay.go：显示文本带标签，prompt 不带）
        disp = text + (("\n\n📎 " + "  ".join(names)) if names else "")
        await self._send({"type": "user_message", "session": sid, "text": disp, "images": imgs})

        # 确认/创建会话（新会话 born 在当前工作区）
        if not await asyncio.to_thread(self.oc.session_exists, sid):
            new = await asyncio.to_thread(self.oc.post, scoped("/session", self.workspace), {"title": text[:40]})
            sid = (new or {}).get("id", sid)
            self._dir_by_sid[sid] = self.workspace
        self.current = sid

        prompt = text + (("\n\n" + "\n".join("附件文件（可用工具读取）：" + p for p in paths)) if paths else "")
        parts = [{"type": "text", "text": prompt}] if prompt else []
        for a in atts:
            d = a.get("data") or ""
            if d.startswith("data:image"):
                parts.append(_image_part(d, a.get("name")))
        body = {"parts": parts}
        m = await self._resolve_model()
        if m:
            body["model"] = m
        log(f"send -> session={sid} 模型={self.current_model or '(默认)'} 工作区={self._dir_by_sid.get(sid) or self.workspace}")
        try:
            await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/prompt_async", self._dir_by_sid.get(sid) or self.workspace), body)
        except Exception as e:
            log(f"prompt_async 失败: {e}")
            await self._send({"type": "error", "delta": f"发送失败：{e}"})

    async def _handle_stop(self, frame):
        sid = frame.get("session")
        if sid:
            await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/abort", self._dir_by_sid.get(sid)), None)
            await self._send({"type": "run:finished", "sessionId": sid})

    async def _handle_delete(self, sid):
        if sid:
            await asyncio.to_thread(self.oc.delete, f"/session/{sid}")
            self.running_map.pop(sid, None)

    async def _handle_rename(self, sid, title):
        if sid and title and title.strip():
            await asyncio.to_thread(self.oc.patch, f"/session/{sid}", {"title": title.strip()})

    # ---- opencode 斜杠命令：列表下发 + 执行 ----
    async def _commands(self, rid):
        """GET /command：真实命令清单（.opencode/command 自定义、skill、mcp 来源）。"""
        try:
            data = await asyncio.to_thread(self.oc.get, scoped("/command", self.workspace))
        except Exception:
            data = []
        out = [{"name": c.get("name") or "", "description": (c.get("description") or "").strip(),
                "agent": c.get("agent") or "", "source": c.get("source") or "command"}
               for c in data or [] if isinstance(c, dict) and c.get("name")]
        return {"type": "commands", "rid": rid, "commands": out}

    async def _ensure_session(self, sid, title):
        """会话为空或已不存在时，在当前工作区新建一个；返回可用 sid。"""
        if sid and await asyncio.to_thread(self.oc.session_exists, sid):
            return sid
        new = await asyncio.to_thread(self.oc.post, scoped("/session", self.workspace), {"title": title})
        nid = (new or {}).get("id", "")
        if nid:
            self._dir_by_sid[nid] = self.workspace
            self.current = nid
            await self._send({"type": "sessions:changed"})
        return nid

    async def _handle_command(self, frame):
        """手机端 /xxx：内建命令映射到 opencode 专有端点，其余转发 POST /session/:id/command。"""
        sid = frame.get("session")
        cmd = (frame.get("command") or "").strip().lstrip("/")
        args = (frame.get("arguments") or "").strip()
        if not cmd:
            return
        sid = await self._ensure_session(sid, f"/{cmd} {args}".strip()[:40])
        if not sid:
            await self._send({"type": "error", "delta": f"/{cmd} 失败：无法创建会话"})
            return
        directory = self._dir_by_sid.get(sid) or self.workspace
        try:
            if cmd == "undo":       # 撤销最近一条消息（连带回滚其文件改动）
                msgs = await asyncio.to_thread(self.oc.get, scoped(f"/session/{sid}/message", directory))
                mid = ((msgs or [{}])[-1].get("info") or {}).get("id") or ""
                if not mid:
                    await self._send({"type": "error", "delta": "没有可撤销的消息"})
                    return
                await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/revert", directory),
                                        {"messageID": mid})
            elif cmd == "redo":     # 恢复被 undo 的消息
                await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/unrevert", directory), {})
            elif cmd == "compact":  # 手动压缩上下文（summarize）
                m = await self._resolve_model()
                if not m:
                    await self._send({"type": "error", "delta": "compact 需要先选模型（providerID/modelID）"})
                    return
                await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/summarize", directory),
                                        {"providerID": m["providerID"], "modelID": m["modelID"]})
            elif cmd == "share":
                s = await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/share", directory), {})
                sh = (s or {}).get("share") if isinstance(s, dict) else None
                link = (sh or {}).get("url") or (s or {}).get("url") or ""
                await self._send({"type": "text",
                                  "delta": f"🔗 分享链接：{link}" if link else "已分享（未返回链接）"})
                return
            elif cmd == "unshare":
                await asyncio.to_thread(self.oc.delete, scoped(f"/session/{sid}/share", directory))
            elif cmd == "diff":     # 本次会话的代码改动（专用端点，回退 vcs 全局 diff）
                try:
                    d = await asyncio.to_thread(
                        self.oc.get, scoped(f"/session/{sid}/diff", directory))
                except Exception:
                    d = await asyncio.to_thread(self.oc.get, scoped("/vcs/diff", directory))
                if isinstance(d, list):
                    parts = []
                    for it in d:
                        if not isinstance(it, dict):
                            continue
                        f = it.get("file") or it.get("path") or ""
                        t = it.get("text") or it.get("diff") or ""
                        if t:
                            parts.append(f"--- {f}\n{t}" if f else t)
                        elif f:
                            parts.append(f"（有改动）{f}")
                    txt = "\n".join(parts)
                else:
                    txt = d if isinstance(d, str) else json.dumps(d, ensure_ascii=False, indent=1)
                if len(txt) > 4000:
                    txt = txt[:4000] + "\n…（已截断）"
                await self._send({"type": "diff", "session": sid, "delta": txt or "（无改动）"})
                return
            elif cmd == "init":     # 让 opencode 分析代码库生成 AGENTS.md
                await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/init", directory), {})
            else:                   # 自定义命令（.opencode/command、skill、mcp）
                await asyncio.to_thread(self.oc.post, scoped(f"/session/{sid}/command", directory),
                                        {"command": cmd, "arguments": args})
        except Exception as e:
            await self._send({"type": "error", "delta": f"/{cmd} 失败：{e}"})
            return
        await self._send({"type": "command_result", "session": sid, "command": cmd, "ok": True})

    # ---- 手机页需要的几类应答帧 ----
    def _git_branch(self):
        import subprocess
        try:
            out = subprocess.check_output(["git", "-C", self.workspace, "rev-parse", "--abbrev-ref", "HEAD"],
                                          stderr=subprocess.DEVNULL)
            return out.decode().strip()
        except Exception:
            return ""

    async def _state(self, rid):
        try:
            sessions = await asyncio.to_thread(self.oc.get, "/session")
        except Exception:
            sessions = []
        self._dir_by_sid = {s.get("id"): (s.get("directory") or s.get("path") or "") for s in sessions or []}
        return {
            "type": "state", "rid": rid,
            "workspace": self.workspace, "mode": self.mode, "current": self.current_model,
            "current_session": self.current, "branch": self._git_branch(), "compact": {},
            "sessions": oc_sessions_to_phone(sessions, self.running_map, {}),
        }

    async def _messages(self, sid, rid):
        if not sid:
            return {"type": "messages", "rid": rid, "id": sid, "messages": []}
        try:
            data = await asyncio.to_thread(self.oc.get, scoped(f"/session/{sid}/message", self._dir_by_sid.get(sid)))
        except Exception:
            return {"type": "messages", "rid": rid, "id": sid, "messages": []}
        msgs = []
        for item in data:
            info = item.get("info") or {}
            role = info.get("role")
            if role not in ("user", "assistant"):
                continue
            text, imgs = "", []
            for p in item.get("parts") or []:
                t = p.get("type")
                if t == "text":
                    text += (p.get("text") or "")
                elif t in ("file", "image") and (p.get("url") or "").startswith("data:image"):
                    imgs.append(dataurl_of_image_part(p))
                elif t == "tool":
                    # 手机页没有工具输出槽，简化为一段说明文本（截断防刷屏）
                    st = p.get("state") or {}
                    tool_out = st.get("output") or st.get("error") or ""
                    if isinstance(tool_out, dict):
                        tool_out = tool_out.get("content") or tool_out.get("output") or str(tool_out)
                    title = st.get("title") or p.get("tool") or "tool"
                    if tool_out:
                        text += f"\n> 🔧 {title}: {str(tool_out)[:300]}"
            if text == "" and not imgs:
                continue
            m = {"role": role, "text": text.strip()}
            if imgs:
                m["images"] = imgs
            msgs.append(m)
        return {"type": "messages", "rid": rid, "id": sid, "messages": msgs}

    async def _models(self, rid):
        try:
            d = await asyncio.to_thread(self.oc.get, "/config/providers")
        except Exception:
            return {"type": "models", "rid": rid, "models": []}
        default = d.get("default") or {}
        models = []
        for prov in d.get("providers") or []:
            pid = prov.get("id")
            default_mid = default.get(pid)
            if not isinstance(prov.get("models"), dict):
                continue
            for mid in (prov.get("models") or {}):
                md = prov["models"][mid] if isinstance(prov["models"].get(mid), dict) else {}
                caps = md.get("capabilities") or {}
                key = f"{pid}/{mid}"
                # 用户切了模型就以桥的 current_model 为准；未切则回退 opencode 默认
                is_cur = (key == self.current_model) if self.current_model else (mid == default_mid)
                models.append({
                    "key": key, "model_id": mid,
                    "display_name": md.get("name") or mid,
                    "is_current": is_cur,
                    "vision": bool((caps.get("input") or {}).get("image")),
                    "reasoning": bool(caps.get("reasoning")),
                })
        return {"type": "models", "rid": rid, "models": models}

    # ---- 启动 ----
    async def run(self):
        self.loop = asyncio.get_running_loop()
        threading.Thread(target=self._sse_thread, daemon=True).start()
        await asyncio.gather(self._relay_loop(), self._event_pump())


def _image_part(dataurl, name):
    """dataURL -> opencode FilePartInput（{type:file, mime, filename, url}）。"""
    mime = "image/png"
    if "," in dataurl:
        head = dataurl.split(",", 1)[0]
        if head.startswith("data:") and ";" in head:
            mime = head[5:].split(";")[0]
    return {"type": "file", "mime": mime, "filename": (name or "image.png"), "url": dataurl}


def _save_upload(ws_dir, name, data):
    """dataURL 落盘到 <工作区>/media/<纳秒时间戳>-<文件名>，返回路径；失败返回 None。
    对齐 desktop/relay.go savePhoneUploads：非图片附件交给 agent 用工具读。"""
    try:
        raw = data.split(",", 1)[1] if "," in data else data
        dec = base64.b64decode(raw)
        base = os.path.basename(name) or "upload.bin"  # 防目录穿越
        if base in (".", "/"):
            base = "upload.bin"
        dirp = os.path.join(ws_dir, "media")
        os.makedirs(dirp, exist_ok=True)
        p = os.path.join(dirp, f"{time.time_ns()}-{base}")
        with open(p, "wb") as f:
            f.write(dec)
        return p
    except Exception:
        return None


# ---------------------------------------------------------------------------
# 配置
# ---------------------------------------------------------------------------
def load_config(path, overrides):
    cfg = {
        "relay": {"server_url": "", "device_token": "", "workspace": os.getcwd(),
                  "mode": "always", "insecure": False},
        "opencode": {"base_url": "http://127.0.0.1:9001", "default_model": "", "password": ""},
    }
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            user = json.load(f)
        for k, v in user.items():
            if isinstance(v, dict) and isinstance(cfg.get(k), dict):
                cfg[k].update(v)
            else:
                cfg[k] = v
    over = vars(overrides)
    if over.get("relay"):
        cfg["relay"]["server_url"] = over["relay"]
    if over.get("token"):
        cfg["relay"]["device_token"] = over["token"]
    if over.get("workspace"):
        cfg["relay"]["workspace"] = over["workspace"]
    if over.get("mode"):
        cfg["relay"]["mode"] = over["mode"]
    if over.get("insecure"):
        cfg["relay"]["insecure"] = True
    if over.get("opencode"):
        cfg["opencode"]["base_url"] = over["opencode"]
    if over.get("model"):
        cfg["opencode"]["default_model"] = over["model"]
    if over.get("password"):
        cfg["opencode"]["password"] = over["password"]
    return cfg


def main():
    ap = argparse.ArgumentParser(description="opencode 中继桥（把手机控制台接到 opencode）")
    ap.add_argument("--config", default=DEFAULT_CONFIG, help="配置 json 路径")
    ap.add_argument("--relay", help="中继服务器地址(https/wss)，如 wss://yours.com 或 http://127.0.0.1:8999")
    ap.add_argument("--token", help="device token（需在 relay-server device_tokens 白名单中）")
    ap.add_argument("--workspace", help="当前项目目录（opencode 工作区）")
    ap.add_argument("--mode", help="权限模式 readonly/ask/always，默认 always")
    ap.add_argument("--opencode", help="opencode serve 地址，默认 http://127.0.0.1:9001")
    ap.add_argument("--model", help="默认模型 key（providerID/modelID）")
    ap.add_argument("--password", help="opencode serve 密码（= OPENCODE_SERVER_PASSWORD；设了才用，无则免登陆）")
    ap.add_argument("--insecure", action="store_true", help="关闭 TLS 证书校验（自签证书时用）")
    args = ap.parse_args()

    cfg = load_config(args.config, args)
    if not cfg["relay"].get("server_url") or not cfg["relay"].get("device_token"):
        print("缺少中继配置：需要 --relay 与 --token（或写入 --config 文件）", file=sys.stderr)
        sys.exit(1)
    log(f"中继={cfg['relay']['server_url']}  workspace={cfg['relay']['workspace']}  opencode={cfg['opencode']['base_url']}")
    log("桥已启动，等待手机连接…")
    try:
        asyncio.run(Bridge(cfg).run())
    except KeyboardInterrupt:
        log("已退出")


if __name__ == "__main__":
    main()
