# cpa-provider-core

CLIProxyAPI 供应商插件（Wokey、Ollama、OpenCode、OpenDesign）共用的核心代码：插件 RPC 入口、执行请求、流式处理、请求日志、出口代理、凭证、管理接口，以及管理页的公共前端代码。

各供应商插件仓库只保留自己的适配器，通过 `go.mod` 引用本模块，并在构建管理页时从本模块拷贝公共前端代码。本仓库不单独发布插件。

目前 Ollama 和 OpenCode 已经迁到本模块，Wokey 和 OpenDesign 还没有迁。

## 包

| 包 | 内容 |
| --- | --- |
| `provider` | 插件和核心之间的约定：`Spec`、`Adapter`、可选接口、`Catalog`、`Env`、协议 |
| `core` | `Service`：RPC 分发、注册、凭证解析、模型注册与目录同步、执行和流式请求、额度查询、管理接口、出口检测 |
| `coretest` | RPC 快照测试的公共 runner、假 CPA 宿主和脚本化上游 |
| `config` | 插件配置子树的解析；`Profile` 是各供应商的默认值和说明文字 |
| `credential` | auth 文件的读取、生成和 CPA auth 数据 |
| `egress` | 出口解析（凭证代理、CPA 全局代理、直连）、每个出口的 HTTP 客户端和排队 |
| `proxystore` | 管理页的代理列表（`proxies.json`） |
| `requestlog` | SQLite 请求日志，含生成速度和端到端速度 |
| `streamgate` | SSE 拆分和闸门：首个输出之前的错误事件仍按失败返回，没有结束事件的流报截断 |
| `payload` | 只在顶层按字节修改请求体，不整体解码 |
| `upstream` | 上游错误到插件错误的映射、带 Bearer 的 JSON GET、数字读取 |
| `jsonfield`、`flags` | JSON 成员扫描；出口检测用的国旗 SVG |

## 适配器约定

插件的 `main.go` 只保留 cgo 外壳，构造时调用 `core.New(<id>.New(), pluginVersion, callHost)`。

```go
type Adapter interface {
	Spec() Spec                          // ID、显示名、仓库、配置 Profile、管理页和 logo、各家文案、差异开关
	Endpoint(protocol Protocol) string   // Chat / Responses / Messages 对应的路径，也是请求日志里的 endpoint
	Catalog() CatalogSource              // 模型目录（Current / Fetch）

	VerifyKey(ctx context.Context, env Env, client *http.Client, cred credential.Credential) *pluginabi.Error
	AccountKey(env Env, cred credential.Credential) string
	FetchAccount(env Env, client *http.Client, cred credential.Credential) (info any, complete bool, perr *pluginabi.Error)
	Quota(info any) pluginapi.QuotaFetchResponse
}

type CatalogSource interface {
	Current() Catalog
	Fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (Catalog, error)
}

type Catalog interface {
	Summary() CatalogSummary             // Count、Revision、FetchedAt、CheckedAt、LastError、DetailErrors
	Has(id string) bool
	ModelInfos(all bool, ids []string) []pluginapi.ModelInfo
	UpstreamID(model, prefix string) string
	ModelsView() any                     // 管理页模型列表，必须编码成 JSON 数组
}
```

可选接口（核心用类型断言检查）：

- `HeaderDecorator`：`Decorate(header http.Header, in CallInput)`，给执行请求加自己的头。OpenCode 用它加 `X-Opencode-Session`。
- `TokenCounter`：`CountTokensEndpoint() string`。实现了就把 count_tokens 当 Messages 请求转发，不写请求日志；没实现就回 501 `<id>: count_tokens is not supported by <显示名>`。

`FetchAccount` 在凭证出口的排队轮次里调用；`complete` 为真时结果缓存一分钟，返回错误时 info 丢弃。`VerifyKey` 也在轮次里调用，`ctx` 限定整个检查（20 秒，含排队）。

由 `ID` 和 `DisplayName` 推出、各家写法一致的字符串：User-Agent `cpa-<id>-plugin/<版本>`，错误和日志前缀 `<id>: `，请求 ID 头 `X-<显示名>-Cpa-Request-Id`，管理路由 `/v0/management/plugins/<id>/...`，以及状态、代理导入、管理页菜单的说明。写法不一致的放在 `Spec.Texts`。

