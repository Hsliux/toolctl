# toolctl 技术方案

## 1. 文档目标

本文将 `toolctl_design.md` 中的总体设计落实为可以直接开始编码的 Go 技术方案。

首期实现目标：

- 建立稳定、可测试的核心执行链路；
- 内置功能以 Module 方式注册，可以逐步增加、替换、禁用或裁剪；
- 相同核心代码可以运行在 Linux amd64/arm64，以及不同 Linux 发行版和基础设施环境；
- 用 RAID 领域完成第一个纵向切片；
- 为后续外部进程插件保留兼容协议，但不在 M0 阶段过早实现全部能力。

关联文档：

- `toolctl_design.md`：总体设计；
- `toolctl_common_design.md`：共享包、公共协议和模块扩展契约；若本文示例与 Common 设计冲突，以 Common 设计为准。

## 2. 技术选型

### 2.1 语言和构建

- 语言：Go；
- `go.mod` 固定项目采用的 Go toolchain；
- 核心优先保持纯 Go，发行构建默认 `CGO_ENABLED=0`；
- 使用 Go Modules；
- 一个仓库产出 `toolctl` 主程序及测试用插件；
- 版本信息通过 `-ldflags` 注入，不在源码中手工修改。

项目创建时应在 `go.mod` 中明确最低 Go 版本，并在 CI 中至少验证项目基线版本和当前发布版本，避免只在开发者本机 toolchain 可用。

### 2.2 基础依赖

优先使用标准库，仅引入必要依赖：

| 用途 | 选择 |
| --- | --- |
| CLI | `github.com/spf13/cobra` |
| YAML 配置 | `gopkg.in/yaml.v3` |
| 日志 | 标准库 `log/slog` |
| JSON | 标准库 `encoding/json` |
| table | 标准库 `text/tabwriter` 起步 |
| 进程 | 标准库 `os/exec` |
| 并发/取消 | 标准库 `context`、`sync` |

M0 不引入 Viper、依赖注入框架、通用 workflow 框架或 RPC 框架。配置合并、依赖组装和批量执行都由小型明确组件实现。

## 3. 仓库结构

建议初始目录：

```text
toolctl/
├── cmd/
│   ├── toolctl/
│   │   └── main.go
│   └── testplugin/
│       └── main.go
├── api/
│   └── v1alpha1/
│       ├── capability.go
│       ├── operation.go
│       ├── execution.go
│       ├── result.go
│       ├── diagnostic.go
│       ├── error.go
│       └── plugin.go
├── internal/
│   ├── app/
│   │   └── app.go
│   ├── core/
│   │   ├── module.go
│   │   ├── runner.go
│   │   └── dependencies.go
│   ├── apperror/
│   │   └── error.go
│   ├── result/
│   │   ├── builder.go
│   │   └── item.go
│   ├── cli/
│   │   ├── root.go
│   │   ├── dynamic.go
│   │   └── options.go
│   ├── config/
│   │   ├── types.go
│   │   ├── loader.go
│   │   ├── merge.go
│   │   └── resolve.go
│   ├── registry/
│   │   ├── registry.go
│   │   ├── discovery.go
│   │   └── conflict.go
│   ├── executor/
│   │   ├── executor.go
│   │   ├── batch.go
│   │   └── exitcode.go
│   ├── runner/
│   │   ├── runner.go
│   │   ├── builtin.go
│   │   └── process.go
│   ├── render/
│   │   ├── renderer.go
│   │   ├── table.go
│   │   ├── json.go
│   │   └── yaml.go
│   ├── process/
│   │   ├── command.go
│   │   └── output.go
│   ├── platform/
│   │   └── linux/
│   │       ├── filesystem.go
│   │       └── process.go
│   ├── diagnostic/
│   │   └── doctor.go
│   └── modules/
│       ├── modules.go
│       ├── system/
│       │   ├── module.go
│       │   ├── host.go
│       │   ├── cpu.go
│       │   ├── memory.go
│       │   ├── disk.go
│       │   └── virtualization.go
│       ├── perf/
│       │   ├── module.go
│       │   ├── runner.go
│       │   ├── history.go
│       │   └── types.go
│       └── raid/
│           ├── module.go
│           ├── adapter.go
│           ├── backend.go
│           ├── storcli.go
│           └── parser.go
├── testdata/
│   ├── config/
│   ├── plugins/
│   ├── procfs/
│   ├── sysfs/
│   └── storcli/
├── docs/
│   ├── protocol-v1alpha1.md
│   └── compatibility.md
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

边界约束：

- `api/v1alpha1` 是外部插件可以依赖的公共协议包，不能引用 `internal`；
- `internal` 中的实现不承诺给外部 Go 项目兼容；
- 每个内置模块只能通过公共核心接口接入，不能直接修改 CLI 根命令；
- 模块不得反向依赖具体 Renderer；
- `cmd/toolctl/main.go` 只负责创建 App、运行和返回退出码。

## 4. 核心对象

### 4.1 Operation

```go
package v1alpha1

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

type Target struct {
	Name    string            `json:"name" yaml:"name"`
	Address string            `json:"address,omitempty" yaml:"address,omitempty"`
	Labels  map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}
```

Operation 不重复 Domain/Resource/Verb，由 Capability ID 唯一确定。Arguments 只供声明 `PassthroughArgs` 的 capability 使用，用于安全地保留 `--` 后的命令 argv。Options 使用 `json.RawMessage`，避免跨语言或通用 map 解码时把整数隐式转换为 float64。协议中使用整数毫秒，而不是 Go `time.Duration` 的 JSON 数值，避免不同语言把纳秒误当毫秒。

Operation ID 使用标准库安全随机数生成，格式只要求稳定、唯一且可打印，不强制引入 UUID 依赖。

### 4.2 Result

```go
type Result struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind" yaml:"kind"`
	Status     ResultStatus   `json:"status" yaml:"status"`
	Operation  OperationRef   `json:"operation" yaml:"operation"`
	Items      []Item         `json:"items" yaml:"items"`
	Warnings   []Diagnostic   `json:"warnings" yaml:"warnings"`
	Errors     []TargetError  `json:"errors" yaml:"errors"`
	Metadata   ResultMetadata `json:"metadata" yaml:"metadata"`
}

type Item struct {
	Target string          `json:"target,omitempty" yaml:"target,omitempty"`
	Name   string          `json:"name,omitempty" yaml:"name,omitempty"`
	Kind   string          `json:"kind" yaml:"kind"`
	Data   json.RawMessage `json:"data" yaml:"-"`
}

type OperationRef struct {
	ID         string `json:"id" yaml:"id"`
	Capability string `json:"capability" yaml:"capability"`
}

type Diagnostic struct {
	Code    string            `json:"code" yaml:"code"`
	Message string            `json:"message" yaml:"message"`
	Target  string            `json:"target,omitempty" yaml:"target,omitempty"`
	Details map[string]string `json:"details" yaml:"details"`
}

