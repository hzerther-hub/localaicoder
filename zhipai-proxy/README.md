# 智排路由代理（Python）

把「简单→本地 / 复杂→flash / 图片→vision」这套智能路由，封装成 OpenAI 兼容的 `/v1/chat/completions` 端点。
Codex（或任一 OpenAI 兼容助手）只要把 `base_url` 指到本代理，即可**无感知**地用上智排——代理按任务类别动态转发到不同上游模型。

只依赖 `requests`（服务器用标准库）。

## 路由映射

| 任务类别 | 路由到 | 判定依据 |
|---|---|---|
| 图片 / 多模态 | `deepseek-v4-flash-vision-exp` | 消息含 image / image_url |
| 复杂（写代码、多步、长文） | `deepseek-v4-flash` | 强关键词 / 代码块 / 超长 / 多段 |
| 简单（短问答、续操作） | 本地模型 | 短 + 无强信号 |
| 拿不准（弱信号区） | 用**本地模型**仲裁一句「简单/复杂」 | 接近阈值但无强信号 |

判定顺序：**图片 > 强关键词/代码块 > 长度 > 弱信号区**（不只按字数）。

## 快速开始

```bash
cd zhipai-proxy
pip install requests

# 1. 配置（填 DeepSeek 密钥；本地端点指向你在跑的本地服务）
cp config.example.json config.json
vi config.json          # 填 flash/vision 的 api_key；确认 local.endpoint 在跑

# 2. 启动
python3 proxy.py --config config.json --host 127.0.0.1 --port 9000
# 看到提示后，它监听 http://127.0.0.1:9000/v1
```

## 让 Codex 用它

把 Codex 的模型服务地址（`base_url`）指向代理即可：

```
http://127.0.0.1:9000/v1
```

Codex 发送的请求会先到代理 → 代理分类 → 转发到对应上游模型 → 原样流式返回。Codex 无需感知路由。

## 配置字段说明

```jsonc
{
  "local":  { "endpoint": "http://127.0.0.1:8099/v1", "api_key": "local-noauth", "model": "ornith-1.5-35b" },
  "flash":  { "endpoint": "https://api.deepseek.com/v1", "api_key": "<密钥>", "model": "deepseek-v4-flash" },
  "vision": { "endpoint": "https://api.deepseek.com/v1", "api_key": "<密钥>", "model": "deepseek-v4-flash-vision-exp" },
  "max_chars": 160,            // 简单/复杂长度阈值（字符）
  "max_words": 28,             // 简单/复杂长度阈值（词；中文按字数×2 折算）
  "arbitrate": true,           // 拿不准时是否用本地模型仲裁
  "arbitrate_timeout": 8       // 本地仲裁超时秒
}
```

也可以用环境变量覆盖：`LOCAL_ENDPOINT` / `LOCAL_API_KEY` / `LOCAL_MODEL`、`FLASH_*`、`VISION_*`。

## 验证

```bash
# ① 简单 → 本地
curl -s http://127.0.0.1:9000/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"用一句话解释什么是递归"}]}'

# ② 复杂（含强关键词）→ flash
curl -s http://127.0.0.1:9000/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"帮我设计一个高并发缓存架构"}]}'

# ③ 图片 → vision
curl -s http://127.0.0.1:9000/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":[{"type":"text","text":"图里是什么"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}'
```

每次请求会打印一条 `[route] <decision> -> <slot> model=<model>` 到 stderr，方便确认命中哪档。

## 说明

- 支持**流式（SSE）**与**非流式**两种，均透明转发。
- `GET /v1/models` 会列出三个上游模型，方便 Codex 识别。
- 本地模型失败时目前**不会**自动切 flash（越权兜底可能产生意外费用）；如需兜底，可在 `forward` 里加一层失败重试到 flash（见《智排.md》「稳定性↔成本」取舍）。
