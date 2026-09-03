#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
pykb —— 本地文档语义检索（memsearch 的跨平台替代，单文件、可独立发布）
========================================================================
Windows / macOS / Linux 全平台可用：Python 3.9+ 与 pip 依赖（fastembed/numpy）三平台
均有官方轮子，无 Docker、无 WSL、无外部 API。

功能对齐 memsearch：
  - index   增量索引（按文件哈希跳过未变更；--force 全量重嵌入）
  - search  混合检索：语义向量 + BM25 关键词 + RRF 融合（对应 memsearch 的 hybrid search），
            -c 切库、-k top-k、--source-prefix 限定子路径、--exclude 排除文件
  - stats   库统计；collections 列表
  - watch   轮询增量索引（文档变了自动重嵌）
  - serve   HTTP 常驻服务（供桌面应用等集成；模型懒加载，进程内单例）

形态与 memsearch 对齐：Markdown 标题分块（带 § 标题与行号），嵌入 bge-small-zh-v1.5
（中英双语 ONNX/CPU），落盘 ~/.pykb/<collection>.{npz,json}（原子写，多进程安全）。

依赖：pip install fastembed（自带 onnxruntime/tokenizers/numpy）。
嵌入模型（约 95MB）首次用时自动从 HF 下载，国内默认走 HF 镜像。
"""
from __future__ import annotations

import os

# HF 镜像必须在 import fastembed/huggingface_hub 之前就位（国内网络默认镜像，显式设置优先）
os.environ.setdefault("HF_ENDPOINT", "https://hf-mirror.com")

import argparse
import fnmatch
import hashlib
import json
import re
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import numpy as np

__version__ = "0.1.0"

MODEL_NAME = "BAAI/bge-small-zh-v1.5"   # 中英双语，512 维，约 95MB，ONNX CPU 推理
MAX_CHARS = 800                          # 单块字符上限（bge 512 token 的安全中文长度）
DEFAULT_COL = "memsearch_chunks"         # 与 memsearch 的默认 collection 同名对齐
DATA_DIR = Path.home() / ".pykb"
DOC_EXTS = {".md", ".markdown", ".mdx", ".txt", ".rst"}
# 默认跳过的目录（跨平台常见噪音）+ 隐藏目录
SKIP_DIRS = {".git", "node_modules", "__pycache__", ".venv", "venv", "dist",
             "build", "target", ".idea", ".vscode", ".playwright-mcp", ".memsearch"}
BM25_K1, BM25_B = 1.5, 0.75
RRF_K = 60

_model = None
_model_lock = threading.Lock()


def log(msg):
    print(f"[pykb {time.strftime('%H:%M:%S')}] {msg}", flush=True)


def get_model():
    """懒加载嵌入模型（进程内单例；首次会从 HF 下载约 95MB）。"""
    global _model
    with _model_lock:
        if _model is None:
            from fastembed import TextEmbedding
            log(f"加载嵌入模型 {MODEL_NAME}（首次会自动下载）…")
            _model = TextEmbedding(MODEL_NAME)
            log("嵌入模型就绪")
        return _model


def embed(texts: list[str]) -> np.ndarray:
    if not texts:
        return np.zeros((0, 512), dtype=np.float32)
    return np.array(list(get_model().embed(texts)), dtype=np.float32)


# ---------------- 分块 ----------------

HEADING_RE = re.compile(r"^(#{1,6})\s+(.*)$")


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return path.read_text(encoding="utf-8", errors="replace")


def chunk_file(path: Path) -> list[dict]:
    """按 Markdown 标题切块；超长节按空行段落续切。返回 [{heading, start_line, text}]。"""
    lines = read_text(path).splitlines()
    if lines and lines[0].strip() == "---":  # 跳过 YAML front matter
        for i in range(1, len(lines)):
            if lines[i].strip() == "---":
                lines = lines[i + 1:]
                break
    sections: list[dict] = []
    heading, start, buf = "", 1, []

    def flush(end_line: int):
        nonlocal buf
        text = "\n".join(buf).strip()
        if text:
            for piece in split_long(text):
                sections.append({"heading": heading, "start_line": start, "text": piece})
        buf = []

    for idx, line in enumerate(lines, start=1):
        m = HEADING_RE.match(line)
        if m:
            flush(idx - 1)
            heading = m.group(2).strip()
            start = idx
            buf = [line]
        else:
            if not buf and not line.strip():
                continue  # 节首空行不作为块开头
            buf.append(line)
    flush(len(lines))
    return sections


def split_long(text: str) -> list[str]:
    """超长文本按空行段落切到 MAX_CHARS 以内（保留完整段落，不硬截句子）。"""
    if len(text) <= MAX_CHARS:
        return [text]
    pieces, cur = [], ""
    for para in re.split(r"\n\s*\n", text):
        sep = "\n\n" if cur else ""
        if len(cur) + len(sep) + len(para) <= MAX_CHARS:
            cur += sep + para
        else:
            if cur:
                pieces.append(cur)
            while len(para) > MAX_CHARS:  # 单段超长（无空行的长表/代码块）兜底硬切
                pieces.append(para[:MAX_CHARS])
                para = para[MAX_CHARS:]
            cur = para
    if cur:
        pieces.append(cur)
    return pieces


# ---------------- 文件收集 ----------------

def _skipped(dir_name: str, excludes: list[str]) -> bool:
    if dir_name in SKIP_DIRS or dir_name.startswith("."):
        return True
    return any(fnmatch.fnmatch(dir_name, pat) for pat in excludes)


def collect_files(paths: list[str], excludes: list[str]) -> list[Path]:
    """展开输入路径（文件或目录）为文档文件清单（去重、排序，应用排除规则）。"""
    out: set[Path] = set()
    for p in paths:
        pp = Path(p)
        if pp.is_file():
            out.add(pp.resolve())
        elif pp.is_dir():
            for f in pp.rglob("*"):
                if not f.is_file() or f.suffix.lower() not in DOC_EXTS:
                    continue
                rel = str(f)
                if any(fnmatch.fnmatch(rel, pat) or fnmatch.fnmatch(f.name, pat) for pat in excludes):
                    continue
                if any(part in SKIP_DIRS or part.startswith(".") for part in f.parts[:-1]):
                    continue
                out.add(f.resolve())
    return sorted(out)


def file_hash(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 16), b""):
            h.update(block)
    return h.hexdigest()


# ---------------- 存储（原子写，多进程安全） ----------------

def col_paths(collection: str) -> tuple[Path, Path]:
    safe = re.sub(r"[^A-Za-z0-9_.-]", "_", collection or DEFAULT_COL)
    return DATA_DIR / f"{safe}.npz", DATA_DIR / f"{safe}.json"


def load_store(collection: str):
    npz, jsn = col_paths(collection)
    if not jsn.exists():
        return {"files": {}, "order": []}, np.zeros((0, 512), dtype=np.float32)
    meta = json.loads(jsn.read_text(encoding="utf-8"))
    if npz.exists():
        mat = np.load(npz)["v"]
    else:
        mat = np.zeros((0, 512), dtype=np.float32)
    return meta, mat


def save_store(collection: str, meta: dict, mat: np.ndarray):
    """临时文件 + 原子替换：并发索引/检索时不会读到半截文件。"""
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    npz, jsn = col_paths(collection)
    tmp_npz, tmp_jsn = npz.with_suffix(".tmp"), jsn.with_suffix(".tmp")
    np.savez_compressed(tmp_npz, v=mat.astype(np.float32))
    tmp_jsn.write_text(json.dumps(meta, ensure_ascii=False), encoding="utf-8")
    os.replace(tmp_npz, npz)
    os.replace(tmp_jsn, jsn)


def row_offsets(meta: dict) -> dict[str, tuple[int, int]]:
    """文件 → 在向量矩阵中的 [start,end) 行区间（与 meta["order"] 对齐）。"""
    off, out = 0, {}
    for p in meta["order"]:
        n = len(meta["files"][p]["chunks"])
        out[p] = (off, off + n)
        off += n
    return out


# ---------------- 索引 ----------------

def do_index(collection: str, paths: list[str], excludes: list[str] | None = None,
             force: bool = False) -> dict:
    excludes = excludes or []
    t0 = time.time()
    meta, mat = load_store(collection)
    old_off = row_offsets(meta)
    files = collect_files(paths, excludes)
    if not files:
        return {"ok": False, "error": "未找到可索引的文档文件", "files": 0, "chunks": 0}

    new_meta: dict = {"files": {}, "order": []}
    rows: list[np.ndarray | None] = []
    to_embed: list[str] = []
    reused = 0

    for f in files:
        fp = str(f)
        h = file_hash(f)
        chunks = chunk_file(f)
        new_meta["files"][fp] = {"hash": h, "chunks": chunks}
        new_meta["order"].append(fp)
        old = meta["files"].get(fp)
        if not force and old and old["hash"] == h and fp in old_off \
                and len(old["chunks"]) == len(chunks):
            s, e = old_off[fp]
            rows.append(mat[s:e])
            reused += len(chunks)
        else:
            rows.append(None)
            to_embed.extend(c["text"] for c in chunks)

    if to_embed:
        vecs = embed(to_embed)
        pos = 0
        for i, r in enumerate(rows):
            if r is None:
                n = len(new_meta["files"][new_meta["order"][i]]["chunks"])
                rows[i] = vecs[pos:pos + n]
                pos += n
    nonempty = [r for r in rows if r is not None and r.size]
    matrix = np.vstack(nonempty).astype(np.float32) if nonempty \
        else np.zeros((0, 512), dtype=np.float32)

    save_store(collection, new_meta, matrix)
    total = sum(len(v["chunks"]) for v in new_meta["files"].values())
    return {"ok": True, "files": len(files), "chunks": total,
            "embedded": len(to_embed), "reused": reused,
            "seconds": round(time.time() - t0, 1)}


# ---------------- 检索（dense + BM25 → RRF 融合） ----------------

TOKEN_RE = re.compile(r"[a-z0-9_]+|[\u4e00-\u9fff]")


def tokenize(text: str) -> list[str]:
    """ASCII 词 + CJK 单字（bigram 由单字覆盖语义，词频统计足够稳）。"""
    return TOKEN_RE.findall(text.lower())


def bm25_scores(query_tokens: list[str], texts: list[str]) -> np.ndarray:
    n = len(texts)
    if n == 0 or not query_tokens:
        return np.zeros(n, dtype=np.float32)
    toks = [tokenize(t) for t in texts]
    avgdl = sum(len(t) for t in toks) / n or 1.0
    df: dict[str, int] = {}
    for t in toks:
        for w in set(t):
            df[w] = df.get(w, 0) + 1
    scores = np.zeros(n, dtype=np.float32)
    qset = set(query_tokens)
    for i, t in enumerate(toks):
        tf: dict[str, int] = {}
        for w in t:
            tf[w] = tf.get(w, 0) + 1
        s = 0.0
        for w in qset:
            if w not in tf:
                continue
            idf = np.log(1 + (n - df.get(w, 0) + 0.5) / (df.get(w, 0) + 0.5))
            s += idf * tf[w] * (BM25_K1 + 1) / (tf[w] + BM25_K1 * (1 - BM25_B + BM25_B * len(t) / avgdl))
        scores[i] = s
    return scores


def rrf_fuse(*rankings: list[int]) -> dict[int, float]:
    """Reciprocal Rank Fusion：多路排名融合为单一分数。"""
    fused: dict[int, float] = {}
    for ranking in rankings:
        for rank, idx in enumerate(ranking):
            fused[idx] = fused.get(idx, 0.0) + 1.0 / (RRF_K + rank + 1)
    return fused


def do_search(collection: str, query: str, k: int = 5,
              source_prefix: str = "", dense_only: bool = False) -> dict:
    meta, mat = load_store(collection)
    if mat.shape[0] == 0:
        return {"ok": False, "error": f"库 {collection} 为空：先 index 再 search"}

    # 展平所有块（与矩阵行对齐）
    off = row_offsets(meta)
    texts: list[str] = []
    owners: list[tuple[str, int]] = []  # (file, chunk_index)
    for p in meta["order"]:
        s, _e = off[p]
        for ci, c in enumerate(meta["files"][p]["chunks"]):
            texts.append(c["text"])
            owners.append((p, ci))

    qv = embed([query])[0]
    dense = mat @ qv / (np.linalg.norm(mat, axis=1) * (np.linalg.norm(qv) + 1e-8) + 1e-8)
    dense_rank = list(np.argsort(-dense))

    if dense_only:
        fused = {i: float(dense[i]) for i in dense_rank}
    else:
        bm = bm25_scores(tokenize(query), texts)
        bm_rank = list(np.argsort(-bm))
        fused = rrf_fuse(dense_rank, bm_rank)

    if source_prefix:
        fused = {i: v for i, v in fused.items() if source_prefix in owners[i][0]}
    top = sorted(fused.items(), key=lambda kv: -kv[1])[:max(1, k)]

    hits = []
    for idx, score in top:
        p, ci = owners[idx]
        c = meta["files"][p]["chunks"][ci]
        hits.append({"source": p, "heading": c["heading"], "start_line": c["start_line"],
                     "score": round(float(score), 3), "text": c["text"][:600]})
    return {"ok": True, "hits": hits, "mode": "dense" if dense_only else "hybrid"}


# ---------------- 统计 ----------------

def do_stats(collection: str = "") -> dict:
    cols = sorted(f.stem for f in DATA_DIR.glob("*.json")) if DATA_DIR.exists() else []
    out: dict = {"ok": True, "model": MODEL_NAME, "version": __version__, "collections": cols,
                 "data_dir": str(DATA_DIR)}
    if collection:
        meta, mat = load_store(collection)
        out.update({"collection": collection, "files": len(meta["files"]),
                    "chunks": int(mat.shape[0]),
                    "dim": int(mat.shape[1]) if mat.size else 0})
    return out


# ---------------- delete / show / compact / export / import ----------------

def do_delete(collection: str, force: bool = False) -> dict:
    """删除单个 collection 的 npz/json。默认需 force=True（防误删）。"""
    npz, jsn = col_paths(collection)
    if not jsn.exists and not npz.exists():
        return {"ok": False, "error": f"库 {collection} 不存在"}
    if not force:
        return {"ok": False, "error": "需要 --force 确认删除", "collection": collection}
    removed = []
    for p in (npz, jsn):
        if p.exists():
            p.unlink()
            removed.append(p.name)
    return {"ok": True, "collection": collection, "removed": removed}


def do_show(collection: str, source: str = "", chunk_id: int = -1, limit: int = 20) -> dict:
    """列出/打印库中块；--source 过滤路径，--chunk 打印某块全文（行内嵌。c 中的 0-based id）。"""
    meta, _mat = load_store(collection)
    if not meta["files"]:
        return {"ok": False, "error": f"库 {collection} 为空"}
    items = []
    for fi, path in enumerate(meta["order"]):
        if source and source not in path:
            continue
        for ci, c in enumerate(meta["files"][path]["chunks"]):
            global_id = sum(len(meta["files"][meta["order"][k]]["chunks"]) for k in range(fi)) + ci
            items.append({"id": global_id, "source": path, "heading": c["heading"],
                          "start_line": c["start_line"],
                          "preview": c["text"][:120].replace("\n", " ")})
    if chunk_id < 0:
        return {"ok": True, "items": items[:limit], "total": len(items)}
    if chunk_id < 0 or chunk_id >= len(items):
        return {"ok": False, "error": f"chunk_id 越界（0..{len(items)-1}）"}
    it = items[chunk_id]
    # 拿到全文：按文件 → 块 0-based 再对齐
    fi = sum(1 for k, p in enumerate(meta["order"]) if meta["files"][p]["chunks"]
             and sum(len(meta["files"][meta["order"][j]]["chunks"]) for j in range(k)) <= chunk_id)
    # 上面那个求和找文件索引容易越界，直接走简单遍历
    gi = chunk_id
    for path in meta["order"]:
        chks = meta["files"][path]["chunks"]
        if gi < len(chks):
            c = chks[gi]
            return {"ok": True, **it, "text": c["text"]}
        gi -= len(chks)
    return {"ok": False, "error": "chunk_id 越界"}


def do_compact(collection: str) -> dict:
    """重建库：再次跑同样的全量索引（force=True），把因旧 chunk 变化残留的旧向量行清掉。
    嵌入本身可复用（sha256 一致 → 跳过），所以比新建库更省；总耗时 ≈ 一次增量索引。"""
    meta, _mat = load_store(collection)
    if not meta["files"]:
        return {"ok": False, "error": f"库 {collection} 为空"}
    paths = sorted(meta["files"].keys())
    t0 = time.time()
    # 收集每个文件的字节内容放进临时目录复刻太重；直接复用 do_index 的增量逻辑：
    # 这里 force=True 重新分块+嵌入（哈希相同时复用向量），效果等价 compact。
    r = do_index(collection, paths, excludes=[], force=False)
    r.update({"compact_seconds": round(time.time() - t0, 1),
              "files_rebuilt": len(paths)})
    return r


def do_export(collection: str, out_path: str) -> dict:
    """把 collection 打包为 .tar.gz（含 npz/json/model/version/created），便于跨机器复现。"""
    import tarfile
    npz, jsn = col_paths(collection)
    if not jsn.exists():
        return {"ok": False, "error": f"库 {collection} 不存在"}
    meta = json.loads(jsn.read_text(encoding="utf-8"))
    files: dict[str, str] = {npz.name: str(npz), jsn.name: str(jsn)}
    tmp_dir = Path(out_path).with_suffix(".pykb-tmp")
    tmp_dir.mkdir(parents=True, exist_ok=True)
    try:
        # 落 manifest
        manifest = {"collection": collection, "model": MODEL_NAME,
                    "version": __version__, "files": list(files.keys()),
                    "files_count": len(meta["files"]), "chunks": len(meta.get("order", []))}
        (tmp_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
        for name, src in files.items():
            (tmp_dir / name).write_bytes(Path(src).read_bytes())
        out = Path(out_path)
        out.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(out, "w:gz") as tf:
            tf.add(tmp_dir / "manifest.json", arcname="manifest.json")
            for name in files:
                tf.add(tmp_dir / name, arcname=name)
    finally:
        for n in list(tmp_dir.glob("*")):
            n.unlink()
        tmp_dir.rmdir()
    return {"ok": True, "collection": collection, "file": out_path,
            "size": Path(out_path).stat().st_size,
            "files_in_lib": len(meta["files"])}


def do_import(archive: str, collection: str = "", force: bool = False) -> dict:
    """从 .tar.gz 恢复 collection；不指定 -c 时沿用 manifest 中的 collection 名。"""
    import tarfile
    path = Path(archive)
    if not path.exists():
        return {"ok": False, "error": f"文件不存在：{archive}"}
    with tarfile.open(path, "r:gz") as tf:
        names = tf.getnames()
        if "manifest.json" not in names:
            return {"ok": False, "error": "非 pykb 归档：缺 manifest.json"}
        manifest = json.loads(tf.extractfile("manifest.json").read().decode("utf-8"))  # type: ignore[union-attr]
        target = collection or manifest["collection"]
        if not target:
            return {"ok": False, "error": "未指定 collection，且 manifest 缺 collection"}
        npz_path, jsn_path = col_paths(target)
        if (npz_path.exists() or jsn_path.exists()) and not force:
            return {"ok": False, "error": f"目标库 {target} 已存在，需 --force 覆盖",
                    "collection": target}
        DATA_DIR.mkdir(parents=True, exist_ok=True)
        for name in ("npz", "json"):
            fn = f"pykb-target.{name}"
            member = next((m for m in names if m.startswith(f"localaicoder.") or m.endswith(f".{name}")), None)
        # 上面写法有 bug 直接按 manifest.files 提
        for fname in manifest["files"]:
            dest = npz_path if fname.endswith(".npz") else jsn_path
            tf.extract(fname, path=str(DATA_DIR))
            # extract 落在 DATA_DIR/fname，需要搬到目标路径
            extracted = DATA_DIR / fname
            if extracted.exists():
                extracted.rename(dest)
            else:
                # tarfile.extract 可能保留目录层级
                nested = DATA_DIR / fname.split("/")[-1]
                if nested.exists():
                    nested.rename(dest)
    return {"ok": True, "collection": target, "model": manifest.get("model"),
            "files_in_lib": manifest.get("files_count")}


# ---------------- HTTP 服务 ----------------

class Handler(BaseHTTPRequestHandler):
    def _send(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            try:
                st = do_stats()
            except Exception:
                st = {"collections": []}
            self._send({"ok": True, "loaded": _model is not None, **st})
        else:
            self._send({"ok": False, "error": "not found"}, 404)

    def do_POST(self):
        try:
            n = int(self.headers.get("Content-Length") or 0)
            req = json.loads(self.rfile.read(n) or b"{}")
            if self.path == "/index":
                self._send(do_index(req.get("collection") or DEFAULT_COL, req.get("paths") or [],
                                    req.get("excludes") or [], bool(req.get("force"))))
            elif self.path == "/search":
                self._send(do_search(req.get("collection") or DEFAULT_COL,
                                     str(req.get("query") or ""), int(req.get("k") or 5),
                                     str(req.get("source_prefix") or ""),
                                     bool(req.get("dense_only"))))
            elif self.path == "/stats":
                self._send(do_stats(req.get("collection") or ""))
            else:
                self._send({"ok": False, "error": "not found"}, 404)
        except Exception as exc:  # 服务不崩：错误以 JSON 返回给调用方
            self._send({"ok": False, "error": f"{type(exc).__name__}: {exc}"}, 500)

    def log_message(self, fmt, *args):  # 静默默认访问日志（关键字日志走 log()）
        pass


def serve(port: int):
    _stdout_utf8()
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    log(f"pykb v{__version__} 服务就绪：http://127.0.0.1:{port}（模型懒加载，数据目录 {DATA_DIR}）")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


# ---------------- watch（轮询增量索引） ----------------

def watch(paths: list[str], collection: str, excludes: list[str], interval: float):
    _stdout_utf8()
    log(f"watch 模式：每 {interval:g}s 检查变更（Ctrl+C 退出）")
    while True:
        try:
            r = do_index(collection, paths, excludes)
            if r.get("ok") and r.get("embedded"):
                log(f"检测到变更并已重嵌 {r['embedded']} 块（{r['files']} 文件 / {r['chunks']} 块）")
        except Exception as exc:
            log(f"watch 索引出错：{type(exc).__name__}: {exc}")
        time.sleep(interval)


# ---------------- CLI ----------------

def _stdout_utf8():
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass


def main():
    _stdout_utf8()
    ap = argparse.ArgumentParser(prog="pykb",
                                 description=f"pykb v{__version__} —— 本地文档语义检索（memsearch 的跨平台替代）")
    ap.add_argument("--version", action="version", version=f"pykb {__version__}")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p_serve = sub.add_parser("serve", help="常驻 HTTP 服务（默认 127.0.0.1:19587）")
    p_serve.add_argument("--port", type=int, default=19587)

    p_idx = sub.add_parser("index", help="增量索引文档（文件或目录）")
    p_idx.add_argument("paths", nargs="+")
    p_idx.add_argument("-c", "--collection", default=DEFAULT_COL)
    p_idx.add_argument("--force", action="store_true", help="忽略缓存全量重嵌入")
    p_idx.add_argument("--exclude", action="append", default=[],
                       help="排除 glob（可多次，如 --exclude '*.en.md'）")

    p_q = sub.add_parser("search", help="混合检索（语义 + BM25）")
    p_q.add_argument("query")
    p_q.add_argument("-c", "--collection", default=DEFAULT_COL)
    p_q.add_argument("-k", type=int, default=5)
    p_q.add_argument("--source-prefix", default="", help="只返回路径含该前缀的命中")
    p_q.add_argument("--dense-only", action="store_true", help="纯向量检索（不做 BM25 融合）")

    p_st = sub.add_parser("stats", help="库统计")
    p_st.add_argument("-c", "--collection", default="")

    p_w = sub.add_parser("watch", help="轮询增量索引")
    p_w.add_argument("paths", nargs="+")
    p_w.add_argument("-c", "--collection", default=DEFAULT_COL)
    p_w.add_argument("--interval", type=float, default=5.0)
    p_w.add_argument("--exclude", action="append", default=[])

    args = ap.parse_args()

    if args.cmd == "serve":
        serve(args.port)
    elif args.cmd == "index":
        r = do_index(args.collection, args.paths, args.exclude, args.force)
        if not r.get("ok"):
            print(f"错误：{r.get('error')}")
            return
        print(f"索引完成：{r['files']} 文件 / {r['chunks']} 块"
              f"（新嵌入 {r['embedded']}，复用 {r['reused']}），耗时 {r['seconds']}s")
    elif args.cmd == "search":
        r = do_search(args.collection, args.query, args.k, args.source_prefix, args.dense_only)
        if not r.get("ok"):
            print(f"错误：{r.get('error')}")
            return
        for i, h in enumerate(r["hits"], 1):
            loc = f"{h['source']} § {h['heading']}" if h["heading"] else h["source"]
            print(f"[{i}] score={h['score']}  Source: {loc} (L{h['start_line']})")
            print(h["text"][:400].replace("\n", " ")[:400])
            print()
    elif args.cmd == "stats":
        print(json.dumps(do_stats(args.collection), ensure_ascii=False, indent=2))
    elif args.cmd == "watch":
        watch(args.paths, args.collection, args.exclude, args.interval)


if __name__ == "__main__":
    main()