type ResultMetadata struct {
	GeneratedAt time.Time `json:"generatedAt" yaml:"generatedAt"`
	DurationMS  int64     `json:"durationMs" yaml:"durationMs"`
	Module      ModuleRef `json:"module" yaml:"module"`
}
```

Result.Kind 固定为 `Result`，Result.Status 只能是 `success`、`partial` 或 `failure`。`items`、`warnings`、`errors` 在 JSON/YAML 中始终输出数组而不是 `null`。核心在渲染前完成空 slice 归一化。YAMLRenderer 通过 JSON 中间表示展开 RawMessage，不能把 Data 输出为字节串。

### 4.3 Capability

Capability 同时驱动命令树、参数校验、插件选择和帮助文本：

```go
type Capability struct {
	ID          string           `json:"id" yaml:"id"`
	Domain      string           `json:"domain" yaml:"domain"`
	Resource    string           `json:"resource" yaml:"resource"`
	Verb        string           `json:"verb" yaml:"verb"`
	Command     CommandPathSpec  `json:"command" yaml:"command"`
	Summary     string           `json:"summary" yaml:"summary"`
	NameMode    NameMode         `json:"nameMode" yaml:"nameMode"`
	Mutating    bool             `json:"mutating" yaml:"mutating"`
	PassthroughArgs bool         `json:"passthroughArgs,omitempty" yaml:"passthroughArgs,omitempty"`
	TargetMode  TargetMode       `json:"targetMode" yaml:"targetMode"`
	Options     []OptionSpec     `json:"options,omitempty" yaml:"options,omitempty"`
	Columns     []ColumnHint     `json:"columns,omitempty" yaml:"columns,omitempty"`
}

type CommandPathSpec struct {
	Path    []string   `json:"path" yaml:"path"`
	Aliases [][]string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
}

type OptionSpec struct {
	Name        string      `json:"name" yaml:"name"`
	Shorthand   string      `json:"shorthand,omitempty" yaml:"shorthand,omitempty"`
	Type        OptionType  `json:"type" yaml:"type"`
	Required    bool        `json:"required" yaml:"required"`
	Default     json.RawMessage `json:"default,omitempty" yaml:"-"`
	Description string      `json:"description" yaml:"description"`
}
```

Capability.ID 使用稳定的 `<domain>.<resource>.<verb>`，Command.Path 是用户使用的 canonical 短命令。首期 OptionType 支持：`string`、`bool`、`int`、`duration`、`stringSlice`。OptionSpec.Shorthand 为空或单个 ASCII 字母，同一 capability 内必须唯一，且不能占用全局 `-h`、`-o`。外部插件不能注册任意 Go/Cobra 回调，只能声明命令路径、参数 schema 和受限 ColumnHint，由核心生成对应 flags 和 table/wide 列。

## 5. Module、Backend 和 Runner

### 5.1 Module 接口

```go
type Module interface {
	Info() v1alpha1.ModuleInfo
	Capabilities() []v1alpha1.Capability
	NewRunner(Dependencies) (Runner, error)
	Doctor(context.Context) []v1alpha1.CheckResult
}
```

Module 构造阶段不得要求目标硬件和底层程序一定存在。缺失依赖应由 Doctor/Probe 表达；否则一个 RAID 依赖缺失会导致整个 toolctl 无法启动。

### 5.2 Runner 接口

```go
type Runner interface {
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}

type RunOutput struct {
	Items    []v1alpha1.Item
	Warnings []v1alpha1.Diagnostic
	Errors   []v1alpha1.TargetError
}
```

Runner 不生成最终 Result。它的 error 只表示框架级失败，例如协议损坏或内部错误；领域执行失败写入 RunOutput.Errors。Executor 根据 RunOutput 统一生成 Result、status、metadata 和退出码。

### 5.3 Backend 接口

```go
type Backend interface {
	Name() string
	Probe(context.Context) ProbeResult
	Supports(capabilityID string) bool
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}
```

选择规则：

1. 用户在配置中明确指定的 backend；
2. 状态为 available 且优先级最高的 backend；
3. 状态为 degraded、但声明可执行的 backend；
4. 无可用 backend 时返回 `DEPENDENCY_MISSING` 或 `UNSUPPORTED_CAPABILITY`。

如果显式 backend 不可用，不静默切换到另一个实现，避免不同工具行为变化难以追踪。

## 6. App 和依赖组装

```go
type App struct {
	ConfigLoader ConfigLoader
	Registry     *Registry
	Executor     *Executor
	Renderers    map[OutputFormat]Renderer
	Stdout       io.Writer
	Stderr       io.Writer
	Logger       *slog.Logger
	Version      BuildInfo
}
```

创建顺序：

```text
main
  -> NewDependencies(stdin, stdout, stderr, env, filesystem, processFactory)
  -> LoadBuiltinModules()
  -> NewApp(dependencies, modules)
  -> app.Command().ExecuteContext(ctx)
  -> app.ExitCode()
```

所有 OS、环境变量、时间、进程和输出依赖通过小接口或函数注入，测试不依赖修改全局状态。

不使用 package `init()` 隐式注册模块。唯一发行物显式返回内置模块列表：

```go
func Builtins() []core.Module {
	return []core.Module{
		system.New(),
		perf.New(),
		raid.New(),
		network.New(),
	}
}
```

项目只维护一套 standard 功能集合，不再设置 profile 字段或 minimal 构建 tag。模块仍然显式注册，可以随版本增加、移除，或在运行时通过配置禁用。

## 7. CLI 构造

### 7.1 两阶段初始化

Cobra 命令树需要在执行前知道 capabilities，因此采用两阶段初始化：

1. bootstrap 解析 `--config`、`--context` 和基础环境；
2. 加载配置、内置 Module 和外部插件 manifest；
3. Registry 合并 capabilities；
4. Command Factory 根据 capabilities 构建领域命令；
5. Cobra 执行完整参数解析和 Operation 构造。

bootstrap 解析器只识别影响插件发现的少量参数，不能执行领域逻辑。

### 7.2 动态命令

Registry 将稳定 capability：

```text
system.host.info
raid.disk.list
```

分别映射为 capability 声明的 canonical path：

```text
toolctl host
toolctl raid disks
```

冲突判断使用完整键：

```go
type CapabilityKey struct {
	ID string
}
```

Registry 还建立独立的 CommandPath 索引。Capability ID 冲突和命令路径/alias 冲突分别诊断，不能因为 ID 不同就允许两个模块占用同一路径。

命令帮助、canonical path、aliases、位置参数规则和领域 flags 均来自 Capability。所有命令共享 output、target、timeout、context、concurrency 等通用 flags。短命令只是表现层；Operation 始终携带稳定 Capability ID。

### 7.3 生命周期

每个叶子命令统一执行：

```text
Complete
  - 合并配置和 flags
  - 解析 targets/selector
  - 生成 Operation ID

Validate
  - 校验名称和 options
  - 校验 capability
  - 校验 timeout/concurrency

Run
  - Registry.Resolve
  - Executor.Execute
  - Renderer.Render
```

## 8. 配置技术实现

### 8.1 配置来源

```text
flags > environment > explicit config > user config > system config > defaults
```

配置路径：

- 用户配置目录遵循 XDG Base Directory；
- 系统配置目录默认使用 `/etc/toolctl`；
- `--config` 接受显式文件路径；
- 测试通过注入路径解析器，不依赖当前用户目录。

### 8.2 类型和严格性

核心字段使用强类型结构。加载过程：

1. 读取字节并限制文件大小；
2. YAML decode；
3. 对核心字段进行未知字段检查；
4. 校验 `apiVersion` 和 `kind`；
5. 分层 merge；
6. 解析引用；
7. 形成不可变的 ResolvedConfig。

插件私有配置放在独立命名空间：

```yaml
plugins:
  raid:
    enabled: true
    backend: storcli
    settings:
      binary: /opt/MegaRAID/storcli/storcli64
