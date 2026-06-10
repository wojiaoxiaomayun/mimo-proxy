---
title: "Messages 协议"
description: |
  通过 Anthropic Messages API 格式访问 Anthropic、DeepSeek、DashScope、Xiaomi、Kimi、MiniMax 和 Zai 模型。
---

Messages 协议以 **Anthropic Messages API** 格式暴露在 `POST /v1/messages`。它是 OpenModel 上覆盖最广的协议，服务七家供应商：**Anthropic、DeepSeek、DashScope、Xiaomi、Kimi、MiniMax 和 Zai**。

由于所有供应商共享相同的端点与请求结构，你对它们全部使用 **Anthropic SDK**——切换供应商只需更改 `model` 参数，无需修改其他代码。安装与完整示例参见 [Anthropic SDK 指南](/docs/sdks/anthropic-sdk)。

## 端点

```
POST https://api.openmodel.ai/v1/messages
```

另有一个端点 `POST /v1/messages/count_tokens`，用于在不创建消息的情况下计算 Token 数（仅 Anthropic）。

## 认证

所有供应商都使用同一个 OpenModel API 密钥。可作为 Bearer 令牌，或通过 `X-Api-Key` 请求头传入：

```
Authorization: Bearer om-your-api-key
```

`anthropic-version` 请求头为兼容 SDK 而接受，默认为 `2023-06-01`。

## 切换供应商

要切换到不同的供应商，只需更改 `model` 参数：

```python
# DeepSeek
client.messages.create(model="deepseek-v4-flash", max_tokens=1024, messages=[...])
# DashScope
client.messages.create(model="qwen3-max", max_tokens=1024, messages=[...])
# Xiaomi
client.messages.create(model="mimo-v2.5-pro", max_tokens=1024, messages=[...])
# Kimi
client.messages.create(model="kimi-k2.5", max_tokens=1024, messages=[...])
# MiniMax
client.messages.create(model="MiniMax-M2.5", max_tokens=1024, messages=[...])
# Zai
client.messages.create(model="glm-5", max_tokens=1024, messages=[...])
```

## 供应商差异与在线试用

在下方选择供应商，即可查看其示例模型、错误响应格式，以及预填了该供应商模型的交互式 playground。多数供应商的行为与 Anthropic 基准完全一致，仅在文档列出的差异点有所不同。

## Anthropic

`POST /v1/messages`

