#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
opencode 中继桥（方案 A）——完整说明
=====================================

做什么
------
把手机控制台页面（../service/page.html，由中继提供）连到 opencode serve，
让手机像原来控制本地 Agent 一样控制 opencode 干活：发消息/传附件、看流式回复与
工具调用过程、切模型/切目录、审批权限、回答提问、跑斜杠命令、看 diff 等。

拓扑（三段式，本脚本是其中最右一段）
------------------------------------

                       ┌─ opencode-relay（FastAPI, 哑管道） ─┐
  手机浏览器(page.html) ──WS /s/ws ──>  [按 device_token 路由] ──WS /client──> 本脚本(桥)
                                          │                                    │
                                          │                          调用 opencode HTTP API
                                          └──────────── 只转发 JSON 帧 ◄──────┘
                                                              + 订阅 /event SSE 转成手机帧

职责边界
--------
- 中继（relay-server）：哑管道。只按 device_token 把手机帧广播给对应设备、把设备
  帧广播给该设备的所有手机页，不理解也不缓存帧内容——桥断线期间的下行帧会丢，
  由本桥重连后的「断线补偿」兜底（见 Bridge._relay_loop）。
- 本桥：不跑 agent，只做「Local AI Studio 协议 ↔ opencode HTTP API」的翻译。
  会话管理、流式输出、工具执行、权限裁决、todo、提问、斜杠命令全部由 opencode
  serve 提供，桥把它们映射成与 desktop/relay.go 完全一致的手机帧
  （协议见 ../../docs/relay/protocol.md）。

线程/协程模型（三条并发流）
--------------------------
1. asyncio 事件循环（主协程）：
   - Bridge._relay_loop：与中继的 WebSocket 出入站 + 指数退避重连；
   - Bridge._event_pump：消费 SSE 队列，翻译成手机帧下行。
2. SSE 守护线程（Bridge._sse_thread）：requests 流式读 /event 是阻塞 IO，不能进
   事件循环，故独立线程常驻订阅，收到帧经 call_soon_threadsafe 投回事件循环队列。
3. 线程池（_to_thread 兼容垫片）：所有对 opencode 的阻塞 HTTP 调用都丢线程池跑，
   避免卡住事件循环。

关键协议映射速览
----------------
- 上行（手机 → 桥）：send / stop / state / messages / models / model / mode /
  workspace / permission_response / dir_list / new_session / commands / command /
  question_reply / question_reject / …，完整分发表见 Bridge._on_phone_frame。
- 下行（桥 → 手机）：text / tool_start / tool_result / usage / todo / run:started /
  run:finished / done / session:opened / sessions:changed / permission_request /
  question_request / state / models / messages / commands / diff / error / …，
  由 translate_event（SSE→帧）与各 _handle_* 产生。

配置（opencode_bridge.json，命令行同名参数覆盖同名字段）
--------------------------------------------------------
    {
      "relay":    {"server_url": "wss://yours.com",    // 中继地址（http/https 亦可）
                   "device_token": "<64位hex>",        // 设备白名单令牌
                   "workspace": "/path/to/project",    // 默认工作区
                   "mode": "always",                   // readonly / ask / always
                   "insecure": false},                 // 自签证书时 true（跳过 TLS 校验）
      "opencode": {"base_url": "http://127.0.0.1:9001",
                   "default_model": "provider/model",  // 可空 = 用 opencode 默认
                   "password": ""}                     // = OPENCODE_SERVER_PASSWORD，设了才走 Basic 认证
    }

用法
----
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

# 脚本所在目录：默认配置文件 opencode_bridge.json 与脚本同目录
HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_CONFIG = os.path.join(HERE, "opencode_bridge.json")
# ask 权限模式下手机端迟迟不审批的兜底：超过该秒数自动 reject，避免 opencode 侧
# 永久挂起等授权。可用环境变量 OPENCODE_BRIDGE_PERM_TIMEOUT 覆盖（单位秒，默认 120）。
PERM_TIMEOUT = int(os.environ.get("OPENCODE_BRIDGE_PERM_TIMEOUT", "120"))


def log(msg):
    # 统一带时间戳的 stdout 日志；flush=True 保证 nohup/systemd 重定向后仍实时可见
    print(f"[bridge {time.strftime('%H:%M:%S')}] {msg}", flush=True)


