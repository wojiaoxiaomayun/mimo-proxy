# MiMo Proxy 优化建议

> 审查日期：2026-06-14
> 审查范围：`main.go` / `internal/keypool/{keypool,handler,upstream}.go` / `internal/keypool/ui/*` / `go.mod` / `README.md`
> 本文按 **严重程度 → 优先级** 排序，每条都给出问题位置、影响与可落地的修复方向。

---

## 🔴 P0 · 安全（必须先处理）

### 1. 管理后台与所有管理 API 完全无鉴权

**位置**：`handler.go` 路由表（69-109 行），`/channels`、`/keys`、`/proxy-keys`、`/backup`、`/api/channels/{id}/keys`、`/health-check`、`/test-key`、`/models`、`/stats`、`/logs`、`/settings` 等。

**问题**：任何能访问该端口的人都可以直接读取全部上游 Key、生成/删除代理 Key、修改渠道、甚至通过 `GET /backup` 导出整个数据库（含所有明文 Key）。`POST /api/channels/{id}/keys` 还可被任意外部程序写入 Key。

**影响**：一旦服务暴露到公网或局域网，等于把所有上游账号密钥直接交出。

**建议**：
- 引入管理 Token（环境变量 `ADMIN_TOKEN`），用一个 `requireAdmin(next)` 中间件包裹所有 `/ui/*`、`/channels`、`/keys`、`/proxy-keys`、`/backup`、`/api/channels/*`、`/health-check` 等路由；UI 侧首次进入要求输入 Token，存到 `sessionStorage`，每次请求带 `Authorization: Bearer <admin_token>` 或 Cookie。
- 或者至少给 UI 加一个「管理密码」+ 登录态（存 `settings` 表的 bcrypt 哈希）。
- `POST /api/channels/{id}/keys` 这种对外接口单独要求一个 `X-Admin-Token`，不要和 UI 共用。

### 2. JSON 错误字符串拼接可产生非法响应 / 注入 ✅ 已修复

**位置**：例如 `handler.go:520`、`1163`、`1225`、`1279`、`1435`、`1453`、`1480`、`1489` 等大量处：
```go
http.Error(w, `{"error":"`+err.Error()+`"}`, ...)
```
只有 `ChannelAPIKeys`（520 行）做了 `strings.ReplaceAll(err.Error(), '"', '\\"')`，其他地方都没转义。一旦 `err.Error()` 里含 `"` 或换行，返回的 JSON 就损坏，前端 `JSON.parse` 抛错；恶意输入还可能注入额外字段。

**建议**：统一封装一个 `writeJSONError(w, status, msg)` 工具函数，内部用 `json.Marshal`。所有错误返回都改用它。

**修复**（2026-06-14）：在 `handler.go` 顶部新增三个工具函数：
- `writeJSONError(w, status, msg)` — 管理 API 风格 `{"error":"<msg>"}`
- `writeOpenAIError(w, status, msg, type, code)` — OpenAI 风格 `{"error":{"message","type","code"}}`
- `writeAnthropicError(w, status, msg, type)` — Anthropic 风格 `{"type":"error","error":{"type","message"}}`

三者均用 `json.Marshal` 安全转义，并统一设置 `Content-Type: application/json`（修复前所有错误响应均未设 Content-Type）。已替换所有 JSON 错误响应调用点（共 60+ 处），原 `ChannelAPIKeys` 的手动 `ReplaceAll` 转义也一并简化。仅保留 3 处 UI 静态资源（CSS/JS/HTML）的纯文本错误，它们不属于 JSON 上下文。`go build` + `go vet` 通过。

### 3. 流式响应缺内存/大小上限，日志无截断

**位置**：`handler.go:296-326`（OpenAI 流式）、`1838-1877`（Anthropic 流式）。
`streamBuf bytes.Buffer` 会把整条 SSE 流完整累积到内存，再 `go h.pool.LogRequest(... ResponseBody: streamBuf.String())`。日志表的 `response_body` 字段没有大小限制。

**影响**：长对话/大模型输出会持续吃内存；`request_logs` 表会快速膨胀（虽然有 1h 清理，但单条仍可达数 MB），`keys.db` 当前已 **314 MB**，部分原因即在此。

**建议**：
- 给 `streamBuf` 设上限（如 256KB，超出后只保留头部/尾部）。
- `LogRequest` 写库前对 `RequestBody/ResponseBody` 做截断（如各 64KB）。
- 考虑给「是否记录完整 body」加一个开关（settings 表），生产环境默认关闭或仅记前 4KB。

### 4. 上游 Key 在日志/返回体里存在泄露风险

