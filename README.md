# LLM SDK Demo

这是一个最小化的 Go + Web demo，支持：

- OpenAI-compatible
- Gemini
- 流式输出
- 本地配置保存
- 可选 Cloudflare Worker 转发

## 启动

```bash
go run ./cmd/llmagent
```

默认打开：

- `http://127.0.0.1:8787`

如果不想自动打开浏览器：

```bash
LLM_AGENT_NO_BROWSER=1 go run ./cmd/llmagent
```

## 配置

页面里分别配置两组 provider：

- OpenAI-compatible
- Gemini（走 `https://generativelanguage.googleapis.com/v1beta/openai` 的 OpenAI-compatible 入口）

默认只保存到项目目录：

- 配置：`./config.json`
- 日志：`./log/`

## Cloudflare Worker

`worker/worker.js` 提供一个受控转发示例，用于将请求转发到你配置的目标地址。

## 日志

启动时会先在控制台打印一条 `bootstrap` 信息，明确告诉你配置和日志的实际位置，且不会写到项目外路径。

## 验证

```bash
mkdir -p /private/tmp/go-build
GOCACHE=/private/tmp/go-build go test ./...
```
