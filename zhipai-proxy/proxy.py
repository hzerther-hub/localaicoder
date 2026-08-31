#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
智排路由代理：把「简单→本地 / 复杂→flash / 图片→vision」这套智能路由，
封装成一个 OpenAI 兼容的 /v1/chat/completions 端点。

用法：让 Codex 把 base_url 指到本代理，本代理按任务类别动态转发到不同上游模型。
对 Codex 完全无感知；只需改一句话 base_url。

只依赖：requests（标准库提供服务器）。
  pip install requests

启动：
  python3 proxy.py --config config.json          # 或环境变量 ZHIPAI_CONFIG=config.json
  python3 proxy.py --host 127.0.0.1 --port 9000

上游槽位（三个）：local / flash / vision。
判定顺序：图片→vision；简单→local；复杂→flash；拿不准→用 local 仲裁一句「简单/复杂」。
"""

import argparse
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

import requests

# ---------------- 配置 ----------------

DEFAULT_CONFIG = {
    # 本地模型（简单通道 + 仲裁大脑）；本地通常用占位 api_key
    "local": {"endpoint": "http://127.0.0.1:8099/v1", "api_key": "local-noauth", "model": "ornith-1.5-35b"},
    # 文本复杂通道（云端 DeepSeek）
    "flash": {"endpoint": "https://api.deepseek.com/v1", "api_key": "", "model": "deepseek-v4-flash"},
    # 图片 / 多模态通道（云端 DeepSeek）
    "vision": {"endpoint": "https://api.deepseek.com/v1", "api_key": "", "model": "deepseek-v4-flash-vision-exp"},
    # 分类阈值
    "max_chars": 160,
    "max_words": 28,
    # 拿不准时是否让本地模型仲裁
    "arbitrate": True,
    # 本地仲裁超时（秒）
    "arbitrate_timeout": 8,
}

# 强信号关键词：英文用词边界正则，中文直接包含匹配
STRONG_EN = re_PAT = None  # 下面用编译后的正则赋值，避免模块顶层 import re 报错
import re
STRONG_EN = re.compile(
    r"\b(plan|design|architect|refactor|debug|investigate|analyze|analysis|"
    r"optimi[sz]e|implement|security|performance|deadlock|race|memory leak|"
    r"root cause|troubleshoot|migrate|audit|step by step|how to)\b", re.I)
STRONG_ZH = ("规划", "设计", "架构", "重构", "调试", "排查", "分析", "根因", "为什么",
             "如何实现", "怎么实现", "怎么解决", "优化", "性能", "安全", "漏洞",
             "死锁", "并发", "迁移", "评估", "审计", "逐步", "实现一个", "完整实现")

ARBITRATE_PROMPT = (
    "你是路由器。判断用户请求属于哪类：简单（一句话直接能答、或继续刚才的操作）"
    "还是复杂（需要写代码、多步骤处理或深度思考）。只回答一个词：简单 或 复杂。")


def contains_han(s: str) -> bool:
    return any("一" <= ch <= "鿿" for ch in s)


def paragraphs(s: str) -> int:
    sep = "\n" if contains_han(s) else "\n\n"  # 中文常单换行分段；英文用空行
    return len([p for p in s.split(sep) if p.strip()])


# ---------------- 分类 ----------------

def classify(text: str, has_image: bool = False,
             max_chars: int = 160, max_words: int = 28) -> str:
    """判定序：附件→空文本→代码块→强关键词→多段→长度→弱信号区。
    返回 simple / strong / vision / unsure。"""
    text = text.strip()
    if has_image:
        return "vision"                      # 1) 图片/多模态 → vision 通道
    if not text:
        return "simple"                      # 2) 空文本：工具链续步，走便宜侧
    if "```" in text:
        return "strong"                      # 3) 代码块：任务型
    if STRONG_EN.search(text) or any(kw in text for kw in STRONG_ZH):
        return "strong"                      # 4) 强关键词（优先级高于长度）
    if paragraphs(text) >= 2:
        return "strong"                      # 5) 多段落
    char_len, words = len(text), len(text.split())
    if contains_han(text):                   # 6) 中文无空格分词：词数×2 折算汉字阈值
        zh_limit = max_words * 2
        if char_len > zh_limit:
            return "strong"
        if char_len >= zh_limit * 3 / 4:
            return "unsure"
        return "simple"
    if char_len > max_chars or words > max_words:
        return "strong"
    if char_len >= max_chars * 3 / 4 or words >= max_words * 3 / 4:
        return "unsure"                      # 7) 弱信号区
    return "simple"


# ---------------- 路由决策 ----------------

def _slot_to_tuple(slot):
    s = slot or {}
    return (s.get("endpoint", ""), s.get("api_key", ""), s.get("model", ""))


class SmartRouter:
    """级联路由器：把 decision 映射到目标槽位，负责本地仲裁。"""

    def __init__(self, cfg: dict):
        self.cfg = cfg
        self.slots = {k: _slot_to_tuple(cfg.get(k)) for k in ("local", "flash", "vision")}
        self.max_chars = int(cfg.get("max_chars", 160))
        self.max_words = int(cfg.get("max_words", 28))
        self.arbitrate = bool(cfg.get("arbitrate", True))
        self.arb_timeout = float(cfg.get("arbitrate_timeout", 8))

    def _chat(self, slot: tuple, system: str, user: str, timeout: float) -> str:
        """对某槽位做一次轻量非流式调用（用于仲裁）。失败返回空串。"""
        endpoint, key, model = slot
        if not endpoint or not model:
            return ""
        try:
            r = requests.post(
                f"{endpoint.rstrip('/')}/chat/completions",
                headers={"Authorization": f"Bearer {key}"},
                json={"model": model, "temperature": 0, "max_tokens": 512,
                      "stream": False,
                      "messages": [{"role": "system", "content": system},
                                   {"role": "user", "content": user}]},
                timeout=timeout)
            r.raise_for_status()
            return r.json().get("choices", [{}])[0].get("message", {}).get("content", "") or ""
        except Exception:
            return ""

    def _arbitrate(self, text: str) -> str:
        """用本地模型判一句「简单/复杂」。任何失败都归 simple（宁便宜，不打断）。"""
        res = self._chat(self.slots["local"], ARBITRATE_PROMPT, text, self.arb_timeout)
        return "strong" if "复杂" in res else "simple"

    def resolve(self, text: str, has_image: bool) -> tuple[str, str]:
        """返回 (目标槽位名, 判定标签)。目标可能被本地仲裁覆盖。"""
        d = classify(text, has_image, self.max_chars, self.max_words)
        if d == "vision":
            return "vision", d
        if d == "strong":
            return "flash", d
        if d == "unsure":
            if self.arbitrate:
                d2 = self._arbitrate(text)
                return ("flash" if d2 == "strong" else "local"), "unsure->" + d2
            return "local", "unsure"
        return "local", "simple"


# ---------------- 请求解析 ----------------

def has_image_content(body: dict) -> bool:
    """判断请求里是否带图片/多模态内容。兼容 OpenAI 以 list-of-parts 表达 content。"""
    for m in body.get("messages", []):
        content = m.get("content")
        if isinstance(content, list):
            for part in content:
                if isinstance(part, dict):
                    t = part.get("type", "")
                    if t in ("image_url", "image", "input_image") or "image_url" in part:
                        return True
                elif isinstance(part, str) and part.startswith("data:image/"):
                    return True
        elif isinstance(content, str) and content.startswith("data:image/"):
            return True
    return False


def extract_user_text(body: dict) -> str:
    """取最后一个 user 消息的纯文本（拼接 text 部分）。"""
    texts = []
    for m in body.get("messages", []):
        if m.get("role") != "user":
            continue
        c = m.get("content")
        if isinstance(c, str):
            texts.append(c)
        elif isinstance(c, list):
            texts.append(" ".join(p.get("text", "") for p in c if isinstance(p, dict) and p.get("text")))
    return "\n".join(texts)


# ---------------- 上游调用 ----------------

def _endpoint_chat(endpoint: str) -> str:
    return f"{endpoint.rstrip('/')}/chat/completions"


def forward(body: dict, target: tuple) -> requests.Response:
    """把请求转发到目标槽位（覆盖 model、保持 stream）；返回 requests.Response。"""
    endpoint, key, model = target
    payload = dict(body)
    payload["model"] = model
    # 上游不认 stream 之外的自定义字段也别传；这里保持原样，只改 model。
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
    return requests.post(_endpoint_chat(endpoint), headers=headers,
                         json=payload, stream=bool(payload.get("stream")), timeout=300)


# ---------------- HTTP 服务 ----------------

class Handler(BaseHTTPRequestHandler):
    router: SmartRouter = None  # 由 main 注入

    def log_message(self, fmt, *args):
        sys.stderr.write("[proxy] %s\n" % (fmt % args))

    def _send_json(self, code: int, obj: dict):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _sse_headers(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("X-Accel-Buffering", "no")
        self.send_header("Connection", "keep-alive")
        self.end_headers()

    def do_GET(self):
        # 让 Codex 看到可用模型
        if urlparse(self.path).path.rstrip("/") == "/v1/models":
            names = []
            for k in ("local", "flash", "vision"):
                endpoint, _, model = self.router.slots[k]
                if endpoint and model:
                    names.append({"id": model, "object": "model", "owned_by": k})
            self._send_json(200, {"object": "list", "data": names})
        else:
            self._send_json(404, {"error": {"message": "not found", "type": "not_found_error"}})

    def do_POST(self):
        path = urlparse(self.path).path.rstrip("/")
        if path != "/v1/chat/completions":
            return self._send_json(404, {"error": {"message": "not found", "type": "not_found_error"}})

        try:
            length = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(length) or b"{}")
        except Exception as e:
            return self._send_json(400, {"error": {"message": f"请求体解析失败: {e}", "type": "invalid_request_error"}})

        has_image = has_image_content(body)
        text = extract_user_text(body)
        try:
            slot_name, _label = self.router.resolve(text, has_image)
        except Exception as e:
            return self._send_json(500, {"error": {"message": f"路由失败: {e}", "type": "internal_error"}})

        target = self.router.slots[slot_name]
        sys.stderr.write(f"[route] {_label} -> {slot_name} model={target[2]}\n")

        try:
            resp = forward(body, target)
        except Exception as e:
            return self._send_json(502, {"error": {"message": f"上游 {slot_name} 请求失败: {e}", "type": "upstream_error"}})

        if resp.status_code != 200:
            self._send_json(resp.status_code, {"error": {"message": f"上游 {slot_name} 返回 {resp.status_code}",
                                                        "type": "upstream_error", "detail": resp.text[:500]}})
            return

        streaming = bool(body.get("stream"))
        if streaming:
            self._sse_headers()
            try:
                for chunk in resp.iter_lines(decode_unicode=False):
                    if not chunk:
                        continue
                    self.wfile.write(chunk + b"\n")
                    self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        else:
            # 非流式：改回 model 字段为真实上游模型，方便 Codex 显示
            try:
                j = resp.json()
                j["model"] = target[2]
            except Exception:
                j = resp.text
            data = j if isinstance(j, bytes) else json.dumps(j).encode()
            self.send_response(resp.status_code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)


# ---------------- 启动 ----------------

def load_config(path: str) -> dict:
    cfg = json.loads(json.dumps(DEFAULT_CONFIG))  # 深拷贝默认
    if path and os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            user = json.load(f)
        for k in ("local", "flash", "vision"):
            if isinstance(user.get(k), dict):
                cfg[k].update(user[k])
        for k in ("max_chars", "max_words", "arbitrate", "arbitrate_timeout"):
            if k in user:
                cfg[k] = user[k]
    # 环境变量覆盖（可选）
    env_map = {
        "LOCAL_ENDPOINT": ("local", "endpoint"), "LOCAL_API_KEY": ("local", "api_key"), "LOCAL_MODEL": ("local", "model"),
        "FLASH_ENDPOINT": ("flash", "endpoint"), "FLASH_API_KEY": ("flash", "api_key"), "FLASH_MODEL": ("flash", "model"),
        "VISION_ENDPOINT": ("vision", "endpoint"), "VISION_API_KEY": ("vision", "api_key"), "VISION_MODEL": ("vision", "model"),
    }
    for env, (slot, key) in env_map.items():
        if os.environ.get(env):
            cfg[slot][key] = os.environ[env]
    return cfg


def main():
    p = argparse.ArgumentParser(description="智排路由代理（OpenAI 兼容）")
    p.add_argument("--config", default=os.environ.get("ZHIPAI_CONFIG", "config.json"))
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=9000)
    args = p.parse_args()

    cfg = load_config(args.config)
    missing = [s for s in ("local", "flash", "vision")
               if not cfg[s].get("endpoint") or not cfg[s].get("model")]
    if missing and not all(cfg[s].get("endpoint") or cfg[s].get("model") for s in missing):
        print("⚠️ 以下槽位缺少 endpoint 或 model（非流式/仲裁可能失败）:", missing, file=sys.stderr)

    Handler.router = SmartRouter(cfg)
    srv = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"智排代理已启动  http://{args.host}:{args.port}/v1  "
          f"（local={cfg['local']['model']} | flash={cfg['flash']['model']} | vision={cfg['vision']['model']}）")
    print("让 Codex 把 base_url 指到: http://%s:%d/v1" % (args.host, args.port))
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        srv.shutdown()


if __name__ == "__main__":
    main()