**位置**：`/test-key`、`/test-mapping`、`/health-check` 等会把上游原始响应 `json.Unmarshal` 后**原样**返回给前端（`handler.go:605-621`、`1389-1405`）。部分上游错误体里会回显请求的 `api_key` 或 Authorization 头。

**建议**：返回给前端前，对已知敏感字段（`api_key`、`authorization`、`x-api-key`）做脱敏。

---

## 🟠 P1 · 稳定性 / 正确性

### 5. SQLite 未启用 WAL，写入并发下会锁死

**位置**：`keypool.go:112` `sql.Open("sqlite3", dbPath)`，DSN 没有任何 pragma。

**问题**：默认 rollback journal 模式，写时整库锁。`LogRequest` 是每个代理请求都会触发的写操作（虽然是 `go` 异步），高并发下与 `GetKey`/`IncrementUsage` 的写互相阻塞，表现为偶发 503/超时。

**建议**：DSN 改为：
```go
sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=on")
```
并设置 `db.SetMaxOpenConns(1)`（或对写连接单开）+ `SetMaxIdleConns`。WAL 还能让读不被写阻塞。

### 6. `http.Server` 未配置优雅关闭 / 超时

**位置**：`main.go:35` 直接 `http.ListenAndServe`。

**问题**：
- 无 `ReadTimeout`/`WriteTimeout`/`IdleTimeout`，慢连接可拖垮服务（流式虽需较长超时，但可用 `http.Server` + 每请求 `context` 控制）。
- 进程被 kill 时 `defer pool.Close()` 在 `ListenAndServe` 返回前不会执行，DB 没机会 flush，WAL 可能损坏。
- 后台清理 goroutine（`handler.go:112`）没有 `context` 取消，进程退出时泄漏。

**建议**：
```go
srv := &http.Server{Addr: ..., Handler: mux, ReadHeaderTimeout: 10*time.Second}
go srv.ListenAndServe()
<-sigChan // SIGINT/SIGTERM
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(ctx)
pool.Close()
```

### 7. `ResolveModelMapping` 的轮询游标并发不安全

**位置**：`keypool.go:1249-1308`。
该方法持有 `p.mu.Lock()`，里面先 `SELECT ... last_target_id`、再 `UPDATE ... last_target_id`，逻辑上是对的。但 **同一请求路径上 `GetKey()` 已经拿过锁**，两次锁之间有间隙；更关键的是 round-robin 的「下一个」是按 `lastTargetID` 在内存数组里找 index，DB 里存的 `last_target_id` 一旦被禁用/删除就找不到，会静默回退到 idx=0（实际行为：找不到就 idx 保持 0），这看起来像「总是命中第一个」。

**建议**：找不到 lastTargetID 时记录一行日志，或把游标改成存「下一个 position」而不是「上次 id」，语义更清晰。

### 8. `Messages`（Anthropic）的模型替换会重复拉取模型列表

**位置**：`handler.go:1773-1780`：
```go
models = h.getCachedModels(channel.ID)
if models == nil {
    models = h.refreshModels(...)  // 注意：没有 type 区分
}
```
而 OpenAI 路径走的是 `getOrFetchModels`（会自动套用 `AllowedModels` 过滤 + 类型参数）。两条路径不一致，导致 Anthropic 渠道的 `allowed_models` 过滤在 swap 阶段被绕过；并且 `refreshModels`（不带 Type）和 `refreshModelsWithType` 是两个函数，前者其实只是包了一层 `""`，行为相同，可以合并删除。

**建议**：Anthropic 路径也调用 `getOrFetchModels`，统一逻辑。

### 9. `settings` handler 是空壳

**位置**：`handler.go:1524-1527`，`/settings` 永远返回 `{}`，但前端有「设置」页且数据库有 settings 表（client_fingerprint 等）。
要么前端这个页是无用功能，要么后端漏实现。需要确认意图：是计划放配置项（如日志保留时长、自动禁用阈值）还没做？

**建议**：明确该页用途；若要保留，补 `GET /settings` 返回可暴露的配置 + `POST /settings` 更新。

### 10. `HealthCheck` 串行探测所有 Key

**位置**：`handler.go:1616` 单 for 循环，每个 key 最多重试 `threshold` 次、每次最长 15s。
50 个 key、阈值 3 的情况下，最坏要等 `50 * 3 * 15s = 37 分钟` 才返回，HTTP 客户端早就断开了，且没设置响应 `Content-Type`（最终才设，中途浏览器无法显示进度）。

**建议**：
- 并发探测（`errgroup` + 限流 semaphore）。
- 给 health-check 请求单独设短超时（如 8s）。
- 或改成异步任务：POST 触发后返回 task id，前端轮询结果。

