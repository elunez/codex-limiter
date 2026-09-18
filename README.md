# Codex 调度控制

CLIProxyAPI 的 Codex 账号并发与额度调度插件。插件在调度阶段过滤已达到额度阈值的账号，并优先选择仍有并发槽位的账号；请求真正发往上游前再进行原子占位，确保并发计数准确。

## 功能

- 可为每个 Codex 账号单独启用调度控制；未启用的账号仍由宿主正常调度。
- 启用后默认最多同时执行 3 个请求，并发已满时等待空位，默认超时 5 分钟。
- 每个账号可独立设置 5 小时和周额度停止阈值，默认均为 95%。
- 默认在调度前直接查询 Codex 实时额度。
- 可切换为 CPA Manager Plus 额度快照接口；快照不完整时自动实时补全该账号的完整额度。
- 额度查询失败时可按账号选择继续调度或暂停调度。
- 当前账号并发已满时会继续寻找其他有空位的账号，包括优先级较低的账号；只有全部账号都满时才排队。
- 同一个请求的上游重试不会重复计算并发；重试切换账号时占位会随之迁移。
- 提供 CLIProxyAPI 管理页面，显示并发、排队、额度与停止原因。

需要 CLIProxyAPI `v7.3.7` 或更高版本。插件会接收所有优先级的候选账号：高优先级账号并发已满或额度达到阈值时，请求会自动切换到下一个仍可调度的账号。该插件同时声明 Scheduler、Management API、Request Interceptor 和 Request Lifecycle 能力；请停用其他会接管 Codex Scheduler 的插件，避免调度冲突。

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-limiter:
      enabled: true
      priority: 99
```

插件优先级使用宿主配置字段，安装示例默认设置为 `99`；已有安装不会在更新时自动覆盖优先级，请在插件管理中将其调整为 `99`。

安装配置页不需要填写插件参数。调度控制页面的公共设置只包含额度数据源；并发数、等待时间、额度阈值、停止条件和查询失败策略都在账号设置中配置。账号设置保存在 `~/.cli-proxy-api/plugins/codex-limiter/state.json`，文件权限为 `0600`。选择 CPA Manager Plus 时无需填写地址或凭证：插件会从当前管理中心请求自动读取服务地址，并像 codex-keepalive 一样复用“记住凭证”保存的管理凭证。插件优先读取 Manager Plus 的额度快照；若某个账号缺少 5 小时或周额度，则实时查询并替换为该账号的完整额度，避免混用不同时间点的数据。管理接口不会把已保存的地址或凭证返回到浏览器。旧配置中的 `quota_source` 和 `state_path` 仍会继续解析，以保持兼容。

## 构建与验证

```sh
make test
make vet
make package VERSION=0.0.3
```

## 自动发布

推送到 `main` 后，GitHub Actions 会自动运行测试，为 Linux `amd64`、Linux `arm64`、macOS `amd64` 和 macOS `arm64` 打包，随后创建 GitHub Release。首次发布版本为 `v0.0.1`，之后按十进制进位：`v0.0.9` 的下一版是 `v0.1.0`，`v0.9.9` 的下一版是 `v1.0.0`。

## 安装

### 从插件商店安装

CLIProxyAPI 需要使用 `v7.3.7` 或更高版本。首次使用本仓库商店源时，在 `config.yaml` 的现有 `plugins` 节点中加入：

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/elunez/codex-limiter/main/registry.json"
```

重新加载配置后，在 CPA 管理中心打开“插件商店”，搜索“Codex 调度控制”或 `codex-limiter`，点击安装。安装器会自动选择当前系统和架构，并将插件写入 `plugins.dir` 对应目录。

如果已经安装过早期名称 `codex-concurrency-limiter`，请先在插件管理中停用并卸载旧插件，再刷新商店并安装 `codex-limiter`；两个标识不要同时启用。

安装完成后，在“插件管理”中启用插件。左侧菜单会出现“调度控制”：公共设置中选择额度数据源，在每个账号的设置中单独启用调度控制并配置并发与额度规则。插件页面会自动读取管理中心勾选“记住凭证”后保存的登录信息，无需另行填写 Manager Plus 地址或管理凭证。

更新时在 CPA 插件管理中点击“重新安装”即可。账号设置保存在 `~/.cli-proxy-api/plugins/codex-limiter/state.json`，只要未删除该目录，重新安装不会清空现有设置。

仓库根目录包含 CLIProxyAPI 插件商店使用的 `registry.json`。如需进入官方默认商店，还需要向 [`router-for-me/CLIProxyAPI-Plugins-Store`](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store) 提交对应条目。

### 手动安装

从 [GitHub Releases](https://github.com/elunez/codex-limiter/releases) 下载与系统和架构对应的压缩包，解压后将动态库放入 CLIProxyAPI 的插件运行目录。例如：

```text
plug/runtime/linux/amd64/codex-limiter.so
plug/runtime/linux/arm64/codex-limiter.so
plug/runtime/darwin/amd64/codex-limiter.dylib
plug/runtime/darwin/arm64/codex-limiter.dylib
```

随后在配置中启用插件，并重启或重新加载 CLIProxyAPI：

```yaml
plugins:
  enabled: true
  configs:
    codex-limiter:
      enabled: true
      priority: 99
```

加载成功后，管理中心左侧会出现“调度控制”。如果同时安装了其他接管 Codex Scheduler 的插件，请先停用它们，避免调度冲突。

本地查看页面：

```sh
node preview_server.cjs
```

然后打开 `http://127.0.0.1:8766/`。
