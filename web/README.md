# 管理页公共前端

各供应商插件（wokey、ollama、opencode、opendesign、clinepass）的管理页共用这里的代码：登录、标签页、凭证卡片外壳、编辑弹窗、模型目录、请求日志、出口代理、配置页、i18n、主题和样式。各插件只写自己不同的部分，放在插件仓库的 `web/src/provider/`。

技术栈和插件仓库一致：React 18 + TypeScript + Vite 5 + vite-plugin-singlefile，测试用 Vitest。

## 目录

```
web/
  src/             公共代码（插件构建时整个拷到 web/src/core/，测试文件除外）
    contract.ts    插件要实现的接口（见下）
    card-grid.css  可选样式：卡片「事实 + 用量」2×2 并排、额度条
  test-provider/   一个最小的假插件，只给本仓库的测试和类型检查用
```

公共代码用 `@core/...` 引用自己以外的公共模块（插件代码也这样引用），用 `@provider/...` 引用插件部分。两个别名在 `tsconfig.json` 的 `paths`、`vite.config.ts` 和 `vitest.config.ts` 的 `resolve.alias` 里配置：

| 仓库 | `@core` | `@provider` |
| --- | --- | --- |
| 本仓库 | `web/src` | `web/test-provider` |
| 插件仓库 | `web/src/core`（拷贝，gitignore） | `web/src/provider` |

## 插件约定（@provider）

`web/src/provider/` 必须有这些文件：

| 文件 | 导出 |
| --- | --- |
| `plugin.ts` | `plugin: ProviderPlugin` |
| `types.ts` | `ModelView`、`QuotaView`、`CredentialExtra`、`CatalogExtra`、`CredentialSaveExtra`、`EditorExtra` |
| `components.tsx` | `components: ProviderComponents` |
| `i18n/en.json`、`i18n/zh.json` | 插件自己的文案，合并在公共文案之后（同名覆盖） |
| `styles.css` | 追加在公共样式之后的规则（可以 `@import "@core/card-grid.css";`） |

接口的完整定义和注释在 `src/contract.ts`，要点：

- `types.ts` 描述管理接口里各插件不同的 JSON：`ModelView`（目录里的模型，至少有 `id`）、`QuotaView`（`/credentials/quota` 的返回，至少有 `fetched_at`），以及 `CredentialView`、`CatalogMeta`、凭证保存请求体、编辑器状态各自多出的字段（没有就写 `{}`）。
- `ProviderPlugin`：
  - `id`：决定管理接口和资源路径、localStorage 键名、配置段 `plugins.configs.<id>`、默认前缀；
  - `name`、`repo`（页脚链接）、`billionTokens`（10 亿以上显示为 `1.2B`）；
  - `quotaGroups` / `quotaRefresh`：额度怎么分组加载（默认按出口分组，组内逐个、组间并行），以及「刷新全部」是否带 `&refresh=1`；
  - `models.matches` / `inAll` / `hint`：模型搜索、「全部模型」包含哪些、目录摘要的参数和附注；
  - `editor`：编辑器初始字段、保存时校验并写入请求体（返回文案键表示拒绝）、关闭后要清空的密钥字段、保存后是否重载额度、选择器可选的模型、计数提示、选择器每行的名称和详情。
- `ProviderComponents`：
  - `KeySummary`：卡片上方的汇总条；
  - `KeyUsage`：卡片里事实下面的用量部分；
  - `keyCorner`、`modelsTip`、`fetchedTip`、`KeyActions`：卡片角落、模型提示、「检查于」提示、编辑按钮前的按钮；
  - `modelColumns` + `ModelCells`：目录表 CPA 名称之前的列；
  - `EditorFields`：编辑器里优先级后面的字段；
  - `Dialogs`：额外的弹窗。

`src/components/keys/parts.tsx` 提供拼卡片用的 `StatItem`、`Meter`、`Bar`、`UsagePending`。需要下拉框时用 `src/components/Select.tsx` 的 `Select`（页面自绘的菜单，不用浏览器原生菜单，支持键盘操作，放在对话框里按 Esc 只关菜单）。插件需要状态时用公共 store 的 `api.request`、`setBusy`、`setQuota`、`notify`、`handleError`、`confirmTwice`、`refresh`、`showLogin`。

注意循环引用：公共模块加载时就会读 `plugin`，所以 `plugin.ts` 只能放常量和函数，引用公共代码的运行时值只能在函数里用。

## 插件仓库怎么构建

插件的 `web/scripts/sync-core.mjs` 在仓库根目录执行

```
go list -m -f '{{.Dir}}' github.com/berry-shake/cpa-provider-core
```

找到本模块目录（跟随 `go.mod`，设置了 `GOWORK` 时用工作区里的本地检出；还没下载时先 `go mod download`），把 `web/src` 拷到插件的 `web/src/core/`（不含 `*.test.ts(x)`），然后才是 `npm ci`、测试和 Vite 构建。`CPA_PROVIDER_CORE_DIR=/path/to/cpa-provider-core` 可以直接指定目录。

- `make web`、`make web-test` 会先同步；
- 插件的 CI web 任务先 `actions/setup-go` 再同步；
- 本地改公共代码时用 go.work 指向本仓库，或者设 `CPA_PROVIDER_CORE_DIR`，然后在插件里 `npm run sync`。

npm 依赖在插件的 `package.json` 里；本仓库的 `package.json` 用同样的版本，只用来测试。

## 测试

```
cd web
npm ci
npm test            # Vitest，src/ 和 test-provider/ 下的测试
npm run typecheck   # tsc -b
```

公共代码的测试放在这里，用 `test-provider` 的假插件跑；插件自己的逻辑在插件仓库测。