### 11. `GetKey` 的失败计数没有持久化，重启即丢

`keyFails` 是纯内存 map（`handler.go:63`）。服务重启后连续失败计数清零，需要再失败 3 次才会禁用——这意味着重启可以"复活"一个本来要被禁用的坏 key（虽然它确实还会失败，但有窗口期）。

**建议**：可选——把连续失败计数写进 `api_keys` 的一个 `consecutive_fails` 列，重启后接着算。

### 12. 模型缓存失败时静默吞掉错误

`refreshModelsWithType`（`handler.go:691-728`）任何出错都 `return nil`，调用方 `swapModelIfNeeded` 看到 `len(models)==0` 就放行原始 model。如果上游短暂不可用，缓存的旧列表会过期→拉取失败→不再做替换，这通常是 OK 的；但完全没有日志，排错困难。

**建议**：拉取失败时 `log.Printf` 一行原因。

---

## 🟡 P2 · 性能 / 架构

### 13. 每个请求都新建 `http.Client` / `Transport`

**位置**：`upstream.go:39-82`，`ChatCompletions`、`Messages`、`TestKey`、`HealthCheck` 全部调用 `upstreamClientForChannel`，每次 new 一个 `Transport`（含 SOCKS5 时还会重新建 dialer）。`http.Transport` 内部维护连接池，频繁 new 等于每次都重新握手 TLS，**首字节延迟显著上升**。

**建议**：按 channel ID 缓存 `*http.Client`（在 `Handler` 里加 `clientCache map[int]*http.Client` + mutex），channel 更新时清缓存。

### 14. 模型列表每次缓存 miss 都同步阻塞代理请求

`ChatCompletions`（`handler.go:234`）走 `getOrFetchModels`，缓存过期时会**同步**等上游 `/v1/models` 返回（最长 10s），用户请求被卡住。模型列表变动频率极低。

**建议**：把刷新改成「后台刷新 + 继续用旧缓存」；只有完全没缓存时才同步阻塞一次。

### 15. `IncrementUsage` 每请求两次写库

`IncrementUsage`（`keypool.go:462-486`）先 `UPDATE api_keys` 再 `INSERT/UPDATE daily_usage`，两次事务。虽然异步执行，但配合上面 #5 的写锁仍是热点。

**建议**：合并成单个事务；或用内存计数器批量刷盘（如每 10s 一次）。

### 16. 日志清理粒度过粗 + 周期长

`CleanLogs`（`keypool.go:1370`）每 5 分钟删一次「1 小时前」的记录。清理间隔 5min 没问题，但 `DELETE FROM ... WHERE created_at < ...` 在大表上会产生大量空闲块，且 `keys.db` 不会自动收缩（已 314MB）。

**建议**：
- 定期（如每天）跑一次 `PRAGMA optimize` / `VACUUM`。
- 或改用「按天分表 + drop 整表」的策略，避免大 DELETE。

---

## 🟢 P3 · 工程化 / 可维护性

### 17. `go.mod` 模块名是占位符

```go
module github.com/myapp
```
`main.go` 里 `import "github.com/myapp/internal/keypool"`。这虽然能编译，但无法 `go install`，别人 fork 后 import 路径全是 `myapp`，且与实际仓库名 `mimo-proxy` 不一致。

**建议**：改成真实路径，如 `module github.com/<owner>/mimo-proxy`，全局替换 import。

### 18. 完全没有测试

`git ls-files` 显示无任何 `*_test.go`。`keypool.go` 里的 URL 拼接（`upstreamEndpointURLFromBase` / `messagesURLFromBase`）、`ResolveModelMapping` 的轮询逻辑、`swapModelIfNeeded`、迁移逻辑都是纯函数级、易测的，且都是最容易出 bug 的地方。

**建议**：至少补：
- `upstreamEndpointURLFromBase` 各种 base URL 形态的表驱动测试。
- `ResolveModelMapping` round-robin / failover 的单元测试（用临时 sqlite）。
- `extractModel` / `replaceModel` / `swapModelIfNeeded` 边界用例。
- 迁移逻辑：用一个旧 schema 的 db 文件跑 `New()` 验证升级正确。

### 19. 无 Dockerfile / 无 CI / 无 Makefile / 无 release

纯手 `go build`。对自用项目够用，但若要部署到多机或交给别人，缺三件套。

**建议**：
- `Dockerfile`（多阶段构建，`CGO_ENABLED=1` 需要 gcc，可用 `golang:alpine` + `musl-dev` 或 `golang:1.25-bookworm`）。
- `.github/workflows/ci.yml`：`go test ./...` + `go build` + golangci-lint。
- `Makefile`：`make build / run / test / clean`。

