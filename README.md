# CLIProxyAPI（本仓库已转为插件形态）

本仓库原为 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 fork，用于维护额外的持久化用量统计等功能。现在这些增量功能已抽离为**独立插件**，本仓库不再维护 fork 代码。

## 插件

- **Usage Statistics（持久化用量统计）** — 将每次请求的用量持久化到 SQLite，并提供查询/删除管理接口，可直接运行在上游 CLIProxyAPI 上。
  👉 https://github.com/Fwindy/cpa-usage-statistics

请前往对应插件仓库获取安装包与文档。

---

This repository was a fork of CLIProxyAPI. Its extra features now live as standalone plugins:

- Usage Statistics → https://github.com/Fwindy/cpa-usage-statistics
