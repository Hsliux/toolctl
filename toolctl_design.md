# toolctl 设计文档

技术落地见 `toolctl_technical_design.md`；共享包、公共协议与模块扩展契约见 `toolctl_common_design.md`。

## 1. 项目定位

toolctl 是一个面向基础设施工具生态的统一 CLI 和执行框架。

它不替代 `storcli`、`mysql`、`mtr`、`dig` 等底层工具，而是解决这些工具在使用方式上的共性问题：

- 提供一致的命令结构；
- 管理环境、目标和配置；
- 将底层工具差异封装在 adapter/plugin 中；
- 统一超时、错误、退出码和结构化输出；
- 支持批量执行，并为未来远程执行预留稳定边界。

项目的最终目标是形成一个功能较完整的基础设施工具集，而不是只覆盖少数固定场景。核心框架保持精简稳定，RAID、MySQL、网络、系统诊断等能力以模块形式持续增加、替换或移除。内置模块和外部插件遵循相同的能力模型，使功能规模扩大时不需要持续改写核心执行链路。

示例：

```text
toolctl raid disks
toolctl raid disk health disk0
toolctl mysql replication
toolctl network trace example.com
toolctl host
toolctl disks
toolctl nics
```

toolctl 当前是“一次性命令执行模型”，不是持续运行、自动调谐期望状态的 Kubernetes Controller。远期可以演进为基础设施控制平台，但不在 V1 中提前引入控制平面复杂度。

## 2. 设计原则

1. **面向用户意图**：命令表达“对什么资源执行什么操作”，不暴露底层工具细节。
2. **核心保持稳定**：配置、插件发现、执行控制、错误模型和输出由核心统一提供。
3. **插件负责适配**：插件解析底层工具输出并返回结构化结果，不自行实现通用输出逻辑。
4. **协议优先**：跨插件、跨进程和未来跨主机都使用版本化请求/结果模型。
5. **渐进演进**：MVP 先以内置 adapter 验证抽象，再开放独立进程插件；暂不使用 RPC、gRPC 和 agent。
6. **安全默认**：避免 shell 字符串拼接，默认设置超时，敏感信息不进入日志。
7. **可诊断**：用户能够知道实际使用了哪个插件、目标、配置来源和底层依赖。
8. **模块可替换**：内置能力不是核心硬编码分支，可以独立注册、禁用、替换和测试。
9. **环境可适配**：核心 CLI 在支持的平台上应始终可启动和诊断；具体能力通过运行时探测判断可用性，不能因某个厂商工具缺失而导致整个 CLI 不可用。

## 3. 范围与非目标

### 3.1 V1 范围

- 本地 CLI；
- 内置 system/host 和 RAID adapter；
- 独立进程插件协议；
- context 和 target 配置；
- 单目标及有限并发的多目标执行；
- table、wide、JSON、YAML 输出；
- timeout、cancel、错误分类和稳定退出码；
- plugin list、plugin inspect 和 doctor；
- 模块启用、禁用和运行时能力探测；
- Linux amd64/arm64 以及常见发行版和部署形态的兼容性验证。

### 3.2 暂不实现

- Go `plugin` 动态加载；
- 常驻 agent；
- RPC/gRPC；
- 服务端任务调度；
- 声明式 Spec/Status 和 reconcile loop；
- 通用工作流编排系统。

## 4. 命令模型

内部能力使用稳定的三段式 ID：

```text
<domain>.<resource>.<verb>
```

例如：

```text
system.host.info
raid.disk.list
raid.controller.list
raid.volume.list
raid.status
raid.doctor
raid.init
mysql.replication.status
network.route.trace
batch.ssh.execute
batch.container.execute
benchmark.disk.run
benchmark.disk.doctor
benchmark.device.run
benchmark.cpu.run
benchmark.memory.run
benchmark.network.run
benchmark.network.serve
benchmark.network.latency
network.dns.resolve
network.route.get
network.tcp.connect
stability.burn.run
assessment.server.run
assessment.result.compare
system.health.check
system.process.top
system.disk.health
container.resource.list
```

用户命令路径不要求机械复制能力 ID。Capability 显式声明短命令路径、默认动作和别名，由核心构造命令树：