```

`settings` 由对应模块校验，核心不解释其字段。

### 8.3 Secret Provider

```go
type SecretProvider interface {
	Resolve(context.Context, SecretRef) (Secret, error)
}
```

T4 仅实现环境变量 provider。Secret 使用后不得进入 Result、verbose 日志或配置展示。执行外部进程时默认不把整个父进程环境传入插件，而是按 allowlist 构建环境。

## 9. Registry 技术实现

Registry 保存：

```go
type Registration struct {
	Source       Source
	Module       ModuleInfo
	Capability   Capability
	Runner       RunnerFactory
	Priority     int
}
```

Source 包含：

- `builtin`；
- `explicit-path`；
- `user-directory`；
- `system-directory`；
- `PATH`。

注册过程先保存全部候选，再按明确规则解析最终项。冲突不会被覆盖丢失，`plugin list/inspect` 可以显示所有候选。

Registry 构建完成后只读，运行中不热加载插件。用户修改插件后需要重新执行命令。这能避免并发执行期间 registry 发生变化。

## 10. 执行器

### 10.1 单目标执行

Executor：

1. 根据 Operation 创建 timeout context；
2. 从 Registry 获取 Runner；
3. 调用 Runner；
4. 捕获并规范化错误；
5. 填充耗时和插件信息；
6. 返回 Result 与逻辑退出码。

Executor 本身不直接写 stdout/stderr。

### 10.2 多目标执行

Batch Executor 使用固定 worker 数量：

```text
targets -> jobs channel -> N workers -> indexed results -> ordered merge
```

约束：

- 默认 concurrency 取较小值，例如 4；
- 上限由核心限制，避免用户误设极大并发；
- 每个目标复制独立 Operation，避免共享 map 产生数据竞争；
- 使用目标原始下标合并，保证输出顺序稳定；
- panic 被转换为 INTERNAL，并记录 stack 到 debug 日志；
- context 取消后停止派发新任务；
- 默认完成已派发任务并汇总部分结果。

首期只定义整体 timeout。若后续确有需求，再增加 `perTargetTimeoutMS`，避免两个相似参数含义不清。

### 10.3 退出码

```go
func ExitCode(result Result, err error) int
```

优先级：

1. 参数/配置错误：2；
2. 插件/依赖/协议错误：3；
3. timeout/cancel：4；
4. 执行失败或部分失败：1；
5. 全部成功：0。

## 11. 底层进程执行

统一通过 ProcessExecutor，模块不得散落直接调用 `exec.Command`：

```go
type CommandSpec struct {
	Path       string
	Args       []string
	Env        []string
	Dir        string
	Stdin      io.Reader
	MaxStdout  int64
	MaxStderr  int64
}

type ProcessExecutor interface {
	LookPath(string) (string, error)
	Run(context.Context, CommandSpec) ProcessResult
}
```

安全要求：

- Path 必须是 Probe 得到或显式配置并验证过的路径；
- Args 独立传递，禁止 `sh -c`；
- stdout/stderr 并发读取，避免 pipe deadlock；
- 两者均限制大小，超限终止子进程并返回稳定错误；
- context timeout/cancel 必须终止进程；
- Linux 下尽可能终止整个子进程组，避免孙进程遗留；
- stderr 原文不直接进入机器可读 Result，先经过脱敏；
- debug 日志展示参数时对敏感 option 做遮蔽。

ProcessResult 保留：exit code、stdout、stderr、duration、是否 timeout 和启动/等待错误，不把底层退出码直接当作 toolctl 的退出码。LookPath 统一完成 PATH 探测，模块不得直接调用 `exec.LookPath`。

## 12. 外部进程插件协议

### 12.1 握手

```text
toolctl-raid info --protocol toolctl.io/v1alpha1
```

stdout 返回：

```json
{
  "apiVersion": "toolctl.io/v1alpha1",
  "kind": "PluginInfo",
  "name": "raid",
  "version": "0.1.0",
  "protocols": ["toolctl.io/v1alpha1"],
  "capabilities": []
}
```

manifest 用于无需执行代码的快速发现，`info` 用于运行前确认实际二进制与 manifest 一致。

### 12.2 执行

```text
toolctl-raid execute --protocol toolctl.io/v1alpha1
```

- stdin：一个 Operation JSON；
- stdout：一个 ExecutionResponse JSON；
- stderr：UTF-8 诊断日志；
- 一次进程只执行一个 Operation；
- 首期不实现流式协议；
- 输入输出均设置大小限制；
- JSON decoder 在 v1alpha1 对核心字段采用严格检查；
- ExecutionResponse.OperationID 必须匹配请求 ID。

ExecutionResponse 只包含 OperationID、Items、Warnings 和 Errors，不允许插件设置最终 Result.Status、Metadata 或退出码。ProcessRunner 校验后将其转换为 RunOutput，再由 Executor 构造最终 Result。

进程退出非 0 且 stdout 有合法 ExecutionResponse 时，核心保留其中的领域错误并补充协议异常诊断；无合法响应时返回 `EXECUTION_FAILED` 或 `PLUGIN_PROTOCOL_ERROR`。

### 12.3 协议兼容

- `apiVersion` 决定消息 schema；
- plugin semantic version 用于展示和发布，不代替协议版本；
- 核心可同时支持有限数量的协议版本；
- capability 中未知字段允许忽略，未知必需 option type 拒绝注册；
- 破坏性字段变化升级协议版本。

## 13. Renderer

```go
type Renderer interface {
	Render(io.Writer, v1alpha1.Result, RenderOptions) error
}

type RenderOptions struct {
	Format    OutputFormat
	NoHeaders bool
	Columns   []v1alpha1.ColumnHint
}
```

### 13.1 JSON/YAML

- stdout 仅输出一个文档；
- JSON 默认缩进，后续可增加 compact；
- slice 归一化为空数组；
- map key 输出保持确定性；
- YAML 与 JSON 保持完全相同的字段、层次和数值单位；
- renderer error 不与领域错误混合。

### 13.2 Table

列来源优先级：

1. capability 声明的 ColumnHint；
2. 资源 kind 的核心已知列；
3. 通用 `TARGET NAME KIND STATUS`；
4. 无法合理表格化时提示使用 `-o json`。

ColumnHint 只允许简单字段路径，不允许插件传入模板代码或表达式执行器。

- `table` 是默认关键列；
- `wide` 使用 ColumnHint 中标记为 wide 的扩展列；
- table 中容量使用 IEC 单位，时间使用人类可读格式，未知值显示 `-`；
- JSON/YAML 中容量保持整数 bytes，duration 使用带单位后缀的整数键；
- `--no-headers` 只影响 table/wide。

### 13.3 输出通道

- Result：stdout；
- warning、日志、进度、doctor 诊断：stderr；
- `--output json|yaml` 时禁止进度内容污染 stdout；
- 是否启用颜色取决于 TTY、`NO_COLOR` 和显式配置。

Operation 构造成功后的领域失败仍向 stdout 输出完整 Result，并通过 status、errors 和非零退出码表达。参数、配置和插件发现等 Operation 构造前错误写入 stderr；JSON/YAML 模式下使用结构化 ToolError。V1 不支持 NDJSON 或流式输出。

## 14. System Host 信息模块

### 14.1 命令和定位

唯一发行版本注册稳定 capability：

```text
system.host.info
```

canonical 用户命令：

```text
toolctl host
```

该命令是服务器接入初期的只读判断工具，不接受 target，描述 toolctl 当前所在的 Linux 执行环境。它不调用外部命令，不要求 root，并允许部分信息采集失败。

### 14.2 输出模型

模块内部使用强类型 HostInfo，最后转换为 Result Item：

```go
type HostInfo struct {
	Hostname       string              `json:"hostname"`
	Hardware       HardwareInfo        `json:"hardware"`
	ServerType    ServerTypeInfo      `json:"serverType"`
	OS            OSInfo              `json:"os"`
	Kernel        KernelInfo          `json:"kernel"`
	CPU           CPUInfo             `json:"cpu"`
	Memory        MemoryInfo          `json:"memory"`
	RootFilesystem FilesystemInfo     `json:"rootFilesystem"`
	BlockDevices  []BlockDeviceInfo   `json:"blockDevices"`
	Disks         []BlockDeviceInfo   `json:"disks"`
	DiskCount     *int                `json:"diskCount"`
	NetworkInterfaces []NetworkInterfaceInfo `json:"networkInterfaces"`
	NICCount      *int                `json:"nicCount"`
	UptimeSeconds *float64            `json:"uptimeSeconds"`
}

