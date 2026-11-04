# Claude Guard Monitor

基于 Go、Lua、nftables、WebSocket 和 SQLite 的 PassWall 独立防直连守卫与管理控制台。

程序读取 PassWall 的 `claude` 分流规则，将静态 IP 和域名学习结果写入独立 nftables 集合，阻断发往这些目标的直接转发流量。Go 服务负责日志采集、状态统计、历史查询和守卫管理，不承担代理流量转发。

## 界面

实时概览：

![实时概览](docs/images/overview.jpg)

部署与管理：

![部署与管理](docs/images/management.jpg)

## 工作逻辑

```text
PassWall UCI claude 规则
        │
        ▼
Lua 守卫：读取规则、学习域名 IP、同步目标集合
        │
        ▼
nftables inet claude_guard
  targets4 / targets6
  forward_guard：限速日志 → counter → drop
        │
        ▼
Go：采集日志、读取计数、查询服务状态
        │
        ▼
SQLite → HTTP API / WebSocket → Web 控制台
```

### 规则与地址学习

`bundle/guard.lua` 读取 `/etc/config/passwall` 中的 `claude` section，要求其类型为 `shunt_rules`。规则支持域名表达式、geosite 展开、IPv4/IPv6 地址和 CIDR。节点信息从全局分流配置的 Claude 节点绑定中读取，用于状态展示。

守卫循环检查配置变化；配置解析失败时尝试使用已保存的策略。目标集合由静态 IP 与域名学习缓存合并生成，通过 nft 批处理更新。规则删除后，相应域名的学习缓存会被清理。

`bundle/observer.lua` 读取 tcpdump 的 PCAP 流，处理指定来源和端口的 UDP DNS 响应，以及客户端 TCP/443 上可见的 TLS ClientHello。`bundle/tls.lua` 处理 TCP 分段、重排与 TLS 记录，提取 SNI。守卫还通过配置的本地 DNS 服务分批查询种子域名。

### 独立阻断

防护表为 `inet claude_guard`，包含 IPv4 集合 `targets4`、IPv6 集合 `targets6` 和 `forward_guard` 链。链使用 forward hook，优先级 -10，匹配连接原始方向和目标地址，执行计数与 DROP。

日志规则位于 DROP 之前，日志限速不限制 DROP。停止监控服务不删除防护表；停止守卫学习也保留已有规则。停用或卸载守卫才会删除本组件的表。

保护对象是已知目标的直接转发流量，不包含路由器本机 OUTPUT。DNS 与 SNI 采用被动学习，首次出现的未知 IP 可能尚未进入集合；不解析 ECH 内层域名和 QUIC。节点状态用于展示，程序不强制绑定代理核心内部出口。

### 日志采集

配置字段 `log_backend` 控制采集后端：

| 值 | 实现 |
| --- | --- |
| 省略 / `auto` | 已加载 `nfnetlink_log` 时使用 NFLOG，否则使用 syslog |
| `nflog` | 默认 group 10631，每地址族限速 30 包/秒，突发 100 包 |
| `syslog` | 读取 `logread -f`，每地址族限速 5 包/秒，突发 10 包 |

syslog 子进程使用 UTC，按日志服务时间入库，精度为秒，并过滤采集启动秒之前的记录。日志提取来源/目标 IP、端口、协议、包长和时间。域名字段来自学习缓存中的候选映射。

内核计数与详细事件分别采集。限速、采集停机、队列溢出或写入失败会使详细事件数少于内核阻断包数。

### Web 与存储

- HTTPS 登录；初始化生成随机管理员密码。
- 管理网段检查、登录限速、会话 Cookie、CSRF 与 Origin 校验。
- WebSocket 每秒推送状态和最近 6 条记录。
- 阻断记录与时间线使用服务端时间过滤和游标分页，每页最多 50 条。
- SQLite WAL 存储；CSV 导出按查询条件限制为最多 10,000 条。
- 详细事件保留 7 天、最多 150,000 行；统计保留上限 30 天、最多 180,000 行；时间线最多 10,000 行。
- 数据库默认 100 MiB 软阈值，达到阈值后暂停详细事件写入。

## 代码结构

