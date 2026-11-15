# 适配器约定（详细）

README 里只列了接口和用途，这里写每个接口的完整约定：调用时机、参数含义、出错时核心怎么回答。接口定义和注释在 [`provider/provider.go`](../provider/provider.go)。

## 可选接口

核心用类型断言检查适配器实现了哪些接口，没实现的就走默认行为。

`CallInput.Request` 是 CPA 发来的执行请求，但 `OriginalRequest` 只在核心需要时才解码（CPA 没在 `Metadata` 里给 `requested_model`），其余情况为 nil。适配器要读请求内容时用 `Payload` 和 `CallInput.Members`。

- `HeaderDecorator`：`Decorate(header http.Header, in CallInput)`，给执行请求加自己的头。OpenCode 用它加 `X-Opencode-Session`。
- `RequestReader`：`RequestMembers() []string`，`Decorate` 或 `StreamFix` 要读的顶层请求成员（核心自己找 `model`、`stream_options` 和推理设置）。核心在同一次扫描里找它们，结果放在 `CallInput.Members`（`CallInput.ScanErr` 非 nil 时请求体有格式错误）。它只是适配器声明自己要读什么，不影响核心的判断：扫描为了找这些成员多走一段、在核心成员都找到之后才碰到的格式错误，不算进是否补 `stream_options`、记录哪个推理强度。OpenCode 为了算会话 ID 读 `prompt_cache_key`、`metadata`、`messages`、`input`、`system`、`instructions`；OpenDesign 的 `StreamFix` 读 `max_tokens`。
- `TokenCounter`：`CountTokensEndpoint() string`。实现了就把 count_tokens 当 Messages 请求转发，不写请求日志（OpenCode）；没实现就不解码请求，直接回 501 `<id>: count_tokens is not supported by <显示名>`。
- `ErrorRemapper`：`RemapError(status int, code, message string) (mapped int, newMessage string)`，成为 `Env.Errors`（`upstream.Errors.Remap`）的一部分，所有上游错误都先经过它：执行请求的错误回复、流里首个输出之前的错误事件、适配器自己用 `Env.Errors` 发的请求。它拿到上游状态码和从回复体读出的 code、message（已去空白、未截断），返回替换的 message 和要报的状态码，状态码为 0 时走核心的默认映射。消息开头的 `[code=...] ` 核心读回复体时已经去掉了。Wokey 把 403 `request_blocked`、`model_denylisted` 报成 400。
- `CredentialExtension`：auth 文件里的供应商字段（Wokey 的 `issuer_token`）。
  - `DecodeCredentialInput(body []byte) (any, error)`：核心解码完自己的字段后，从同一个请求体（POST / PATCH credentials）读供应商的字段；出错时按 `invalid_json` 回 400，消息是错误原文。
  - `ApplyCredentialInput(fields map[string]any, input any) *InputError`：校验并写进 auth 文件字段，位置在 `base_url`、`proxy_url` 之后、`prefix`、`models`、`priority` 之前；返回错误时回 400 `{"error":Code,"message":Message}`。
  - `CheckCredential(cred, previous *credential.Credential) *CredentialCheck`：带校验保存时，除了 `VerifyKey` 还要检查什么；新增时 `previous` 为 nil，在 `VerifyKey` 通过之后检查；编辑时传编辑前的凭证（编辑本来不调 `VerifyKey`）。返回 nil 就不检查、也不排队。`CredentialCheck.Run(ctx, env, client)` 在凭证出口的轮次里执行，`ctx` 同样限定 20 秒含排队；失败时回 `{"error":CredentialCheck.Code,"message":错误消息}`，上游 400/401/403 报 400，其余报 502。
  - `CredentialView(cred) []ViewMember`：凭证视图（状态页和保存结果）里加的成员，只对能读出的 auth 文件加。`ViewMember{Name, Value, Before}` 放在名为 `Before` 的成员前面（必须是视图里一定有的成员，比如 `prefix`、`disabled`），`Before` 为空就放在最后。Wokey 在 `prefix` 前加 `issuer_token_masked`。