type HardwareInfo struct {
	Vendor      string `json:"vendor,omitempty"`
	Model       string `json:"model,omitempty"`
	ChassisType string `json:"chassisType,omitempty"`
}

type ServerTypeInfo struct {
	Type           string   `json:"type"` // physical, virtual-machine, container, unknown
	Virtualization string   `json:"virtualization,omitempty"`
	Confidence     string   `json:"confidence"` // high, medium, low
	Evidence       []string `json:"evidence,omitempty"`
}

type CPUInfo struct {
	Architecture  string `json:"architecture"`
	Model         string `json:"model,omitempty"`
	Sockets       int    `json:"sockets,omitempty"`
	PhysicalCores int    `json:"physicalCores,omitempty"`
	LogicalCPUs   int    `json:"logicalCPUs"`
}

type MemoryInfo struct {
	TotalBytes          *uint64 `json:"totalBytes"`
	AvailableBytes      *uint64 `json:"availableBytes"`
	CgroupLimitBytes    *uint64 `json:"cgroupLimitBytes"`
	EffectiveTotalBytes *uint64 `json:"effectiveTotalBytes"`
}

type FilesystemInfo struct {
	Mountpoint     string `json:"mountpoint"`
	FilesystemType string `json:"filesystemType,omitempty"`
	TotalBytes     *uint64 `json:"totalBytes"`
	UsedBytes      *uint64 `json:"usedBytes"`
	AvailableBytes *uint64 `json:"availableBytes"`
}

type BlockDeviceInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // disk, partition, device-mapper, nvme, unknown
	Model      string `json:"model,omitempty"`
	SizeBytes  uint64 `json:"sizeBytes"`
	Rotational *bool  `json:"rotational,omitempty"`
	Removable  bool   `json:"removable"`
}