创建消息。此端点完全兼容 [Anthropic Messages API](https://docs.anthropic.com/en/api/messages)，只需更改 Base URL 即可使用 Anthropic SDK。

### 请求头参数

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `anthropic-version` | string | 是 | 必填。指定 Anthropic API 版本。 (默认: `"2023-06-01"`) |

### 请求体 (application/json)

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `model` | string | 是 | The model ID to use (e.g. claude-sonnet-4-6, claude-opus-4-20250514). |
| `messages` | object[] | 是 | 表示对话的消息对象数组。 |
| `max_tokens` | integer | 是 | 停止前可生成的最大 Token 数。 |
| `system` | string \| object[] | 否 | 设置助手上下文和行为的系统提示词。 |
| `stream` | boolean | 否 | 是否使用服务器发送事件 (SSE) 流式返回响应。 (默认: `false`) |
| `temperature` | number | 否 | 响应中的随机程度。默认为 1.0。 |
| `top_p` | number | 否 | 核采样。使用 0 到 1 之间的值。 |
| `top_k` | integer | 否 | 每个后续 Token 仅从前 K 个选项中采样。 |
| `tools` | object[] | 否 | 模型可能使用的工具定义。 |
| `tool_choice` | object | 否 | 控制模型如何使用提供的工具。 |
| `metadata` | object | 否 | 描述请求元数据的对象。 |
| `stop_sequences` | string[] | 否 | 自定义文本序列，遇到时模型将停止生成。 |

#### 示例

```json
{
  "model": "claude-sonnet-4-6",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Explain the concept of recursion in programming."
    }
  ]
}
```

### 响应

#### 200 — 成功响应

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Recursion is a programming technique where a function calls itself to solve a problem by breaking it down into smaller, identical subproblems."
    }
  ],
  "model": "claude-sonnet-4-6",
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 15,
    "output_tokens": 168
  }
}
```

#### 400 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 401 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 429 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

## Anthropic — Count Tokens

`POST /v1/messages/count_tokens`

在不创建消息的情况下计算消息负载中的 Token 数量。可用于估算成本、检查输入是否在上下文窗口限制内以及优化提示词。

### 请求头参数

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `anthropic-version` | string | 是 | 必填。指定 Anthropic API 版本。 (默认: `"2023-06-01"`) |

### 请求体 (application/json)

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `model` | string | 是 | 用于分词的模型 ID。不同模型可能以不同方式进行分词。 |
| `messages` | object[] | 是 | 要计算 Token 数的消息数组。使用与 Messages API 相同的格式。 |
| `system` | string \| object[] | 否 | 包含在 Token 计数中的系统提示词。 |
| `tools` | object[] | 否 | 包含在 Token 计数中的工具定义。 |

#### 示例

```json
{
  "model": "claude-sonnet-4-20250514",
  "messages": [
    {
      "role": "user",
      "content": "What is the meaning of life?"
    }
  ]
}
```

### 响应

#### 200 — 成功响应

```json
{
  "input_tokens": 15
}
```

#### 400 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 401 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

## DeepSeek

`POST /v1/messages`

使用 DeepSeek 模型创建消息。此端点使用 [Anthropic Messages API](https://docs.anthropic.com/en/api/messages) 格式，只需更改 Base URL 即可使用 Anthropic SDK。

DeepSeek 模型（如 deepseek-v4-flash、deepseek-v4-pro）通过与 Anthropic 模型相同的 `/v1/messages` 端点访问。

### 请求头参数

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `anthropic-version` | string | 是 | 必填。指定 Anthropic API 版本。 (默认: `"2023-06-01"`) |

### 请求体 (application/json)

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `model` | string | 是 | 要使用的 DeepSeek 模型 ID（如 deepseek-v4-flash、deepseek-v4-pro）。 |
| `messages` | object[] | 是 | 表示对话的消息对象数组。 |
| `max_tokens` | integer | 是 | 停止前可生成的最大 Token 数。 |
| `system` | string \| object[] | 否 | 设置助手上下文和行为的系统提示词。 |
| `stream` | boolean | 否 | 是否使用服务器发送事件 (SSE) 流式返回响应。 (默认: `false`) |
| `temperature` | number | 否 | 响应中的随机程度。范围 0.0 到 2.0。默认为 1.0。 |
| `top_p` | number | 否 | 核采样。使用 0 到 1 之间的值。 |
| `top_k` | integer | 否 | 每个后续 Token 仅从前 K 个选项中采样。DeepSeek 会忽略此参数（仅为兼容 Anthropic API 而接受）。 |
| `tools` | object[] | 否 | 模型可能使用的工具定义。 |
| `tool_choice` | object | 否 | 控制模型如何使用提供的工具。DeepSeek 会忽略 `disable_parallel_tool_use`。 |
| `metadata` | object | 否 | 描述请求元数据的对象。DeepSeek 会忽略此字段（仅为兼容 Anthropic API 而接受）。 |
| `stop_sequences` | string[] | 否 | 自定义文本序列，遇到时模型将停止生成。 |

#### 示例

```json
{
  "model": "deepseek-v4-flash",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Explain the concept of recursion in programming."
    }
  ]
}
```

### 响应

#### 200 — 成功响应

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Recursion is a programming technique where a function calls itself to solve a problem by breaking it down into smaller, identical subproblems."
    }
  ],
  "model": "deepseek-v4-flash",
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 15,
    "output_tokens": 168
  }
}
```

#### 400 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 401 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 429 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

## DashScope

`POST /v1/messages`

