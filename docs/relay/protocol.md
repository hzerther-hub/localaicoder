# 中继线路协议（v1 草案）

传输：两条 WebSocket（桌面客户端 ↔ 服务器；手机页面 ↔ 服务器）。
服务器是**哑管道**：只按 `device_token` 路由，不解释业务语义。
编码：UTF-8 JSON，一行一帧（WS TextMessage）。

## 1. 握手

### 客户端 → 服务器

```
GET wss://<server>/client?d=<device_token>&v=1
```

- `d` 不在服务器白名单 → HTTP 403 关闭
- 握手成功后服务器回第一帧：

```json
{"type":"ready","session":"s_a1b2c3","phone_url":"https://relay.example.com/s/?d=<device_token>"}
```

### 手机 → 服务器

```
GET wss://<server>/s/ws?d=<device_token>
```

- 校验同上；成功后服务器向客户端广播：

```json
{"type":"phone","n":1}
```

## 2. 消息帧（手机 → 客户端）

| type    | 字段          | 说明                                   |
|---------|---------------|----------------------------------------|
| `send`  | `text`        | 用户输入（客户端喂给 agent）           |
| `stop`  | —             | 请求停止当前轮（v2，协作停止）         |
| `ping`  | —             | 手机侧保活（服务器代答 pong）          |

## 3. 消息帧（客户端 → 手机）

与 `desktop/mobile.go` 现有 SSE 事件对齐，字段不变，仅换载体（SSE → WS 帧）：

| type    | 字段      | 说明                                   |
|---------|-----------|----------------------------------------|
| `text`  | `delta`   | 流式文本增量                           |
| `tool`  | `delta`   | 工具名（手机端显示 🔧 行）             |
| `error` | `delta`   | 错误/拒绝提示（含"已拒绝远程写操作"）  |
| `done`  | —         | 一轮结束                               |
| `hello` | `workspace`, `model`, `version` | `ready` 后客户端上报的元信息 |

## 4. 心跳与重连

- 服务器对两侧每 30s 发 WS `ping`；60s 无 pong 判死、清理会话
- 客户端断线重连：1s、2s、4s … 上限 30s；重连成功重发 `hello`
- 手机页面 `EventSource/WS` onerror → 整页刷新（与局域网页面行为一致）

## 5. 服务器行为约束

- 只做路由：`send` 仅转发给该 device 注册的客户端 WS；`text/tool/error/done` 仅广播给该 device 的手机 WS
- 一台设备多手机：`send` 采第一份、同帧去重（按 `seq`，手机页随机生成）
- 不落盘消息内容；仅连接日志（时间/IP/方向/字节数）
- 单实例并发目标：≤64 设备 × ≤4 手机；超出即拒绝（防滥用）

## 6. 示例：一轮完整对话

```
手机  → 服务器: {"type":"send","text":"跑一下测试并总结失败原因"}
服务器→ 客户端: {"type":"send","text":"跑一下测试并总结失败原因"}
客户端→ 服务器: {"type":"tool","delta":"run_shell"}
服务器→ 手机:   {"type":"tool","delta":"run_shell"}
客户端→ 服务器: {"type":"text","delta":"3 个用例失败，原因是…"}
服务器→ 手机:   {"type":"text","delta":"3 个用例失败，原因是…"}
客户端→ 服务器: {"type":"done"}
服务器→ 手机:   {"type":"done"}
```