### 20. README 与实际路由严重不符

**位置**：`README.md`。
- README 写的是 `/v1/chat/completions`、`/v1/models`、`/c/{prefix}/v1/...`，但代码里实际是 `/openai/v1/...`、`/anthropic/v1/...`、`/c/{channel}/v1/...`（注意是 `{channel}` 不是 `{prefix}`，且按 ID 还是 prefix 取决于 `GetChannelByPrefix`）。
- README 完全没提 Anthropic `/v1/messages`、模型映射、模型替换、上游代理、备份导入等已实现的核心功能。
- README「项目结构」缺 `mappings.html`、`upstream.go`。
- 「配置」表只列了 `--port` 和 `MIMO_API_KEYS`，实际默认端口代码里是 `10081`（`main.go:14`），README 写 `10080`。

**建议**：照着当前路由表和功能重写 README 的「路由结构」「API 接口」「功能特性」「配置」四节。

### 21. 前端 sidebar 在 logs.html 里硬编码、和 common.js 重复

`common.js` 里有一个 `sidebarHTML(activePage)` 函数（83-110 行）但 `logs.html` 没用它，而是把整段 sidebar（含图标、4 个 nav item）**手写了一遍**（10-36 行）。但 common.js 的 pages 列表里 **没有 mappings 项**（只有 keys/channels/logs/settings），而 logs.html 手写版却有「映射」入口。两边不一致，改 nav 要改两处。

**建议**：统一让所有页面都调用 `sidebarHTML()`，并把 `mappings` 加进 `common.js` 的 pages 数组。

### 22. 前端定时器未清理

`logs.html` 末尾 `setInterval(loadLogs, 5000)` 永不清除，且每次 `showLogDetail` 等异步操作失败时不重置。多个页面来回切换若有同名 timer 变量会互相覆盖（目前每个页独立文件，问题不大，但模式不佳）。

**建议**：用 `pagehide`/`visibilitychange` 暂停轮询。

### 23. 大量提交进 git 的二进制 / 临时文件

`dir` 显示仓库根有 `mimo-proxy.exe`、`mimo-proxy.exe~`、`myapp.exe`、`proxy.exe`、`keys.db`（314MB）。`.gitignore` 里写了 `*.exe` 和 `*.db`，但 `mimo-proxy.exe` 是否已被 track 过需确认（`git ls-files` 没列出它们，说明目前是 untracked，OK）。`screenshot.png` 在 `.gitignore` 里但 `git ls-files` 里有它——已被 track，gitignore 对已 track 文件无效。

**建议**：`git rm --cached screenshot.png`，让 gitignore 真正生效。

### 24. main.go 硬编码默认上游 URL

`main.go:17` `targetURL := "https://api.xiaomimimo.com/v1/chat/completions"` 写死在代码里，只在「首次创建默认渠道」时用到（`keypool.go:149`）。换个部署环境就得改代码。

**建议**：改成从 env（如 `DEFAULT_UPSTREAM_URL`）读取，env 缺省再 fallback 到硬编码。

---

## 📋 推荐实施顺序

| 阶段 | 内容 | 大致工作量 |
|------|------|-----------|
| **第一波（安全收口）** | #1 管理鉴权、~~#2 JSON 错误封装~~ ✅、#3 日志截断、#4 上游响应脱敏 | 1-2 天 |
| **第二波（稳定基线）** | #5 SQLite WAL、#6 优雅关闭、#8 Anthropic 路径统一、#10 health-check 并发 | 1 天 |
| **第三波（文档对齐）** | #20 README 重写、#17 模块名、#9 settings handler 定性 | 半天 |
| **第四波（工程化）** | #18 测试、#19 Docker/CI、#21 前端 sidebar 统一 | 2-3 天 |
| **第五波（性能）** | #13 client 缓存、#14 模型列表后台刷新、#15 usage 合并事务 | 1-2 天 |

---

## 附：已发现的「文档与代码不一致」清单

| 项 | README 描述 | 代码实际 |
|----|------------|---------|
| 默认端口 | `10080` | `10081`（`main.go:14`） |
| 默认渠道路由 | `/v1/chat/completions`、`/v1/models` | `/openai/v1/...`、`/anthropic/v1/...`、`/c/{channel}/v1/...` |
| 路径变量 | `/c/{prefix}/...` | `/c/{channel}/...`（实际通过 prefix 解析，但路径占位符叫 channel） |
| 支持协议 | 仅 OpenAI | OpenAI + Anthropic Messages + count_tokens |
| 模型映射 / 上游代理 / 备份导入 | 未提及 | 均已实现 |
| 项目结构 | 缺 `mappings.html`、`upstream.go` | 实际存在 |