```text
toolctl host                         # system.host.info
toolctl raid disks                   # raid.disk.list
toolctl raid controllers             # raid.controller.list
toolctl raid volumes                 # raid.volume.list
toolctl raid status                  # raid.status
toolctl raid doctor                  # raid.doctor
toolctl init raid                    # raid.init
toolctl mysql replication            # mysql.replication.status
toolctl network trace example.com    # network.route.trace
toolctl batch ssh -H n1,n2 -- uptime # batch.ssh.execute
toolctl batch containers -- ip addr  # batch.container.execute（默认 net scope）
toolctl batch containers --scope fs -- df -h
toolctl batch containers --scope exec -- id
toolctl batch containers --scope process -- ps aux
toolctl bench disk /data             # benchmark.disk.run
toolctl bench device /dev/nvme1n1    # benchmark.device.run
toolctl bench cpu                     # benchmark.cpu.run
toolctl bench mem                     # benchmark.memory.run
toolctl bench net 10.0.0.2            # benchmark.network.run
toolctl burn mixed                    # stability.burn.run
toolctl assess full                   # assessment.server.run
```

命令设计规则：

- 高频只读查询优先使用简短名词命令；
- 单例资源可以省略 `info`，例如 `toolctl host`；
- 集合资源可以使用复数名词表达默认 `list`，例如 `toolctl raid disks`；
- 有副作用或语义不唯一的动作必须显式写 verb；
- canonical path 必须唯一，alias 不能与其他 capability 冲突；
- 自动化依赖稳定 capability ID，不应依赖某个 CLI alias；
- 批量执行类 capability 可以声明 passthrough args，将 `--` 后的 argv 放入 Operation.Arguments，不与资源 name 混用；
- `toolctl capability inspect <id>` 可以显示 ID、canonical path 和 aliases。

管理类命令不遵循资源命令结构：

```text
toolctl version
toolctl config current-context
toolctl plugin list
toolctl plugin inspect raid
toolctl capability inspect system.host.info
toolctl doctor
```

通用 flags：

```text
--config <path>
--context <name>
--target <name>       # 可重复
--selector <expr>
--timeout <duration>
--concurrency <n>
-o, --output table|wide|json|yaml
--no-headers
--verbose
```

插件可以注册领域专属 flags，但不得覆盖通用 flags。

## 5. 总体架构

```text
CLI Command
    |
    v
Config + Context Resolver
    |
    v
Plugin Registry
    |
    v
Operation Builder
    |
    v
Executor / Batch Executor
    |
    v
Runner ----------------------+
    |                         |
    v                         v
Built-in Adapter      External Plugin Process
    |                         |
    +------------+------------+
                 |
                 v
           Structured Result
                 |
                 v
              Renderer
```

主执行流程：

```text
Parse -> Complete -> Validate -> Build Operation
      -> Resolve Plugin -> Execute -> Render -> Exit
```

不将 `Factory`、`Builder`、`Visitor` 作为必须暴露的领域概念：

- 依赖组装可以使用一个简单的 `App` 或构造函数完成；
- Builder 只负责把输入规范化为 `Operation`；
- 批量执行由 `BatchExecutor` 负责；只有未来出现复杂的惰性遍历需求时，才引入 Visitor。

## 6. 核心模块

### 6.1 Command

职责：

- 使用 Cobra 解析命令和 flags；
- 完成 `Complete()`、`Validate()`、`Run()` 生命周期；
- 将用户输入交给 Operation Builder；
- 不直接调用底层二进制，不解析业务输出。

### 6.2 Config Resolver

职责：

- 加载系统、用户和显式指定的配置；
- 解析 current context；
- 合并 flags、环境变量和配置文件；
- 解析 target 与 credential 引用；
- 记录配置值来源，供诊断命令展示。

配置优先级由高到低：

```text
command flags
environment variables
explicit --config file
user config
system config
defaults
```

### 6.3 Plugin Registry

职责：

- 注册内置 adapter；
- 发现外部插件；
- 校验 manifest 和协议兼容性；
- 根据 domain/resource/verb 选择插件；
- 检测命令冲突并给出确定性结果。

### 6.4 Operation Builder

职责：

- 将命令输入转换成通用 `Operation`；
- 展开 target 或 selector；
- 设置 timeout、并发度及插件参数；
- 只做转换和校验，不执行操作。

参考模型：

```go
type Operation struct {
	APIVersion string                     `json:"apiVersion"`
	ID         string                     `json:"id"`
	Capability string                     `json:"capability"`
	Name       string                     `json:"name,omitempty"`
	Arguments  []string                   `json:"arguments,omitempty"`
	Targets    []Target                   `json:"targets"`
	Options    map[string]json.RawMessage `json:"options"`
	TimeoutMS  int64                      `json:"timeoutMs"`
}
```

`Options` 是协议扩展点；核心字段必须保持类型稳定，不应全部塞入 `Options`。

### 6.5 Executor

职责：

- 创建可取消、带超时的执行上下文；
- 调用选定 Runner；
- 对多目标操作进行有限并发执行；
- 汇总成功、失败和 warning；
- 保持结果顺序确定；
- 根据执行结果生成统一退出码。

Executor 不包含 RAID、MySQL 等领域业务逻辑。

### 6.6 Runner

