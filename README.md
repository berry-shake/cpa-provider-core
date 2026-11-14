# cpa-provider-core

CLIProxyAPI 供应商插件（Wokey、Ollama、OpenCode、OpenDesign）共用的核心代码：插件 RPC 入口、执行请求、流式处理、请求日志、出口代理、凭证、管理接口，以及管理页的公共前端代码。

各供应商插件仓库只保留自己的适配器，通过 `go.mod` 引用本模块，并在构建管理页时从本模块拷贝公共前端代码。本仓库不单独发布插件。

四个插件（Ollama、OpenCode、Wokey、OpenDesign）都已经迁到本模块，没有待迁的。

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
| `requestlog` | SQLite 请求日志，含生成速度和端到端速度；按凭证汇总 5 小时、24 小时、7 天、30 天用量（`Usage`） |
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
- `TokenCounter`：`CountTokensEndpoint() string`。实现了就把 count_tokens 当 Messages 请求转发，不写请求日志；没实现就回 501 `<id>: count_tokens is not supported by <显示名>`（另见 `Spec.CountTokensRefusal`）。
- `ErrorRemapper`：`RemapError(status int, code, message string) (mapped int, newMessage string)`，成为 `Env.Errors`（`upstream.Errors.Remap`）的一部分，所有上游错误都先经过它：执行请求的错误回复、流里首个输出之前的错误事件、适配器自己用 `Env.Errors` 发的请求。它拿到上游状态码和从回复体读出的 code、message（已去空白、未截断），返回替换的 message 和要报的状态码，状态码为 0 时走核心的默认映射。Wokey 把 403 `request_blocked`、`model_denylisted` 报成 400；OpenDesign 在这里去掉消息前面的 `[code=...] `。
- `CredentialExtension`：auth 文件里的供应商字段（Wokey 的 `issuer_token`）。
  - `DecodeCredentialInput(body []byte) (any, error)`：核心解码完自己的字段后，从同一个请求体（POST / PATCH credentials）读供应商的字段；出错时按 `invalid_json` 回 400，消息是错误原文（Wokey 的结构体也叫 `credentialInput`，所以类型错误的文案和原来一样）。
  - `ApplyCredentialInput(fields map[string]any, input any) *InputError`：校验并写进 auth 文件字段，位置在 `base_url`、`proxy_url` 之后、`prefix`、`models`、`priority` 之前；返回错误时回 400 `{"error":Code,"message":Message}`。
  - `CheckCredential(cred, previous *credential.Credential) *CredentialCheck`：带校验保存时，除了 `VerifyKey` 还要检查什么；新增时 `previous` 为 nil，在 `VerifyKey` 通过之后检查；编辑时传编辑前的凭证（编辑本来不调 `VerifyKey`）。返回 nil 就不检查、也不排队。`CredentialCheck.Run(ctx, env, client)` 在凭证出口的轮次里执行，`ctx` 同样限定 20 秒含排队；失败时回 `{"error":CredentialCheck.Code,"message":错误消息}`，上游 400/401/403 报 400，其余报 502。
  - `CredentialView(cred) []ViewMember`：凭证视图（状态页和保存结果）里加的成员，只对能读出的 auth 文件加。`ViewMember{Name, Value, Before}` 放在名为 `Before` 的成员前面（必须是视图里一定有的成员，比如 `prefix`、`disabled`），`Before` 为空就放在最后。Wokey 在 `prefix` 前加 `issuer_token_masked`。
