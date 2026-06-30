# LLM Agent

不通过 VPN，直连国外大模型。

## 原理

通过 Cloudflare Worker 做代理转发，绕过 GFW 对 Google / OpenAI API 域名的封锁：

1. **申请域名**，在 Cloudflare 上添加 DNS 记录指向 Worker
2. **Worker 部署**：`worker/worker.js`，placement 设为 **region-美西**（避免出口 IP 被 Gemini 等服务的地区限制拦截）
3. **Cloudflare 优选 IP**：通过 IPDB 查询 Cloudflare 最优边缘 IP，避免 DNS 解析被 GFW 污染
4. **SNI 伪装**：请求 SNI 指向你的 Worker 域名，实际目标地址通过 `X-Target-Url` 头传递

## 性能对比

| 场景 | TLS 握手完成 | 首字返回 |
|------|------------|---------|
| 开启 VPN | ~450ms | ~1.65s |
| Cloudflare Worker（不开 VPN） | ~175ms | ~1.2s |

不开 VPN 反而更快，因为绕过了 VPN 多一跳的延迟。

## 启动

```bash
go run ./cmd/llmagent
```

默认打开 `http://127.0.0.1:8787`。

不自动打开浏览器：

```bash
LLM_AGENT_NO_BROWSER=1 go run ./cmd/llmagent
```

## 配置

在页面中填写：

- **URL**：API 地址（如 `https://generativelanguage.googleapis.com/v1beta/openai`）
- **API Key**：你的 API Key
- **Model**：模型名称（如 `gemini-2.5-flash`）
- **Worker URL**：你的 Cloudflare Worker 地址（如 `https://sszj.me`），填写后请求会走 Worker 代理

配置保存在项目目录 `config.json`。

## Cloudflare Worker

`worker/worker.js` 是一个转发代理，配置要点：

- Worker 的 **placement** 设为美国西部（Region - US West），确保出口 IP 不被 API 地区限制
- 请求通过 `X-Target-Url` 指定实际目标，Worker 透明转发

## 日志

启动时控制台打印配置和日志路径：

- 配置：`./config.json`
- 日志：`./log/llm-agent.log`

## 验证

```bash
GOCACHE=/tmp/go-build go test ./...
```
