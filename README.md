# Codex 调度控制

CLIProxyAPI 的 Codex 账号并发与额度调度插件。插件在调度阶段过滤已达到额度阈值的账号，并优先选择仍有并发槽位的账号；请求真正发往上游前再进行原子占位，确保并发计数准确。

## 功能

- 默认每个 Codex 账号最多同时执行 3 个请求。
- 并发已满时等待空位，默认超时 5 分钟。
- 支持 5 小时和周额度停止阈值，默认均为 95%。
- 默认在调度前直接查询 Codex 实时额度。
- 可切换为 CPA Manager Plus 额度快照接口。
- 支持全局默认设置和单账号独立覆盖。
- 额度查询失败时可选择继续调度或暂停调度。
- 当前账号并发已满时会继续寻找其他有空位的账号，包括优先级较低的账号；只有全部账号都满时才排队。
- 同一个请求的上游重试不会重复计算并发；重试切换账号时占位会随之迁移。
- 提供 CLIProxyAPI 管理页面，显示并发、排队、额度与停止原因。

需要 CLIProxyAPI `v7.2.142` 或更高版本。该插件同时声明 Scheduler、Management API、Request Interceptor 和 Request Lifecycle 能力；请停用其他会接管 Codex Scheduler 的插件，避免调度冲突。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-concurrency-limiter:
      enabled: true
      priority: 80
      max_concurrency_per_account: 3
      queue_timeout_seconds: 300
      quota_source: realtime
      five_hour_enabled: true
      five_hour_cutoff_percent: 95
      weekly_enabled: true
      weekly_cutoff_percent: 95
      match_policy: any
      query_failure_policy: allow
```

页面设置保存在 `~/.cli-proxy-api/plugins/codex-concurrency-limiter/state.json`，文件权限为 `0600`。选择 CPA Manager Plus 时无需填写地址或凭证：插件会从当前管理中心请求自动读取服务地址，并像 codex-keepalive 一样复用“记住凭证”保存的管理凭证。管理接口不会把已保存的地址或凭证返回到浏览器。

## 构建与验证

```sh
make test
make vet
make package VERSION=0.0.1
```

## 自动发布

推送到 `main` 后，GitHub Actions 会自动运行测试并打包 macOS ARM64 插件，随后创建 GitHub Release。首次发布版本为 `v0.0.1`，之后按十进制进位：`v0.0.9` 的下一版是 `v0.1.0`，`v0.9.9` 的下一版是 `v1.0.0`。

本地查看页面：

```sh
node preview_server.cjs
```

然后打开 `http://127.0.0.1:8766/`。
