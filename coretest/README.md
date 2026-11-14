# 快照测试（coretest）

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

## fixture 格式

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

## 表示方式（输入输出相同，无损）

- CPA 以 `[]byte` 传、RPC JSON 里是 base64 的字段（`Payload`、`OriginalRequest`、`StorageJSON`、`storage_json`、`RawJSON`、`config_yaml`、`Body`、`ResponseBody`、`payload`）解码后展示：内容是能用 encoding/json 逐字节还原的 JSON 时展示成对象或数组，否则展示成原文字符串。runner 调插件前再编码回去。
- 不是 JSON、或不能逐字节还原的响应和宿主调用请求，原样存成字符串。
- 上游请求头就是 httptest 收到的样子（规范化的键）；请求体是原样字符串。
- 文件格式：保持成员顺序，两格缩进，不转义 HTML，末尾换行。

## 归一化（只有这些值允许每次运行不同）

- 占位符：httptest 上游 URL 写成 `{{UPSTREAM}}`，裸的 host:port 写成 `{{UPSTREAM_HOST}}`，每次运行的数据目录写成 `{{DATA_DIR}}`（含解析符号链接后的路径）。输入输出都一样。
- 只在 management.handle 的响应里，按名字把 `time`、`duration_ms`、`ttft_ms`、`last_frame_ms`、`created_at`、`proxy_id`、`latency_ms`、`output_tokens_per_second`、`e2e_tokens_per_second`，以及 `proxy` 下或 `proxies` 数组里对象的 `id`，替换成 `"<volatile>"`。两个速度字段在请求日志条目（带 `duration_ms` 的对象）里直接删掉，因为只有耗时超过 50 毫秒的调用才有。字符串里出现的同名 JSON 成员按文本替换。`Binding.VolatileFields` 追加插件自己的名字（OpenDesign 的 `log_since`）。
- `Binding.ConcurrentPaths` 里的路由（Ollama 的 `POST /api/show`）是插件并发请求的，连续的一段按请求体排序。
- `Binding.GeneratedIDs`：插件随机生成、通过 management.handle 返回的值（OpenDesign 的 `{{login_id}}`、`{{proxy_id}}`）。后续步骤的输入可以用占位符引用每类最新的值，输出里出现的每个值都写成 `"<volatile>"`。
- 其他一律不归一化：时钟固定，状态、目录、账号和 auth 刷新时间都精确比较。
