# 自建中继（仿飞书模式）· 手机远程控制跨网方案

> 状态：**设计稿（未实现）**。目标：手机不在同一局域网、甚至在外网时，
> 也能通过扫码打开网页与本机 agent 对话——本地客户端**主动出站**连接
> 用户自建的中继服务器，二维码指向服务器地址，全程无需公网 IP、无需端口映射。

## 1. 背景

现有 `desktop/mobile.go` 的手机远程是**局域网直连**：本机起 HTTP 服务，
二维码编码 `http://<局域网IP>:<端口>/?t=<token>`。跨网不可用（私有 IP 不可路由、NAT 挡入站）。

飞书长连接给出了正确思路：**客户端主动向外建 WS**（出站连接 NAT 不挡），
云侧只做"信令桥"。本方案把飞书云换成**用户自己的服务器**（任何一台带公网的
VPS 即可，最小 1C/512M），数据不经第三方。

## 2. 总体架构

```
┌─ 本机（Local AI Studio 桌面端）────┐          ┌─ 中继服务器（用户 VPS）────┐         ┌─ 手机浏览器 ────────┐
│ desktop/relay.go 客户端            │          │ relay-server/（独立模块）  │         │                     │
│  ├ 出站 WS: wss://srv/client?d=t  │──────────>│  会话表 device→clientWS   │         │                     │
│  ├ 二维码: https://srv/s/?d=t     │          │  ├ 静态页  GET /s/?d=t    │<────────│ 扫码打开页面         │
│  ├ 复用 mobile.go 的 runTurn 逻辑 │<─────────│  ├ 手机WS  /s/ws?d=t      │────────>│ SSE/WS 收流式回复    │
│  └ （schedMu 串行 / 拒绝写操作）   │  桥接泵   │  └ 纯转发，不解析业务      │  桥接泵  │ POST 消息经 WS 发送  │
└───────────────────────────────────┘          └───────────────────────────┘         └─────────────────────┘
        出站连接（NAT 友好）                    服务器只看到加密流量两侧            出站连接（NAT 友好）
```

要点：

- **本机是唯一持有 agent 的一端**；服务器与手机之间只是"管道"，服务器不跑任何业务逻辑
- 本机到服务器、手机到服务器**全部出站 + TLS**（服务器前置 Caddy 自动签证书，或自备证书）
- 每台设备一个 `device_token`（长随机，存本机 `models.json`）；二维码里带同一 token，
  信任模型与现有局域网 token 一致：**拿到二维码的人 = 拿到控制权**，因此二维码即密钥，按需刷新

## 3. 组件设计

### 3.1 客户端（`desktop/relay.go`，加入现有 desktop 模块）

- 配置：`models.json` 顶层 `relay` 块（复用 config getter/setter 模式）：

  ```json
  "relay": { "server_url": "wss://relay.example.com", "device_token": "<64hex>", "enabled": false }
  ```

- `Start`：读配置 → 出站拨号 `wss://<server>/client?d=<token>` → 发 `hello`（版本、
  当前工作区、模型名）→ 进入桥接循环；断线按 1s→2s→…→30s 指数退避重连
- 收到手机 `send` 消息 → 完全复用 `mobile.go` 的执行路径：
  `schedMu` 串行（与定时任务/局域网远程互斥）→ 切工作目录 → `agent.Agent`
  （`OnApproval` 拒绝一切写操作）→ 事件逐条封装成协议帧发回服务器
- 二维码：`qrcode.Encode(server_url + "/s/?d=" + device_token)`，绑定 `MobileStatus`
  增加来源标记（`mode: "lan" | "relay"`），面板显示当前模式

### 3.2 服务器（新独立模块 `relay-server/`，仓库根目录，纯 stdlib + gorilla/websocket）

```
relay-server/
├── go.mod                # module localai/relay-server（无 wails 依赖，可独立部署）
├── main.go               # 启动、读配置、路由
├── hub.go                # 会话表：device_token → {clientWS, phones map, 广播锁}
├── client.go             # /client  WS：桌面客户端侧
├── phone.go              # /s      页面；/s/ws WS：手机侧（复用 mobilePageHTML 改造）
└── config.example.json   # { "listen": ":443", "device_tokens": ["<64hex>"] }
```

- 服务器**只做路由与转发**：`device_token` 未在白名单 → 403；一台设备允许多个手机
  页面同时在线（都收广播，`send` 只取第一份，其余去重）
- 部署形态：二进制 + systemd（见 `deploy.md`），或 Caddy 反代终结 TLS 后监听 127.0.0.1

### 3.3 前端（`MobilePanel.tsx` 增量改造）

- 面板顶部加模式切换：`局域网` / `自建中继`
- 中继模式表单：服务器地址、device_token（首次「生成并复制」→ 用户粘贴到服务器配置）、
  连接状态点、二维码区复用现有组件
- 飞书/企业微信卡片不受影响

## 4. 安全模型

| 层 | 措施 |
|---|---|
| 传输 | 强制 TLS（Caddy/Let's Encrypt 或自签 + 固定指纹）；明文回退直接拒绝连接 |
| 设备准入 | 服务器侧 `device_tokens` 白名单；token 为 64 位随机 hex，仅存本机与服务器 |
| 会话准入 | 二维码即凭证（同现有局域网模型）；提供「重置 token」使旧二维码立即失效 |
| 写保护 | 远端 `send` 触发的 agent 一律 `OnApproval` 拒绝写工具（客户端侧强制，服务器无法越过） |
| 数据面 | 服务器可看到聊天明文——因此必须**自建**；文档明示不要用不受信的第三方中继 |
| 审计 | 服务器仅记连接元数据（时间、IP、字节数），不落消息内容 |

## 5. 实施阶段

| 阶段 | 内容 | 估时 |
|---|---|---|
| R1 | relay-server 骨架（hub + 两侧 WS + 页面）+ systemd/Caddy 部署文档 | 1 天 |
| R2 | desktop/relay.go 客户端 + config 存储 + 重连退避 | 半天 |
| R3 | 前端模式切换/表单/二维码 + `mobile.go` 执行路径抽公共函数（`runTurn` 提到共用的 `remote_exec.go`） | 半天 |
| R4 | 联调（含断网重连、多手机、写拒绝回归测试） | 半天 |

## 6. 风险与对策

- **服务器成为单点**：断线时客户端按退避重连；UI 明示"中继离线"；飞书/局域网模式不受影响
- **token 泄露**：面板一键重置（改 `models.json` + 服务器白名单同步更新）
- **滥用（扫到二维码即获控制权）**：与局域网方案同一信任模型；可选增强——手机首连需在
  桌面端点「允许此手机」确认（二期）
- **长连接稳定性**：WS ping/pong 30s 心跳；服务器重启 ≤30s 内自动恢复

## 7. 关联文档

- 线路协议与消息表：`protocol.md`
- 服务器部署（systemd / Caddy TLS / Docker）：`deploy.md`
- 现有局域网实现：`desktop/mobile.go`；飞书长连接参考实现：`desktop/feishu.go`
