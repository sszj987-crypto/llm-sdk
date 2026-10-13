# LLM Agent Gateway

本地 OpenAI-compatible 网关，用一个本地地址接入多个大模型 profile，并按 profile 选择直连、Cloudflare Worker SNI 和 HTTP3/QUIC。

## 核心能力

- **本地 OpenAI API**：`POST /v1/chat/completions`、`GET /v1/models`
- **多 Profile 管理**：每个 profile 独立配置 provider、模型、上游地址、API Key、传输策略
- **协议统一**：外部客户端只使用 OpenAI 格式，Gemini 走 OpenAI-compatible endpoint
- **传输策略**：`direct` 或 `worker_sni` 互斥，HTTP3/QUIC 作为独立优化开关
- **诊断可视化**：Worker 健康检查、状态码、错误分类、TCP/TLS/首字节/首 token/总耗时

## 原理

通过 Cloudflare Worker 做代理转发，绕过 GFW 对 Google / OpenAI API 域名的封锁：

1. **申请域名**，在 Cloudflare 上添加 DNS 记录指向 Worker
2. **Worker 部署**：`worker/worker.js`，placement 设为 **region-美西**（避免出口 IP 被 Gemini 等服务的地区限制拦截）
3. **Cloudflare 优选 IP**：通过 IPDB 查询 Cloudflare 最优边缘 IP，避免 DNS 解析被 GFW 污染
4. **SNI 伪装**：请求 SNI 指向你的 Worker 域名，实际目标地址通过 `X-Target-Url` 头传递

## 性能对比

公网网络不稳定，数据可能存在偏差，以下为接近时间的验证结果

| 场景 | TLS 握手完成 | 首字返回 |
|------|------------|---------|
| 开启 VPN | ~450ms | ~1.65s |
| Cloudflare Worker | ~175ms | ~1.2s |
| Worker + QUIC | ~93ms | ~988ms |

## 启动

```bash
go run ./cmd/llmagent
```

默认打开 `http://127.0.0.1:8787`。

不自动打开浏览器：

```bash
LLM_AGENT_NO_BROWSER=1 go run ./cmd/llmagent
```

## OpenAI-compatible 接入

本地网关地址：

```text
http://127.0.0.1:8787/v1
```

示例：

```bash
curl http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer local-placeholder' \
  -d '{
    "model": "gemini-2.5-flash",
    "messages": [{"role":"user","content":"hello"}],
    "stream": true
  }'
```

本地 `Authorization` 第一版不强校验；真实上游 key 从对应 profile 读取。

## 配置

配置保存在项目目录 `config.json`，页面支持多个 profile：

- **Provider**：`openai` 或 `gemini`
- **URL**：OpenAI-compatible API 地址，如 `https://generativelanguage.googleapis.com/v1beta/openai`
- **API Key**：真实上游 API Key
- **Model**：模型名称，如 `gemini-2.5-flash`
- **Route Mode**：`direct` 或 `worker_sni`
- **Worker URL**：Cloudflare Worker 地址，`worker_sni` 时填写
- **HTTP3 / QUIC**：独立优化开关，可和 `direct` 或 `worker_sni` 组合

旧版单配置会在读取时自动迁移为默认 profile。

## Cloudflare Worker

`worker/worker.js` 是一个转发代理，配置要点：

- Worker 的 **placement** 设为美国西部（Region - US West），确保出口 IP 不被 API 地区限制
- 请求通过 `X-Target-Url` 指定实际目标，Worker 透明转发

### 部署

在 CF 面板上传 `worker/worker.js`，创建 Worker 并绑定域名。

**自动化方案**（可选）：通过 `wrangler` CLI 部署，免去手动上传步骤。

添加 `worker/wrangler.toml`：

```toml
name = "llm-agent-worker"
main = "worker.js"
compatibility_date = "2025-01-01"
```

```bash
npx wrangler login      # 首次：登录 Cloudflare 账号
npx wrangler deploy     # 部署
```

**无法自动化的部分：**

- **域名** — 需要先在 CF 上注册域名、配好 DNS，然后面板绑定 Worker route
- **Placement** — `wrangler.toml` 只支持 `mode = "smart"`（自动选择），指定具体 region（美西）需在 CF 面板设置一次，之后持久生效

## 日志

- 配置：`./config.json`
- 日志：`./log/llm-agent.log`
- 正常日志只写文件，不输出到终端

## 验证

```bash
GOCACHE=/tmp/go-build-cache go test ./...
```