type NetworkInterfaceInfo struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	State     string `json:"state,omitempty"`
	MTU       *int64 `json:"mtu"`
	SpeedMbps *int64 `json:"speedMbps"`
	Duplex    string `json:"duplex,omitempty"`
	Carrier   *bool  `json:"carrier"`
	Virtual   bool   `json:"virtual"`
}
```

字段无法可靠获取时使用 `null` 或省略，并向 Result.Warnings 写入具体来源和原因。不能用零值冒充真实探测结果；对会产生歧义的数值字段使用指针实现可空值。

### 14.3 数据采集

| 信息 | 主要来源 | 备用来源 |
| --- | --- | --- |
| hostname | `os.Hostname()` | `/proc/sys/kernel/hostname` |
| OS | `/etc/os-release` | `/usr/lib/os-release` |
| kernel | `/proc/sys/kernel/osrelease` | 无 |
| hardware | `/sys/class/dmi/id` 的非唯一字段 | 无 |
| CPU | `/proc/cpuinfo`、`runtime.GOARCH` | `/sys/devices/system/cpu` |
| memory | `/proc/meminfo` | cgroup v1/v2 memory 文件 |
| root filesystem | Linux `statfs(2)` | `/proc/self/mountinfo` 用于文件系统类型 |
| block devices | `/sys/class/block` | 无 |
| network interfaces | `/sys/class/net`、`/sys/devices/virtual/net` | 无 |
| uptime | `/proc/uptime` | 无 |
| virtualization | cgroup、容器标记、DMI、CPU flags | 无 |

所有读取都经过可注入的 FileSystem 接口，单文件设置大小上限。解析器只接受预期格式，忽略未知字段，并对不完整内容返回 warning。

### 14.4 服务器类型判断

按以下顺序判断：

1. 检查 `/.dockerenv`、`/run/.containerenv`、`/proc/1/cgroup` 和 `/proc/self/mountinfo` 的容器证据；
2. 读取 `/sys/class/dmi/id/sys_vendor`、`product_name`、`product_version` 等非唯一标识；
3. 检查 `/proc/cpuinfo` 的 hypervisor flag/vendor；
4. 映射已知类型：KVM/QEMU、VMware、Xen、Hyper-V、VirtualBox、Amazon Nitro 等；
5. 有可靠硬件 DMI 且无虚拟化证据时判断 physical；
6. 证据冲突或不足时返回 unknown。

判断结果必须包含 confidence：

- `high`：有容器标记，或 DMI 与 CPU 证据一致；
- `medium`：只有一个较可靠来源；
- `low`：只能排除部分类型，或证据存在冲突。

Evidence 只输出来源类别及非敏感摘要，不输出 product UUID、serial number 或 machine-id。

### 14.5 CPU、内存和磁盘口径

- CPU 的 LogicalCPUs 表示当前进程可见的逻辑 CPU；socket 和物理核心优先根据 `/proc/cpuinfo` 的 physical/core ID 去重，x86/ARM 的 cpuinfo 字段缺失时回退到 `/sys/devices/system/cpu/cpu*/topology/{physical_package_id,core_id}`，仍无法可靠推导时省略；
- Memory.TotalBytes/AvailableBytes 来自 `/proc/meminfo`；容器环境额外读取 cgroup limit，EffectiveTotalBytes 取可见总量与有效 cgroup limit 的较小值；
- 根文件系统通过 `statfs(2)` 计算容量，AvailableBytes 使用非特权用户可用块；
- `/sys/class/block/<device>/size` 按 Linux 规定的 512-byte sector 换算，不能误用 logical block size；
- 默认过滤 loop、ram、zram 和零容量设备；分区及 device-mapper 明确标注 Kind，不能将各层容量相加作为“磁盘总量”；
- JSON/YAML 返回完整可见设备列表；host table 的 DISKS 使用 `named-bytes-list` 展示全部顶层 disk/NVMe 的 name:size；
- 网卡从 sysfs 读取 type、operstate、MTU、speed、duplex、carrier，并识别 loopback/virtual；默认不读取 MAC/IP。

### 14.6 规范 JSON 示例

```json
{
  "apiVersion": "toolctl.io/v1alpha1",
  "kind": "Result",
  "status": "success",
  "operation": {
    "id": "op-01",
    "capability": "system.host.info"
  },
  "items": [
    {
      "name": "node-01",
      "kind": "HostInfo",
      "data": {
        "hostname": "node-01",
        "hardware": {
          "vendor": "Dell Inc.",
          "model": "PowerEdge R750",
          "chassisType": "rack-mount"
        },
        "serverType": {
          "type": "physical",
          "confidence": "medium",
          "evidence": ["dmi-hardware-present", "no-hypervisor-flag"]
        },
        "os": {
          "id": "rocky",
          "name": "Rocky Linux",
          "version": "9.4"
        },
        "kernel": {"release": "5.14.0-427.el9.x86_64"},
        "cpu": {
          "architecture": "amd64",
          "model": "Intel Xeon Gold 6330",
          "sockets": 2,
          "physicalCores": 32,
          "logicalCPUs": 64
        },
        "memory": {
          "totalBytes": 68719476736,
          "availableBytes": 51539607552,
          "effectiveTotalBytes": 68719476736
        },
        "rootFilesystem": {
          "mountpoint": "/",
          "filesystemType": "xfs",
          "totalBytes": 1099511627776,
          "usedBytes": 137438953472,
          "availableBytes": 962072674304
        },
        "blockDevices": [
          {
            "name": "nvme0n1",
            "kind": "nvme",
            "model": "NVMe SSD",
            "sizeBytes": 1920383410176,
            "rotational": false,
            "removable": false
          }
        ],
        "disks": [
          {
            "name": "nvme0n1",
            "kind": "nvme",
            "model": "NVMe SSD",
            "sizeBytes": 1920383410176,
            "rotational": false,
            "removable": false
          }
        ],
        "diskCount": 1,
        "networkInterfaces": [
          {
            "name": "eno1",
            "kind": "ethernet",
            "state": "up",
            "mtu": 1500,
            "speedMbps": 10000,
            "duplex": "full",
            "carrier": true,
            "virtual": false
          }
        ],
        "nicCount": 1,
        "uptimeSeconds": 1048320
      }
    }
  ],
  "warnings": [],
  "errors": [],
  "metadata": {
    "generatedAt": "2026-08-06T12:00:00Z",
    "durationMs": 8,
    "module": {"name": "system", "version": "0.1.0"}
  }
}
```

JSON/YAML 字段是兼容契约；table/wide 只是该 Result 的人类视图。后续模块必须复用相同顶层 envelope，只扩展各自 Item.Data。

### 14.7 默认 table

```text
HOST     TYPE      MODEL          VIRT  OS             ARCH   CPU  MEMORY  ROOT     DISKS                  NICS  UPTIME
node-01  physical  PowerEdge R750  -    Rocky Linux 9  amd64  64   64 GiB  1.0 TiB  nvme0n1:1.8 TiB,sda:500.0 GiB  2  12d
```

容器示例中的 TYPE 必须为 container；VIRT 可以展示已识别的容器 runtime。CPU 默认显示逻辑 CPU 数，socket、物理核心和 CPU 型号放入 wide。DISKS 展示顶层 disk/NVMe 的 name:size，不重复累加分区和 device-mapper；完整设备列表保留在 JSON/YAML。NICS 是排除 loopback 的数量。容量统一使用 IEC 单位，但 JSON/YAML 始终输出整数 bytes/seconds。

### 14.8 Disk 与 NIC 明细命令

```text
toolctl disks
toolctl disks -o wide
toolctl nics
toolctl nics -o wide
```

对应 capability：

```text
system.disk.list
system.network-interface.list
```

`disks` 默认列为 NAME、TYPE、SIZE、MODEL、ROTATIONAL，wide 增加 REMOVABLE；只返回顶层 disk/NVMe。`nics` 默认列为 NAME、TYPE、STATE、MTU、SPEED(Mbps)、DUPLEX，wide 增加 CARRIER、VIRTUAL；返回 loopback、物理和虚拟接口，但不返回 MAC/IP。

### 14.9 测试 fixture

`testdata/procfs` 和 `testdata/sysfs` 至少覆盖：

- amd64 物理机；
- arm64 物理机；
- KVM、VMware、Xen 虚拟机；
- Docker/Podman 容器；
- cgroup v1 和 v2 内存限制；
- SATA HDD、SATA SSD、NVMe、分区和 device-mapper；
- DMI 不可读、`/proc` 字段缺失和证据冲突；
- 超大或畸形伪文件。

golden 测试验证 table/wide/json/yaml。测试固定采集时间和 uptime，不依赖 CI 机器自身的 `/proc`、`/sys` 或硬件。

### 14.10 Performance 实时与历史模块

首版 capability 与命令映射：

| Capability ID | 命令 |
| --- | --- |
| `system.performance.doctor` | `toolctl perf doctor` |
| `system.performance.cpu` | `toolctl perf cpu` |
| `system.performance.disk-io` | `toolctl perf io` |
| `system.performance.network` | `toolctl perf net` |
| `system.performance.tcp.retransmission` | `toolctl perf tcp retrans` |
| `system.performance.history.doctor` | `toolctl perf history doctor` |
| `system.performance.history.cpu` | `toolctl perf history cpu` |
| `system.performance.history.disk-io` | `toolctl perf history io` |
| `system.performance.history.network` | `toolctl perf history net` |
| `system.performance.history.tcp.retransmission` | `toolctl perf history tcp retrans` |

默认实时 Runner 不使用 ProcessExecutor。数据源和算法为：

- CPU：两次 `/proc/stat` CPU 累计 jiffies 的 delta；
- 磁盘：两次 `/proc/diskstats` 的完成 IO、sector、耗时和 weighted IO delta；
- 网络：两次 `/proc/net/dev` 的 bytes、packets、errors、drops delta；
- TCP：按 `/proc/net/snmp` 的字段名读取 `OutSegs` 和 `RetransSegs` delta；
- 速率除以 Clock 记录的真实 elapsed seconds，不假定 sleep 精确等于 interval；
- 计数器回退按 reset 处理，本次 delta 为 0；采样期间资源消失返回结构化执行失败。

Waiter 是 Common 可取消等待接口。生产实现使用 timer 和 context，测试实现同步推进 fake Clock，不让测试真实 sleep。实时参数默认 `interval=1s,count=1`，interval 限制在 100ms 到 1m，count 限制在 1 到 100；timeout/cancel 可以中断等待。

实时 capability 还实现可选 `StreamingRunner`。指定 `--live` 时，Runner 每个 interval 发出一个完整 RunOutput，App 立即用相同 Columns 渲染；table header 只输出一次，并持续到 SIGINT/SIGTERM。live 默认每秒刷新、忽略 count，且不受默认 30 秒 operation timeout 限制。当前只允许 table/wide，JSON/YAML 仍保持单个有界 Result 文档。

可选 history Runner 通过 ProcessExecutor 查找并执行 `ssar`：

```text
ssar -P --api -o <allowlisted metric expression> -r <minutes> -i <minutes>
```

指标表达式只由模块根据本机 `/proc`、`/sys` 中的 CPU、块设备和网卡名称生成。用户 selector 必须先与本机资源集合匹配，最多选择 64 项；不向用户开放任意 `-o` 表达式。Path 和 Args 分离传递，禁止 shell。history 默认查询最近 5 小时、按 5 分钟聚合；`--from/--to` 只接受 RFC3339。

两种 Runner 都不直接暴露原始计数：CPU 转为百分比，磁盘 sector 转为 bytes/s 并计算 await、queue、util，网络转为每接口 bytes/packets/errors/drops 每秒，TCP 输出 out segments、retransmitted segments 和 retransmission percent。时间统一输出 RFC3339Nano；table 列和 JSON schema 保持一致。

`perf doctor` 只检查实时 procfs 数据源。`perf history doctor` 报告 ssar 路径、版本、`/var/log/sre_proc` 和架构。缺少 ssar 时仅 history 返回 `DEPENDENCY_MISSING`；实时查询仍然可用。

## 15. RAID 纵向切片

### 15.1 能力

T7 注册：

```text
raid.doctor
raid.status
raid.controller.list
raid.volume.list
raid.disk.list
raid.init
```

CLI 路径为 `raid doctor/status/controllers/volumes/disks` 和 `init raid`。除 `init raid` 只写入本地工具引用配置外，不实现阵列写操作，先验证查询、解析、输出和错误链路。

控制器发现直接读取 `/sys/bus/pci/devices` 的 class、vendor 和 subsystem vendor。backend 以控制器为单位选择，同一主机允许聚合多个 backend。Linux MD 阵列直接读取 `/proc/mdstat`。

### 15.2 storcli backend

Probe：

1. 检查显式配置路径；
2. 检查常见安装位置；
3. 使用受控 PATH 查找；
4. 执行版本命令并限制 timeout/output；
5. 检查是否能获得控制器信息；
6. 返回 available/degraded/unavailable 及原因。

执行依次请求控制器、虚拟盘和物理盘的 storcli JSON 输出。Parser 将厂商字段转换为统一 Item，原始厂商状态保留在 `vendorState`，统一健康状态限制为 `optimal/degraded/failed/rebuilding/initializing/offline/unknown`。

inventory 同时记录 backend 完整性。PCI 发现失败、管理工具缺失/架构不匹配、adapter 未实现、任一厂商查询超时/失败/无法解析时，`inventoryComplete=false` 并在 `incompleteBackends` 标识范围。不完整清单中的局部 optimal 不能推导整体 optimal，汇总 health 降为 unknown；已经确认的 failed/degraded/rebuilding 仍保留更高优先级。

`raid doctor` 对识别到的 Dell、Broadcom/LSI、HPE 和 Microchip/Adaptec 控制器分别探测 perccli、storcli、ssacli 和 arcconf。结果区分 tool-missing、unregistered、ready 和 unsupported。`init raid` 可以自动发现，也可以通过 `--backend` 与 `--path` 引用已安装的本地可执行文件；引用保存为绝对路径，不复制厂商程序。SSACLI adapter 执行 `ctrl all show config detail`；Arcconf adapter 先通过 `LIST` 获取 controller ID，再逐个执行 `GETCONFIG <id>`。两者都只执行只读命令并归一化 controller/volume/physical disk 及 enclosure/slot。

工具引用按从低到高的优先级合并：`/etc/toolctl/raid-tools.json` 后由当前用户 XDG config 覆盖。`init raid` 默认只修改用户配置，`--system` 只修改系统配置；`TOOLCTL_CONFIG_DIR` 用于显式单目录覆盖，设置后不再隐式读取 `/etc/toolctl`。

RAID doctor 不只保留合并后的 binary path，还保留命中来源：`auto/system/user/explicit/builtin`。对于已登记工具，`configPath` 必须指向真正生效的 `raid-tools.json`；如果用户配置覆盖系统配置，doctor 只展示用户来源。自动发现的工具标记 `registered=false, scope=auto`，不伪造配置文件路径。

### 15.3 测试 fixture

`testdata/storcli` 保存脱敏后的不同版本输出：

```text
controller-list-success.json
disk-list-success.json
no-controller.json
permission-denied.txt
malformed.json
version-<major>.txt
```

测试使用 fake executable 或注入 ProcessExecutor，不要求 CI 机器安装 storcli 或 RAID 硬件。

## 15.5 批量执行模块

内置能力：

```text
batch.ssh.execute        -> toolctl batch ssh -- <command> [args...]
batch.container.execute  -> toolctl batch containers -- <command> [args...]
```

这两项 capability 声明 `PassthroughArgs`，Command Factory 将 `--` 后至少一个参数原样放入 `Operation.Arguments`，不能与资源 NameMode 同时使用。

SSH backend 探测系统 OpenSSH `ssh`，以独立 argv 传递 BatchMode、连接超时、可选的显式端口、identity、目标和远程命令，不经过本地 shell。端口未显式指定时不传 `-p`，使 Host alias 的 `HostName`、`Port`、ProxyJump、认证和 known_hosts 均由 OpenSSH 配置处理；默认不允许交互密码且不关闭主机密钥校验。hosts 来自 `--hosts` 或有大小限制的 host file，去重并验证后最多 1024 个；worker pool 并发限制为 1～128。

Container backend 直接枚举 `/proc/<pid>/cgroup`，识别 docker、cri-containerd、crio 和 libpod cgroup，并以每个 container ID 的最小 PID 作为 init PID。不调用 docker、ctr 或 crictl，selector 只接受 container ID prefix。

`--scope` 将 namespace 组合限制为四个经过验证的 profile，不允许用户拼接任意 nsenter 参数：

| scope | nsenter 组合 | 语义 |
|---|---|---|
| net | net | 默认；使用宿主机工具检查容器网络 |
| exec | mount、UTS、IPC、net、PID、target root/wd | 显式完整容器执行 |
| fs | mount、target root/wd | 容器文件系统检查 |
| process | mount、PID、target root/wd | 使用容器 procfs 查看进程 |

未指定 scope 时直接采用 net：`toolctl batch containers -- <command>` 只进入每个容器的 network namespace并复用宿主机命令。`--network-only` 是显式表达同一行为的兼容别名。exec/fs/process 的 target root 和 working directory 都使用预先打开的 `/proc/<pid>/root`，避免只切换 mount namespace 后仍保留宿主机 root/cwd。物理磁盘性能属于宿主机，不通过 namespace 检查；未来容器级 I/O 指标从 cgroup blkio/`io.stat` 采集。

两类执行均设置每目标 command timeout、默认 128 KiB stdout/stderr 上限和整批 operation timeout。`--max-output` 允许在 1 KiB–16 MiB 之间调整每个 stream 的捕获上限；超限后终止子进程，返回 `OUTPUT_LIMIT_EXCEEDED` 并设置 stdout/stderr/output truncated 字段。每个目标生成包含 exitCode、duration、stdout、stderr 和 timedOut 的 Item；失败目标同时生成 TargetError，从而获得非零退出码和 partial/failure 结果。table 展示单行摘要，完整输出保留在 JSON/YAML。

## 15.6 Benchmark 与服务器评估模块

内置 CPU、memory、disk、raw-device、network 和 stability workload。`benchmark.cpu.run` 使用固定 1 MiB block 的 SHA-256 workload，输出单核或多核 hashes/s 和 bytes/s；`benchmark.memory.run` 输出内存 copy bytes/s 与伪随机读取的近似单次访问延迟。两者只依赖 Go runtime，主要用于相同 toolctl 版本和参数下的服务器横向对比。

`benchmark.network.serve` 和 `benchmark.network.run` 使用带固定 magic、随机 session ID、stream index/count、方向、duration 和 rate 的固定长度握手。一个一次性 session 支持 1～128 条 TCP 连接，并支持客户端视角的 send、receive 和 both；双方通过 TCP half-close 标识每个方向传输完成。客户端可以绑定本地 IPv4/IPv6 源地址。结果分别保存 sent/received/aggregate bytes 和 bytes/s；默认仍为单流 send。rate 是每方向跨 stream 的合计 bits/s 限制，拆分到 writer 后按累计 bytes/elapsed 节流。live 模式通过 StreamingRunner 周期输出累计快照，测试本身仍受 duration 限制。协议读取、accept 和 transfer 都受 operation context、握手 deadline 与 I/O deadline 约束。当前不提供 UDP、jitter 或逐流明细。

同一 server 识别 latency handshake 后切换为固定 8-byte sequence echo；`benchmark.network.latency` 计算 min/average/P50/P95/P99/max TCP application RTT。`network.dns.resolve` 使用 Go resolver，`network.tcp.connect` 使用带 timeout/optional source bind 的 TCP Dialer，`network.route.get` 使用 UDP connect 触发内核路由选择并从接口地址和 `/proc/net/route` 补充 IPv4 interface/gateway。上述能力不执行 ping、ip 或 ss。

`stability.burn.run` 对应 `burn [cpu|mem|mixed]`，以 duration 限制纯 CPU、纯内存或并发混合 workload。它是可取消、可计量的稳定性负载，不负责读取温度、ECC/MCE 或 BMC SEL。

文件磁盘能力为 `benchmark.disk.run` 和 `benchmark.disk.doctor`，CLI 为 `bench disk <directory>` 与 `bench disk doctor`。Runner 使用系统 fio，但只允许固定 profile 和校验后的参数，不开放任意 fio argv。

默认 profile 为 rand-write，size=1GiB、duration=10s、warmup=2s、jobs=1、iodepth=16；随机 profile 默认 4KiB，顺序 profile 默认 1MiB。目标必须是解析 symlink 后仍存在的目录。模块使用 `O_CREATE|O_EXCL` 创建权限 0600 的 `.toolctl-bench-<operation-id>.fio`，绝不覆盖同名文件；可用空间必须覆盖 size，并额外保留 max(256MiB, available*10%)。

fio 固定使用 direct=1、libaio、time_based、group_reporting 和 JSON 输出。read/randread/randrw 在测量前先顺序写满文件，避免稀疏文件读取；prepare 结果不计入测量。解析层归一化 read/write IOPS、bytes/s、平均及 P50/P95/P99/P99.9 latency、CPU、context switch 和最大 disk util。测试文件默认清理，`--keep-file` 才保留；清理失败必须返回 warning。

`--dry-run` 执行相同的 target/statfs/fio/options 预检，输出测试文件路径、mountpoint 和完整 fio argv，但不创建文件也不启动进程。Runner 通过 `/proc/self/mountinfo` 选择覆盖 target 的最长挂载点；真实写压测落在 `/` 时必须增加 `--force`。

`benchmark.device.run` 对应 `bench device <block-device>`。只接受 Linux 整盘 block device；拒绝 partition、存在 child partition、sysfs holder，或出现在 mountinfo、`/proc/swaps`、`/proc/mdstat` 的设备。默认 profile 为 seq-read。任何 write/mixed profile 必须增加 `--destroy-data`，且该确认不能绕过占用检查。dry-run 会输出设备容量、破坏性标志和 fio argv，不执行 I/O。

`assessment.server.run` 对应 `assess [quick|full]`，自身不重复采集逻辑，而是按稳定 capability ID 编排 host、RAID、实时 CPU、CPU benchmark、memory benchmark、stability、disk 和 network Runner。每段输出 `AssessmentSection` 并嵌入原始 Items/Warnings/Errors；缺少 full 模式的 `--disk` 或 `--network-peer` 时对应段为 skip。quick 不依赖 fio 或网络对端，full 的外部测试按显式参数启用。

## 15.7 运行健康与资源热点

`system.health.check` 聚合 load/CPU、available memory、swap、根文件系统和网卡状态，输出逐项 HealthCheck。warn 形成 Diagnostic，fail 形成 TargetError。它检查服务器运行状态；`doctor` 仍只检查 toolctl 平台、数据源和可选依赖。

`system.process.top` 对 procfs 做双快照，以 aggregate CPU tick 归一化每进程 CPU 百分比，并从 process I/O counter delta 计算 bytes/s；支持 cpu/mem/io 排序和 live 输出。不读取完整 cmdline。进程退出、PID 新出现等采样竞争按缺失样本忽略。

`container.resource.list` 复用 batch 模块的 cgroup 容器发现，读取 cgroup v2 的 cpu.stat、memory.current/max、pids.current 和 io.stat。当前值为累计 CPU/I/O 与即时 memory/PID，不伪装成速率。cgroup v1 明确降级为 warning。

`system.disk.health` 先读取 sysfs state/ro/queue；可选 smartctl JSON，NVMe 再回退 nvme-cli JSON。外部工具只通过 ProcessExecutor 固定 argv 调用，输出有大小上限。SMART critical/failed 为 Error，历史 media error 为 warning；RAID 物理盘继续由 RAID backend 负责。

`assessment.result.compare` 读取两份有大小限制的 toolctl JSON Result，抽取路径匹配的 throughput、IOPS 和 latency 指标。吞吐/IOPS 只判断下降，latency 只判断上升，按可配置 regression threshold 生成 pass/warn/fail，不生成无依据的综合分数。

## 16. Doctor

诊断结果采用结构化 CheckResult：

```go
type CheckResult struct {
	Name       string
	Status     CheckStatus // pass, warn, fail, skip
	Message    string
	Suggestion string
	Details    map[string]string
}
```

检查分层：

```text
core
  - build/platform
  - config
  - writable/cache paths