# ---------------------------------------------------------------------------
# asyncio.to_thread 兼容垫片：Python 3.8 没有该 API（3.9 新增）。
# 实测 Windows 本机 Python 3.8.6 上每次调用都抛 AttributeError，且多被调用方
# except 吞掉（如 _models 静默回空列表），因此统一走 run_in_executor 等价实现。
# ---------------------------------------------------------------------------
if hasattr(asyncio, "to_thread"):
    _to_thread = asyncio.to_thread
else:
    import functools

    def _to_thread(fn, *args, **kwargs):
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            loop = asyncio.get_event_loop()
        return loop.run_in_executor(None, functools.partial(fn, *args, **kwargs))


# ---------------------------------------------------------------------------
# opencode serve HTTP 封装（每个调用都是阻塞 requests，桥侧一律经 _to_thread
# 丢子线程跑，避免阻塞 asyncio 事件循环）
# ---------------------------------------------------------------------------
class OpenCodeClient:
    """opencode serve 的极简 HTTP 客户端。

    - base_url 形如 http://127.0.0.1:9001，path 以 /session、/event 等开头；
    - opencode serve 设了 OPENCODE_SERVER_PASSWORD 时走 HTTP Basic 认证
      （用户名固定 opencode，密码即该环境变量值），未设则免认证直连；
    - 所有方法失败即抛异常（raise_for_status），兜底方式由调用方决定。
    """

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
        # path 可能已带 query（如 ?directory=…），直接拼接即可
        r = self.s.get(self._url(path), timeout=20)
        r.raise_for_status()
        return r.json()

    def post(self, path, body=None):
        # opencode 部分端点（abort/unrevert 等）成功返回 204 无 body，按 None 处理
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
        # 会话可能已被桌面端/opencode 侧删掉；用实时探活代替本地缓存，保证跨端一致
        try:
            self.get(f"/session/{sid}")
            return True
        except Exception:
            return False


# ---------------------------------------------------------------------------
# 手机帧 ↔ opencode 模型的翻译（纯函数，供 Bridge 调用）
# ---------------------------------------------------------------------------
def dataurl_of_image_part(part):
    """把 opencode file/image part 转成 dataURL（手机页用 <img src> 显示）。

    opencode 不同版本的图片 part 字段不统一，按 url → data → value 顺序取值：
    - 已是 dataURL：原样返回；
    - 是裸 base64 串：按 part 的 mime（缺省 image/png）包成 dataURL；
    - 其他（http 链接等）：原样返回，由前端自行处理。
    """
    url = part.get("url") or part.get("data") or part.get("value") or ""
    if url.startswith("data:image"):
        return url
    mime = part.get("mime") or part.get("mimeType") or "image/png"
    if url and not url.startswith("data:"):
        return f"data:{mime};base64,{url}"
    return url


def scoped(path, directory):
    """API 路径追加 ?directory=（opencode 1.x 按目录作用域；旧版本忽略该参数时行为同全局，安全降级）。

    opencode 1.x 的会话/文件/命令等资源都挂在「项目目录」命名空间下：同一个
    serve 进程可同时服务多个项目目录，调 API 时用 directory 指明操作哪个目录。
    桥用 _dir_by_sid 记住每个会话的出生目录，会话级调用都带上它——手机切了
    工作区后也不会误伤其他目录下的会话。
    """
    if not directory:
        return path
    sep = "&" if "?" in path else "?"
    return f"{path}{sep}directory={quote(directory)}"