## 第二轮统一时去掉的差异开关

这些开关只是为了让第一轮迁移逐字节不变，保留了各插件原来的差异。第二轮统一时应该去掉：

- `Spec.StreamErrors`：流内错误关键字表。Ollama 用 `StreamErrorsBase`，OpenCode 用 `StreamErrorsExtended`（多了 budget、usage limit、insufficient funds、AuthError、MissingSession 等）。统一成扩展表即可。
- `Spec.RequestMembers`：核心扫描请求体时额外要找的顶层成员。OpenCode 为了算会话 ID 多找 `prompt_cache_key`、`metadata`、`messages`、`input`、`system`、`instructions`。扫描在全部找到后停止，所以成员越多，越可能扫到请求体后面的格式错误；这会影响是否补 `stream_options`、是否记录推理强度。统一时让所有插件扫同一组成员。
- count_tokens 的请求解码：核心总是先解码执行请求，解码失败回 `invalid_request`，然后才判断是否支持（Ollama 和 OpenCode 原来就是这样）。Wokey 和 OpenDesign 不解码、直接回 501，迁移时需要加开关或者统一成先解码。

## Wokey 和 OpenDesign 迁移还需要的接入点

还没有做，迁移时在核心里加：

- Wokey：转发 `X-End-User-Id` 请求头；上游 403 带 `request_blocked`、`model_denylisted` 时改成 400（`upstream.Errors` 里加封禁码）；凭证的 `issuer_token`（录入、校验、视图里的 `issuer_token_masked`）；额度查询带 issuer 的那部分和 quota.fetch 的额外检查；请求日志迁移旧的 `requests.jsonl` 和改名前的 `requests.db`；count_tokens 不解码。快照用的 `seam.refresh_tick` 伪方法需要核心导出一次同步目录刷新的钩子，issuer 地址的测试接缝由适配器构造参数提供。
- OpenDesign：按 key 的模型目录（`Fetch` 带 key、`ModelInfos` 带 keyID、按 key 的 revision，目录摘要多一个 `keys` 字段）；Messages 流的修补（`redacted_thinking` 改成 `thinking`、带 tool_use 的流把 `end_turn` 改成 `tool_use`、按 `max_tokens` 收尾），需要 `StreamFixer` 一类的接口和 `max_tokens` 请求成员；账号登录的管理路由（插在额度路由后面）；quota.fetch 的差异（会 `rememberHost`、解码错误文案、quota.reset 回成功、用请求日志汇总用量）；错误消息去掉 `[code=...] ` 前缀；测试接缝 `velaAPIURL`、`hostname` 和登录轮询的等待。

## 快照测试

插件在 `internal/<id>/snapshot_test.go` 里调用 `coretest.RunSnapshots`，fixture 放在同目录的 `testdata/snapshots/*.json`。

```sh
go test ./internal/<id> -run TestSnapshots           # 对比
go test ./internal/<id> -run TestSnapshots -update   # 只重写 steps[].output
```

`Binding` 把 runner 接到插件上：

- `ProviderID`：假宿主解析 auth 文件时用的 provider。
- `NewService(version, host, upstreamURL)`：照 `main.go` 构造服务，时钟固定为 `coretest.Clock`（2026-01-02T03:04:05Z），不启动后台目录刷新，出口检测指向 `upstreamURL + "/json"`。返回的 `Plugin` 要有 `Call`、`WaitIdle`、`RunQueuedRefresh`、`DropQueuedRefresh`、`Close`；`*core.Service` 配合 `core.Options{Now, ManualRefresh, EgressCheckURLs}` 直接满足。需要伪方法（Wokey 的 `seam.refresh_tick`）或额外等待（OpenDesign 的登录轮询）时，包一层自己的 `Plugin`。
- `Cases`：生成还没有 fixture 的用例骨架。fixture 写出来以后以文件为准，不再读这张表。
- `ConcurrentPaths`、`VolatileFields`、`GeneratedIDs`：插件自己的归一化规则，见下文。没有就留空。

### fixture 格式