使用 DashScope（阿里云百炼）模型创建消息。此端点使用 [Anthropic Messages API](https://docs.anthropic.com/en/api/messages) 格式，只需更改 Base URL 即可使用 Anthropic SDK。

DashScope 模型（如 qwen3-max）通过与 Anthropic 模型相同的 `/v1/messages` 端点访问。

有关 Messages 格式的供应商特定详情，请参阅 [DashScope Anthropic API 文档](https://help.aliyun.com/zh/model-studio/anthropic-api-messages)。

### 请求头参数

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `anthropic-version` | string | 是 | 必填。指定 Anthropic API 版本。 (默认: `"2023-06-01"`) |

### 请求体 (application/json)

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `model` | string | 是 | 要使用的 DashScope 模型 ID（如 qwen3-max）。 |
| `messages` | object[] | 是 | 表示对话的消息对象数组。 |
| `max_tokens` | integer | 是 | 停止前可生成的最大 Token 数。 |
| `system` | string \| object[] | 否 | 设置助手上下文和行为的系统提示词。 |
| `stream` | boolean | 否 | 是否使用服务器发送事件 (SSE) 流式返回响应。 (默认: `false`) |
| `temperature` | number | 否 | 响应中的随机程度。默认为 1.0。 |
| `top_p` | number | 否 | 核采样。使用 0 到 1 之间的值。 |
| `top_k` | integer | 否 | 每个后续 Token 仅从前 K 个选项中采样。 |
| `tools` | object[] | 否 | 模型可能使用的工具定义。 |
| `tool_choice` | object | 否 | 控制模型如何使用提供的工具。 |
| `metadata` | object | 否 | 描述请求元数据的对象。 |
| `stop_sequences` | string[] | 否 | 自定义文本序列，遇到时模型将停止生成。 |

#### 示例

```json
{
  "model": "qwen3-max",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Explain the concept of recursion in programming."
    }
  ]
}
```

### 响应

#### 200 — 成功响应

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Recursion is a programming technique where a function calls itself to solve a problem by breaking it down into smaller, identical subproblems."
    }
  ],
  "model": "qwen3-max",
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 15,
    "output_tokens": 168
  }
}
```

#### 400 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 401 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 429 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

## Xiaomi

`POST /v1/messages`

使用 Xiaomi（小米 MiMo）模型创建消息。此端点使用 [Anthropic Messages API](https://docs.anthropic.com/en/api/messages) 格式，只需更改 Base URL 即可使用 Anthropic SDK。

Xiaomi 模型（如 mimo-v2.5-pro、mimo-v2-flash）通过与 Anthropic 模型相同的 `/v1/messages` 端点访问。

有关供应商特定详情，请参阅 [Xiaomi MiMo Anthropic API 文档](https://platform.xiaomimimo.com/docs/zh-CN/api/chat/anthropic-api)。

### 请求头参数

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `anthropic-version` | string | 是 | 必填。指定 Anthropic API 版本。 (默认: `"2023-06-01"`) |

### 请求体 (application/json)

| 参数 | 类型 | 必填 | 描述 |
| --- | --- | --- | --- |
| `model` | string | 是 | 要使用的 Xiaomi MiMo 模型 ID（如 mimo-v2.5-pro、mimo-v2-pro、mimo-v2.5、mimo-v2-omni、mimo-v2-flash）。 |
| `messages` | object[] | 是 | 表示对话的消息对象数组。 |
| `max_tokens` | integer | 是 | 停止前可生成的最大 Token 数。 |
| `system` | string \| object[] | 否 | 设置助手上下文和行为的系统提示词。 |
| `stream` | boolean | 否 | 是否使用服务器发送事件 (SSE) 流式返回响应。 (默认: `false`) |
| `temperature` | number | 否 | 响应中的随机程度。默认为 1.0。 |
| `top_p` | number | 否 | 核采样。使用 0 到 1 之间的值。 |
| `top_k` | integer | 否 | 每个后续 Token 仅从前 K 个选项中采样。 |
| `tools` | object[] | 否 | 模型可能使用的工具定义。 |
| `tool_choice` | object | 否 | 控制模型如何使用提供的工具。 |
| `metadata` | object | 否 | 描述请求元数据的对象。 |
| `stop_sequences` | string[] | 否 | 自定义文本序列，遇到时模型将停止生成。 |

#### 示例

```json
{
  "model": "mimo-v2.5-pro",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Explain the concept of recursion in programming."
    }
  ]
}
```

### 响应

#### 200 — 成功响应

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Recursion is a programming technique where a function calls itself to solve a problem by breaking it down into smaller, identical subproblems."
    }
  ],
  "model": "mimo-v2.5-pro",
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 15,
    "output_tokens": 168
  }
}
```

#### 400 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 401 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```

#### 429 — 错误响应（Anthropic 格式）

```json
{
  "type": "string",
  "error": {
    "type": "string",
    "message": "string"
  }
}
```
