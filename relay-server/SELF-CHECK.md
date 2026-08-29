# relay-server · AI 自查指南

> 这份文档让 AI（或运维）**一键自检**自建中继链路：域名 → TLS → 页面 → WS → 桌面是否上线。
> 任何一步不通过，按「判定」列直接定位；不用再人肉猜。

## 0. 一键自检（推荐）

```bash
cd relay-server
python3 selftest.py <域名> <device_token>
# 例：biancheng.mei.biz 是正确域名（.bz 是错的，别用）
python3 selftest.py biancheng.mei.biz 6e26313990758a35a4fc28868e9dd8aa319164cb3740128f1dc36d5d9f5f3e2a
```

输出示例（全部正常时）：

```
===== 自检: biancheng.mei.biz (token 6e2631…) =====
[1] DNS        : ['154.9.25.70']
[2] HTTPS /s/  : HTTP 200  ✅
[3] WS /s/ws   : ✅ desktop_on=always sessions=25
[4] WS /client : ✅ handshake ok
判定: ✅ 桌面已在线，手机端可用
```

`判定` 给出结论。若想让 AI 进一步处理，读下面「判定表」。

## 1. 逐步人工检查（selftest 背后做的事，也可单跑）

### 1.1 域名与 DNS
```bash
getent hosts <域名>            # 应有解析结果
# 多个 IP 要警惕：只有托管服务器的那个 IP 才正常；多 IP 会导致手机/桌面连到不同主机
```
- 判定：解析为空 / 多条 A 记录但 443 不通 → 删掉多余解析，只留服务器 IP

### 1.2 HTTPS 页面是否可达（手机能打开的前提）
```bash
curl -s -o /dev/null -w "%{http_code}\n" https://<域名>/s/?d=<token>
```
| 状态码 | 含义 | 处置 |
|---|---|---|
| 200 | ✅ 页面正常 | 继续下一步 |
| 403 | token 不在白名单 | 把 token 加入服务器 `device_tokens[]` 并重启 |
| 502 | Caddy 在跑但后端 9000 没起 | 启动 relay-server |
| 000 / SSL 错误 | 443/TLS 没就绪 | 检查 Caddy 配置/证书、443 放行 |

### 1.3 WS 端点是否可用（桌面出站 + 手机连接）
```bash
python3 - <<'PY'
import asyncio,websockets,json
D="<token>"; H="<域名>"
async def go():
  try:
    ws=await asyncio.wait_for(websockets.connect(f"wss://{H}/client?d={D}",open_timeout=10),10)
    print("✅ /client 握手成功"); await ws.close()
  except Exception as e: print("❌ /client:",type(e).__name__,str(e)[:60])
asyncio.run(go())
PY
```
- 判定：握手成功 = 服务器与 token 白名单正常；失败 = token 或服务器问题。

### 1.4 桌面端是否已上线（手机“正在连接桌面”的根源）
```bash
python3 - <<'PY'
import asyncio,websockets,json
D="<token>"; H="<域名>"
async def go():
  try:
    ws=await asyncio.wait_for(websockets.connect(f"wss://{H}/s/ws?d={D}",open_timeout=10),10)
  except Exception as e: print("手机 WS 连不上:",type(e).__name__,str(e)[:50]); return
  try:
    await ws.send(json.dumps({"type":"state","rid":1}))
    m=json.loads(await asyncio.wait_for(ws.recv(),6))
    print("✅ 桌面已上线, 会话数=", len(m.get("sessions",[])))
  except asyncio.TimeoutError: print("⚠️ 连上但桌面无响应(未注册)")
  except Exception as e:
    print("❌ 被服务器拒绝(code 1008)= 桌面未上线:", str(e)[:40])
  await ws.close()
asyncio.run(go())
PY
```
- `会话数=N` → 桌面在线 ✅
- `code 1008 (policy violation)` → **桌面未上线** → 重启桌面端 或 桌面面板点「连接」

## 2. 判定表（AI 按此给出结论）

| selftest / 检查结果 | 结论 | AI 处置建议 |
|---|---|---|
| 第2步非 200 | HTTPS/TLS/服务未就绪 | 查 Caddy、443、`relay-server` 是否在跑 |
| 第3步 403 | token 不在白名单 | 加 token 进 `config.json` → 重启 relay-server |
| 第4步 1008 | 桌面未上线 | 提示用户重启桌面端 / 点「连接」；确认配置的域名是 `.biz` |
| 第4步 `desktop_on=… sessions=N` | 桌面已在线，一切正常 | 手机刷新即可用 |
| 模型下拉显示「正在连接桌面」 | 同 1008（桌面未上线） | 同上 |
| 思考等级显示「—」 | 当前模型非推理模型 | 切到 deepseek 系模型才显示 low/high 等 |

## 3. 最容易踩的三个坑（AI 优先核查）

1. **域名写错**：正确是 `biancheng.mei.biz`；`biancheng.mei.bz` 解析到多个 IP 且 443 不通，**不能用**。
2. **token 不一致**：三处必须完全一致——服务器 `device_tokens[]`、桌面面板、手机链接 `?d=`。
3. **改了地址没重连**：桌面端自动连接只在**启动时**读一次配置；改完 `.biz` 要么重启桌面端、要么面板点「连接」。

## 4. 手机端三控件位置（供对照）

顶栏第 2 行，从左到右：
```
[ 模型 ▾ ]  [ 推理 ▾ ]  [ 权限 ▾ ]
```
- 模型：列全部模型（桌面在线后填充）
- 推理：仅推理模型（如 deepseek 系）显示 low/medium/high/xhigh/max；本地 Qwen 显示「—」
- 权限：总是允许 / 询问 / 只读（改任一端，电脑顶栏即时同步）