Runner 是本地 adapter、外部插件以及未来远程 agent 之间的稳定边界：

```go
type Runner interface {
	Execute(ctx context.Context, op Operation) (RunOutput, error)
}
```

Runner 只返回 Items、Warnings 和 Errors 组成的 RunOutput。最终 Result、status、metadata 和退出码由 Executor 统一生成，模块不能自行改变顶层输出格式。

V1 实现：

- `BuiltinRunner`：调用编译进 toolctl 的 adapter；
- `ProcessRunner`：通过独立进程插件协议执行。

未来可以新增 `RemoteRunner`，无需改变 Command 和 Renderer。

### 6.7 Adapter

Adapter 封装具体领域行为：

- 检查依赖的底层二进制；
- 将 Operation 转换为安全的参数数组；
- 调用 `storcli`、`mysql`、`mtr` 等程序；
- 解析输出；
- 返回统一 RunOutput 和领域字段。

Adapter 不负责 table/JSON/YAML 渲染。

一个领域可以包含多个 backend。例如 RAID adapter 可以同时支持 Linux MD、`storcli`、`perccli`、`ssacli` 和 `arcconf`：

```go
type Backend interface {
	Name() string
	Probe(ctx context.Context) ProbeResult
	Execute(ctx context.Context, op Operation) (RunOutput, error)
}
```

RAID backend 按控制器选择，而不是整台主机只选择一个 backend；同一主机上的多个 backend 结果由模块聚合。选择过程必须可解释，`doctor` 应展示 PCI 厂商识别、所需工具、安装路径、是否已登记、架构兼容性及最终状态。不得仅凭固定路径或操作系统名称猜测工具一定存在。

### 6.8 Renderer

职责：

- 将 Result 渲染为 table、wide、JSON 或 YAML；
- 保证 JSON/YAML 结构稳定；
- 将机器可读结果写入 stdout；
- 将日志和诊断信息写入 stderr。

table 可以根据插件返回的列提示进行展示，但是否接受这些提示由核心决定。

## 7. 资源、操作与结果模型

toolctl 不强制所有对象拥有 Kubernetes 风格的 `Spec` 和 `Status`，而是使用三个更贴近一次性命令执行的概念：

- `Resource`：被操作对象，例如 host、disk、controller、mysql-instance；
- `Operation`：本次意图，例如 list、health、backup、trace；
- `Result`：本次执行产生的数据、错误和元信息。

参考 Result：

```go
type Result struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`   // Result
	Status     string         `json:"status"` // success, partial, failure
	Operation  OperationRef   `json:"operation"`
	Items      []Item         `json:"items"`
	Warnings   []Diagnostic   `json:"warnings"`
	Errors     []TargetError  `json:"errors"`
	Metadata   ResultMetadata `json:"metadata"`
}

type Item struct {
	Target string         `json:"target,omitempty"`
	Name   string         `json:"name,omitempty"`
	Kind   string         `json:"kind"`
	Data   json.RawMessage `json:"data"`
}
```

设计要求：

- Result 顶层固定为 `apiVersion/kind/status/operation/items/warnings/errors/metadata`；
- `kind` 在 v1alpha1 固定为 `Result`；领域对象类型放在 Item.Kind；
- `items`、`warnings`、`errors` 始终为数组，空值输出 `[]`，不能输出 `null` 或因空而省略；
- Item 使用 `data` 承载领域数据，不再使用语义模糊的 `fields`；wire type 使用 `json.RawMessage` 保留整数精度，模块内部仍使用强类型结构；
- JSON/YAML 字段使用稳定的 lowerCamelCase；
- 时间使用 RFC 3339；
- duration 字段使用 `durationMs`，容量字段使用 `*Bytes`，机器格式不输出带单位字符串；
- 枚举值使用小写 kebab-case；
- 不以 table 文本作为插件间协议；
- 部分成功时同时保留成功 Items 和失败 Errors；
- 未知扩展字段应尽可能向前兼容。

## 8. 插件设计

### 8.1 形态选择

插件分为两类：

1. **内置 adapter**：与 toolctl 一起编译，用于 MVP 和核心能力；
2. **外部进程插件**：名为 `toolctl-<domain>` 的独立可执行文件，通过 JSON 协议通信。

不使用 Go `plugin` 包动态加载，以避免 Go 版本、依赖版本、构建参数和平台兼容问题。

内置 adapter 也采用显式注册机制，不在 Command 中写领域分支：

```go
type Module interface {
	Info() ModuleInfo
	Capabilities() []Capability
	NewRunner(deps Dependencies) (Runner, error)
}
```

核心只依赖 Module 接口。项目只发布一套完整的 standard 功能集合；增加或移除内置功能时，只改变显式模块注册集合，不改变 Config、Executor、Result 和 Renderer，也不再维护 minimal profile 或模块组合 build tags。

运行时可以通过配置禁用模块。发行物保持单一，避免多套 profile 造成测试矩阵、文档和现场行为分叉。

### 8.2 Manifest

外部插件提供 manifest：

```yaml
apiVersion: toolctl.io/v1alpha1
kind: Plugin
name: raid
version: 0.1.0
executable: toolctl-raid
protocol: exec-json
capabilities:
  - id: raid.controller.list
    resource: controller
    verb: list
    command:
      path: [raid, controllers]
  - id: raid.disk.list
    resource: disk
    verb: list
    command:
      path: [raid, disks]