def oc_sessions_to_phone(sessions, running_map, trash_map):
    """opencode 会话列表 → 手机端会话卡片列表。

    - running_map: session_id -> bool，桥从 session.status 事件推断的「运行中」标记；
    - trash_map: 已删进垃圾箱的 workspace 集合，命中则整项隐藏（与桌面端行为一致）；
    - 输出字段与 desktop/relay.go 的 state.sessions 严格对齐，勿改字段名。
    """
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
    """把一条 opencode /event SSE 消息转成 0..n 条手机帧（一条 SSE 常对应多帧）。

    running_map 有两种条目：
    - session_id -> bool：会话是否在跑，用于推断 run:started/run:finished 的「沿」；
    - "_tool_seen" -> {part_id: "start"|"done"}：工具 part 去重表。opencode 对同一
      tool part 会连发多次 message.part.updated（pending/running/completed/error），
      手机端只关心两个沿：开跑一次（tool_start）、结束一次（tool_result）。

    事件覆盖表：
    - message.part.delta(field=text)   → text（流式正文增量；reasoning/title 等字段手机页无展示，不发）
    - message.part.updated(tool 系)    → tool_start / tool_result（带参数与输出，输出截到末尾 900 字）
    - message.part.updated(agent 系)   → tool（子 agent 切换提示行）
    - session.status(busy/idle)        → run:started（仅空闲→忙沿发一次，避免多步 agent 反复重置步骤栏）/ run:finished + done
    - session.idle                     → run:finished + done（旧版本事件名兼容）
    - session.updated(info.tokens)     → usage（累计 token 计数）
    - session.todo                     → todo（opencode 真实任务清单 → 手机端步骤栏）
    - question.asked / replied / rejected → question_request / question_done（问答闭环）
    其余事件（file/storage/lsp/…）与手机端无关，返回空列表直接丢弃。
    """
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
# 桥主体：持有全部运行状态，串起「中继 WebSocket 出入站」与「opencode SSE 订阅」
# ---------------------------------------------------------------------------
class Bridge:
    """桥核心，两条长连接在这里汇合。

    - 对中继：_relay_loop 维持一条 WebSocket（/client?d=<token>），收手机帧交给
      _on_phone_frame 分发，下行帧统一经 _send 发出；
    - 对 opencode：_sse_thread 后台订阅 /event SSE，事件进 evq 队列，由 _event_pump
      消费并翻译成手机帧；所有 HTTP 调用经 _to_thread 丢线程池。

    生命周期：main() 构造后调 run()，进程常驻，断线自动重连。
    """

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
        # Lock/Queue 在 Python 3.8/3.9 构造时就绑定当前事件循环，而本对象在
        # asyncio.run 之外创建——这里先置 None，进 run() 的运行循环后再实例化，
        # 否则 _event_pump 等会报 "attached to a different loop"。
        self.wlock = None
        self.evq = None
        self.loop = None

        self.current = ""          # 当前打开的 opencode session id
        self.current_model = self.default_model
        self._ver = ""             # opencode 版本号（/global/health，见 _opencode_version）
        self._ver_at = 0.0
        self.running_map = {}      # session_id -> bool（另有 "_tool_seen" 子表做工具 part 去重）
        self._dir_by_sid = {}      # session_id -> 创建目录（session 级调用按目录作用域，见 scoped()）
        self.pending_perms = {}    # permissionID -> Permission(ask 模式等待手机审批)
        self.perm_timers = {}      # permissionID -> TimerHandle(超时自动拒绝，见 PERM_TIMEOUT)
        self._provider_cache = None  # /config/providers 懒加载缓存（解析无前缀模型名用）
        self._missed = set()       # 断线期间错过帧的会话，重连后通知手机重拉
        self.insecure = cfg["relay"].get("insecure", False)  # true=跳过中继 TLS 证书校验（自签证书）

    # ---- 发送到手机（经中继广播给该设备所有手机页） ----
    async def _send(self, frame):
        """下行一帧 JSON 给手机页。

        中继是哑管道不缓存帧：若此刻未连上中继（ws 为空）或发送失败，则把该帧
        所属会话记进 _missed，等重连成功后统一补发 session:opened 让手机端重拉
        全量消息（见 _relay_loop）。wlock 串行化发送，避免多协程交错写同一
        WebSocket。返回是否真正发出。
        """
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
        """常驻守护线程：订阅 opencode 的全局事件流 GET /event。

        - requests 流式读是阻塞 IO，必须独立于 asyncio 事件循环跑；
        - SSE 行协议只关心 "data:" 行，注释行/空行跳过，非 JSON 帧忽略；
        - 解析出的事件经 call_soon_threadsafe + evq.put_nowait 线程安全地交给
          事件循环侧的 _event_pump 消费；
        - 断流（opencode 重启/网络抖动）则打日志、sleep 3s 后无限重订阅，
          桥的生命周期内事件订阅自愈。
        """
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
        """事件循环侧的消费者：evq 取 SSE 事件 → translate_event → _send 下行。

        两处特殊处理：
        - permission.asked 不走 translate_event，单独分流：readonly/always 模式
          由桥自动应答，ask 模式转手机审批（_handle_permission_asked）；
        - 翻译出的帧若缺 sessionId，用事件自带的 sessionID 补上——断线记账
          （_missed）与手机端按会话过滤都依赖它。
        单帧翻译抛异常只打日志，绝不让泵停转。
        """
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
        """opencode 工具执行前的授权请求，按桥的 mode 三态分流：

        - readonly：一律 reject（只读模式禁止任何写操作）；
        - always：一律回 always（同类操作以后也不再问）；
        - ask：挂进 pending_perms 等手机审批，PERM_TIMEOUT 秒无应答自动 reject。
        """
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
        """把审批结果 POST 回 opencode，并清掉挂起态与超时定时器。

        response: 'once' | 'always' | 'reject'（opencode 接受的三态）。
        唯一的应答出口：模式自动应答、超时兜底、手机审批都汇到这里，天然幂等
        （重复应答时 pending/timer 已被清空，只会多发一次 POST，opencode 侧容忍）。
        """
        self.pending_perms.pop(pid, None)
        t = self.perm_timers.pop(pid, None)
        if t is not None:
            t.cancel()
        try:
            await _to_thread(
                self.oc.post, f"/session/{sid}/permissions/{pid}", {"response": response})
            log(f"权限应答 {pid} -> {response}")
        except Exception as e:
            log(f"权限应答 {pid} 失败: {e}")

    async def _respond_phone_permission(self, frame):
        """手机审批帧（allow/deny/always）→ opencode 三态（once/reject/always）。

        未知响应值一律按 reject 处理（fail-closed）；sessionID 优先取手机帧自带
        的，缺失时回查挂起表 pending_perms。
        """
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
        """与中继维持长连接的主循环，进程生命周期内永不退出。

        每轮：websockets 连 /client?d=<token>（15s 连接超时、20s 心跳）→ 连上后
        发 hello 握手帧（工作区/当前模型/权限模式/版本）→ 断线补偿 → 收帧循环
        （每帧 JSON 解析后交 _on_phone_frame）→ 连接断开则退避后重来
        （1s 起指数退避，30s 封顶；成功连上即重置为 1s）。

        断线补偿：中继哑管道不缓存帧，桥断线窗口里产生的下行帧已丢（其所属
        会话记在 _missed），重连成功后对每个受影响会话补发 session:opened，
        手机端收到会重拉该会话全量消息，等效恢复。
        """
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
        """手机 → 桥的指令分发表（帧格式与 desktop/relay.go 一致）。

        send                发消息/附件（_handle_send，核心路径）
        stop                中止会话运行（POST /session/:id/abort）
        state               全量状态快照（工作区/分支/模式/模型/会话列表）
        messages            拉某会话历史消息（进手机端聊天窗）
        models / model      模型清单下发 / 切换当前模型（仅记录在桥侧）
        effort              推理档位：opencode 无对应概念，v1 直接忽略
        mode                切权限模式（readonly/ask/always），只改桥侧
        delete_session / rename_session / open_session   会话删除/改名/选中
        workspace           手机端切桥的活动工作区（影响 state 显示/git 分支/新会话归属）
        permission_response ask 权限的审批结果回传
        dir_list / new_session   子目录浏览 / 按目录新建会话
        commands / command  斜杠命令清单下发 / 执行（_handle_command）
        question_reply / question_reject   opencode 提问的作答/拒绝

        任何分支抛异常都不许弄断连接：捕获后转 error 帧回手机。
        """
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
                # create=true 时目录不存在则先创建。opencode serve 的执行目录由
                # ?directory= 按会话下发，见 scoped()。
                d = (frame.get("dir") or "").strip()
                err = ""
                if d and not os.path.isdir(d) and frame.get("create"):
                    try:
                        os.makedirs(d, exist_ok=True)
                        log(f"已创建目录 {d}")
                    except Exception as e:
                        err = f"创建目录失败：{e}"
                if not err and d and os.path.isdir(d):
                    self.workspace = d
                    log(f"工作区切换（手机端）-> {d}")
                    await self._send(await self._state(rid))
                else:
                    if not err:
                        err = f"切换失败：目录不存在 {d}"
                    await self._send({"type": "error", "delta": err, "rid": rid})
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
                    await _to_thread(self.oc.post, f"/question/{qid}/reply",
                                            {"answers": answers})
            elif t == "question_reject":
                qid = frame.get("id")
                if qid:
                    await _to_thread(self.oc.post, f"/question/{qid}/reject", {})
            elif t == "fs_read":
                # 读取文件内容
                await self._fs_read(frame.get("path"), rid)
            elif t == "fs_list":
                # 列出目录内容
                await self._fs_list(frame.get("path"), rid)
            elif t == "fs_write":
                # 写入文件内容
                await self._fs_write(frame.get("path"), frame.get("content"), rid)
            elif t == "fs_rename":
                # 重命名文件
                await self._fs_rename(frame.get("path"), frame.get("name"), rid)
            elif t == "fs_delete":
                # 删除文件
                await self._fs_delete(frame.get("path"), rid)
        except Exception as e:
            log(f"处理 {t} 失败: {e}")
            traceback.print_exc()
            await self._send({"type": "error", "delta": f"桥接错误：{e}"})

    async def _session_list(self):
        # 全局会话清单（不带 directory，跨目录全返回）；当前 state 走的是内联版本，此处备用
        return await _to_thread(self.oc.get, "/session")

    async def _dir_list(self, path, rid):
        """列出 path 下的子目录。/file 按目录作用域调用，任意绝对路径均可浏览；
        失败（如非项目目录）时回空列表并带 error，前端提示可直接粘贴路径。"""
        url = f"/file?path={quote(path)}&directory={quote(path)}"
        try:
            nodes = await _to_thread(self.oc.get, url)
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
            s = await _to_thread(
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

    async def _fs_read(self, path, rid):
        """读取文件内容：GET /file?path=...&directory=..."""
        path = (path or "").strip()
        if not path:
            await self._send({"type": "error", "delta": "路径为空", "rid": rid})
            return
        directory = self.workspace or path.rsplit("/", 1)[0] if "/" in path else ""
        try:
            content = await _to_thread(self.oc.get, f"/file?path={quote(path)}&directory={quote(directory or path)}")
            await self._send({"type": "fs_read", "rid": rid, "path": path, "content": content or ""})
        except Exception as e:
            await self._send({"type": "error", "delta": f"读取失败：{e}", "rid": rid})

    async def _fs_list(self, path, rid):
        """列出路径下的文件和目录：GET /file?path=...&directory=..."""
        path = (path or "").strip() or self.workspace or "/"
        directory = self.workspace or path.rsplit("/", 1)[0] if "/" in path else ""
        try:
            nodes = await _to_thread(self.oc.get, f"/file?path={quote(path)}&directory={quote(directory or path)}")
            dirs = [
                {"name": n.get("name"), "path": n.get("absolute") or (path.rstrip("/") + "/" + n.get("name", ""))}
                for n in (nodes or [])
                if isinstance(n, dict) and n.get("type") == "directory" and n.get("name")
            ]
            files = [
                {"name": n.get("name"), "path": n.get("absolute") or (path.rstrip("/") + "/" + n.get("name", "")), "size": n.get("size") or 0}
                for n in (nodes or [])
                if isinstance(n, dict) and n.get("type") == "file" and n.get("name")
            ]
            # 过滤隐藏文件/目录
            dirs = [d for d in dirs if not d["name"].startswith(".")]
            files = [f for f in files if not f["name"].startswith(".")]
            dirs.sort(key=lambda d: d["name"].lower())
            files.sort(key=lambda f: f["name"].lower())
            await self._send({"type": "fs_list", "rid": rid, "path": path, "dirs": dirs, "files": files,
                              "safe": True, "fs": True})
        except Exception as e:
            await self._send({"type": "error", "delta": f"列表失败：{e}", "rid": rid})

    async def _fs_write(self, path, content, rid):
        """写入文件内容：POST /file with body {path, content}"""
        path = (path or "").strip()
        if not path:
            await self._send({"type": "error", "delta": "路径为空", "rid": rid})
            return
        directory = self.workspace or path.rsplit("/", 1)[0] if "/" in path else ""
        try:
            await _to_thread(self.oc.post, f"/file?path={quote(path)}&directory={quote(directory or path)}",
                           {"content": content or ""})
            await self._send({"type": "fs_write", "rid": rid, "path": path, "ok": True})
        except Exception as e:
            await self._send({"type": "error", "delta": f"写入失败：{e}", "rid": rid})

    async def _fs_rename(self, old_path, new_name, rid):
        """重命名文件：POST /file with body {path, new_name}"""
        old_path = (old_path or "").strip()
        new_name = (new_name or "").strip()
        if not old_path or not new_name:
            await self._send({"type": "error", "delta": "路径或新名称为空", "rid": rid})
            return
        directory = self.workspace or old_path.rsplit("/", 1)[0] if "/" in old_path else ""
        new_path = new_name if not old_path.endswith("/") else old_path + new_name
        try:
            await _to_thread(self.oc.post, f"/file?path={quote(old_path)}&directory={quote(directory or old_path)}",
                           {"new_path": new_path})
            await self._send({"type": "fs_rename", "rid": rid, "old_path": old_path, "new_path": new_path, "ok": True})
        except Exception as e:
            await self._send({"type": "error", "delta": f"重命名失败：{e}", "rid": rid})

    async def _fs_delete(self, path, rid):
        """删除文件：DELETE /file?path=..."""
        path = (path or "").strip()
        if not path:
            await self._send({"type": "error", "delta": "路径为空", "rid": rid})
            return
        try:
            await _to_thread(self.oc.delete, f"/file?path={quote(path)}")
            await self._send({"type": "fs_delete", "rid": rid, "path": path, "ok": True})
        except Exception as e:
            await self._send({"type": "error", "delta": f"删除失败：{e}", "rid": rid})

    def _model_obj(self):
        """'providerID/modelID' 字符串转 opencode 期望的 model 对象（同步版，仅带前缀时可用）。

        当前无人直接调用（_resolve_model 是其异步超集，无前缀时还能查 provider
        清单消歧），保留给未来的同步场景。
        """
        k = self.current_model or ""
        if "/" in k:
            pid, mid = k.split("/", 1)
            return {"providerID": pid, "modelID": mid}
        return None

    async def _ensure_providers(self):
        """懒加载 provider 清单（用于解析无前缀的模型名）。"""
        if self._provider_cache is None:
            try:
                d = await _to_thread(self.oc.get, "/config/providers")
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
        """手机「发送」帧 → opencode prompt_async，桥内最核心的翻译路径。

        步骤：
        1. 附件分流（对齐 desktop/relay.go）：dataURL 图片 → opencode 原生多模态
           file part 直传；其他文件 → 落盘工作区 media/（_save_upload），把绝对
           路径附进 prompt 正文，让 agent 自己用工具去读；
        2. 无会话（或传入 sid 已被删）则先在当前工作区建会话，标题取消息前 40 字；
        3. 先回 user_message 帧做手机端回显（显示文本带 📎 附件名，prompt 不带）；
        4. 组 body：parts（text + 图片 file part）+ model（_resolve_model 解析），
           POST /session/:sid/prompt_async（按会话目录作用域）后立即返回——
           异步端点，后续产出全靠 /event SSE 流回来。
        """
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
                p = await _to_thread(_save_upload, self.workspace, name, d)
                if p:
                    paths.append(p)
                else:
                    log(f"附件落盘失败: {name}")
        # 无会话直接发（空项目开聊）：先在当前工作区建会话
        if not sid:
            try:
                title = text[:40] or (names[0][:40] if names else "新会话")
                new = await _to_thread(
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
        if not await _to_thread(self.oc.session_exists, sid):
            new = await _to_thread(self.oc.post, scoped("/session", self.workspace), {"title": text[:40]})
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
            await _to_thread(self.oc.post, scoped(f"/session/{sid}/prompt_async", self._dir_by_sid.get(sid) or self.workspace), body)
        except Exception as e:
            log(f"prompt_async 失败: {e}")
            await self._send({"type": "error", "delta": f"发送失败：{e}"})

    async def _handle_stop(self, frame):
        # 中止运行：POST /session/:id/abort；本地直接补发 run:finished 让手机端
        # 立即收尾，不等 SSE 的 session.idle 事件
        sid = frame.get("session")
        if sid:
            await _to_thread(self.oc.post, scoped(f"/session/{sid}/abort", self._dir_by_sid.get(sid)), None)
            await self._send({"type": "run:finished", "sessionId": sid})

    async def _handle_delete(self, sid):
        # 删除会话（opencode 侧进垃圾箱，可恢复）；顺手清掉运行标记
        if sid:
            await _to_thread(self.oc.delete, f"/session/{sid}")
            self.running_map.pop(sid, None)

    async def _handle_rename(self, sid, title):
        # 改标题：PATCH /session/:id，空标题直接忽略
        if sid and title and title.strip():
            await _to_thread(self.oc.patch, f"/session/{sid}", {"title": title.strip()})

    # ---- opencode 斜杠命令：列表下发 + 执行 ----
    async def _commands(self, rid):
        """GET /command：真实命令清单（.opencode/command 自定义、skill、mcp 来源）。"""
        try:
            data = await _to_thread(self.oc.get, scoped("/command", self.workspace))
        except Exception:
            data = []
        out = [{"name": c.get("name") or "", "description": (c.get("description") or "").strip(),
                "agent": c.get("agent") or "", "source": c.get("source") or "command"}
               for c in data or [] if isinstance(c, dict) and c.get("name")]
        return {"type": "commands", "rid": rid, "commands": out}

    async def _ensure_session(self, sid, title):
        """会话为空或已不存在时，在当前工作区新建一个；返回可用 sid。"""
        if sid and await _to_thread(self.oc.session_exists, sid):
            return sid
        new = await _to_thread(self.oc.post, scoped("/session", self.workspace), {"title": title})
        nid = (new or {}).get("id", "")
        if nid:
            self._dir_by_sid[nid] = self.workspace
            self.current = nid
            await self._send({"type": "sessions:changed"})
        return nid

    async def _handle_command(self, frame):
        """手机端 /xxx：内建命令映射到 opencode 专有端点，其余转发 POST /session/:id/command。

        内建映射（目录参数均按会话出生目录作用域，见 scoped()）：
        - undo    回滚最近一条消息及其文件改动（先查消息列表取末条，再 /revert）
        - redo    恢复被 undo 的改动（/unrevert）
        - compact 手动压缩上下文（/summarize，需先选好模型才能执行）
        - share   生成分享链接并作为 text 帧回显（/share）
        - unshare 取消分享（DELETE /share）
        - diff    本会话代码改动：优先 /session/:id/diff，旧版本无此端点则回退
                  全局 /vcs/diff；拼成 diff 帧下发（超 4000 字截断）
        - init    让 opencode 分析代码库生成 AGENTS.md（/init）
        其余命令（.opencode/command 自定义命令、skill、mcp 来源）原样转发给
        /session/:id/command，由 opencode 侧执行；执行前 _ensure_session 兜底建会话。
        """
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
                msgs = await _to_thread(self.oc.get, scoped(f"/session/{sid}/message", directory))
                mid = ((msgs or [{}])[-1].get("info") or {}).get("id") or ""
                if not mid:
                    await self._send({"type": "error", "delta": "没有可撤销的消息"})
                    return
                await _to_thread(self.oc.post, scoped(f"/session/{sid}/revert", directory),
                                        {"messageID": mid})
            elif cmd == "redo":     # 恢复被 undo 的消息
                await _to_thread(self.oc.post, scoped(f"/session/{sid}/unrevert", directory), {})
            elif cmd == "compact":  # 手动压缩上下文（summarize）
                m = await self._resolve_model()
                if not m:
                    await self._send({"type": "error", "delta": "compact 需要先选模型（providerID/modelID）"})
                    return
                await _to_thread(self.oc.post, scoped(f"/session/{sid}/summarize", directory),
                                        {"providerID": m["providerID"], "modelID": m["modelID"]})
            elif cmd == "share":
                s = await _to_thread(self.oc.post, scoped(f"/session/{sid}/share", directory), {})
                sh = (s or {}).get("share") if isinstance(s, dict) else None
                link = (sh or {}).get("url") or (s or {}).get("url") or ""
                await self._send({"type": "text",
                                  "delta": f"🔗 分享链接：{link}" if link else "已分享（未返回链接）"})
                return
            elif cmd == "unshare":
                await _to_thread(self.oc.delete, scoped(f"/session/{sid}/share", directory))
            elif cmd == "diff":     # 本次会话的代码改动（专用端点，回退 vcs 全局 diff）
                try:
                    d = await _to_thread(
                        self.oc.get, scoped(f"/session/{sid}/diff", directory))
                except Exception:
                    d = await _to_thread(self.oc.get, scoped("/vcs/diff", directory))
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
                await _to_thread(self.oc.post, scoped(f"/session/{sid}/init", directory), {})
            else:                   # 自定义命令（.opencode/command、skill、mcp）
                await _to_thread(self.oc.post, scoped(f"/session/{sid}/command", directory),
                                        {"command": cmd, "arguments": args})
        except Exception as e:
            await self._send({"type": "error", "delta": f"/{cmd} 失败：{e}"})
            return
        await self._send({"type": "command_result", "session": sid, "command": cmd, "ok": True})

    # ---- 手机页需要的几类应答帧 ----
    async def _opencode_version(self):
        """opencode 版本号（/global/health 的 version 字段），10 分钟缓存；失败返回空串。
        手机页在标题旁以 v1.18.x 角标展示。"""
        if self._ver and time.time() - self._ver_at < 600:
            return self._ver
        try:
            d = await _to_thread(self.oc.get, "/global/health")
            self._ver = (d or {}).get("version") or ""
        except Exception:
            self._ver = ""
        self._ver_at = time.time()
        return self._ver

    def _git_branch(self):
        # 当前工作区的 git 分支名（同步阻塞，只在 _state 组帧时调一次；
        # 非 git 目录/无 git 命令返回空串，手机端就不显示分支角标）
        import subprocess
        try:
            out = subprocess.check_output(["git", "-C", self.workspace, "rev-parse", "--abbrev-ref", "HEAD"],
                                          stderr=subprocess.DEVNULL)
            return out.decode().strip()
        except Exception:
            return ""

    async def _state(self, rid):
        """全量状态快照 → state 帧（手机页连上 hello 后先要一次，也是各操作后的刷新手段）。

        顺带用会话自带的 directory 刷新 _dir_by_sid：桌面端建的会话桥不知道出生
        目录，靠这里补齐，保证后续该会话级调用都带对 directory。
        """
        try:
            sessions = await _to_thread(self.oc.get, "/session")
        except Exception:
            sessions = []
        self._dir_by_sid = {s.get("id"): (s.get("directory") or s.get("path") or "") for s in sessions or []}
        return {
            "type": "state", "rid": rid,
            "workspace": self.workspace, "mode": self.mode, "current": self.current_model,
            "current_session": self.current, "branch": self._git_branch(), "compact": {},
            "version": await self._opencode_version(),
            "sessions": oc_sessions_to_phone(sessions, self.running_map, {}),
        }

    async def _messages(self, sid, rid):
        """拉取会话历史 → messages 帧（手机端进会话时渲染聊天记录）。

        映射规则（尽量保留原貌、压制噪音）：
        - 只取 user/assistant 两种角色，其余跳过；
        - text part 拼接为正文；data:image 的 file/image part 收进 images 数组；
        - tool part 手机页没有对应槽位，折成一段「> 🔧 工具名: 输出前300字」
          引用文本（dict 型输出先取 content/output 字段）；
        - 纯空消息（无文本无图）丢弃。
        """
        if not sid:
            return {"type": "messages", "rid": rid, "id": sid, "messages": []}
        try:
            data = await _to_thread(self.oc.get, scoped(f"/session/{sid}/message", self._dir_by_sid.get(sid)))
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
        """provider/模型清单 → models 帧（手机端模型选择器数据源）。

        - key 用 'providerID/modelID' 复合键，与桥侧 current_model、手机端
          切模型的 model 帧保持同一格式；
        - is_current：用户切过模型就以 current_model 为准；没切过则跟随
          opencode 各 provider 的默认模型（default[providerID] → modelID）；
        - vision/reasoning 从 capabilities 推导，手机端用来打能力角标。
        """
        try:
            d = await _to_thread(self.oc.get, "/config/providers")
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
        """入口：记下事件循环、拉起 SSE 守护线程，然后 gather 两条主协程。

        _relay_loop 与 _event_pump 都是无限循环；任一真正崩溃会上抛让进程退出
        （交给 systemd/supervisor 拉起），不做内部静默重生。
        """
        self.loop = asyncio.get_running_loop()
        self.wlock = asyncio.Lock()
        self.evq = asyncio.Queue()
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
# 配置：内置默认 ← json 文件 ← 命令行参数，三层覆盖
# ---------------------------------------------------------------------------
def load_config(path, overrides):
    """合成最终配置。

    优先级：代码内置默认 < --config 指向的 json 文件 < 命令行同名参数。
    json 允许只写部分段：dict 值按键合并进默认段（局部覆盖），非 dict 值
    整体替换；overrides 里值为 None 表示命令行没传该参数，跳过不覆盖。
    """
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
    """CLI 入口：解析参数 → 合成配置 → 校验必填项（中继地址 + device token，
    缺一项即退出码 1）→ 构造 Bridge 常驻运行，Ctrl+C 优雅退出。"""
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
