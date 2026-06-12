# WinDnsSyncAgent

WinDnsSyncAgent 是一个面向 Windows Server DNS 的轻量同步工具。源 DNS 服务器和目标 DNS 服务器都运行 Agent，管理员手动执行同步命令后，工具会从源服务器读取 DNS Zone 与解析记录，更新到目标服务器，并在同步完成后按配置改写目标服务器上的指定解析 IP。

项目不内置定时任务、不引入数据库，适合内网 DNS 迁移、灾备 DNS 同步、测试环境/容灾环境 IP 映射等场景。

## 目录

- [一、项目介绍](#一项目介绍)
- [二、环境要求与前提条件](#二环境要求与前提条件)
- [三、快速开始](#三快速开始)
- [四、配置说明](#四配置说明)
- [五、Windows Server 2008/2008 R2 兼容方案](#五windows-server-20082008-r2-兼容方案)
- [六、API 文档](#六api-文档)
- [七、同步行为说明](#七同步行为说明)
- [八、生产部署建议](#八生产部署建议)
- [九、安全说明](#九安全说明)
- [十、常见问题](#十常见问题)
- [十一、开发与构建](#十一开发与构建)
- [十二、版本历史](#十二版本历史)
- [十三、许可证](#十三许可证)
- [十四、联系方式](#十四联系方式)

# 一、项目介绍

## 1.1 项目简介

`windnssyncagent.exe` 提供两种运行模式：

- `agent`：在 DNS 服务器上启动 HTTP Agent，提供 DNS 查询和写入接口。
- `sync`：手动执行同步任务，读取同步配置，拉取源端数据并更新目标端数据。

典型架构：

```bash
源 DNS 服务器                  同步执行机/管理员机器                  目标 DNS 服务器
Agent 读接口        <----      sync 手动同步命令       ---->        Agent 写接口
```

如果源服务器或目标服务器是 Windows Server 2008/2008 R2，则可使用 `legacy/source-agent.ps1` 兼容 Agent。它用 WMI 读取 DNS Zone，用 `dnscmd.exe` 读取和写入解析记录；新系统仍建议优先使用 Go Agent。

## 1.2 核心功能

- 启动 HTTP Agent 提供 DNS Zone 和 Record 的查询/写入接口。
- 通过 Windows `DnsServer` PowerShell 模块操作新版本 Windows DNS 服务。
- 支持 Windows Server 2008/2008 R2 Legacy 兼容模式，通过 WMI 读取 Zone，通过 `dnscmd.exe` 读写解析记录。
- 支持 `mirror` 和 `addOnly` 两种同步模式。
- 支持同步后改写目标服务器上的指定 A/AAAA 记录 IP。
- 支持 `dryRun` 预览变更。
- 支持 API Key 认证。

## 1.3 项目结构

```bash
WinDnsSyncAgent/
├── cmd/
│   └── windnssyncagent/          # 程序入口
├── config/
│   ├── agent.json                # Agent 配置，Go Agent 和 Legacy Agent 共用
│   └── sync.json                 # 同步任务配置
├── legacy/
│   └── source-agent.ps1          # Windows Server 2008/2008 R2 Legacy 兼容 Agent
├── internal/
│   ├── agent/                    # HTTP Agent 服务
│   ├── config/                   # 配置加载与校验
│   ├── dns/                      # DNS 模型与 PowerShell Provider
│   └── syncer/                   # 同步客户端、diff、rewrite 逻辑
├── agent.ps1                     # 启动 Agent 的入口脚本
├── sync.ps1                      # 执行同步的入口脚本
├── windnssyncagent.exe
├── go.mod
├── LICENSE
└── README.md
```

# 二、环境要求与前提条件

## 2.1 服务器要求

标准 Go Agent 推荐 Windows Server 2012 或更高版本，生产推荐 Windows Server 2016+。服务器需要：

- 已安装 DNS Server 角色。
- 已安装 `DnsServer` PowerShell 模块。
- 运行 Agent 的账号具备 DNS 管理权限。
- 两端服务器之间网络互通，默认需要放通 Agent 端口，例如 `8443`。

如果源服务器或目标服务器是 Windows Server 2008/2008 R2，请使用第五章的 Legacy 兼容方案。

## 2.2 安装 DNS Server 角色

管理员 PowerShell 中执行：

```powershell
Install-WindowsFeature DNS -IncludeManagementTools
```

检查：

```powershell
Get-WindowsFeature DNS
```

## 2.3 安装 DnsServer PowerShell 模块

检查模块是否可用：

```powershell
Get-Module -ListAvailable DnsServer
Import-Module DnsServer
Get-DnsServerZone
```

如果模块不存在，安装 DNS 管理工具：

```powershell
Install-WindowsFeature RSAT-DNS-Server
```

Windows Server 2008/2008 R2 通常没有新版 `DnsServer` 模块，源端请使用 `legacy/source-agent.ps1`。

# 三、快速开始

## 3.1 编译程序

```powershell
go build -o windnssyncagent.exe ./cmd/windnssyncagent
```

## 3.2 准备配置

项目默认使用：

- [config/agent.json](config/agent.json)：Agent 配置。
- [config/sync.json](config/sync.json)：同步配置。

部署到服务器时，建议保留这个目录结构：

```text
C:\WinDnsSyncAgent\
├── windnssyncagent.exe
├── agent.ps1
├── agent.cmd
├── sync.ps1
├── sync.cmd
└── config\
    ├── agent.json
    └── sync.json
```

## 3.3 启动 Agent

标准 Go Agent：

```powershell
.\agent.ps1
```

如果是双击运行，或者窗口一闪而过看不到错误，可以改用：

```cmd
agent.cmd
```

启动 Windows Server 2008/2008 R2 Legacy Agent：

```cmd
agent.cmd -LegacySource
```

> **注**：
>
> - 在 Windows Server 2008/2008 R2 上，即使漏写 `-LegacySource`，`agent.ps1` 也会自动切换到 Legacy Agent，避免误启动不兼容的 Go Agent。
> - `.cmd` 启动器会自动使用 `-ExecutionPolicy Bypass`，并在 Agent 退出后暂停窗口，方便查看报错。
> - Legacy Agent 启动后可在控制台按 `Q` 停止。Windows Server 2008/PowerShell 2.0 下 `Ctrl+C` 有时无法打断正在等待 HTTP 请求的监听线程，因此推荐用 `Q` 退出。

如果系统提示“禁止执行脚本”，可以使用不修改全局策略的临时启动方式：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\agent.ps1
```

等价命令：

```powershell
.\windnssyncagent.exe agent -config .\config\agent.json
```

如果配置文件不在默认位置：

```powershell
.\agent.ps1 -Config .\my-agent.json
```

`agent.ps1` 为了兼容 Windows Server 2008 / PowerShell 2.0，没有使用 PowerShell 的 `param(...)` 参数块，而是手动解析命令行参数。因此在老系统上也可以直接执行上面的命令。

`agent.ps1` 正常启动后会一直运行，不会进入“按 Enter 退出”。`-NoPause` 只在启动失败或 Agent 进程异常退出时有用，用于避免脚本停在错误提示界面；常规手动启动不需要加这个参数。

```powershell
.\agent.ps1 -NoPause
```

## 3.4 手动同步

同步命令依赖 `windnssyncagent.exe sync`，请在 Windows Server 2012+ 或 Windows 10/11 管理机上执行。Windows Server 2008/2008 R2 只运行 Legacy Agent，不运行 `sync.cmd`。

第一次执行建议保持 `config/sync.json` 里的：

```json
"dryRun": true
```

执行：

```powershell
.\sync.ps1
```

如果窗口一闪而过，可以改用：

```cmd
sync.cmd
```

如果系统提示“禁止执行脚本”，可以使用：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\sync.ps1
```

等价命令：

```powershell
.\windnssyncagent.exe sync -config .\config\sync.json
```

确认输出无误后，将 `dryRun` 改为 `false` 再执行。

`sync.ps1` 的参数解析兼容旧 PowerShell，但同步核心是 Go 可执行文件，不能在 Windows Server 2008/2008 R2 上运行。2008/2008 R2 服务器只负责启动 `agent.cmd -LegacySource`，同步请放到新系统管理机执行。

脚本执行结束后默认会提示按 Enter 退出；如果是在 PowerShell ISE 中运行，直接在提示后按 Enter 即可。如果不需要暂停，可以加 `-NoPause`：

```powershell
.\sync.ps1 -NoPause
```

# 四、配置说明

## 4.1 agent.json

`config/agent.json` 示例：

```json
{
  "scheme": "http",
  "port": 8443,
  "allowAnonymous": true,
  "apiKey": "CHANGE_ME",
  "logPath": "C:\\ProgramData\\WinDnsSyncAgent\\agent.log"
}
```

| 字段 | 类型 | 说明 |
| :-: | :-: | :-: |
| `scheme` | string | 当前建议使用 `http`。 |
| `port` | number | Agent 监听端口。 |
| `allowAnonymous` | bool | 是否允许匿名访问。生产建议设为 `false`。 |
| `apiKey` | string | `allowAnonymous=false` 时客户端必须通过 `X-API-Key` 传入。 |
| `logPath` | string | 日志路径。 |

Go Agent 和 Windows Server 2008 Legacy Agent 都读取这份 `agent.json`。

## 4.2 sync.json

`config/sync.json` 示例：

```json
{
  "sourceAgent": "http://source-dns:8443",
  "targetAgent": "http://target-dns:8443",
  "apiKey": "",
  "includeZones": ["example.com"],
  "excludeZones": [],
  "zoneConcurrency": 2,
  "recordBatchSize": 50,
  "requestTimeoutSeconds": 90,
  "syncMode": "mirror",
  "dryRun": true,
  "createPtrRecords": false,
  "enableRewriteRecords": false,
  "rewriteRecords": [
    {
      "zone": "example.com",
      "name": "www",
      "type": "A",
      "oldIp": "192.168.10.10",
      "targetIp": "192.168.10.20"
    }
  ]
}
```

| 字段 | 类型 | 说明 |
| :-: | :-: | :-: |
| `sourceAgent` | string | 源 DNS 服务器 Agent 地址。 |
| `targetAgent` | string | 目标 DNS 服务器 Agent 地址。 |
| `apiKey` | string | 源和目标共用 API Key。 |
| `sourceApiKey` | string | 源 Agent 单独 API Key。未配置时使用 `apiKey`。 |
| `targetApiKey` | string | 目标 Agent 单独 API Key。未配置时使用 `apiKey`。 |
| `includeZones` | string[] | 要同步的 Zone 或 Zone 下的子级域目录。为空时默认同步源端全部正向业务区域，不自动同步反向区域，并跳过 `TrustAnchors` 等系统区域。显式配置 `test.cursor.com` 且源端存在 `cursor.com` Zone 时，会只同步 `cursor.com` 下的 `test` 节点及其子节点记录。旧配置字段 `zones` 仍可兼容读取，但建议迁移到 `includeZones`。 |
| `excludeZones` | string[] | 要排除的 Zone 或 Zone 下的子级域目录。默认为空数组，表示不排除任何区域。显式配置 `test.cursor.com` 且源端存在 `cursor.com` Zone 时，会排除 `cursor.com` 下的 `test` 节点及其子节点记录；若目标端存在独立的 `test.cursor.com` Zone，即使源端不存在该 Zone，`mirror` 模式也不会删除该目标 Zone。 |
| `zoneConcurrency` | number | Zone 级并发数。默认 `2`，建议生产使用 `2` 到 `4`，最大允许 `16`。 |
| `recordBatchSize` | number | 每次提交给目标 Agent 的记录数量。默认 `50`，最大允许 `500`。目标端为 Windows Server 2008/2008 R2 Legacy Agent 时建议配置为 `5` 到 `10`；目标端为 Windows Server 2012+ Go Agent 时可使用默认值或按性能调大。 |
| `requestTimeoutSeconds` | number | 同步端等待源/目标 Agent 单次 HTTP 请求响应的超时时间。默认 `90` 秒，最大允许 `3600` 秒。目标端为 Windows Server 2008/2008 R2 Legacy Agent 且批量写入较慢时，可配置为 `300` 或 `600`。 |
| `syncMode` | string | `mirror` 或 `addOnly`。 |
| `dryRun` | bool | `true` 只预览写入动作，`false` 才真正修改目标 DNS。无论是否 dry-run，都会真实连接源/目标并读取数据。 |
| `createPtrRecords` | bool | 新增 A 记录时是否尝试创建关联 PTR 指针记录，类似 DNS 控制台里的“更新关联的指针记录”。默认 `false`。 |
| `enableRewriteRecords` | bool | 是否启用 `rewriteRecords`。默认 `false`，避免配置了示例改写规则后被误执行。 |
| `rewriteRecords` | object[] | 同步完成后改写目标端指定 A/AAAA 记录。只有 `enableRewriteRecords=true` 时才生效。`oldIp` 为必填字段，`ttl` 为可选字段。 |

如果源和目标的 `agent.json` 都配置了相同 `apiKey`，在 `sync.json` 写 `apiKey` 即可。如果两端不同，则分别写 `sourceApiKey` 和 `targetApiKey`。

# 五、Windows Server 2008/2008 R2 兼容方案

## 5.1 推荐架构

- 源 `Windows Server 2008`：`legacy/source-agent.ps1` ，读取 DNS，也可执行写入
- 目标 `Windows Server 2012+`：`windnssyncagent.exe agent` ，负责写入 DNS
- 同步执行机：`Windows Server 2012+` 或 `Windows 10/11` ，运行 `windnssyncagent.exe sync` ，手动执行同步

推荐目标端继续使用 Go Agent，因为它支持 `Set-DnsServerResourceRecord` 这类新模块能力，更新 A/AAAA 记录更直接。如果目标端也只能是 Windows Server 2008/2008 R2，可以同样启动 Legacy Agent，此时写操作会通过 `dnscmd.exe` 完成。

Legacy Agent 提供与 Go Agent 基本一致的接口：

- `GET /health`
- `GET /dns/zones`
- `GET /dns/zones/{zone}/records`
- `POST /dns/zones`
- `DELETE /dns/zones/{zone}`
- `POST /dns/zones/{zone}/records`
- `DELETE /dns/zones/{zone}/records/{type}/{name}?value={value}`
- `POST /dns/zones/{zone}/records/batch`

Go Agent 额外提供 body 版记录接口，避免 Zone 名称出现在明文 HTTP URL 路径中：

- `POST /dns/records/query`
- `POST /dns/records/batch`

同步程序会优先调用 Go Agent 的 body 版接口；如果目标端是 2008/2008 R2 Legacy Agent 或旧版 Go Agent，接口返回 `404` 后会自动回退到上方旧路径接口。

## 5.2 前提条件

2008/2008 R2 服务器需要：

- 已安装 DNS Server 角色。
- PowerShell 可用，建议 PowerShell 2.0+。Windows Server 2008 SP2 如果仍是 PowerShell 1.0，请先安装 Windows Management Framework Core 2.0（`Windows6.0-KB968930-x64.msu`），详见 [10.9](#109-windows-server-2008-sp2-运行脚本时报--executionpolicy-或--File-参数错误怎么办)。
- 当前账号可以读取 `root\MicrosoftDNS` WMI 命名空间。
- 如果需要写操作，当前账号需要 DNS 管理权限，并且系统能找到 `dnscmd.exe`。
- 如果 2008/2008 R2 作为目标端接收写入请求，需要可加载 `.NET Framework` 的 `System.Web.Extensions` 程序集，用于解析批量写入接口的 JSON 请求体。多数 2008 R2 环境可通过启用/安装 .NET Framework 3.5.1 或 4.x 满足，详见 [10.8](#108-2008-目标端报-json-request-body-parsing-requires-net-systemwebextensions-怎么办)。
- 防火墙放通 Agent 端口。

检查 WMI：

```powershell
Get-WmiObject -Namespace "root\MicrosoftDNS" -Class "MicrosoftDNS_Zone"
```

检查 `dnscmd.exe`：

```powershell
dnscmd.exe /Info
```

如果提示找不到命令，请安装 DNS Server 管理工具，或确认 DNS Server 角色和管理工具已完整安装。

## 5.3 启动 Legacy Agent

把项目目录复制到 2008/2008 R2 服务器后，直接执行：

```powershell
.\agent.ps1 -LegacySource
```

Legacy 脚本会优先读取：

```text
legacy\agent.json
```

如果不存在，会继续查找：

```text
config\agent.json
agent.json
```

因此常规部署只需要保留 `config\agent.json` 即可。

## 5.4 Legacy 写操作限制

Legacy 写入使用 `dnscmd.exe`，主要用于解决 Windows Server 2008/2008 R2 没有新版 `DnsServer` PowerShell 模块的问题。它和 Go Agent 的行为有几个差异：

- 支持同步常用记录类型：`A`、`AAAA`、`CNAME`、`MX`、`TXT`、`PTR`、`NS`、`SRV`。其中非根节点 `NS` 记录用于同步 DNS 委派。
- 读取记录时先使用 `dnscmd.exe /EnumRecords <zone> @ /Additional` 枚举根节点记录，再使用 `dnscmd.exe /ZonePrint <zone>` 从完整记录名中发现子级目录和单标签委派节点，并对这些目录继续执行 `dnscmd.exe /EnumRecords <zone> <node> /Additional` 补充枚举，例如 `yfb`、`test.yfb`、`vdesktop`。解析时会根据 `dnscmd.exe` 输出中的当前节点上下文拼接子域记录名，例如 `aduser.boss`。如果旧环境只接受 `/Addtional` 拼写，Legacy Agent 会自动回退重试。Windows Server 2008/2008 R2 的 MicrosoftDNS WMI 在部分环境下无法稳定读取子域记录，因此 Legacy Agent 不再使用 WMI 作为记录读取路径。
- 同名多 IP 记录在 `dnscmd.exe` 输出中可能只有第一行显示名称，后续行只显示 TTL、类型和值；Legacy Agent 会用独立的“上一条记录名”状态继承续行，避免后续 IP 被误判为 `@` 记录，也避免普通记录名污染当前节点上下文。
- `SOA` 和 Zone 根节点 `NS` 会被保护，不会新增、更新或删除；非根节点 `NS` 会作为委派记录参与同步。
- `update` 没有真正的原生命令，会兼容实现为“删除旧记录，再新增新记录”。如果新记录已经存在，会跳过。
- `dnscmd.exe` 写入时当前不强制设置 TTL，实际 TTL 由 Windows DNS 默认行为决定；同步对比仍会读取 TTL，但 2008 Legacy 写入不会保证 TTL 完全一致。
- `createPtrRecords=true` 时，新增 A 记录后会 best-effort 尝试在对应反向 Zone 中创建 PTR；反向 Zone 不存在或 PTR 已存在时只记录 warning，不会让同步失败。

# 六、API 文档

## 6.1 认证规则

Agent 是否校验 API Key 只由 `agent.json` 里的 `allowAnonymous` 决定。

仅配置 `apiKey` 不代表已经开启认证。例如下面这个配置仍然允许浏览器直接访问 `/dns/zones`：

```json
{
  "allowAnonymous": true,
  "apiKey": "MY_SECRET"
}
```

原因是 `allowAnonymous=true` 表示允许匿名访问，Agent 不会检查 `X-API-Key`。

如果希望浏览器直接访问 `/dns/zones` 返回 `401 Unauthorized`，必须这样配置：

```json
{
  "allowAnonymous": false,
  "apiKey": "MY_SECRET"
}
```

开启认证后，除 `/health` 外，其它接口都需要携带请求头：

```http
X-API-Key: MY_SECRET
```

`GET /health` 始终允许匿名访问，便于浏览器检查、负载均衡探测和基础监控。

## 6.2 浏览器访问说明

普通浏览器地址栏无法直接添加 `X-API-Key` 请求头。因此：

- `allowAnonymous=true` 时，浏览器可以直接打开 `http://server:8443/dns/zones`。
- `allowAnonymous=false` 时，浏览器直接打开 `/health` 仍可访问，打开 `/dns/zones` 会返回 `401`。
- 开启认证后，请使用 `Invoke-RestMethod`、`curl`、Postman，或同步程序访问接口。

## 6.3 PowerShell 调用示例

匿名访问：

```powershell
Invoke-RestMethod http://127.0.0.1:8443/dns/zones
```

携带 API Key：

```powershell
$headers = @{ "X-API-Key" = "MY_SECRET" }
Invoke-RestMethod http://127.0.0.1:8443/dns/zones -Headers $headers
```

Windows Server 2008 / PowerShell 2.0 环境如果没有 `Invoke-RestMethod`，可以使用 `Invoke-WebRequest` 或在其他机器上用 `curl` 测试。

## 6.4 curl 调用示例

```powershell
curl.exe -H "X-API-Key: MY_SECRET" http://127.0.0.1:8443/dns/zones
```

新增记录示例：

```powershell
curl.exe -X POST `
  -H "X-API-Key: MY_SECRET" `
  -H "Content-Type: application/json" `
  -d "{\"name\":\"www\",\"type\":\"A\",\"value\":\"192.168.10.20\",\"ttl\":3600}" `
  http://127.0.0.1:8443/dns/zones/example.com/records
```

## 6.5 同步配置与 API Key

如果源和目标 Agent 都配置了相同的 `apiKey`：

```json
{
  "apiKey": "MY_SECRET"
}
```

如果源和目标 Agent 的密钥不同：

```json
{
  "sourceApiKey": "SOURCE_SECRET",
  "targetApiKey": "TARGET_SECRET"
}
```

同步程序访问源 Agent 时会带 `sourceApiKey`，访问目标 Agent 时会带 `targetApiKey`。如果没有配置 `sourceApiKey` / `targetApiKey`，会自动使用通用的 `apiKey`。

## 6.6 接口列表

| 方法 | 路径 | Go Agent | 2008 Legacy Agent | 说明 |
| :-: | :-: | :-: | :-: | :-: |
| `GET` | `/health` | 支持 | 支持 | 健康检查。始终允许匿名访问。 |
| `GET` | `/dns/zones` | 支持 | 支持 | 获取 DNS Zone 列表。 |
| `POST` | `/dns/zones` | 支持 | 支持 | 创建 Primary Zone。Legacy Agent 使用 `dnscmd.exe /ZoneAdd`。 |
| `DELETE` | `/dns/zones/{zone}` | 支持 | 支持 | 删除指定 Zone。`mirror` 模式下可能用于删除目标端多余 Zone。 |
| `GET` | `/dns/zones/{zone}/records` | 支持 | 支持 | 获取指定 Zone 的解析记录。 |
| `POST` | `/dns/records/query` | 支持 | 不支持 | 获取指定 Zone 的解析记录，Zone 名称通过 JSON body 的 `zone` 字段传递。同步程序优先使用，Legacy Agent 返回 `404` 后自动回退旧 GET 接口。 |
| `POST` | `/dns/zones/{zone}/records` | 支持 | 支持 | 新增解析记录。Legacy Agent 使用 `dnscmd.exe /RecordAdd`。 |
| `DELETE` | `/dns/zones/{zone}/records/{type}/{name}?value={value}` | 支持 | 支持 | 删除精确匹配的解析记录。Legacy Agent 使用 `dnscmd.exe /RecordDelete`。 |
| `POST` | `/dns/zones/{zone}/records/batch` | 支持 | 支持 | 批量新增、删除、更新记录。同步程序主要使用此接口。 |
| `POST` | `/dns/records/batch` | 支持 | 不支持 | 批量新增、删除、更新记录，Zone 名称通过 JSON body 的 `zone` 字段传递，批次内容通过 `batch` 字段传递。同步程序优先使用，Legacy Agent 返回 `404` 后自动回退旧 batch 接口。 |

当前 Agent API 支持读取记录类型：`A`、`AAAA`、`CNAME`、`MX`、`TXT`、`PTR`、`NS`、`SRV`。Legacy 写操作支持 `A`、`AAAA`、`CNAME`、`MX`、`TXT`、`PTR`、`NS`、`SRV`。

同步逻辑会排除 `SOA` 和 Zone 根节点 `NS` 记录：不新增、不更新、不删除。非根节点 `NS` 记录会作为 DNS 委派参与同步。Legacy Agent 收到 `SOA` 或根节点 `NS` 写入请求时会跳过并记录 warning。

## 6.7 返回格式

成功响应：

```json
{
  "success": true,
  "data": [],
  "requestId": "..."
}
```

失败响应：

```json
{
  "success": false,
  "error": {
    "code": "UNAUTHORIZED",
    "message": "invalid api key"
  },
  "requestId": "..."
}
```

# 七、同步行为说明

## 7.1 记录唯一键

同步对比使用：

```text
zone + type + name + value
```

TTL 不参与是否同一条记录的判断。新增记录时使用源端 TTL；缺省为 `3600`。

### 7.1.1 dryRun 的作用

`dryRun=true` 表示只生成执行计划，不真正修改目标 DNS。它会执行以下动作：

- 检查源 Agent 和目标 Agent 是否在线。
- 从源 Agent 读取 Zone 和解析记录。
- 从目标 Agent 读取 Zone 和解析记录。
- 对比源/目标差异。
- 输出将要创建的 Zone、将要新增/删除的记录、将要改写的记录。

它不会执行以下动作：

- 不会在目标服务器创建 Zone。
- 不会在目标服务器新增记录。
- 不会在目标服务器删除记录。
- 不会在目标服务器执行 IP 改写。

因此，如果 `includeZones` 里配置了源端不存在的 Zone，即使 `dryRun=true` 也会报错，例如：

```text
zone example.com not found on source
```

解决方式是把 `config/sync.json` 里的 `includeZones` 改成源 DNS 上真实存在的 Zone，或者先将 `includeZones` 配置为空数组，让程序同步源端全部正向 Zone：

```json
"includeZones": []
```

`includeZones` 为空时，程序默认只同步源端正向查找区域，不会自动同步反向查找区域，并且会跳过 `TrustAnchors` 这类 Windows DNS 系统区域。条件转发器不属于普通业务解析 Zone，不参与同步，也不会在目标端创建为正向查找区域。如果确实要同步某个反向 Zone，需要显式写入：

```json
"includeZones": [
  "sunline.cn",
  "1.168.192.in-addr.arpa"
]
```

如果需要排除某个 Zone 或子级域目录，可以配置 `excludeZones`。默认空数组表示不排除任何区域：

```json
"excludeZones": [
  "test.cursor.com"
]
```

`excludeZones` 会同时用于保护目标端独有 Zone。比如目标端存在独立的 `test.cursor.com` Zone，而源端不存在该 Zone 时，配置上述排除项后，`mirror` 模式不会删除目标端的 `test.cursor.com` Zone。如果目标端只有 `cursor.com` Zone，则该配置仍按子级域目录处理，只排除 `cursor.com` 下的 `test` 节点及其子节点记录。

不建议同步 `TrustAnchors`。它是 DNSSEC 信任锚相关的系统区域，不属于普通业务解析区域。

### 7.1.2 更新关联 PTR 指针记录

如果希望同步正向 A 记录时，像 Windows DNS 控制台里的“更新关联的指针记录”一样尝试创建 PTR，可以开启：

```json
"createPtrRecords": true
```

开启后，程序新增目标端 A 记录时会把 `createPtr` 标记传给目标 Agent。目标 Agent 会先创建 A 记录，再根据 IPv4 地址尝试创建对应 PTR。

注意：

- 只对新增 `A` 记录生效，不处理 `AAAA`、`CNAME` 等其它类型。
- PTR 创建是 best-effort：如果目标 DNS 上没有对应反向查找区域、PTR 已存在、权限不足或其它原因导致 PTR 创建失败，A 记录仍然保留，同步不会失败。
- PTR 创建失败时目标 Agent 会输出 PowerShell warning，后续可按需手动补反向区域或 PTR。
- `dryRun=true` 时只会显示新增计划，不会真正创建 A 或 PTR。
- `rewriteRecords` 新增目标 A 记录时也会遵守此开关。

## 7.2 mirror 模式

- 源有 Zone、目标没有：在目标创建 Zone。
- 目标有 Zone、源没有：从目标删除 Zone。反向区域、`TrustAnchors` 等系统区域，以及命中 `excludeZones` 的目标端独有 Zone 会跳过。
- 源有、目标没有：新增到目标。
- 同名同类型的单条 `A` / `AAAA` 记录 IP 不同：优先执行更新。
- 目标有、源没有：从目标删除。
- `SOA` 和 Zone 根节点 `NS` 记录不参与同步，不新增、不更新、不删除；非根节点 `NS` 记录会作为 DNS 委派参与同步。

## 7.3 addOnly 模式

- 源有 Zone、目标没有：在目标创建 Zone。
- 目标有 Zone、源没有：保留不动。
- 源有、目标没有：新增到目标。
- 同名同类型的单条 `A` / `AAAA` 记录 IP 不同：优先执行更新。
- 目标有、源没有：保留不动。

创建 Zone 时，目标 Agent 会统一创建可写的 Primary Zone。即使源端 Zone 被识别为 Secondary、Stub 或旧系统 WMI 返回数字类型，目标端也会按 Primary Zone 创建。Go Agent 会优先尝试创建 AD 集成 Primary Zone，并使用 `ReplicationScope=Domain`；如果目标服务器不支持或不适用 AD 集成，会回退为文件型 Primary Zone。Legacy Agent 会使用 `dnscmd.exe /ZoneAdd <zone> /Primary` 创建。

### 7.3.1 批量写入与更新

同步程序会先按 Zone 计算新增、更新和删除操作。`dryRun=true` 时只输出完整计划，不会写入目标端；`dryRun=false` 时会按 `recordBatchSize` 配置分批处理，每批先输出该批计划日志，再提交给目标 Agent：

```text
POST /dns/records/batch
```

每次提交优先使用目标 Go Agent 的 body 版批量接口，避免 Zone 名称出现在 HTTP URL 路径中；目标端为 Windows Server 2008/2008 R2 Legacy Agent 或旧版 Go Agent 时会自动回退到 `POST /dns/zones/{zone}/records/batch`。单批规模由 `recordBatchSize` 控制，默认 `50`。这样可以减少超大批次触发 HTTP 超时的概率。`requestTimeoutSeconds` 针对每一次 HTTP 请求单独计时，包括每一批记录写入；它不是整个同步任务的总超时时间。目标端为 Windows Server 2008/2008 R2 Legacy Agent 时建议配置为 `5` 到 `10`，并按需把 `requestTimeoutSeconds` 调整为 `300` 或 `600`；目标端为 Windows Server 2012+ Go Agent 时可使用默认值或按性能调大。Go Agent 执行 PowerShell 时会先写入临时 `.ps1` 文件再通过 `-File` 运行，避免记录内容较长时触发 Windows 命令行长度限制。

对于同一个 Zone 中同名同类型的单条 `A` / `AAAA` 记录，如果只是 IP 不同且当前为 `mirror` 模式，程序会识别为 `update`。Go Agent 会优先使用 `Set-DnsServerResourceRecord` 更新记录，更新失败时回退为删除旧记录再新增新记录；Windows Server 2008/2008 R2 Legacy Agent 没有原生 update 命令，会直接用 `dnscmd.exe` 删除旧记录再新增新记录。对于同名同类型的多 IP 记录，包括名称为 `@` 的“与父文件夹相同”记录，程序会按 `zone + type + name + value` 精确判断，源端缺少的 IP 才新增，目标端多余的 IP 仅在 `mirror` 模式删除。程序内部统一使用 `@` 表示控制台里的“名称为空则使用父域名称”。

Go Agent 写入记录时会规范化记录名：`@`、空名称、`.` 和与 Zone 同名的 FQDN 都会按“与父文件夹相同”写入；形如 `qq.test3.test.cursor.com` 的完整 FQDN 会先转换为当前 Zone 内的相对名称 `qq.test3.test`，再由 Windows DNS 在对应子级目录下创建记录。写入后会按规范化名称和记录值二次查询验证，避免记录被误建到根节点或因 `@` 名称差异导致校验失败。

删除操作是幂等的：如果目标记录已经不存在，会输出 warning 并跳过，不会让同步失败。Go Agent 删除记录后会从被删记录名对应的节点开始向上检查空目录节点；如果确认节点及其子节点下都没有任何资源记录，会 best-effort 调用 `dnscmd.exe /NodeDelete <zone> <node> /f` 清理 DNS 控制台里的空子级目录。空目录清理失败只输出 warning，不会影响记录删除结果。

新增操作也是幂等的：如果目标记录已经存在，会输出 warning 并跳过。某些 Windows DNS 版本可能出现“PowerShell 返回错误但记录实际已经更新成功”的情况，目标 Agent 会二次检查目标记录；如果新记录已经存在，会把这次 update 视为成功，不再回退新增。

### 7.3.2 Zone 级并发

同步程序支持按 Zone 并发处理，通过 `zoneConcurrency` 控制同时处理的 Zone 数量：

```json
"zoneConcurrency": 2
```

每个 Zone 内部仍然按顺序执行“必要时创建目标 Zone、读源记录、读目标记录、diff、分批写目标”，避免同一个 Zone 内并发写入造成锁冲突。

如果目标端缺少多个 Zone，创建 Zone 的动作也会跟随 `zoneConcurrency` 并发执行。例如 `zoneConcurrency=2` 时，最多同时创建 2 个缺失 Zone。

`rewriteRecords` 会在所有 Zone 同步任务全部完成且没有错误之后才执行。如果任意 Zone 同步失败，rewrite 不会执行，避免在半同步状态下改写 IP。

同步命令会实时输出每个 Zone 的计划和执行动作，不会等全部同步结束后才一次性打印。由于多个 Zone 会并发处理，不同 Zone 的输出顺序可能和配置顺序不同。

`mirror` 模式下，目标端多余 Zone 的删除会在 Zone 并发同步前串行执行。当前每删除一个 Zone 会调用一次目标 Agent，因此目标端会启动一次 PowerShell；这一步通常数量较少且风险较高，所以先保持串行处理。

## 7.4 IP 改写流程

`rewriteRecords` 默认不执行。只有配置了下面的开关才会在普通同步完成后执行：

```json
"enableRewriteRecords": true
```

启用后，流程为：

1. 在目标服务器读取指定 Zone 的记录。
2. 找到匹配 `zone + name + type` 的记录。
3. `oldIp` 为必填，只处理值等于 `oldIp` 的记录。
4. 优先使用 update 将旧 IP 改成 `targetIp`。
5. 如果目标 IP 已存在，则跳过。
6. 如果没有找到 `oldIp` 对应的旧记录，则返回错误，不再自动新增，避免同名多 IP 场景误改。

rewrite 会继承旧记录 TTL；如果配置了 `ttl` 且大于 0，则使用配置值。

如果 `rewriteRecords` 里有内容，但 `enableRewriteRecords=false`，程序会跳过改写，并输出：

```text
rewriteRecords skipped because enableRewriteRecords=false
```

# 八、生产部署建议

## 8.1 部署目录

```text
C:\Program Files\WinDnsSyncAgent\
├── windnssyncagent.exe
├── agent.ps1
├── sync.ps1
└── config\agent.json
```

同步执行机需要额外放置：

```text
config\sync.json
```

## 8.2 注册为 Windows 服务

当前程序暂未内置服务安装命令，可以用 `sc.exe`：

```powershell
sc.exe create WinDnsSyncAgent `
  binPath= "\"C:\Program Files\WinDnsSyncAgent\windnssyncagent.exe\" agent -config \"C:\Program Files\WinDnsSyncAgent\config\agent.json\"" `
  start= auto
```

启动服务：

```powershell
sc.exe start WinDnsSyncAgent
```

停止服务：

```powershell
sc.exe stop WinDnsSyncAgent
```

## 8.3 同步建议

- 首次同步必须使用 `dryRun=true`。
- `mirror` 模式会删除目标端多余 Zone 和多余记录，使用前要确认目标端没有必须保留的本地区域或记录。
- 如果目标端存在本地专用记录，优先使用 `addOnly`。
- 对关键 Zone 操作前，建议先导出目标 DNS 记录作为备份。

# 九、安全说明

- 生产环境建议设置 `allowAnonymous=false`，并配置强随机 `apiKey`。
- Agent 端口只允许内网管理机器访问，不建议暴露公网。
- 使用 Windows 防火墙限制访问来源 IP。
- 不要将包含真实 `apiKey` 的配置文件提交到仓库。
- 当前版本不内置 HTTPS 证书监听；如果网络环境不可信，建议放在 VPN、专线或反向代理 TLS 后面使用。

# 十、常见问题

## 10.1 `agent.json` 配置了 API Key，`sync.json` 也要配置吗？

如果 Agent 设置：

```json
"allowAnonymous": false
```

那么 `sync.json` 必须配置对应 key。源和目标相同写 `apiKey`；不同则写 `sourceApiKey` 和 `targetApiKey`。

## 10.2 源或目标是 Windows Server 2008 怎么办？

源端可以使用：

```powershell
.\agent.ps1 -LegacySource
```

目标端如果是 Windows Server 2012+，继续使用标准 Go Agent；如果目标端也是 Windows Server 2008/2008 R2，也可以使用同一个 Legacy Agent，但需要确认 `dnscmd.exe` 可用，并了解 update 会以“删除旧记录再新增新记录”的方式执行。

同步执行机不能是 Windows Server 2008/2008 R2。请在 Windows Server 2012+ 或 Windows 10/11 管理机上运行 `sync.cmd`，让它通过 HTTP 调用 2008/2008 R2 上的 Legacy Agent。

## 10.3 找不到 DnsServer 模块怎么办？

Windows Server 2012+ 可安装：

```powershell
Install-WindowsFeature RSAT-DNS-Server
```

Windows Server 2008/2008 R2 不使用该模块，Legacy Agent 使用 WMI 读取 DNS Zone，使用 `dnscmd.exe` 读取和写入解析记录。

## 10.4 mirror 模式会删除哪些记录？

如果目标端存在源端没有的记录，`mirror` 模式会尝试删除；但 `SOA` 和 Zone 根节点 `NS` 记录不参与同步，不会被新增、更新或删除。非根节点 `NS` 委派记录会参与同步。若不希望删除目标端已有记录，请使用 `addOnly`。

## 10.5 同步时报 `zone example.com not found on source` 怎么办？

这是因为 `config/sync.json` 示例里默认写了：

```json
"includeZones": ["example.com"]
```

但源 DNS 服务器上没有 `example.com` 这个 Zone。请把它改成源服务器上真实存在的 Zone，例如：

```json
"includeZones": ["your-domain.local"]
```

如果想同步源端全部正向查找区域，可以写成空数组：

```json
"includeZones": []
```

注意：`dryRun=true` 也会真实读取源端 Zone，所以源端不存在的 Zone 仍然会报错。

## 10.6 执行脚本时报 `无法将 param 项识别为 cmdlet` 怎么办？

请确认服务器上的 `agent.ps1` 和 `sync.ps1` 是最新版本。新版脚本已经移除了 `param(...)` 参数块，兼容 Windows Server 2008 / PowerShell 2.0。

如果仍然报错，通常是部署目录里还残留旧脚本。请重新复制以下文件：

```text
agent.ps1
agent.cmd
sync.ps1
sync.cmd
legacy\source-agent.ps1
windnssyncagent.exe
config\agent.json
config\sync.json
```

重新复制后再执行：

```powershell
.\agent.ps1
.\sync.ps1
```

## 10.7 执行脚本时报 `因为在此系统中禁止执行脚本` 怎么办？

这是 PowerShell 执行策略拦截了 `.ps1` 脚本，不是 WinDnsSyncAgent 本身报错。

推荐使用临时绕过方式，不修改系统全局策略：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\agent\agent.ps1
```

同步脚本：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\agent\sync.ps1
```

如果希望当前用户长期允许本机脚本执行，可以执行：

```powershell
Set-ExecutionPolicy RemoteSigned -Scope CurrentUser
```

查看当前执行策略：

```powershell
Get-ExecutionPolicy -List
```

## 10.8 2008 目标端报 `JSON request body parsing requires .NET System.Web.Extensions` 怎么办？

这个错误只针对 Windows Server 2008/2008 R2 Legacy Agent 作为目标端接收写入请求的场景，例如：

```text
POST /dns/zones/google.com/records/batch failed: JSON request body parsing requires .NET System.Web.Extensions. Install/enable .NET Framework 3.5/4.x on this server.
```

原因是目标端 2008 Legacy Agent 需要使用 `.NET System.Web.Extensions` 解析批量写入接口的 JSON 请求体，但当前服务器没有启用或安装对应 .NET 组件。2012+ 中转机和标准 Go Agent 不需要处理这个问题。

先在目标 2008/2008 R2 上检查组件是否可加载：

```powershell
[Reflection.Assembly]::LoadWithPartialName("System.Web.Extensions")
```

如果返回为空或报错，请在目标 2008/2008 R2 上启用 .NET Framework 3.5.1 功能。常见命令如下：

```cmd
servermanagercmd -install NET-Framework-Core
```

也可以通过图形界面启用：

```text
服务器管理器 -> 功能 -> 添加功能 -> .NET Framework 3.5.1 功能
```

如果环境无法启用 3.5.1，安装 .NET Framework 4.x 也可以满足 `System.Web.Extensions` 依赖。安装完成后重新执行检查命令，确认能返回程序集信息，然后重启目标端 Legacy Agent：

```cmd
agent.cmd -LegacySource
```

或使用当前部署方式重新启动 `legacy/source-agent.ps1`。如果仍然报错，请确认正在运行的是更新后的 Legacy Agent 脚本，并检查服务器是否需要重启后才能加载新安装的 .NET 组件。

## 10.9 Windows Server 2008 SP2 运行脚本时报 `-ExecutionPolicy` 或 `-File` 参数错误怎么办？

如果在 Windows Server 2008 SP2 上执行 `agent.cmd` 或手动运行 PowerShell 命令时出现类似错误：

```text
一元运算符“-”后缺少表达式。
所在位置 行:1 字符: 2
+ -E <<<< xecutionPolicy Bypass -File .\agent.ps1
```

通常说明当前系统仍是 PowerShell 1.0，无法正确支持本项目启动脚本使用的 `-ExecutionPolicy`、`-File` 等参数。请先安装 Windows Management Framework Core 2.0，将 PowerShell 升级到 2.0。

Windows Server 2008 SP2 x64 对应安装包为：

```text
Windows6.0-KB968930-x64.msu
```

安装方式示例：

```cmd
wusa.exe Windows6.0-KB968930-x64.msu
```

安装完成后建议重启服务器，然后检查 PowerShell 版本：

```cmd
powershell.exe -NoProfile -Command "$PSVersionTable.PSVersion"
```

如果 `$PSVersionTable` 没有输出，也可以检查 Host 版本：

```cmd
powershell.exe -NoProfile -Command "$host.Version"
```

确认 PowerShell 已升级到 2.0 后，再重新启动 Legacy Agent：

```cmd
agent.cmd -LegacySource
```

该补丁主要针对 Windows Server 2008 SP2 / PowerShell 1.0 环境。Windows Server 2008 R2 通常已内置 PowerShell 2.0，一般不需要安装 `Windows6.0-KB968930-x64.msu`。

# 十一、开发与构建

## 11.1 测试

```powershell
go test ./...
```

## 11.2 编译

```powershell
go build -o windnssyncagent.exe ./cmd/windnssyncagent
```

# 十二、版本历史

## v1.0.1 - 2026-05-23

- 稳定性修复与同步性能优化版本，新增记录批次大小和请求超时配置，优化 `mirror` 模式排除 Zone 保护，并增强 Windows Server 2008/2008 R2 Legacy Agent 新增、删除记录的幂等处理。
- 详细更新日志见 [verchanglog/v1.0.1.md](verchanglog/v1.0.1.md)。

## v1.0.0 - 2026-5-22

- 首个正式版本，完成 Go Agent、2008/2008 R2 Legacy Agent、DNS Zone/Record 同步、子级域目录同步、委派 NS 同步、include/exclude 选择规则、dryRun、rewrite 和发布包构建流程。
- 详细更新日志见 [verchanglog/v1.0.0.md](verchanglog/v1.0.0.md)。

# 十三、许可证

本项目采用 MIT License，详见 [LICENSE](LICENSE)。

# 十四、联系方式

如果您在使用过程中遇到问题,或有任何建议和反馈,欢迎通过以下方式联系:

- **Email**: 416685476@qq.com
- **GitHub Issues**: [https://github.com/zyx3721/WinDnsSyncAgent/issues](https://github.com/zyx3721/WinDnsSyncAgent/issues)
- **项目主页**: [https://github.com/zyx3721/WinDnsSyncAgent](https://github.com/zyx3721/WinDnsSyncAgent)

---

**⭐ 如果这个项目对您有帮助,欢迎 Star 支持!**