requires:
  binaries: [storcli]
platforms:
  - os: linux
    arch: amd64
  - os: linux
    arch: arm64
toolctl:
  minVersion: 0.1.0
```

插件安装目录建议为：

```text
<user-config-dir>/toolctl/plugins/<plugin-name>/<version>/plugin.yaml
<user-config-dir>/toolctl/plugins/<plugin-name>/<version>/toolctl-<plugin-name>
```

### 8.3 进程协议

V1 使用 `exec-json`：

```text
toolctl -> plugin stdin:  一个 Operation JSON
plugin  -> stdout:       一个 ExecutionResponse JSON
plugin  -> stderr:       日志和诊断信息
process -> exit code:    协议级执行状态
```

插件至少支持：

```text
toolctl-raid info
toolctl-raid execute
toolctl-raid doctor
```

协议约束：

- `info` 返回插件信息、协议版本和 capabilities；
- `execute` 从 stdin 读取 Operation，从 stdout 输出 ExecutionResponse；
- ExecutionResponse 只包含 operationId、items、warnings 和 errors；最终 Result 由核心构造；
- stdout 不允许混入日志；
- 插件必须响应进程取消和超时终止；
- 核心校验输出大小和 JSON schema；
- 协议版本不兼容时拒绝执行并提示升级方向；
- 插件声明的平台与当前环境不匹配时，不尝试执行，并返回稳定的 `UNSUPPORTED_PLATFORM` 错误。

### 8.4 发现和优先级

按以下来源发现插件：

```text
explicit plugin path in config
<user-config-dir>/toolctl/plugins
system plugin directory
$PATH
```

内置 adapter 默认优先于隐式发现的同名外部插件。用户可以在配置中显式覆盖。

发生重名时不静默选择：`toolctl plugin list` 应展示全部候选、来源、版本以及最终选中项。

## 9. 环境兼容与可移植性

“在各个环境中正常使用”分为两个层次：

1. **核心可运行**：CLI 可以启动、读取配置、列出模块并执行 doctor；
2. **能力可用**：某项功能所需的平台、权限、设备和底层工具满足要求。

即使主机没有 RAID 卡或 `storcli`，`toolctl version`、`plugin list`、`config` 及其他无关领域仍应正常工作。不可用能力返回清晰诊断，而不是在启动阶段使整个程序失败。

### 9.1 平台基线

正式支持范围只包含 Linux，架构为 amd64 和 arm64：

| 平台 | 核心 CLI | RAID | MySQL | Network |
| --- | --- | --- | --- | --- |
| Linux amd64 | 支持 | 依赖硬件和厂商工具 | 支持 | 支持 |
| Linux arm64 | 支持 | 视厂商工具是否提供 arm64 版本而定 | 支持 | 支持 |

“支持”必须对应 CI 或发布前验证。其他 OS/ARCH 不发布官方产物、不进入兼容承诺；即使源码能够编译，也只视为社区自行验证。

### 9.2 构建和分发

- 核心优先保持纯 Go，并以 `CGO_ENABLED=0` 构建静态二进制；
- 发布明确的 GOOS/GOARCH 产物、SHA-256 校验值和版本信息；
- 支持离线环境下载、校验和安装；
- 不依赖 bash、GNU 专属参数或固定 shell；
- 路径处理使用 Go 标准库，不依赖某个 Linux 发行版的固定安装布局；
- 配置目录遵循 XDG Base Directory，并允许 `--config` 覆盖；
- 插件目录和可执行文件解析必须兼容符号链接及不同文件系统权限。

如果某个模块必须使用 CGO，应将其隔离为外部插件，不让它影响核心二进制的可移植性。

### 9.3 运行时探测

每个 backend 实现 Probe，至少报告：

- 当前系统是否为 Linux，以及 CPU 架构是否为 amd64/arm64；
- 依赖二进制的实际路径和版本；
- 所需设备或 socket 是否存在；
- 当前用户权限是否足够；
- 能力状态：`available`、`degraded`、`unavailable`；
- 建议的修复方式。

探测结果可在单次进程内缓存，但不得长期假设环境不变化。

### 9.4 环境差异处理

- 优先消费底层工具的 JSON/机器可读输出；
- 只能解析文本时，固定可控 locale（例如 `LC_ALL=C`）并按工具版本维护 parser fixture；
- 同一能力的不同厂商工具由 backend 做字段归一化；
- 不自动调用 `sudo`，权限不足时给出所需权限和建议；
- 容器内运行时识别设备未挂载、socket 不可见等典型限制；
- TTY、非交互 shell、CI 和管道场景不得改变机器可读输出协议；
- 文件编码统一为 UTF-8，换行和终端颜色不影响 JSON/YAML。

### 9.5 兼容性策略

- 核心、Module API、进程协议和输出 schema 分别版本化；
- 插件声明最低/最高兼容版本及支持平台；
- capability 不存在时返回 `UNSUPPORTED_CAPABILITY`，而不是假成功或空结果；
- backend 输出解析按实际工具版本测试；
- 发布前在支持矩阵执行 smoke test 和契约测试。

### 9.6 内置主机信息能力

唯一的 standard 发行版本内置以下无外部依赖的只读能力：

```text
toolctl host
toolctl disks
toolctl nics
```

它用于服务器接入初期的快速判断，查看 toolctl 当前运行环境中的基础主机信息：

- hostname；
- Linux 发行版名称和版本；
- kernel release；
- 服务器厂商、产品型号和机箱类型；
- 服务器类型：`physical`、`virtual-machine`、`container` 或 `unknown`；
- 虚拟化类型或厂商，例如 KVM、VMware、Xen、Hyper-V、QEMU；
- CPU 架构、型号、socket、物理核心数和逻辑 CPU 数；
- 内存总量与当前可用量；
- 根文件系统的总量、已用量和可用量；
- 块设备名称、类型、容量、型号、是否旋转盘和是否可移除；
- 非 loopback 网卡数量，以及网卡名称、类型、状态、MTU、速率、双工和 carrier；
- uptime；
- 数据采集时间。

数据来源限定为 Go 标准库、`/proc`、`/sys`、`/etc/os-release` 和文件系统 syscall，不调用 `uname`、`hostname`、`free`、`lsblk`、`dmidecode` 等外部命令。某个非关键来源不可读时返回已有字段并附带 warning，不让整条命令失败。

服务器类型判断综合容器标记、cgroup、DMI 信息和 CPU hypervisor 标记。结果包含 `confidence=high|medium|low` 和命中的非敏感依据；证据不足时必须返回 `unknown`，不能仅根据单一厂商字符串武断判断。

磁盘信息分为两层：

- `rootFilesystem` 表示当前运行环境看到的根文件系统容量；
- `blockDevices` 表示当前环境可见的实际块设备，默认过滤 loop、ram 和无容量伪设备，但保留 device-mapper 设备的类型说明。

默认不采集或输出 machine-id、product UUID、硬件序列号、磁盘序列号、MAC、IP、用户名和环境变量等敏感或强身份标识。容器内执行时输出的是当前容器命名空间和 cgroup 可观察到的信息，并明确标记 `serverType=container`，不声称它等于物理宿主机信息。

展示约定：

- `toolctl host` 的 DISKS 列按 `name:size` 展示全部顶层 disk/NVMe，例如 `sda:500.0 GiB,nvme0n1:1.8 TiB`；
- `toolctl disks` 每行展示一块顶层 disk/NVMe，不把 partition 和 device-mapper 重复当作物理盘；完整 `blockDevices` 仍保留在 host JSON/YAML；
- `toolctl nics` 每行展示一个当前环境可见的网络接口，包括 loopback 和虚拟接口；
- host 的 NICS 计数排除 loopback；
- MAC/IP 不属于基础网卡输出，后续只有增加明确选项后才允许采集。

该能力不需要 target：它始终描述 toolctl 当前执行所在的 Linux 环境。远程模式出现后，相同 Operation 可以由 RemoteRunner 在远端 agent 上执行。

### 9.7 实时性能与可选历史能力

standard 发行版本注册 performance 模块，首版提供：

```text
toolctl perf doctor
toolctl perf cpu [--cpu cpu,cpu0] [--live]
toolctl perf io [--device sda,nvme0n1] [--live]
toolctl perf net [--interface eth0,ens3] [--live]
toolctl perf tcp retrans [--live]