registry
  - manifest
  - conflicts
  - protocol compatibility

module
  - supported platform
  - dependency binary/version
  - device/socket
  - permissions
  - backend selection
```

Doctor 不输出 secret，不执行有副作用的探测命令。`doctor` 本身应尽可能完成全部检查，而不是遇到第一个失败就退出。

根命令 `toolctl doctor` 由内置 doctor module 聚合其他 Module 的 `Doctor(ctx)`。各模块在 `NewRunner(deps)` 时保存只读 Dependencies 引用，Doctor 只执行文件可读性、二进制 LookPath/version 和硬件发现等无副作用操作。聚合层将内部 module 名称映射为稳定的公共 component：core、perf、bench、raid、batch；结果按 component/check 稳定排序，并支持 `--component` 过滤。

默认 table 展示 component、check、status 和 message；wide 增加 path、version、capability 和 suggestion。缺失可选依赖使用 warn/skip Item，默认不生成 Error；真实 fail 生成带 `component/check` target 的 Error 并返回非零退出码。`--strict` 下 warn 也生成 Error，用于 CI/自动化的完整能力验收。远程 SSH endpoint、nsenter 实际权限、fio 工作负载和 RAID 查询等可能产生连接、namespace 或 I/O 行为的检查延迟到对应命令执行。

## 17. 跨环境实现策略

### 17.1 平台代码

只在确实需要 Linux 架构或内核能力差异时使用带 build constraint 的小文件：

```text
process_linux.go
process_linux_amd64.go
process_linux_arm64.go
```

领域代码不得到处判断 `runtime.GOARCH`；架构和内核能力通过注入的 Platform/Process 接口获得。非 Linux 系统不属于正式构建及运行范围。

### 17.2 能力降级

模块加载和模块可执行分离：

```text
registered + available    -> 正常执行
registered + degraded     -> 可执行但返回 warning
registered + unavailable  -> 命令可见，执行给出原因
not registered            -> capability 不存在
disabled                  -> 命令可在 plugin inspect 中看到，但不注册为可执行命令
```

让不可用命令仍出现在 help 中还是隐藏，首期采用：标准内置模块命令可见并提示环境问题；外部插件只有成功读取 manifest 后可见。

### 17.3 发布矩阵

首期：

```text
linux/amd64
linux/arm64
```

RAID capability 仅在 backend 支持对应 CPU 架构且运行时探测成功时可执行。其他 GOOS/GOARCH 不生成官方产物，也不纳入兼容承诺。

## 18. 测试方案

### 18.1 测试层次

- 单元测试：模型、配置 merge、selector、registry、退出码；
- parser 测试：厂商输出 fixture；
- 契约测试：插件 info/execute、非法输出、版本不兼容；
- 集成测试：完整 Cobra 命令到 Renderer；
- race 测试：Batch Executor；
- fuzz 测试：配置、插件 JSON 和厂商输出 parser；
- golden 测试：help、table、wide、JSON、YAML；
- smoke test：每个正式支持的发布产物。

### 18.2 核心测试要求

- 测试不得依赖真实用户 HOME、PATH、locale 或时区；
- 测试不得依赖真实 RAID 硬件；
- 并发测试必须运行 `go test -race`；
- golden 输出中固定时间、Operation ID 和版本；
- ProcessExecutor 测试覆盖 timeout、取消、超大输出和非零退出；
- 每个错误码至少有一个命令级测试。

### 18.3 CI 门禁

```text
go fmt check
go vet
go test ./...
go test -race ./...
cross build matrix
protocol contract tests
artifact smoke tests
```

静态检查工具可以在项目稳定后加入，但不让初始工具链复杂度阻塞 M0。

## 19. 构建和发布

版本信息：

```go
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
}
```

产物：

```text
toolctl_<version>_linux_<arch>.tar.gz
toolctl_<version>_linux_<arch>.sha256
```

发布包包含：

- toolctl；
- LICENSE；
- README；
- shell completion；
- capability/platform matrix；
- 协议版本说明。

发布流程必须从干净源码构建并生成可复现的版本元信息。签名机制可以在首个公开发布前加入。

## 20. 实施顺序

### T0：初始化工程

- 创建 `go.mod`；
- 建立目录边界；
- 实现 BuildInfo 和 `toolctl version`；
- 建立基础测试、Makefile 和 CI；
- 验证 Linux amd64/arm64 的唯一 standard 发行物均可交叉编译。

完成标准：四个发行组合构建成功，version 可输出 JSON 和人类格式。

### T1：公共 Wire API 和错误

- 实现 v1alpha1 Operation、ExecutionResponse、Result、Capability、Diagnostic、ToolError；
- Item.Data 和 Option value 使用 `json.RawMessage`；
- 实现错误码和退出码映射；
- 建立 JSON golden 和协议文档。

完成标准：模型序列化稳定，空数组、RawMessage、duration、错误码符合 Common 设计约定。

### T2：核心执行骨架

- 建立 App 依赖组装；
- Cobra 根命令和通用 flags；
- Module/Runner 接口和显式注册；
- RunOutput 与 framework error 边界；
- 只读 Registry 和 capability 冲突处理；
- capability 动态生成 Cobra 命令；
- 基础单目标 Executor；
- 统一 Result Builder 和 status 计算；
- table/wide/json/yaml Renderer；
- 单一内置模块集合；
- 完成 CLI 级测试。

完成标准：fake Module 可以形成 Operation、经过 Runner 执行并输出三种格式；新增 Module 不需要修改 CLI 核心代码。

### T3：System Host 信息

- `system.host.info` capability，canonical path 为 `host`；
- `system.disk.list` 和 `system.network-interface.list`，canonical path 为 `disks`、`nics`；
- HostInfo 强类型模型；
- `/proc`、`/sys`、os-release 和 statfs collector；
- 物理机、虚拟机、容器及虚拟化类型判断；
- CPU、内存、根文件系统和块设备采集；
- 网卡类型、状态、MTU、速率、双工和 carrier 采集；
- table/wide/json/yaml golden；
- amd64/arm64、虚拟机、容器和信息缺失 fixture；
- 唯一发行物注册该模块。

完成标准：`toolctl host`、`toolctl disks`、`toolctl nics` 可以运行；部分来源不可读时 host 保留可用信息并输出 warning；专用明细命令返回结构化失败；不采集唯一硬件或网络标识。

### T4：Performance 实时与历史模块

- 注册 `perf doctor/cpu/io/net/tcp retrans`；
- 直接读取 procfs 累计计数器并按真实 elapsed time 计算实时 delta；
- 使用可取消 Waiter 实现 `--interval`，使用有界 `--count` 保持统一 Result，并用 `--live` 持续逐快照渲染；
- 注册显式的 `perf history ...` 可选命令树；
- 实现受控 ssar metric expression builder；
- 通过 ProcessExecutor 调用 `ssar --api`；
- 解析 JSON array/NDJSON 并映射强类型 Result Item；
- 统一时间、百分比、bytes/s、计数率和错误码；
- 使用 fake Clock/Waiter/ProcessExecutor 覆盖实时 delta、参数、缺少依赖和异常 JSON。

完成标准：任意受支持 Linux 主机无需额外组件即可执行默认 perf 实时采样；安装并采集 ssar 数据的主机还可以查询有界历史性能；未安装 ssar 时只影响 `perf history`。

### T5：配置

- 配置路径解析；
- typed loader、merge 和 validate；
- context、target、selector；
- 环境变量 Secret Provider；
- `config view/current-context`。

完成标准：配置优先级、脱敏、XDG 路径和 `/etc/toolctl` 系统路径均有测试。

### T6：Executor 和 ProcessExecutor

- timeout/cancel；
- stdout/stderr 限制；
- Batch Executor；
- 部分成功和稳定排序；
- Linux 进程组处理及架构抽象。

完成标准：race、timeout、取消、超大输出和部分失败测试通过。

### T7：RAID 模块

- storcli Probe；
- controller list、disk list；
- fixture parser；
- doctor raid；
- table 列定义。

完成标准：无真实硬件的 CI 可覆盖整个命令链；真实环境完成一次人工 smoke test。

### T8：外部插件

- manifest discovery；
- ProcessRunner；
- info/execute/doctor；
- 协议兼容与冲突展示；
- 测试插件。

完成标准：将 RAID 测试模块替换为独立进程时，用户命令和输出结构不变。

### T9：第二领域和兼容性收敛

- 增加 MySQL 或 Network 模块；
- 验证 backend 抽象；
- 完善发行矩阵和离线包；
- 发布 capability/platform matrix。

完成标准：新领域不修改核心公共协议，支持环境内 smoke test 通过。

## 21. 首轮编码边界

建议第一轮实施 T0～T3，建立可运行骨架和第一条真实服务器信息命令，不立即接入 storcli：

```text
toolctl version
toolctl host
toolctl host -o json
toolctl host -o yaml
toolctl disks
toolctl nics
```

system/host Module 验证：

- 模块注册；
- capability 生成命令；
- Operation 构造；
- Runner 调用；
- Result 渲染；
- 错误到退出码映射；
- Linux amd64/arm64 环境信息采集和降级 warning。

这一阶段不依赖厂商工具和特殊硬件，完成后再进入配置、批量执行与 RAID 模块。它既能证明架构主链路，也能立即提供一项有实际用途的功能。

## 22. 技术决策摘要

- Go + Cobra，核心保持纯 Go 和少依赖；
- 公共协议置于 `api/v1alpha1`，实现置于 `internal`；
- Module 显式注册，不使用 `init()` 或 Go 动态插件；
- Capability 数据驱动生成 CLI，内置和外部能力使用同一命令模型；
- Runner 统一内置、进程和未来远程执行边界；
- Module 构造不检查硬件，运行时通过 Backend Probe 判断能力；
- 所有底层命令集中经过 ProcessExecutor；
- Registry 构建后只读，冲突保留且可诊断；
- 只维护一套 standard 功能集合，不产生 profile 和 build-tag 组合；
- system/host 是轻量只读模块，也是首条真实纵向能力；
- 首轮完成骨架和 system/host，再接需要外部依赖的 RAID 等领域工具。
