# 快速上手：三平台启动 opencode 远程链路（Windows / Linux / macOS）

> 目标链路（方案 A）：**手机浏览器 → 公网中继(VPS, 8999) → 桥(家里 PC, 纯出站) → opencode serve(家里 PC, 9001)**
> 本机自测时不需要 VPS：中继也可以先跑在本机。完整背景见 [README-opencode.md](README-opencode.md)，
> 环境要求与版本坑位见其「环境要求」一节；本文只讲"从零到手机能聊"。

---

## 0. 前置（三平台通用）

| 依赖 | 版本 | 用途 |
|---|---|---|
| [Node.js + npm](https://nodejs.org) | ≥ 20 | 安装 opencode CLI |
| opencode CLI | **≥ 1.18.25** | 干活的后端（1.18.21 在 Windows 上 serve 会静默退出！） |
| Python | **≥ 3.9** | 中继 + 桥（3.8 无 `asyncio.to_thread`，桥有垫片但别依赖） |
| 本仓库 | — | `scripts/opencode-remote.sh`（Git Bash / Linux / macOS 均可跑） |

```bash
npm i -g opencode-ai          # 安装/升级 opencode
opencode --version            # 确认 ≥ 1.18.25
```

---

## 1. 建 Python venv + 装依赖

**Windows（推荐 miniconda；别用 Microsoft Store 的 python3 占位 stub）**

```bash
D:/miniconda3/python.exe -m venv .ocdata/venv
.ocdata/venv/Scripts/python.exe -m pip install -r opencode-relay/service/requirements.txt \
                                                   -r opencode-relay/pc/requirements.txt
```

**Linux**

```bash
python3 -m venv .ocdata/venv          # 缺 venv 模块：sudo apt install python3-venv
.ocdata/venv/bin/python -m pip install -r opencode-relay/service/requirements.txt \
                                               -r opencode-relay/pc/requirements.txt
```

**macOS**

```bash
python3 -m venv .ocdata/venv          # Homebrew Python：brew install python
.ocdata/venv/bin/python -m pip install -r opencode-relay/service/requirements.txt \
                                               -r opencode-relay/pc/requirements.txt
```

> venv 固定放 `.ocdata/`（已 gitignore）。`scripts/opencode-remote.sh` 检测到它就自动优先使用，**无需 activate**。

---

## 2. 写两份运行配置（均已 gitignore）

**中继** `opencode-relay/service/config.json`：

```json
{ "listen": "127.0.0.1:8999", "device_tokens": ["<64位随机token，测试可用 testtoken123>"] }
```

**桥** `opencode-relay/pc/opencode_bridge.json`（模板 `opencode_bridge.json.example`）：

```json
{
  "relay": {
    "server_url": "wss://op.mei.biz",
    "device_token": "<与中继白名单一致的 token>",
    "workspace": "D:/localai_code",
    "mode": "ask",
    "insecure": false
  },
  "opencode": { "base_url": "http://127.0.0.1:9001", "default_model": "", "password": "" }
}
```

- 本机自测：`server_url` 填 `http://127.0.0.1:8999`；走公网填 `https://你的域名`（桥自动转 wss）。
- `workspace` 写你要让 opencode 干活的项目目录（Windows 正斜杠最省心）。
- `mode`：`readonly` / `ask`（写操作手机审批）/ `always`（放行），手机端可随时切。
- `default_model` 留空 = opencode 默认；要指定必须是 opencode 里真实存在的 `providerID/modelID`
  （`curl -s http://127.0.0.1:9001/config/providers` 可查），填错模板里的占位模型会导致 send 失败。

---

## 3. 一键启动 / 停止 / 看状态（三平台同一条命令）

**Windows 请在 Git Bash（装 Git for Windows 自带）里跑**；Linux/macOS 直接终端：

```bash
scripts/opencode-remote.sh start     # 起 9001(opencode) + 8999(中继) + 桥
scripts/opencode-remote.sh status    # 三个组件各自的健康状态
scripts/opencode-remote.sh stop      # 全停（含端口兜底清理，防残留）
scripts/opencode-remote.sh serve|relay|bridge   # 单独起某一个
```

日志都在 `.ocdata/`：`opencode-serve.log` / `relay.log` / `bridge.log`。

---

## 4. 验证

```bash
curl -s http://127.0.0.1:9001/global/health     # {"healthy":true,"version":"1.18.25"}
curl -s -o /dev/null -w "%{http_code}\n" "http://127.0.0.1:8999/s/?d=<token>"   # 200
tail -3 .ocdata/bridge.log                      # 应有「已接入中继 ws://…/client?d=…」
```

手机打开 **`http://<本机局域网IP>:8999/s/?d=<token>`**（本机/局域网自测）或
**`https://你的域名/s/?d=<token>`**（公网，VPS 部署见 `config.md` 第五节）。
左上角出现「opencode v1.18.x」角标 + 会话列表即全通。

---

## 5. 平台差异速查

| 事项 | Windows | Linux | macOS |
|---|---|---|---|
| 跑脚本的终端 | **Git Bash**（cmd/PowerShell 跑不了 bash 脚本） | 任意 | 任意 |
| venv python 路径 | `.ocdata/venv/Scripts/python.exe` | `.ocdata/venv/bin/python` | `.ocdata/venv/bin/python` |
| venv 进程数 | Python ≥3.12 的 venv 是「shim 父 + 解释器子」**两个进程属正常** | 1 个 | 1 个 |
| 杀端口占用 | 脚本自动用 netstat+taskkill 兜底 | fuser | lsof（脚本未覆盖，手动 `kill`） |
| opencode serve 起不来 | 先升到 ≥1.18.25（1.18.21 静默退出无日志） | 正常 | 正常 |
| 商店版 python3 stub | 是（脚本已自动绕开） | — | — |

---

## 6. 起不来？

先 `scripts/opencode-remote.sh status` 定位是哪一环，再对照
[README-opencode.md](README-opencode.md) 第六节「常见故障」：
`1006`=多桥互抢、`1008`=token 不在白名单或桥未上线、`ServeError`=端口占用、模型空=桥没起。
