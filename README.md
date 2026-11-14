# cpa-provider-core

CLIProxyAPI 供应商插件（Wokey、Ollama、OpenCode、OpenDesign）共用的核心代码：插件 RPC 入口、执行请求、流式处理、请求日志、出口代理、凭证、管理接口，以及管理页的公共前端代码。

各供应商插件仓库只保留自己的适配器，通过 `go.mod` 引用本模块，并在构建管理页时从本模块拷贝公共前端代码。本仓库不单独发布插件。