- `AccountViewer`：`FetchAccountView(env, client, cred) (info any, complete bool, perr)`，管理页额度路由用它代替 `FetchAccount`（同样在出口轮次里、同样的缓存规则，缓存键是 `AccountKey` 加 ` view`）。Wokey 在这里多查 issuer 余额；CPA 额度面板（quota.fetch）仍然用 `FetchAccount`。
- `QuotaChecker`：`QuotaError(info any) *pluginabi.Error`，quota.fetch 拿到账号信息后先问它，非 nil 就回这个错误、不画额度条。Wokey 在缺余额或缺用量时报 `balance_unavailable`、`usage_unavailable`，Ollama 在缺余额时报 `balance_unavailable`（错误消息是 `<id>: read balance: <原因>`）。
- `KeyedCatalogSource`：每个 key 有自己的模型列表（OpenDesign 按套餐列模型）。目录除了 `CatalogSource` 还要实现 `FetchKey(ctx, client, baseURL, apiKey, keyID, userAgent string, now time.Time) (Catalog, error)`（只拉一个 key 的列表，返回整个目录）和 `Retain(keep map[string]bool) Catalog`（丢掉不在 `keep` 里的 key），`Current()` 返回的目录要实现 `KeyedCatalog`：`KnowsKey`、`Keys`、`RevisionFor(keyID)`、`KeyModelInfos(all, ids, keyID)`、`KeyHas(keyID, id)`。`keyID` 是 `credential.Fingerprint(api_key)`。核心照此：注册模型和 `model_revision` 按凭证自己的 key 走（目录还不认识这个 key 时先同步拉一次，失败记 `<id>: model list unavailable`）；后台刷新改成逐个 key 拉（失败记 `model list refresh failed` / `some model lists did not load`，几个 key 的不同错误用 `; ` 连起来），列出的凭证非空或目录里原来有 key 时 `Retain` 掉已经没有凭证的 key；列 auth 文件失败时记 `model list refresh failed`、所有 key 的列表原样保留；状态页目录摘要在 `count` 后面多一个 `keys`；`unknown_models` 按凭证自己的 key 查。`Fetch` 不会被调用。
- `StreamFixer`：`StreamFix(protocol Protocol, in CallInput) StreamFix`，在流进闸门之前修补事件（OpenDesign 修 Messages 流）。每次流式请求调一次（count_tokens 不调），返回 nil 就不修。`StreamFix` 有三个方法：`Event(event) [][]byte` 把一个 SSE 事件换成零个或多个事件交给闸门；`Flush() [][]byte` 在上游流正常结束、闸门判断结束之前调用，交出还压着的事件（直接交给闸门，不再经过 `Event`）；`Close() [][]byte` 在已经开始输出、流却没有结束事件、本来要报截断时调用，返回的事件像上游事件一样经过 `Event` 推给闸门（闸门据此认为流已正常结束），返回空就照常报截断。要读请求成员时实现 `RequestReader`。
- `HostUser`：`Attach(host Host)`，`New` 时把核心的宿主能力交给适配器，供它自己的管理路由和额度回答用：`Env()`、`Logf`、`AuthFiles()`（本供应商的 auth 文件，`AuthFile` 带解析好的字段、`Credential` 和读取错误 `ReadError`，跳过 runtime-only 和没有 auth_index 的）、`ReadAuthFile(authIndex) (fields, name, err)`、`SaveAuthFile(name, fields)`、`Client(proxyURL)`（凭证出口的 HTTP 客户端，空串时走 CPA 全局代理，不排队）、`Usage(authIndex, authID, now)`（请求日志的用量汇总，第二个返回值是日志不可用的原因）。
- `ManagementExtension`：`ManagementRoutes() []ManagementRoute`，适配器自己的管理路由。`ManagementRoute{Method, Path, Description, Handle}` 的 `Path` 相对 `/v0/management/plugins/<id>`（比如 `/account/login`），声明顺序在额度路由之后、日志路由之前；同一路径换方法回 405。核心自己的路由优先，不能覆盖。OpenDesign 用它做账号登录、取消、退出。
- `QuotaFetcher`：`FetchQuota(req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, *pluginabi.Error)`，quota.fetch 整个交给适配器，不再走 `FetchAccount` / `Quota` / `QuotaChecker`（这时 `FetchAccount`、`Quota` 不会被调用，留空实现即可）。OpenDesign 回账号套餐的 5h / 7d 窗口。
- `QuotaPage`：`ServeQuota(req pluginapi.ManagementRequest) pluginapi.ManagementResponse`，接管管理页的额度路由（`GET .../credentials/quota`）。OpenDesign 回请求日志的用量加账号窗口。

## 额度 RPC

各插件一样：quota.fetch 请求解码失败回 `invalid_request` `decode quota request: ...`，不改动缓存的宿主配置（全局代理、force-model-prefix 只从 auth / model 请求记下）；quota.reset 回 `{"success":false,"message":"<id>: quota reset is not supported"}`（CPA 据此回 502 加这句说明）。

## 管理请求的回答

适配器自己回管理请求时用 `provider.JSONResponse(status, v)`（和核心一样的 `Content-Type`、`Cache-Control: no-store`）和 `provider.ErrorBody{Error, Message}`。

## 账号查询和缓存

`FetchAccount` 在凭证出口的排队轮次里调用；`complete` 为真时结果缓存一分钟，返回错误时 info 丢弃；`AccountKey` 返回空串时不缓存（Wokey 的余额要实时，故意不缓存）。`VerifyKey` 也在轮次里调用，`ctx` 限定整个检查（20 秒，含排队）。

## Spec 里各家不同的字段

- `ForwardHeaders`：除了 `Anthropic-Beta`、`Anthropic-Version` 以外要原样转发给上游的客户端请求头。Wokey 转发 `X-End-User-Id`。不能放客户端凭证。

## 旧日志迁移

请求日志打开数据目录时会迁移 Wokey 早期留下的文件：库文件（`Profile.Database`）还不存在而 `requests.db`（Wokey 0.1.7 早期的库名）存在时改名过来，`requests.jsonl`（SQLite 之前的日志）导入一次后删除。其他插件（包括 OpenDesign）的数据目录里没有这两个文件，什么也不做。

## 由 ID 和显示名推出的字符串

User-Agent `cpa-<id>-plugin/<版本>`，错误和日志前缀 `<id>: `，请求 ID 头 `X-<显示名>-Cpa-Request-Id`，管理路由 `/v0/management/plugins/<id>/...`，以及状态、代理导入、管理页菜单的说明。写法不一致的放在 `Spec.Texts`。

## 请求日志的记法

流式请求的日志记客户端实际收到的结果：开始输出之后上游再发错误事件时，事件原样转给客户端，日志的 `status` 记 `error`，`http_status` 仍是 200，`error_phase` 是 `stream`，`error_code`、`error` 是这个错误事件的 code 和消息。`frames` 只数带输出的事件：Messages 流数 `content_block_delta`，Responses 流数 `*.delta`，Chat 流数带内容的数据块（除了 role 还有 delta 的）；`content_block_start`、`content_block_stop`、`message_delta` 之类不算。不支持的请求格式也写一条失败日志。