toolctl perf history doctor
toolctl perf history cpu
toolctl perf history io
toolctl perf history net
toolctl perf history tcp retrans
```

默认 `perf` 命令是无外部依赖的实时采样器，直接读取 `/proc/stat`、`/proc/diskstats`、`/proc/net/dev` 和 `/proc/net/snmp`。每次执行先读取基线，等待 `--interval` 后读取下一份快照，根据累计计数器 delta 和真实 elapsed time 计算 CPU 百分比、IO/网络速率和 TCP 重传率。默认 `interval=1s`、`count=1`，允许通过 `--count` 返回有界的连续样本；增加 `--live` 后逐个渲染快照并持续到 Ctrl-C。

实时 capability 不调用外部命令、不需要常驻进程、不要求 root。`perf doctor` 检查各 procfs 数据源；某个数据源不可读时只影响对应能力并返回明确诊断。

`perf history` 保留 ssar adapter，不把 `sresar`、`ssar` 或 `tsar2` 源码嵌入 toolctl。查询通过 ProcessExecutor 以 argv 调用 `ssar -P --api -o <metrics>`，不经过 shell，不解析 tsar2 table 文本。历史查询依赖主机已安装 ssar，并由 sresar 在 `/var/log/sre_proc` 产生数据；依赖不存在时只有 history capability 返回 `DEPENDENCY_MISSING`。

两种后端输出相同的 `CPUStat`、`DiskIOStat`、`NetworkStat` 和 `TCPRetransmissionStat` Item。`--range`、`--from`、`--to` 只属于 history；`--interval` 在实时命令中允许 100ms 到 1m，在历史命令中必须是整分钟。普通调用仍用最大 100 的 `--count` 保持单个 Result 有界；`--live` 使用可选 StreamingRunner 契约，每个周期生成一个独立快照。当前 live 只支持 table/wide，机器流式协议后续单独定义为 JSON Lines，而不复用 JSON 文档格式。

## 10. 配置设计

默认配置遵循 XDG Base Directory，通常位于 `$XDG_CONFIG_HOME/toolctl/config.yaml`；若未设置该变量，则使用 `$HOME/.config/toolctl/config.yaml`：

```text
<user-config-dir>/toolctl/config.yaml
```

参考配置：

```yaml
apiVersion: toolctl.io/v1alpha1
kind: Config
currentContext: prod

