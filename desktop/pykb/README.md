# pykb

**本地文档语义检索（memsearch 的跨平台替代）** —— Windows / macOS / Linux 全平台可用，单文件、零服务、零云 API。

> Python Knowledge Base。把项目文档（Markdown/文本）切块、本地向量化，提供
> memsearch 风格的 `index / search / stats / watch / serve`。检索为
> **混合模式**：语义向量 + BM25 关键词 + RRF 融合，中文英文都好使。

English: a single-file, cross-platform local semantic search for project docs (a drop-in alternative to [memsearch](https://pypi.org/project/memsearch/) that works natively on Windows). CLI + HTTP service, hybrid retrieval (dense + BM25 + RRF), fully local.

## 为什么是 pykb

| | memsearch | pykb |
|---|---|---|
| Windows 原生 | ❌（milvus-lite 无 Windows 轮子） | ✅ |
| macOS / Linux | ✅ | ✅ |
| 运行依赖 | uv tool + milvus-lite | `pip install fastembed` |
| 架构 | milvus-lite 向量库 | numpy 暴力余弦（千块级毫秒） |
| 检索 | 语义 + BM25 + RRF | 同（hybrid） |
| 形态 | CLI | CLI + HTTP 服务（可嵌入应用） |
| 模型 | bge-m3（约 570MB） | bge-small-zh-v1.5（约 95MB，中英双语） |

## 安装

```bash
pip install fastembed          # 唯一第三方依赖（自带 onnxruntime/tokenizers/numpy）
python kb_service.py --version # Python >= 3.9；模型约 95MB 首次用时自动下载
```

国内网络默认走 HF 镜像（`HF_ENDPOINT=https://hf-mirror.com`，可用环境变量覆盖）。

或者作为包安装（提供 `pykb` 命令）：

```bash
pip install .
```

## 快速上手

```bash
# 索引当前仓库文档（增量：未变更文件自动复用向量）
python kb_service.py index -c myproj AGENTS.md docs

# 混合检索
python kb_service.py search "权限模式 有哪几种" -c myproj -k 5
python kb_service.py search "中继连接" -c myproj --source-prefix opencode-relay

# 文档变更自动重嵌（轮询）
python kb_service.py watch AGENTS.md docs -c myproj

# 库统计
python kb_service.py stats -c myproj
```

## HTTP 服务（供应用集成）

```bash
python kb_service.py serve --port 19587
```

| 端点 | 方法 | 说明 |
|---|---|---|
| `/health` | GET | `{ok, loaded, collections, data_dir, ...}` |
| `/index` | POST | `{collection, paths[], excludes[]?, force?}` → 增量索引 |
| `/search` | POST | `{collection, query, k, source_prefix?, dense_only?}` → `{hits:[{source, heading, start_line, score, text}]}` |
| `/stats` | POST | `{collection?}` → 库统计 |

模型懒加载：服务秒起，首次检索/索引时才加载（并按需下载）模型。

## 设计要点

- **分块**：Markdown 标题切块（命中带 `§ 标题` 与起始行号），超长节按空行段落续切（≤800 字符，匹配 bge 512 token）
- **混合检索**：dense 余弦 + BM25（ASCII 词 + CJK 单字）→ RRF 融合（`--dense-only` 可切纯向量）
- **增量索引**：文件 sha256 缓存，未变更文件直接复用旧向量行（按行区间对齐）
- **原子落盘**：临时文件 + `os.replace`，并发索引/检索不会读到半截数据
- **存储**：`~/.pykb/<collection>.{npz,json}`，删目录即重置

## 被 Local AI Studio 集成的方式

[Local AI Studio](https://github.com/hzerther-hub/localaicoder) 桌面端将本脚本 `go:embed` 进 exe：
启动后按需拉起 `127.0.0.1:19587` 服务，`/memsearch` 斜杠命令自动路由到它——
Windows 上无需 WSL/Docker 即可获得语义检索。

## 单独发布

本目录自包含（不依赖仓库其它文件），可整体发布：

```bash
# 方式一：复制本目录为新仓库
cp -r desktop/pykb /tmp/pykb && cd /tmp/pykb && git init && git add -A && git commit -m "init" && git remote add origin <你的仓库> && git push -u origin main

# 方式二：从本仓库 subtree 推送
git subtree push --prefix=desktop/pykb <你的仓库> main
```

## License

MIT
