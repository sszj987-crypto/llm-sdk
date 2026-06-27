# macOS LLM Agent 最小化 Demo 设计

## 目标

做一个最小化的 Go demo：

- 启动后自动打开本地网页
- 支持配置两个 provider：`OpenAI-compatible` 和 `Gemini`
- 支持保存 `API Key`、`Model`、`Base URL` 等配置
- 支持单轮聊天和流式输出
- 前端实时展示模型返回内容

## 范围

### 先做

- 本地 Go HTTP 服务
- 单页 Web UI
- 配置读写到本地 JSON
- 一个统一聊天入口
- 两个 provider 适配器：
  - OpenAI-compatible
  - Gemini
- 流式响应转发到前端

### 暂不做

- 多会话
- 历史记录
- 账号系统
- 原生 macOS `.app` 打包
- 多模型自动发现
- 复杂权限管理

## 架构

### 组件

- `cmd/app/main.go`
  - 启动 HTTP 服务
  - 自动打开浏览器
- `internal/config`
  - 读取/保存配置
- `internal/server`
  - 提供页面和 API
- `internal/provider`
  - OpenAI-compatible 适配器
  - Gemini 适配器
- `web/`
  - HTML、CSS、JS

### 数据流

1. 用户在页面里选择 provider 并填写配置
2. Go 后端把配置保存到本地 JSON
3. 用户发送消息
4. 后端根据 provider 选择对应适配器
5. 适配器构造上游请求
6. 上游返回流式内容
7. 后端把流式内容原样或按统一格式转发给前端
8. 前端把内容实时追加到聊天窗口

## Provider 设计

### OpenAI-compatible

- 目标接口保持 `chat/completions` 风格
- 支持 `stream: true`
- 适配器负责将统一消息结构映射到 OpenAI 请求体

### Gemini

- 使用 Gemini 原生接口
- 适配器负责将统一消息结构映射为 Gemini 请求体
- 将流式返回统一转换成前端可消费的数据片段

### 统一抽象

后端内部只认一种统一消息结构：

- `role`
- `content`

这样前端不需要关心不同厂商的协议差异。

## Worker 设计

Worker 只做受控转发，不做任意目标代理。

- 通过固定上游地址或白名单配置转发
- 只允许已配置的路径
- 透传必要请求头
- 保持流式响应
- 返回前清理不该暴露的头部

## API 设计

### `GET /api/config`

返回当前保存的配置。

### `POST /api/config`

保存 provider 配置。

### `POST /api/chat`

发起聊天请求，参数包含：

- provider
- model
- apiKey
- baseURL
- messages
- stream

后端根据 provider 选择适配器并流式转发结果。

## UI 设计

页面分两块：

- 配置区
  - provider 下拉框
  - `API Key`
  - `Model`
  - `Base URL`
  - 保存按钮
- 聊天区
  - 输入框
  - 发送按钮
  - 流式输出区域

切换 provider 时，只显示对应配置项。

## 错误处理

- 配置缺失时给出明确提示
- 上游请求失败时显示错误信息
- 流式中断时保留已返回内容
- 解析失败时返回可读错误

## 验收标准

- 能保存配置
- 能切换 `OpenAI-compatible` 和 `Gemini`
- 能发起聊天
- 能看到流式输出
- 重启后配置仍然存在

## 下一步实现顺序

1. 初始化 Go 项目
2. 实现配置存储
3. 实现本地 HTTP 服务
4. 实现前端页面
5. 实现 OpenAI-compatible 适配器
6. 实现 Gemini 适配器
7. 接入流式返回
8. 补 Worker 代码
9. 做最小联调验证