contexts:
  prod:
    targets: [mysql01, mysql02]
    timeout: 30s
    concurrency: 4

targets:
  mysql01:
    address: 10.0.0.11
    labels:
      env: prod
      role: mysql
    credentialsRef: mysql-prod
  mysql02:
    address: 10.0.0.12
    labels:
      env: prod
      role: mysql
    credentialsRef: mysql-prod

credentials:
  mysql-prod:
    provider: env
    usernameEnv: TOOLCTL_MYSQL_USER
    passwordEnv: TOOLCTL_MYSQL_PASSWORD

plugins:
  raid:
    enabled: true
```

要求：

- 配置包含 `apiVersion`，升级时可以迁移；
- target、context 和 credential 分离；
- 默认不建议在配置文件中写明文密码；
- 支持环境变量或未来的外部 secret provider；
- `toolctl config view` 默认脱敏；
- selector 基于 target labels，例如 `--selector env=prod,role=mysql`。

## 11. 批量执行

多目标执行由 Batch Executor 负责，而不是插件自行创建无限并发。

规则：

- 默认并发度较小且可配置；
- 单个目标拥有独立结果和错误；
- 支持 fail-fast，但默认尽可能完成所有目标；
- 输出按输入目标顺序稳定排列；
- 整体超时与单目标超时需要明确区分；
- Ctrl-C 取消所有尚未完成的执行。

部分成功示例：

```json
{
  "apiVersion": "toolctl.io/v1alpha1",
  "kind": "Result",
  "status": "partial",
  "operation": {
    "id": "op-01",
    "capability": "mysql.replication.status"
  },
  "items": [
    {
      "target": "mysql01",
      "kind": "ReplicationStatus",
      "data": {"healthy": true}
    }
  ],
  "warnings": [],
  "errors": [
    {
      "target": "mysql02",
      "code": "TIMEOUT",
      "message": "operation timed out"
    }
  ],
  "metadata": {
    "generatedAt": "2026-08-06T12:00:00Z",
    "durationMs": 125,
    "module": {"name": "mysql", "version": "0.1.0"}
  }
}
```

## 12. 错误模型和退出码

错误需要包含稳定机器码和面向人的信息：

```go
type ToolError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Target    string         `json:"target,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]string `json:"details"`
}
```

基础错误码：

```text
INVALID_ARGUMENT
CONFIG_ERROR
PLUGIN_NOT_FOUND
PLUGIN_INCOMPATIBLE
PLUGIN_PROTOCOL_ERROR
UNSUPPORTED_PLATFORM
UNSUPPORTED_CAPABILITY
DEPENDENCY_MISSING
AUTHENTICATION_FAILED
PERMISSION_DENIED
TIMEOUT
CANCELLED
EXECUTION_FAILED
PARSE_FAILED
PARTIAL_FAILURE
INTERNAL
```

