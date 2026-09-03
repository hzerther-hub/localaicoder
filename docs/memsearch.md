# memsearch —— 项目知识库（本地语义检索）

> 目的：**省 token**。回答架构/部署/协议类问题前先向量检索 top-k 相关小块（几百 token），
> 代替把 AGENTS.md、docs/、opencode-relay/ 等长文档整篇塞进上下文。

## 形态

- 工具：PyPI [`memsearch`](https://pypi.org/project/memsearch/)（uv tool 安装）。
- 向量库：**milvus-lite**，物理上就是单文件 `~/.memsearch/milvus.db`，零服务、零云依赖。
- 向量化：**bge-m3 int8/ONNX**（`gpahal/bge-m3-onnx-int8`，约 570MB，首跑自动从 HF 下载；
  国内网络加 `HF_ENDPOINT=https://hf-mirror.com`）。纯本地 CPU 推理，不是对话模型、不耗 API。
- 总结/提炼类 LLM 工作不在此工具内，由 harness 侧 agent 完成。

## 平台路由（桌面端自动，/memsearch 同一套）

| 平台 | 首选 | 兜底 |
|---|---|---|
| Linux/macOS | 原生 memsearch | **pykb**（自研，`desktop/pykb/`，pip 装 fastembed 即用）→ 内置知识库（零安装） |
| Windows | **pykb**（原生装不了：milvus-lite 无 Windows 轮子） | WSL 内 memsearch / 内置知识库 |

**pykb**（[github.com/hzerther-hub/pykb](https://github.com/hzerther-hub/pykb)）是自研的单文件
Python 语义检索：bge-small-zh-v1.5（约 95MB）+ BM25 + RRF 混合检索，桌面端把它 `go:embed` 进
exe、启动时自动拉起为 `127.0.0.1:19587` 服务，`/memsearch` 自动路由。数据在 `~/.pykb/`。

## 本项目的 collection

| collection | 内容 | 维护 |
|---|---|---|
| `localaicoder` | 本仓库文档：AGENTS/README/CLAUDE、`docs/`、`opencode-relay/`、relay-server 部署文档等（2026-09-03 首次索引 448 块，`*.en.md` 排除） | 文档改动后增量重索引（命令见下） |
| 默认（`memsearch_chunks`） | `.memsearch/memory/` 会话日记 | 由日常会话流程维护 |

两个库同在一个 milvus.db 文件里，靠 collection 区分；检索跨项目时注意带上 `-c`。

## 常用命令

```bash
# 语义检索（日常问答入口；结果带 Source 路径 + Heading，按需再读原文那一节）
memsearch search "问题关键词" -c localaicoder -k 5
memsearch search "问题" -c localaicoder --source-prefix opencode-relay   # 限定子目录

# 索引 / 增量更新（改完文档跑一次，自动跳过未变文件）
memsearch index -c localaicoder AGENTS.md docs opencode-relay
memsearch index -c localaicoder --force docs    # 全量重索引

# 观感
memsearch stats
```

## 安装与维护（备查）

**按机器安装**——「已完成」只对装过的那台机器成立，换机器要重装。桌面应用（LocalAI Studio）启动时会自动检测：
- Linux/macOS：未装则出引导条一键原生安装（实现 `desktop/memsearch.go`，检测含 uv shim 目录，规避装后 PATH 不刷新的坑）。
- Windows：引导条提供「安装到 WSL2」（发行版内自动装 uv + memsearch + ONNX 配置，经 `wsl.exe` 调用）；无 WSL 则引导用内置知识库（零安装，`/memsearch` 自动回退）。

```bash
uv tool install memsearch --force --with onnxruntime --with tokenizers
#   ↑ onnxruntime 必须用 --with 注入：裸装 memsearch 不含 ONNX provider，重装时别丢
# 配置在 ~/.memsearch/config.toml：[embedding] provider = "onnx"（桌面端一键安装会自动补这份配置）
```

## 与 AGENTS.md 的关系

仓库根 `AGENTS.md` 的「项目知识检索」一节已约定：agent 回答项目问题**先检索再回答**，
本文件是该约定的展开。两者改动后记得 `memsearch index -c localaicoder` 同步进库。