- `AccountViewer`：`FetchAccountView(env, client, cred) (info any, complete bool, perr)`，管理页额度路由用它代替 `FetchAccount`（同样在出口轮次里、同样的缓存规则，缓存键是 `AccountKey` 加 ` view`）。Wokey 在这里多查 issuer 余额；CPA 额度面板（quota.fetch）仍然用 `FetchAccount`。
- `QuotaChecker`：`QuotaError(info any) *pluginabi.Error`，quota.fetch 拿到账号信息后先问它，非 nil 就回这个错误、不画额度条。Wokey 在缺余额或缺用量时报 `balance_unavailable`、`usage_unavailable`。
- `KeyedCatalogSource`：每个 key 有自己的模型列表（OpenDesign 按套餐列模型）。目录除了 `CatalogSource` 还要实现 `FetchKey(ctx, client, baseURL, apiKey, keyID, userAgent string, now time.Time) (Catalog, error)`（只拉一个 key 的列表，返回整个目录）和 `Retain(keep map[string]bool) Catalog`（丢掉不在 `keep` 里的 key），`Current()` 返回的目录要实现 `KeyedCatalog`：`KnowsKey`、`Keys`、`RevisionFor(keyID)`、`KeyModelInfos(all, ids, keyID)`、`KeyHas(keyID, id)`。`keyID` 是 `credential.Fingerprint(api_key)`。核心照此：注册模型和 `model_revision` 按凭证自己的 key 走（目录还不认识这个 key 时先同步拉一次，失败记 `<id>: model list unavailable`）；后台刷新改成逐个 key 拉（失败记 `model list refresh failed` / `some model lists did not load`），列出的凭证非空或目录里原来有 key 时 `Retain` 掉已经没有凭证的 key（列 auth 文件失败时也会全部丢掉，OpenDesign 原来就这样）；状态页目录摘要在 `count` 后面多一个 `keys`；`unknown_models` 按凭证自己的 key 查。`Fetch` 不会被调用。
- `StreamFixer`：`StreamFix(protocol Protocol, in CallInput) StreamFix`，在流进闸门之前修补事件（OpenDesign 修 Messages 流）。每次流式请求调一次（count_tokens 不调），返回 nil 就不修。`StreamFix` 有三个方法：`Event(event) [][]byte` 把一个 SSE 事件换成零个或多个事件交给闸门；`Flush() [][]byte` 在上游流正常结束、闸门判断结束之前调用，交出还压着的事件（直接交给闸门，不再经过 `Event`）；`Close() [][]byte` 在已经开始输出、流却没有结束事件、本来要报截断时调用，返回的事件像上游事件一样经过 `Event` 推给闸门（闸门据此认为流已正常结束），返回空就照常报截断。要读请求成员时把成员名加进 `Spec.RequestMembers`，从 `CallInput.Members` 读（`CallInput.ScanErr` 非 nil 时请求体有格式错误）。
- `HostUser`：`Attach(host Host)`，`New` 时把核心的宿主能力交给适配器，供它自己的管理路由和额度回答用：`Env()`、`Logf`、`AuthFiles()`（本供应商的 auth 文件，`AuthFile` 带解析好的字段、`Credential` 和读取错误 `ReadError`，跳过 runtime-only 和没有 auth_index 的）、`ReadAuthFile(authIndex) (fields, name, err)`、`SaveAuthFile(name, fields)`、`Client(proxyURL)`（凭证出口的 HTTP 客户端，空串时走 CPA 全局代理，不排队）、`Usage(authIndex, authID, now)`（请求日志的用量汇总，第二个返回值是日志不可用的原因）。
- `ManagementExtension`：`ManagementRoutes() []ManagementRoute`，适配器自己的管理路由。`ManagementRoute{Method, Path, Description, Handle}` 的 `Path` 相对 `/v0/management/plugins/<id>`（比如 `/account/login`），声明顺序在额度路由之后、日志路由之前；同一路径换方法回 405。核心自己的路由优先，不能覆盖。OpenDesign 用它做账号登录、取消、退出。
- `QuotaFetcher`：`FetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error)`，quota.fetch 整个交给适配器，不再走 `FetchAccount` / `Quota` / `QuotaChecker`（这时 `FetchAccount`、`Quota` 不会被调用，留空实现即可）。OpenDesign 回账号套餐的 5h / 7d 窗口。
- `QuotaPage`：`ServeQuota(req pluginapi.ManagementRequest) pluginapi.ManagementResponse`，接管管理页的额度路由（`GET .../credentials/quota`）。OpenDesign 回请求日志的用量加账号窗口。

适配器自己回管理请求时用 `provider.JSONResponse(status, v)`（和核心一样的 `Content-Type`、`Cache-Control: no-store`）和 `provider.ErrorBody{Error, Message}`。

`FetchAccount` 在凭证出口的排队轮次里调用；`complete` 为真时结果缓存一分钟，返回错误时 info 丢弃；`AccountKey` 返回空串时不缓存（Wokey）。`VerifyKey` 也在轮次里调用，`ctx` 限定整个检查（20 秒，含排队）。

`Spec` 里各家真实不同、不打算统一的字段：

- `ForwardHeaders`：除了 `Anthropic-Beta`、`Anthropic-Version` 以外要原样转发给上游的客户端请求头。Wokey 转发 `X-End-User-Id`。不能放客户端凭证。