| 路径 | 职责 |
| --- | --- |
| `main.go` | 配置、证书、初始化与服务入口 |
| `server.go` | HTTP API、会话、CSRF、WebSocket |
| `poll.go` | 防火墙计数、进程状态与日志规则维护 |
| `collector_linux.go` | NFLOG 采集 |
| `collector_syslog_linux.go` / `log_backend.go` | syslog 采集、解析及后端选择 |
| `packet.go` | IP 数据包头解析 |
| `store.go` | SQLite、分页查询、聚合与清理 |
| `lifecycle.go` | 守卫渲染、安装、备份、恢复与服务操作 |
| `bundle/` | Lua 守卫、PCAP 观察器、SNI 解析及 procd 脚本 |
| `web/` | HTML、CSS、JavaScript 控制台 |
| `deploy/` | 引导安装脚本与 Go procd 服务 |

## 构建

Go 版本要求见 `go.mod`。SQLite 使用纯 Go 驱动，构建时禁用 CGO。

```sh
go test ./...
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go build -trimpath -o dist/claude-guard-monitor-linux-amd64 .
```

PowerShell：

```powershell
.\build.ps1
.\build.ps1 -GoExecutable 'C:\path\to\go.exe'
.\build.ps1 -TestOnly
```

脚本运行测试并生成 Windows 与 Linux amd64 二进制。Go 可从 PATH 获取，也可通过参数指定。

## 部署

目标环境：Linux x86_64、OpenWrt 系列、procd、持久 `/overlay`，以及以下依赖：

- Lua 5.1：`uci`、`nixio`、`nixio.fs`、`luci.jsonc`。
- `nft`、`tcpdump`、`ubus`。
- geosite 规则需要 `geoview` 及对应数据文件。
- NFLOG 需要匹配固件内核的 `nfnetlink_log`；syslog 需要 `nft_log`、内核日志支持与 `logread`。

将 Linux 二进制与 `deploy/` 下两个文件放到同一目录，以 root 执行：

```sh
sh install.sh 192.168.10.1:9088 127.0.0.1/32,::1/128,192.168.10.0/24
```

地址和网段按目标设备填写。引导脚本安装 Go 服务、生成配置和随机密码、启用开机自启；不立即安装守卫。

登录凭据位于 `/etc/claude-guard-monitor/credentials.txt`。登录 HTTPS 控制台后，在“部署与管理”设置 DNS、可信来源、客户端网段与路由器 IP，保存参数，再执行“安装 / 更新守卫”。默认 TLS 证书由程序自签，也可替换配置目录中的证书与私钥。

## 配置与维护

| 路径 | 内容 |
| --- | --- |
| `/etc/claude-guard-monitor/config.json` | 监听地址、管理网段、密码哈希、日志后端、存储策略、守卫参数 |
| `/etc/claude-guard-monitor/server.crt` / `server.key` | TLS 证书与私钥 |
| `/overlay/claude-guard-monitor/monitor.db` | SQLite 数据库 |
| `/overlay/claude-guard-monitor/backup-*.zip` | 守卫安装/卸载备份 |
| `/usr/libexec/claude-guard/` | 守卫脚本 |
| `/etc/claude-guard/` | 策略与学习缓存 |

网页支持安装/更新、启动、停止学习、停用、卸载，以及守卫和 Go 服务的独立自启开关。安装前检查脚本与策略，备份已有文件；失败时尝试恢复旧脚本。

```sh
/etc/init.d/claude-guard status
/etc/init.d/claude-guard-monitor status
nft list chain inet claude_guard forward_guard
lua /usr/libexec/claude-guard/cli.lua status
```

升级二进制时先上传临时文件并校验，再停止 Go、替换、启动；保留配置、证书、密码和数据库。备份 SQLite 时停止 Go，并复制包含 WAL 的整个数据目录。

## 测试

Go 测试覆盖包头与日志解析、内核计数、SQLite、鉴权、分页、WebSocket 和参数渲染。目标机 Lua 测试：

```sh
lua /usr/libexec/claude-guard/test.lua
lua /usr/libexec/claude-guard/test-sni.lua
```

真实 WebSocket 测试默认跳过。设置 `CG_LIVE_TEST=1` 后，以 `CG_LIVE_URL`、`CG_LIVE_CA`、`CG_LIVE_CREDENTIALS` 和可选 `CG_LIVE_TLS_NAME` 指定测试目标及本地凭据。