进程退出码：

```text
0  全部成功
1  执行失败或部分失败
2  参数或配置错误
3  插件、依赖或协议错误
4  超时或取消
```

脚本应优先依赖 JSON 中的错误码；退出码只表达粗粒度类别。

## 13. 输出约定

### table

- 面向人类阅读；
- `table` 是默认格式，只展示用于快速判断的关键字段；
- `wide` 是 table 的扩展列版本，不改变底层 Result；
- 表头使用大写英文，字段顺序由 capability 的稳定 ColumnHint 决定；
- 缺失值显示 `-`，不能用 `0` 冒充未知值；
- 容量使用 IEC 单位，例如 GiB、TiB；duration 使用可读形式，例如 `12d4h`；
- 多目标结果必须包含 TARGET 列；
- `--no-headers` 只影响 table/wide；
- 默认不因终端宽度静默删除行；过长单元格可以截断并在 wide 中展示完整摘要；
- warning 写入 stderr；
- 终端颜色遵循 `NO_COLOR`，非 TTY 默认禁用颜色。

### JSON/YAML

- 面向自动化消费；
- stdout 只包含一个完整文档；
- YAML 与 JSON 使用完全相同的字段、层次和数据类型；
- 顶层始终使用统一 Result envelope；
- 不因某个字段为空而改变顶层数据类型；
- 数值保持原始标准单位，例如 bytes、milliseconds、seconds；
- key 和数组顺序保持确定，便于 golden test 和文本 diff；
- schema 发生破坏性变化时必须升级 `apiVersion`。

### 错误输出

- Operation 已成功构造后，即使执行失败，JSON/YAML 仍向 stdout 输出一个 `status=failure|partial` 的完整 Result，进程同时返回非零退出码；
- CLI 参数、配置文件或插件发现阶段的错误尚不能形成 Operation，写入 stderr；指定 JSON/YAML 时 stderr 使用结构化 ToolError；
- 日志永远不混入 stdout；
- V1 不提供 NDJSON 或流式输出，避免模块各自定义流格式。

### 日志

- 日志全部写入 stderr；
- `--verbose` 展示插件选择、配置来源和执行耗时；
- 密码、token、DSN 等敏感字段必须脱敏；
- 每次 Operation 生成 ID，便于关联日志。

## 14. 执行安全

调用底层程序时必须遵守：

- 使用 `exec.CommandContext` 和参数数组，不使用 `sh -c` 拼接命令；
- 为每次调用设置 timeout，并支持取消；
- 分离 stdout 和 stderr，并限制最大输出大小；
- 仅传递明确允许的环境变量；
- 临时文件使用受限权限并确保清理；
- 日志和错误信息不回显 credential；
- 外部插件被视为本机可执行代码，安装时必须明确其来源和校验信息；
- `plugin list/inspect` 展示实际可执行文件的绝对路径；
- V1 不承诺提供插件沙箱，文档和 CLI 必须明确这一安全边界。

对于可能产生副作用的操作，后续可在 capability 中增加：

```yaml
mutating: true
confirmation: required
```

但是否交互确认应由核心策略决定，而不是插件任意实现。

## 15. 可诊断性

提供以下命令：

```text
toolctl plugin list
toolctl plugin inspect raid
toolctl doctor
toolctl doctor raid
toolctl config view
```

`doctor` 检查：

- 配置文件是否合法；
- current context 和 targets 是否存在；
- 插件协议是否兼容；
- 底层二进制是否存在及版本是否符合要求；
- 插件冲突和最终选择结果；
- credential 引用是否可解析，但不输出 secret 内容。
- OS/ARCH、容器环境以及模块/backend 可用性。

统一入口为 `toolctl doctor`，按面向用户的稳定 component 名称 `core`、`perf`、`raid`、`batch` 和 `bench` 汇总检查，不暴露内部 Go package/module 名称；`toolctl doctor -o wide` 展示 dependency path/version、受影响 capability 和修复建议，`--component` 用于缩小范围。领域内的 `raid doctor`、`perf doctor`、`bench disk doctor` 继续保留详细输出。

## 16. 测试策略

### 单元测试

- 配置合并及优先级；
- Operation 构造与校验；
- target selector；
- 错误到退出码的映射；
- Result 的 table/wide/json/yaml 渲染。

### 契约测试

- manifest schema；
- `info/execute/doctor` 协议；
- stdout/stderr 隔离；
- 协议版本兼容性；
- 插件异常退出、非法 JSON、超大输出和超时。

### 集成测试

- 使用 fake `storcli` 等 fixture，不依赖真实硬件；
- 覆盖成功、部分成功、权限错误和解析失败；
- 验证参数使用数组传递，避免 shell 注入；
- 验证 Ctrl-C 和 timeout 能终止子进程。