```json
{
  "case": "文件名（不含 .json）",
  "description": "说明",
  "setup": {
    "version": "插件版本",
    "config": { "base-url": "{{UPSTREAM}}", "data-dir": "{{DATA_DIR}}" },
    "credentials": [ { "name": "k1.json", "fields": { "type": "ollama", "api_key": "..." }, "read_only": true } ],
    "drop_queued_refresh": true
  },
  "upstream_script": [
    { "method": "GET", "path": "/api/tags", "status": 200, "header": { "Content-Type": ["application/json"] }, "body": "...", "chunks": [] }
  ],
  "steps": [
    {
      "rpc": { "method": "executor.execute", "request": { } },
      "output": {
        "upstream_requests": [ { "method": "POST", "path": "/v1/chat/completions", "query": "", "header": { }, "body": "原样的请求体" } ],
        "host_calls": [ { "method": "host.log", "request": { } } ],
        "response": { "ok": true, "result": { } }
      }
    }
  ]
}
```

- `read_only` 和 `drop_queued_refresh` 省略时为 false。
- 准备阶段（不记录）：构造服务，用 `setup.config` 转成的 YAML 和 `pluginabi.SchemaVersion` 调 `plugin.register`，丢掉注册排进队列的目录刷新（所以每个用例都从空目录开始），再把 `setup.credentials` 按顺序放进假宿主的 auth 目录（auth_index 依次是 `idx-1`、`idx-2`……），不经过 RPC。准备阶段碰到上游就算失败。
- 每一步：用 `Plugin.Call` 发 RPC，等后台工作结束；如果这一步排了目录刷新（plugin.register / plugin.reconfigure），默认同步跑完再记录输出，`drop_queued_refresh` 为真时直接丢掉。
- 上游脚本按“方法 + 路径”（不看 query）各自排队依次回复。没写脚本的请求回 418 并让测试失败，没被请求到的回复也让测试失败。`chunks` 非空时逐块写出并 flush。
- 假宿主：`host.auth.save` 和 CPA 一样，先调插件的 `auth.parse`，再调 `model.for_auth` 登记模型，前缀规则照 CPA（不强制前缀时同时登记裸 ID 和带前缀 ID）。`read_only` 的文件保存失败，错误是 `failed to write auth file: open /auths/<name>: permission denied`。

### 表示方式（输入输出相同，无损）

- CPA 以 `[]byte` 传、RPC JSON 里是 base64 的字段（`Payload`、`OriginalRequest`、`StorageJSON`、`storage_json`、`RawJSON`、`config_yaml`、`Body`、`ResponseBody`、`payload`）解码后展示：内容是能用 encoding/json 逐字节还原的 JSON 时展示成对象或数组，否则展示成原文字符串。runner 调插件前再编码回去。
- 不是 JSON、或不能逐字节还原的响应和宿主调用请求，原样存成字符串。
- 上游请求头就是 httptest 收到的样子（规范化的键）；请求体是原样字符串。
- 文件格式：保持成员顺序，两格缩进，不转义 HTML，末尾换行。

### 归一化（只有这些值允许每次运行不同）

- 占位符：httptest 上游 URL 写成 `{{UPSTREAM}}`，裸的 host:port 写成 `{{UPSTREAM_HOST}}`，每次运行的数据目录写成 `{{DATA_DIR}}`（含解析符号链接后的路径）。输入输出都一样。
- 只在 management.handle 的响应里，按名字把 `time`、`duration_ms`、`ttft_ms`、`last_frame_ms`、`created_at`、`proxy_id`、`latency_ms`、`output_tokens_per_second`、`e2e_tokens_per_second`，以及 `proxy` 下或 `proxies` 数组里对象的 `id`，替换成 `"<volatile>"`。两个速度字段在请求日志条目（带 `duration_ms` 的对象）里直接删掉，因为只有耗时超过 50 毫秒的调用才有。字符串里出现的同名 JSON 成员按文本替换。`Binding.VolatileFields` 追加插件自己的名字（OpenDesign 的 `log_since`）。
- `Binding.ConcurrentPaths` 里的路由（Ollama 的 `POST /api/show`）是插件并发请求的，连续的一段按请求体排序。
- `Binding.GeneratedIDs`：插件随机生成、通过 management.handle 返回的值（OpenDesign 的 `{{login_id}}`、`{{proxy_id}}`）。后续步骤的输入可以用占位符引用每类最新的值，输出里出现的每个值都写成 `"<volatile>"`。
- 其他一律不归一化：时钟固定，状态、目录、账号和 auth 刷新时间都精确比较。
