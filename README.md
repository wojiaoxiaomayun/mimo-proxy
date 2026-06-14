# MiMo Proxy

OpenAI 兼容的 API 代理服务，支持多渠道、多 Key 轮询、请求日志与访问控制。

## 功能特性

- **多渠道管理** — 每个渠道独立的上游 API 地址与路由前缀，支持动态增删改、设为默认
- **双协议支持** — 每个渠道可指定类型：OpenAI（`/v1/chat/completions`）或 Anthropic（`/v1/messages`）
- **Key 池调度** — 按渠道管理上游 API Key，支持轮询（round-robin）/ 故障切换（failover）/ 固定 Key（pinned），可启用/禁用/备注
- **模型映射** — 将客户端请求的模型名映射到一个或多个目标模型，支持轮询/故障切换调度
- **代理认证** — 生成 `sk-xxx` 代理 Key，客户端必须携带才能访问
- **请求日志** — 记录最近 1 小时的请求详情（含 request/response body），自动清理
- **健康检查** — 手动测试所有 Key 可用性，连续失败自动禁用
- **自动熔断** — 代理调用中 401/403 连续 3 次自动禁用该 Key
- **Token 统计** — 按 Key 记录 prompt/completion/total tokens
- **流式支持** — 完整代理 SSE 流式响应，同时记录完整流内容到日志
- **上游代理** — 渠道级配置 HTTP/SOCKS 上游代理（`proxy_url`）
- **备份恢复** — 一键导出/导入全量配置（渠道、Key、代理 Key、模型映射）
- **Web UI** — shadcn 风格管理界面，Lucide 图标

## 快速开始

```bash
# 编译
CGO_ENABLED=1 go build -o proxy .

# 运行（默认端口 10081）
./proxy

# 自定义端口
./proxy --port 8080
```

启动后打开 `http://localhost:10081` 进入管理界面。

## 使用流程

### 1. 添加渠道

在 **渠道** 页面添加上游 API 渠道：

| 字段 | 示例 |
|------|------|
| 名称 | MiMo 主力 |
| 前缀 | mimo |
| Base URL | `https://api.xiaomimimo.com/v1/chat/completions` |

每个渠道会生成代理 URL：

- OpenAI 格式：`http://localhost:10081/c/{prefix}/v1/chat/completions`
- Anthropic 格式：`http://localhost:10081/c/{prefix}/v1/messages`

可将某个渠道设为 **默认**，默认渠道同时响应 `/openai/v1/...` 与 `/anthropic/v1/...` 路由。

### 2. 添加上游 Key

在 **Key 管理** 页面为对应渠道添加上游 API Key，支持备注标识来源。

### 3. 生成代理 Key

在 **设置** 页面生成 `sk-xxx` 代理 Key，客户端调用时必须携带。

### 4. 客户端调用

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:10081/c/mimo/v1",
    api_key="sk-xxx"  # 代理 Key
)

response = client.chat.completions.create(
    model="mimo-v2.5-pro",
    messages=[{"role": "user", "content": "Hello"}]
)
```

## 路由结构

```
/                                           Web 管理界面（/ui/...）
/openai/v1/chat/completions                 默认渠道 - OpenAI Chat Completions
/openai/v1/models                           默认渠道 - OpenAI 模型列表
/anthropic/v1/messages                      默认渠道 - Anthropic Messages
/anthropic/v1/messages/count_tokens         默认渠道 - Anthropic Token 计数
/anthropic/v1/models                        默认渠道 - Anthropic 模型列表
/c/{prefix}/v1/chat/completions             指定渠道 - OpenAI Chat Completions
/c/{prefix}/v1/models                       指定渠道 - OpenAI 模型列表
/c/{prefix}/v1/messages                     指定渠道 - Anthropic Messages
/c/{prefix}/v1/messages/count_tokens        指定渠道 - Anthropic Token 计数
```

## API 接口

### 代理接口（需要代理 Key）

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/openai/v1/chat/completions` | OpenAI Chat Completions（默认渠道） |
| `GET`  | `/openai/v1/models` | OpenAI 模型列表（默认渠道） |
| `POST` | `/anthropic/v1/messages` | Anthropic Messages（默认渠道） |
| `POST` | `/anthropic/v1/messages/count_tokens` | Anthropic Token 计数（默认渠道） |
| `GET`  | `/anthropic/v1/models` | Anthropic 模型列表（默认渠道） |
| `POST` | `/c/{prefix}/v1/chat/completions` | OpenAI Chat Completions（指定渠道） |
| `GET`  | `/c/{prefix}/v1/models` | OpenAI 模型列表（指定渠道） |
| `POST` | `/c/{prefix}/v1/messages` | Anthropic Messages（指定渠道） |
| `POST` | `/c/{prefix}/v1/messages/count_tokens` | Anthropic Token 计数（指定渠道） |

请求头：`Authorization: Bearer sk-xxx`

### 管理接口

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET/POST` | `/channels` | 渠道管理 |
| `GET/POST` | `/keys` | 上游 Key 管理 |
| `GET/POST` | `/api/channels/{id}/keys` | 指定渠道下的上游 Key（渠道详情用） |
| `GET/POST` | `/proxy-keys` | 代理 Key 管理 |
| `GET/POST` | `/model-mappings` | 模型映射管理 |
| `POST` | `/test-mapping` | 测试模型映射 |
| `GET` | `/stats` | 统计数据 |
| `GET` | `/logs` | 请求日志（支持 `?page=` 和 `?id=`） |
| `POST` | `/test-key` | 测试上游 Key |
| `POST` | `/health-check` | 健康检查 |
| `GET` | `/models` | 获取上游模型列表（内部用） |
| `GET/POST` | `/backup` | 配置备份导出/导入 |

## 项目结构

```
.
├── main.go                        入口，--port 参数
├── internal/keypool/
│   ├── keypool.go                 数据库层（channels/api_keys/proxy_keys/request_logs/settings/model_mappings）
│   ├── handler.go                 HTTP 路由与处理
│   ├── upstream.go                上游请求客户端（支持渠道级 HTTP/SOCKS 代理）
│   └── ui/                        前端页面（嵌入到二进制）
│       ├── style.css              共享样式
│       ├── common.js              共享脚本（图标、工具函数）
│       ├── keys.html              Key 管理页
│       ├── channels.html          渠道管理页
│       ├── mappings.html          模型映射页
│       ├── logs.html              请求日志页
│       └── settings.html          设置页
├── go.mod
└── keys.db                        SQLite 数据库（运行时生成）
```

## 配置

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--port` | `10081` | 监听端口 |
| `MIMO_API_KEYS` 环境变量 | - | 启动时自动导入的上游 Key（逗号分隔） |

## 技术栈

- **后端**：Go + `net/http` + SQLite（`go-sqlite3`）
- **前端**：单文件 HTML + CSS 变量 + 原生 JS
- **图标**：Lucide（通过 better-icons 获取）
- **设计**：shadcn/ui 风格（中性色板、圆角、focus ring）

## License

MIT
