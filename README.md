# cpa-provider-core

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）供应商插件共用的核心模块。插件接入 CPA 要做的事情大部分都在这里：RPC 入口、凭证、模型注册、请求转发和流式处理、请求日志、出口代理、管理接口和 CPA 额度面板，以及管理页的公共前端。

每个供应商插件只写一个适配器，说明上游有哪些接口、模型目录怎么拉、Key 怎么校验、额度怎么算，剩下的交给本模块。本仓库不是插件，不发 release，也不进 CPA 插件商店。插件按 `go.mod` 里锁定的版本引用它。

## 用到它的插件

| 插件 | 上游 | 适配器里各家特有的部分 |
| --- | --- | --- |
| [cpa-wokey-plugin](https://github.com/berry-shake/cpa-wokey-plugin) | Wokey | issuer token 和 issuer 余额、转发 `X-End-User-Id`、部分 403 改报 400 |
| [cpa-ollama-plugin](https://github.com/berry-shake/cpa-ollama-plugin) | Ollama Cloud | 套餐额度、账号用量 |
| [cpa-opencode-plugin](https://github.com/berry-shake/cpa-opencode-plugin) | OpenCode | 会话请求头、count_tokens 转发、Go 订阅的 5h / 7d / 月度窗口 |
| [cpa-opendesign-plugin](https://github.com/berry-shake/cpa-opendesign-plugin) | OpenDesign | 每个 Key 一份模型目录、vela 账号登录、Messages 流修补 |
| [cpa-clinepass-plugin](https://github.com/berry-shake/cpa-clinepass-plugin) | ClinePass（Cline） | 只声明 Chat Completions、非流式聚合、`cline-pass/` 命名空间、套餐 5h / 7d / 月度窗口、`success:false` 流修补 |

这些插件仍然各自编成独立的 .so，各有自己的插件 ID、认证文件类型、数据目录和版本号。

## 结构

```
CPA ──c-shared RPC──▶ 插件 main.go（cgo 外壳）
                          │ core.New(adapter, version, callHost)
                          ▼
                     core.Service ── 注册与能力、凭证、模型目录同步、执行和流式、
                          │          请求日志、出口代理、管理接口、额度面板
                          ▼
                     Adapter（插件的 internal/<id>）── 端点、模型目录、Key 校验、账号和额度，
                                                      以及按需实现的可选接口
```

| 包 | 内容 |
| --- | --- |
| `provider` | 插件和核心之间的约定：`Spec`、`Adapter`、可选接口、`Catalog`、`Env`、协议 |
| `core` | `Service`：RPC 分发、注册、凭证解析、模型注册和目录同步、执行和流式请求、额度查询、管理接口、出口检测 |
| `coretest` | RPC 快照测试：runner、假 CPA 宿主、脚本化的上游，见 [coretest/README.md](coretest/README.md) |
| `config` | 插件配置的解析；`Profile` 是各家的默认值和配置项说明 |
| `credential` | auth 文件的读取和生成 |
| `egress` | 出口选择（凭证代理 → CPA 全局代理 → 直连）、每个出口一个 HTTP 客户端，请求按出口排队 |
| `proxystore` | 管理页的代理列表（`proxies.json`） |
| `requestlog` | SQLite 请求日志：生成速度和端到端速度，按凭证汇总 5 小时、24 小时、7 天、30 天用量 |
| `streamgate` | SSE 拆分和闸门：首个输出之前的错误按失败返回，开始输出之后的错误照转、日志记失败，没有结束事件的流报截断 |
| `upstream` | 上游错误映射到插件错误、带 Bearer 的 JSON GET |
| `payload`、`jsonfield` | 只在顶层按字节改请求体、扫描 JSON 成员，不整体解码 |
| `flags` | 出口检测用的国旗图标 |
| `web/` | 管理页公共前端，见 [web/README.md](web/README.md) |

## 核心统一处理的部分

适配器不用管下面这些，各插件行为一致：

- **注册**：Chat Completions、Responses、Messages 三种协议都原样转发，CPA 不做协议转换（`Spec.Protocols` 可以只声明其中几种，其余由 CPA 转换）；凭证文件解析成 CPA 认证；模型按凭证的 `models`、`prefix` 登记，目录变了就更新 `model_revision`，让 CPA 重新登记。
- **请求**：只在请求体顶层按字节换 `model`，Chat 流式请求补 `stream_options`（`Spec.AggregateChat` 时非流式 Chat 请求也改发流式，再聚合成一个回答）；User-Agent 是 `cpa-<id>-plugin/<版本>`；转发 `Anthropic-Beta`、`Anthropic-Version`，以及 `Spec.ForwardHeaders` 列出的请求头。
- **错误**：能读出 `{"error":{"message"}}`、`{"error":"..."}`、`{"message"}` 三种错误体，去掉消息开头的 `[code=…]`；流里的错误关键字按同一张表定状态码（余额不足报 402、限流报 429 等）；错误消息统一以 `<id>: ` 开头。
- **请求日志**：存在 `plugins/<id>-data/<库名>`，记 Token、首字时间、两种速度、出口和上游请求 ID。流式请求按客户端实际收到的结果记。
- **出口**：每个凭证可以单独配代理，没配就用 CPA 全局代理，再没有就直连。同一个出口的请求排队，管理页能检测出口 IP 和延迟。
- **管理接口**：放在 `/v0/management/plugins/<id>/` 下，包括状态、模型目录、凭证增改、额度、请求日志、代理列表，以及管理页、logo、国旗图标。
- **额度**：账号查询在凭证的出口排队轮次里执行，结果缓存一分钟（`AccountKey` 返回空串则不缓存），交给 CPA 额度面板显示；`quota.reset` 统一回答不支持。
- **count_tokens**：适配器实现 `TokenCounter` 时转发到上游，否则直接回 501。

完整约定见 [docs/adapter.md](docs/adapter.md)。

## 适配器

```go
type Adapter interface {
	Spec() Spec                        // ID、显示名、仓库、配置默认值和说明、管理页和 logo、各家的文案
	Endpoint(protocol Protocol) string // 三种协议各自的上游路径，也是请求日志里的 endpoint
	Catalog() CatalogSource            // 模型目录

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
	Summary() CatalogSummary // 模型数、版本、拉取和检查时间、最近的错误
	Has(id string) bool
	ModelInfos(all bool, ids []string) []pluginapi.ModelInfo
	UpstreamID(model, prefix string) string // 一般直接用 provider.UpstreamID
	ModelsView() any         // 管理页的模型列表，必须编码成 JSON 数组
}
```

可选接口，实现了才启用：

| 接口 | 用途 | 谁在用 |
| --- | --- | --- |
| `HeaderDecorator` | 给转发的请求加自己的请求头 | OpenCode（`X-Opencode-Session`） |
| `RequestReader` | 声明要读哪些顶层请求成员，和核心的扫描合成一次 | OpenCode、OpenDesign |
| `TokenCounter` | count_tokens 转发到上游 | OpenCode |
| `ErrorRemapper` | 改写部分上游错误的状态码和消息 | Wokey |
| `CredentialExtension` | auth 文件里的供应商字段：输入、校验、展示 | Wokey（`issuer_token`） |
| `AccountViewer` | 管理页单独查一份更完整的账号信息 | Wokey |
| `QuotaChecker` | 账号信息不全时让额度面板报错，不画进度条 | Wokey、Ollama |
| `KeyedCatalogSource` | 每个 Key 一份模型目录 | OpenDesign |
| `StreamFixer` | 流进闸门之前修补事件 | OpenDesign |
| `HostUser` | 拿到核心的宿主能力：auth 文件读写、HTTP 客户端、用量汇总 | OpenDesign |
| `ManagementExtension` | 增加自己的管理路由 | OpenDesign（账号登录、退出） |
| `QuotaFetcher`、`QuotaPage` | 自己回答 CPA 额度面板和管理页的额度路由 | OpenDesign |

## 新增一个供应商

结构最简单的是 Ollama 和 OpenCode，照着其中一个做：

1. **建仓库**：复制模板仓库，改模块路径、插件 ID、显示名和 logo。`go.mod` 里 `require github.com/berry-shake/cpa-provider-core vX.Y.Z`。
2. **写适配器** `internal/<id>/adapter.go`：在 `Spec` 里填 ID、显示名、仓库地址，以及 `config.Profile`（默认 base URL、模型前缀、数据目录、库名、配置项说明）；`Endpoint` 返回三种协议的路径；再实现模型目录、`VerifyKey`、账号和额度。目录的 `UpstreamID` 直接交给 `provider.UpstreamID(model, prefix, c.Has, provider.SlashedIDs)`；上游模型 ID 一定不带斜杠时用 `provider.FlatIDs`，不认识的前缀一律去掉（Wokey）。上游有特殊需求时再实现对应的可选接口。
3. **`main.go`**：只保留 cgo 外壳，原样照抄模板，把构造改成自己的适配器：

   ```go
   next := core.New(<id>.New(), pluginVersion, callHost)
   ```

4. **管理页**：在 `web/src/provider/` 写各家不同的部分，包括额度卡片、模型列的列、编辑框的附加字段、文案和样式。约定见 [web/README.md](web/README.md)。`make web` 会先把 core 的公共前端拷到 `web/src/core/`，再用 Vite 构建，结果嵌进插件。
5. **快照测试**：在 `internal/<id>/snapshot_test.go` 里调用 `coretest.RunSnapshots`，录下发往上游的请求、宿主回调和 RPC 回答。用法见 [coretest/README.md](coretest/README.md)。
6. **发版**：复制模板的 `.github/workflows/build.yml`，推 `v*` tag 后会构建五个平台并发布 release；再在插件商店的 `registry.json` 里加一个条目。

## 开发

同时改 core 和插件时，在仓库外放一个 `go.work`（不提交），把 core 和插件都加进去：

```
go 1.26.10

use (
	./cpa-provider-core
	./cpa-ollama-plugin
)
```

设好 `GOWORK=<路径>/go.work` 后，插件就直接编译本地的 core，管理页的拷贝脚本也取本地目录。不用 go.work 时，可以设 `CPA_PROVIDER_CORE_DIR` 只给前端指定 core 目录。

```sh
go vet ./... && go test ./...                           # Go
cd web && npm ci && npm run typecheck && npm test       # 公共前端，用 web/test-provider 这个假插件跑
```

CI（`.github/workflows/test.yml`）在推送和打 tag 时跑同样的检查。

## 发版

1. core 的修改推到 main，CI 通过后打 tag（`v0.1.1` 这样，只加最后一位，满 9 进位：`v0.1.9` 之后是 `v0.2.0`）。tag 推出去就被 Go 模块代理和校验和数据库记下，不能重打，所以先测完再打。
2. 每个插件仓库执行 `go mod edit -require=github.com/berry-shake/cpa-provider-core@vX.Y.Z && go mod tidy`。关掉 go.work（`GOWORK=off`）再跑一遍测试和 `make web`，提交后按插件自己的版本号打 tag。
3. 所有插件的 release 都出来以后，改插件商店里的版本号，然后部署。

所有插件始终升到同一个 core 版本，不让它们停在不同版本上。core 的导出接口是所有插件共用的 API，尽量只增不改；要改就在同一轮里把所有插件一起改完。

## License

MIT。国旗图标来自 [Twemoji](https://github.com/jdecked/twemoji)（CC-BY 4.0，见 `flags/LICENSE-twemoji`）。