请求日志打开数据目录时会迁移 Wokey 早期留下的文件：库文件（`Profile.Database`）还不存在而 `requests.db`（0.1.7 早期的库名）存在时改名过来，`requests.jsonl`（SQLite 之前的日志）导入一次后删除。其他插件（包括 OpenDesign）的数据目录里没有这两个文件，什么也不做。

由 `ID` 和 `DisplayName` 推出、各家写法一致的字符串：User-Agent `cpa-<id>-plugin/<版本>`，错误和日志前缀 `<id>: `，请求 ID 头 `X-<显示名>-Cpa-Request-Id`，管理路由 `/v0/management/plugins/<id>/...`，以及状态、代理导入、管理页菜单的说明。写法不一致的放在 `Spec.Texts`。

## 第二轮统一时去掉的差异开关

这些开关只是为了让第一轮迁移逐字节不变，保留了各插件原来的差异。第二轮统一时应该去掉：

- `Spec.StreamErrors`：流内错误关键字表。Ollama 用 `StreamErrorsBase`，OpenCode 用 `StreamErrorsExtended`（多了 budget、usage limit、insufficient funds、AuthError、MissingSession 等）。统一成扩展表即可。
- `Spec.RequestMembers`：核心扫描请求体时额外要找的顶层成员。OpenCode 为了算会话 ID 多找 `prompt_cache_key`、`metadata`、`messages`、`input`、`system`、`instructions`。扫描在全部找到后停止，所以成员越多，越可能扫到请求体后面的格式错误；这会影响是否补 `stream_options`、是否记录推理强度。统一时让所有插件扫同一组成员。
- `Spec.CountTokensRefusal`：count_tokens 的请求解码和拒绝文案。不设时核心先解码执行请求，解码失败回 `invalid_request`，然后转发（`TokenCounter`）或者回 501 `<id>: count_tokens is not supported by <显示名>`（Ollama 和 OpenCode 原来就是这样）。设了就不解码、直接回 501 `<id>: <这段文字>`：Wokey 是 `token counting is not supported`，OpenDesign 迁移时用 `OpenDesign has no token counting endpoint`。统一时去掉这个开关，统一成先解码、同一句文案。
- Wokey 的 `AccountKey` 返回空串，账号查询不缓存（Ollama 和 OpenCode 缓存一分钟）。统一时改成 key 指纹加查询地址。
- `Spec.QuotaRPC`：OpenDesign 的 quota RPC 差异。`DecodeError` 是 quota.fetch 解码失败的文案（默认 `decode quota request`，OpenDesign 是 `decode quota fetch request`）；`RememberHost` 让 quota.fetch 像 auth / model 请求一样缓存宿主摘要里的全局代理和 force-model-prefix；`Reset` 设了就让 quota.reset 回这个结果（OpenDesign 回 `success:false` 加说明），不设回 501。注意 `RememberHost` 是 OpenDesign 原来的 bug：CPA 的 quota 请求不带宿主摘要，会把缓存的全局代理清空。统一时去掉 `RememberHost`、统一文案和 quota.reset 的回答。
- OpenDesign 也用 `StreamErrorsExtended`，`RequestMembers` 加了 `max_tokens`（`StreamFix` 读它）。

## 快照测试

插件在 `internal/<id>/snapshot_test.go` 里调用 `coretest.RunSnapshots`，fixture 放在同目录的 `testdata/snapshots/*.json`。

```sh
go test ./internal/<id> -run TestSnapshots           # 对比
go test ./internal/<id> -run TestSnapshots -update   # 只重写 steps[].output
```

`Binding` 把 runner 接到插件上：

- `ProviderID`：假宿主解析 auth 文件时用的 provider。
- `NewService(version, host, upstreamURL)`：照 `main.go` 构造服务，时钟固定为 `coretest.Clock`（2026-01-02T03:04:05Z），不启动后台目录刷新，出口检测指向 `upstreamURL + "/json"`。返回的 `Plugin` 要有 `Call`、`WaitIdle`、`RunQueuedRefresh`、`DropQueuedRefresh`、`Close`；`*core.Service` 配合 `core.Options{Now, ManualRefresh, EgressCheckURLs}` 直接满足。需要伪方法（Wokey 的 `seam.refresh_tick`）或额外等待（OpenDesign 的登录轮询）时，包一层自己的 `Plugin`：`Service.RefreshTick()` 同步跑一轮后台目录循环（不强制的刷新，再把用旧目录注册的 auth 文件重新保存），返回目录和刷新错误；`Service.WaitIdleFor(d)` 是带时限的 `WaitIdle`。
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
