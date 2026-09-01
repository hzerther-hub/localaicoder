# AGENTS.md — opencode-relay (Switchboard)

## 这是什么

手机/任意浏览器远程驱动**家里 PC 上的 opencode** 做开发的中继链路。本目录是上层
git 大仓 `localaicoder`（Go 内核，见其根 `AGENTS.md`）里的一个 Python 子项目，同时
以独立仓库发布在 `hzerther-hub/Switchboard`（GitHub，**只能走 SSH 推送**，本机连不上
github.com:443）。

核心原则：**中继只认识 token，不认识 opencode；所有翻译逻辑在家里 PC 的桥里。**

## 目录结构（按部署位置拆分，勿混放）

| 目录 | 部署位置 | 内容 |
|---|---|---|
| `service/` | 公网 VPS | `main.py`（FastAPI 哑管道中继）+ `page.html`/`css/`/`js/`（手机控制台）+ `config.json.example` + `nginx.conf.example` |
| `pc/` | 家里 PC | `opencode_bridge.py`（协议翻译桥）+ `opencode_bridge.json.example` |
| `docs/` | 仓库 | 截图等展示资产（入库前必须抹掉地址栏里的 `?d=` token） |

端口：中继 9000（VPS）/ 8999（本机测试）；`opencode serve` 9001（家里 PC，仅 127.0.0.1）；
Nginx 443 是唯一公网入口。

## 命令

```bash
# 本机一键起停（大仓根目录的脚本；会自动清理残留桥进程）
../scripts/opencode-remote.sh {start|stop|status|serve|relay|bridge}

# 单独起桥（本机测试）
cd pc && python3 opencode_bridge.py --config opencode_bridge.json

# 语法检查（本仓库没有测试框架；改代码后至少过这两步）
python3 -m py_compile service/main.py pc/opencode_bridge.py
bash -n ../scripts/opencode-remote.sh

# 同步到 Switchboard 仓库（见"Git 注意"）
git subtree split --prefix=opencode-relay -b switchboard-dist   # 在大仓根执行
git push git@github.com:hzerther-hub/Switchboard.git switchboard-dist:main
```

## 架构边界（改代码必须遵守）

- **`service/main.py` 是哑管道**：不解析、不缓存、不产生任何业务帧。收到的帧原样
  转发（桥→手机广播 1→N；手机→桥单转 N→1）。不要往这里加业务逻辑、持久化或
  opencode 相关代码——换被控端时中继必须零改动。
- **`pc/opencode_bridge.py` 是唯一的翻译层**：手机帧 ↔ opencode HTTP API/SSE。三条
  并发流：asyncio 主循环（`_relay_loop`/`_event_pump`）、SSE 守护线程
  （`_sse_thread`，阻塞 requests 不能进事件循环）、`asyncio.to_thread` 线程池（所有
  阻塞 HTTP）。**不要在事件循环里直接调 `requests`。**
- **帧协议与外部契约兼容**：手机帧格式必须与 `../../desktop/relay.go`（Local AI
  Studio 桌面端，同协议另一实现）及 `../../docs/relay/protocol.md` 保持一致；
  `send/state/messages/models/permission_*/question_*/run:started|finished` 等帧名、
  WebSocket close 码（1000=正常踢旧桥、1008=拒绝、1006=异常）、opencode 端点与
  `?directory=` 目录作用域参数（`scoped()`）都是线上契约，勿随意改。
- **权限必须 fail-closed**：`readonly` 自动拒、`always` 自动放、`ask` 转发手机且
  超时（`PERM_TIMEOUT` 默认 120s）自动拒；未知响应值映射为 `reject`。

## 文档规则（双语）

每份文档都有中文原版（原文件名）和英文版（`.en.md` 后缀）：`README.md`（英）/
`README.zh-CN.md`（中）+ `ARTICLE`/`README-opencode`/`config`/`TUNNEL-3STEPS` 四对。
**改任何中文 .md 必须同步改对应 .en.md**（反之亦然）；文件首行是语言切换链接。
中文文件名不改（代码注释、脚本、README 都引用了它们）。

## 已知陷阱

- **一个 token 只允许一个桥进程**：多个桥互抢 `/client` 槽位会造成手机端反复
  1006 重连。启动新桥前 `pkill -f opencode_bridge.py`（脚本已内置）。
- **中继不缓存帧**：桥断线期间的下行帧会丢，靠 `_missed` 记账 + 重连后补发
  `session:opened` 让手机重拉消息兜底——别试图在中继里加帧队列。
- **token 即控制权**：`service/config.json`、`pc/opencode_bridge.json`（含 token）
  已 gitignore，绝不提交；截图里若出现手机地址栏的 `?d=` token 必须涂黑后才能入库
  （参考 `docs/screenshot-phone-console.png` 的做法）；泄露立即轮换（三处同步：
  VPS 白名单、桥配置、手机 URL）。
- **中继能看到聊天明文**——只能部署在用户自己的服务器上。
- `main.py` 需兼容 Python 3.8（已用 `from __future__ import annotations`，别引入
  3.9+ 语法）；单帧上限由 `RELAY_WS_MAX`（默认 5MB）控制。

## Git 注意

- 本目录是大仓子目录：`git mv`/提交都在大仓根操作；推送 Switchboard 用
  `git subtree split`（保留历史）。
- **这个 git 版本的 `subtree split` 不支持 `-f`**（静默报用法错误导致分支不更新、
  推送显示 up-to-date）。正确做法：`git branch -D switchboard-dist` 后重新
  `git subtree split … -b switchboard-dist` 再 push。
- 提交信息用中文，风格沿用现有：`feat(relay):` / `docs(relay):` / `chore(relay):`。

## 动手前必读

- 改协议/帧 → `ARTICLE.md`（技术剖析）+ `../../docs/relay/protocol.md`
- 改部署/配置 → `config.md`（全部字段）+ `service/nginx.conf.example`
- 改隧道方案 → `TUNNEL-3STEPS.md`（SSH 反向隧道直出官方 UI）