### 兼容性测试

- 在 Linux amd64/arm64 上分别验证唯一发行物的核心 smoke test；
- 对不同版本的厂商工具保存脱敏输出 fixture；
- 验证缺少二进制、缺少设备、权限不足和容器隔离等环境；
- 验证标准模块清单和二进制启动；
- 验证旧版兼容插件以及不兼容插件的明确拒绝路径。

### Golden 测试

- 固定 table、wide、JSON、YAML 输出样例；
- 对外协议变更必须显式更新 golden files。

## 17. MVP 实施计划

### M0：工程骨架

- Cobra 根命令；
- `toolctl version`；
- Config Resolver；
- Operation、Result 和 ToolError 类型；
- Renderer；
- 统一退出码。
- Module 显式注册机制与单一发行物；
- GOOS/GOARCH 构建矩阵。

验收：版本命令可运行；配置错误能稳定映射到 stderr 和退出码。

### M1：第一个纵向切片

实现：

```text
toolctl host
toolctl disks
toolctl nics
```

使用内置 system/host 模块打通：

```text
Command -> Operation -> Executor -> Builtin Adapter -> Result -> Renderer
```

验收：table/wide/json/yaml 可用；amd64/arm64 字段一致；`/proc` 或 `os-release` 部分不可读时能降级并返回 warning。

### M2：配置和批量目标

- context、target 和 selector；
- Batch Executor；
- 并发限制、部分成功和稳定排序；
- `toolctl config view` 和 `toolctl doctor system`。

验收：可对多个 fake targets 执行，并准确表达部分失败。

### M3：RAID 纵向切片

实现：

```text
toolctl raid doctor
toolctl raid status
toolctl raid controllers
toolctl raid volumes
toolctl raid disks
toolctl init raid
```

使用内置 RAID adapter、Linux `/proc/mdstat` 和 fake `storcli` fixture，验证按控制器选择 backend、依赖缺失、未登记提示、架构不匹配、timeout 和厂商输出解析。`init raid` 只登记本地已安装工具的绝对路径，不复制二进制。

验收：无真实硬件的 CI 可以覆盖成功、无控制器、权限不足和解析失败；真实 RAID 环境完成 smoke test。

### M4：外部插件

- manifest；
- Plugin Registry；
- `exec-json` 协议；
- `toolctl plugin list/inspect`；
- 协议契约测试和冲突检测。

验收：将 RAID adapter 构建为外部测试插件，无需修改 Command 和 Renderer 即可运行。

### M5：第二个领域验证

新增 MySQL 或 Network 插件，验证现有抽象不是只适用于 RAID。

验收：新增领域不需要修改核心 Operation、Executor 和 Renderer 的公共契约。

### M6：环境兼容性收敛

- backend Probe 和选择策略；
- Linux amd64/arm64 兼容性流水线；
- 静态二进制、离线安装包及校验文件；
- 容器、权限不足、依赖缺失等降级场景测试；
- 发布 capability matrix。

验收：核心在支持矩阵内均可运行；模块不可用时不影响其他命令，并能通过 doctor 得到可操作的原因。

## 18. 演进方向

### V1：本地统一执行

```text
toolctl -> built-in adapters / external plugins -> local tools
```

### V2：远程执行

```text
toolctl -> RemoteRunner -> agent -> local adapters/tools
```

V2 复用 Operation、Result、错误码和 capability 模型，再选择合适的 RPC 传输，不在 V1 中提前绑定 gRPC。

### V3：基础设施控制平台

只有出现以下明确需求时才引入控制平面：

- 长时间任务；
- 调度、审计和权限治理；
- 期望状态与持续调谐；
- 多租户和集中式资产管理。

届时可以在现有一次性 Operation 之外增加 Task、DesiredState 和 Reconciler，而不是改变 V1 的基本执行协议。

## 19. 核心决策摘要

- toolctl 是统一 CLI 和执行框架，不是底层工具替代品；
- 主链路为 Command → Operation → Executor → Runner → Result → Renderer；
- MVP 使用内置 adapter，V1 支持独立进程插件；
- 内置 adapter 通过统一 Module 接口注册，可以随版本增加、替换、禁用或从标准发行物中移除；
- 不使用 Go 动态插件；
- 插件返回结构化结果，核心统一渲染；
- Resource、Operation、Result 取代对 Kubernetes Spec/Status 的直接照搬；
- Runner 是未来本地执行和远程 agent 的稳定扩展边界；
- 安全、错误、退出码、诊断和协议兼容性从第一版开始定义；
- “核心可运行”和“具体能力可用”分层保证，通过平台矩阵、backend Probe 和 doctor 适配不同环境。
