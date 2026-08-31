# toolctl Common 层设计

## 1. 目标

Common 层是所有内置模块、外部插件适配、CLI 和执行器共同依赖的稳定基础。它需要在新增 RAID、MySQL、Network 等模块时保持基本不变。

本设计解决：

- 哪些类型属于公共协议；
- 哪些接口允许模块依赖；
- 谁负责生成统一 Result；
- 配置、进程、文件系统、日志和诊断如何复用；
- 如何避免循环依赖和 `common/utils` 逐渐变成无边界杂物包。

## 2. 核心原则

1. 不创建泛化的 `common`、`utils`、`helpers` Go package；共享代码按职责命名。
2. `api/v1alpha1` 只放跨进程 wire types，不放执行逻辑。
3. `internal/core` 只放内置模块需要实现的接口和最小依赖对象。
4. Module/Runner 只返回领域执行数据，不生成最终 CLI Result envelope。
5. Executor 是 Result、status、metadata、退出码的唯一拥有者。
6. Renderer 只消费 Result，不了解 Module、Backend 或底层工具。
7. 内置模块不能依赖 `app`、`cli`、`registry`、`render` 或其他领域模块。
8. 外部插件只能依赖版本化协议；Go SDK 是便利层，不能成为协议本身。
9. 所有共享扩展点都必须有契约测试，不能只依赖接口注释。

## 3. 包和稳定性分层

```text
public wire contract
  api/v1alpha1

internal shared contracts
  internal/core
  internal/apperror
  internal/result

shared infrastructure
  internal/config
  internal/registry
  internal/executor
  internal/process
  internal/render
  internal/diagnostic
  internal/platform/linux

composition
  internal/app
  internal/cli
  cmd/toolctl

features
  internal/modules/system
  internal/modules/raid
  internal/modules/mysql
  internal/modules/network
```

稳定性级别：

| 层 | 兼容承诺 | 变更要求 |
| --- | --- | --- |
| `api/v1alpha1` JSON wire format | 外部兼容 | schema/golden/契约测试；破坏性变更升级 apiVersion |
| `internal/core` | 仓库内模块兼容 | 修改必须同步全部内置模块和接口测试 |
| infrastructure | 内部实现 | 保持对 core/API 的行为约定 |
| app/cli/modules | 组合实现 | 可以迭代，但不能绕过公共执行链 |
| modules | 独立功能 | 不能改变共享协议和全局输出规则 |

V1 发布前 `v1alpha1` 允许受控调整；进入正式 `v1` 后，同一 apiVersion 内不做破坏性变更。

## 4. 依赖方向

```text
cmd/toolctl
    -> app -> cli
       |      |
       v      v
     modules  executor -> result -> api/v1alpha1
       |         |           ^
       v         v           |
     modules -> core --------+
                 ^
                 |
       process --+-- platform/linux

config -------> api/v1alpha1
registry -----> core + api/v1alpha1
render -------> api/v1alpha1
diagnostic ---> core + api/v1alpha1
```

禁止依赖：

- `api/v1alpha1` 不能依赖任何 `internal` package；
- `core` 不能依赖 `app`、`cli`、`registry`、`render` 或具体 module；
- module 之间不能相互 import；
- module 不能 import Cobra；
- Renderer 不能 import module；
- config 不能执行 Module 业务；
- registry 不能执行 Operation。

CI 增加 import-boundary 测试或脚本，防止依赖方向被无意破坏。

## 5. 公共 Wire API

目录：

```text
api/v1alpha1/
├── capability.go
├── operation.go
├── execution.go
├── result.go
├── diagnostic.go
├── error.go
└── plugin.go
```

Wire API 只使用 JSON 可稳定表达的类型：

- `string`、`bool`、明确位宽整数；
- slice 和 map；
- `json.RawMessage` 扩展载荷；
- RFC 3339 字符串或拥有固定 marshal 行为的时间类型；
- 不直接在线协议中使用 `time.Duration`、`error`、interface 实现或函数。

所有 wire type 同时定义 JSON/YAML 名称，但 JSON 是插件进程协议的唯一编码。YAML 只是 CLI Renderer 的输出格式。

## 6. Capability 契约

```go
type Capability struct {
	ID         string          `json:"id" yaml:"id"`
	Domain     string          `json:"domain" yaml:"domain"`
	Resource   string          `json:"resource" yaml:"resource"`
	Verb       string          `json:"verb" yaml:"verb"`
	Command    CommandPathSpec `json:"command" yaml:"command"`
	Summary    string          `json:"summary" yaml:"summary"`
	NameMode   NameMode        `json:"nameMode" yaml:"nameMode"`
	TargetMode TargetMode      `json:"targetMode" yaml:"targetMode"`
	Mutating   bool            `json:"mutating" yaml:"mutating"`
	PassthroughArgs bool       `json:"passthroughArgs,omitempty" yaml:"passthroughArgs,omitempty"`
	Options    []OptionSpec    `json:"options" yaml:"options"`
	Columns    []ColumnHint    `json:"columns" yaml:"columns"`
}

type CommandPathSpec struct {
	Path    []string   `json:"path" yaml:"path"`
	Aliases [][]string `json:"aliases" yaml:"aliases"`
}
```

规则：

- ID 使用 `<domain>.<resource>.<verb>`，发布后不可复用为其他语义；
- Command.Path 是唯一 canonical 用户路径，例如 `host`；
- Aliases 是完整路径列表，不是单个 token 别名；
- path token 只允许小写字母、数字和短横线；
- alias 冲突导致 registry 构建失败，不按加载顺序静默覆盖；
- Mutating 为 true 时 verb 不允许省略；
- Options 和 Columns 的 slice 即使为空也规范化为 `[]`。
- Option shorthand 只能是单个 ASCII 字母，在 capability 内唯一，并避开核心全局短参数；长参数仍是自动化使用的稳定接口。

system host 示例：

```json
{
  "id": "system.host.info",
  "domain": "system",
  "resource": "host",
  "verb": "info",
  "command": {
    "path": ["host"],
    "aliases": []
  },
  "summary": "Show basic server information",
  "nameMode": "none",
  "targetMode": "none",
  "mutating": false,
  "options": [],
  "columns": []
}
```

## 7. Operation 契约

Operation 是核心发给 Runner 的唯一执行请求：

```go
type Operation struct {
	APIVersion  string                     `json:"apiVersion"`
	ID          string                     `json:"id"`
	Capability  string                     `json:"capability"`
	Name        string                     `json:"name,omitempty"`
	Arguments   []string                   `json:"arguments,omitempty"`
	Targets     []Target                   `json:"targets"`
	Options     map[string]json.RawMessage `json:"options"`
	TimeoutMS   int64                      `json:"timeoutMs"`
}
```

决定：

- 不在 Operation 重复传输 Domain/Resource/Verb；它们由稳定 Capability ID 唯一确定；
- Options 使用 `json.RawMessage`，避免 `map[string]any` 将整数隐式解码为 float64；
- Operation Builder 按 OptionSpec 验证后才生成 Options；
- 只有声明 PassthroughArgs 的 capability 可以接收 `--` 后的命令 argv，且不能同时使用 NameMode；
- wire 值固定为：string→JSON string、bool→JSON boolean、int→JSON integer、duration→整数毫秒、stringSlice→JSON string array；
- Runner 仍须防御性校验自己消费的 option；
- Targets 和 Options 为空时编码为 `[]` 和 `{}`；
- TimeoutMS 必须大于 0，并已包含配置和 flags 合并结果；
- 不向 Operation 放入明文 secret；凭据使用内部 ResolveContext 单独注入。

## 8. Runner 返回值

Runner 不返回最终 Result，而返回内部 RunOutput：

```go
type RunOutput struct {
	Items    []v1alpha1.Item
	Warnings []v1alpha1.Diagnostic
	Errors   []v1alpha1.TargetError
}

type Runner interface {
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}
```

规则：

- RunOutput 不包含 apiVersion、status、operation、metadata 或退出码；
- error 表示框架级失败，例如进程无法启动、协议损坏或内部不变量破坏；
- 正常的领域失败写入 RunOutput.Errors；
- 批量部分成功同时返回 Items 和 Errors；
- warning 不能单独导致失败状态；
- 返回前 Runner 将 nil slices 规范化的责任可以交给 Executor，模块无需重复代码。

这保证所有模块无法自行发明 Result 顶层格式。

## 9. Item Data 契约

```go
type Item struct {
	Target string          `json:"target,omitempty" yaml:"target,omitempty"`
	Name   string          `json:"name,omitempty" yaml:"name,omitempty"`
	Kind   string          `json:"kind" yaml:"kind"`
	Data   json.RawMessage `json:"data" yaml:"-"`
}
```

Module 内部使用强类型结构，通过统一 helper 创建 Item：

```go
func NewItem(kind, name, target string, value any) (v1alpha1.Item, error)
func DecodeItem[T any](item v1alpha1.Item) (T, error)
```

决定使用 RawMessage 而不是 `map[string]any`：

- 保持整数精度；
- 内置模块可以用强类型结构；
- 外部插件可以输出任意语言生成的 JSON object；
- 核心可以限制单个 Item 大小；
- 模块 schema 可以独立版本化。

约束：

- Data 顶层必须是 JSON object，不能是 scalar 或 array；
- Kind 使用 PascalCase，例如 `HostInfo`、`RaidDisk`；
- 同一 Capability 的 Item.Kind 必须稳定；
- 字段使用 lowerCamelCase；
- 未知数值使用 `null` 或省略，不能用 0 冒充；
- 容量字段以 `Bytes` 结尾；
- 毫秒字段以 `Ms` 结尾；
- 秒字段以 `Seconds` 结尾；
- 时间使用 RFC 3339 UTC；
- enum 使用小写 kebab-case。

YAMLRenderer 先把 Result 通过 JSON 解码为通用值，再编码 YAML，从而让 RawMessage 变成正常对象，不能把原始 bytes 输出为 base64 或字符串。

## 10. Result 所有权

最终 Result 只由 `internal/result.Builder` 创建：

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
```

Builder 固定执行：

1. 设置 apiVersion 和 kind；
2. 复制 Operation ID 和 Capability ID；
3. 合并单目标或批量 RunOutput；
4. 规范化 nil collection；
5. 根据 Items/Errors 计算 status；
6. 填充 generatedAt、durationMs 和实际 module 信息；
7. 校验 Item 大小、Kind 和 JSON Data；
8. 交给 Renderer。

Status 规则：

```text
errors == 0                    -> success
errors > 0 && items > 0        -> 按 target 判定 partial/failure
errors > 0 && items == 0       -> failure
framework error                -> failure
```

当 Error 带 target 时，如果 Items 中仍存在未失败 target，结果为 `partial`；如果所有 Item target 都失败，结果为 `failure`。不带 target 的全局 Error 与 Items 同时存在时为 `partial`。

公共字段单位固定为：

- `*Bytes`：原始 bytes 整数；
- `*BytesPerSecond`：bytes/s；
- `*MS`：毫秒；
- `*Seconds`：秒；
- `*Percent`：0–100；
- timestamp：RFC3339Nano，metadata `generatedAt` 统一输出 UTC。

未知数值使用 `null` 或省略可选字段，不得用 `0` 伪装已知值。同一 `apiVersion` 内可新增可选字段，不删除、重命名或改变现有字段语义。

Module、Runner、ProcessRunner 和 Renderer 均不能修改 status。

## 11. Error 和 Diagnostic

```go
type TargetError struct {
	Target    string            `json:"target,omitempty" yaml:"target,omitempty"`
	Code      ErrorCode         `json:"code" yaml:"code"`
	Message   string            `json:"message" yaml:"message"`
	Retryable bool              `json:"retryable" yaml:"retryable"`
	Details   map[string]string `json:"details" yaml:"details"`
}

type Diagnostic struct {
	Code    string            `json:"code" yaml:"code"`
	Message string            `json:"message" yaml:"message"`
	Target  string            `json:"target,omitempty" yaml:"target,omitempty"`
	Details map[string]string `json:"details" yaml:"details"`
}
```

约束：

- Code 是稳定机器码；Message 是简洁用户信息；
- Details 只允许 string map，避免无限嵌套和数字精度问题；
- 原始 stderr、stack、命令行和 secret 不进入公共错误；
- 同一失败不能同时包装成多个不同错误码；
- `internal/apperror` 负责 Go error 与 ErrorCode 的转换；
- 模块优先使用公共错误构造器，不能通过字符串匹配让核心猜错误类型。

## 12. Module 和 Backend 接口

```go
type Module interface {
	Info() v1alpha1.ModuleInfo
	Capabilities() []v1alpha1.Capability
	NewRunner(Dependencies) (Runner, error)
	Doctor(context.Context) []v1alpha1.CheckResult
}

type Backend interface {
	Name() string
	Probe(context.Context) ProbeResult
	Supports(capabilityID string) bool
	Execute(context.Context, v1alpha1.Operation) (RunOutput, error)
}
```

Module 规则：

- constructor 无 IO、副作用和硬件探测；
- Capabilities 每次返回等价、确定的数据；
- NewRunner 可以组装依赖，但不能因为可选工具缺失让整个 App 启动失败；
- Doctor 只读且尽可能完成所有检查；
- Runner 必须支持并发调用，或由 Module 明确声明 concurrency=1；
- Module 不持有 Cobra command、stdout/stderr 或全局 logger。

## 13. Dependencies

```go
type Dependencies struct {
	Files       FileSystem
	Processes   ProcessExecutor
	Environment Environment
	Secrets     SecretResolver
	Clock       Clock
	Waiter      Waiter
	IDs         IDGenerator
	Logger      *slog.Logger
}
```

### 13.1 FileSystem

```go
type FileSystem interface {
	ReadFile(path string, maxBytes int64) ([]byte, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	Stat(path string) (fs.FileInfo, error)
	StatFS(path string) (FileSystemStats, error)
}
```

只暴露当前需求，不提前封装整个 `os` package。生产使用 Linux OSFileSystem，测试使用 fixture/rooted FS。

### 13.2 ProcessExecutor

```go
type ProcessExecutor interface {
	LookPath(string) (string, error)
	Run(context.Context, ProcessSpec) ProcessResult
}
```

唯一拥有 `os/exec`、PATH 探测、进程组、stdout/stderr limit 和环境 allowlist。Module 不直接使用 `exec.Command` 或 `exec.LookPath`。

### 13.3 Environment

```go
type Environment interface {
	Lookup(key string) (string, bool)
	Allowed(keys ...string) []string
}
```

禁止模块遍历和泄漏完整父环境。

### 13.4 Clock 和 IDGenerator

用于固定测试中的 generatedAt、duration 和 Operation ID。模块不直接调用 `time.Now()` 生成公共 metadata。

Waiter 提供 `Wait(context.Context, time.Duration) error`，用于可取消的实时采样间隔。测试通过 fake Waiter 推进 fake Clock，不真实等待，也不修改全局时间。

### 13.5 SecretResolver

Secret 通过内部 ExecutionContext 注入需要它的 Runner，不序列化进 Operation、Result 或日志。首期只有 env provider。

## 14. Config Common

配置分三层：

```text
RawConfig       YAML 对应结构
MergedConfig    多来源合并结果
ResolvedConfig  引用、默认值和 selector 已解析的只读运行配置
```

约束：

- 只有 config package 读取 YAML 和环境配置；
- CLI flags 转为 ConfigOverride，不直接修改 RawConfig；
- Module 私有 settings 保留为 `yaml.Node` 或 JSON RawMessage；
- Module 在注册/doctor 阶段校验自己的 settings；
- ResolvedConfig 创建后只读；
- 所有 secret 展示经过统一 Redactor；
- 配置 merge 对 scalar、map、slice 的语义必须分别测试，不做不明确的深度 merge。

## 15. Registry Common

Registry 使用两个独立索引：

```text
Capability ID -> registrations/candidates -> selected runner
Command path  -> capability ID
Alias path    -> capability ID
```

Registry 决定：

- 同一个 ID 的候选优先级；
- canonical path 和 alias 冲突；
- module enabled/disabled；
- platform/capability 是否可见；
- 最终 RunnerFactory。

Registry 不决定：

- target 展开；
- timeout；
- backend 运行时选择；
- Result status；
- table 字段值。

Registry Build 完成后不可变，可安全并发读取。

## 16. Executor Common

Executor 持有通用执行策略：

- timeout 和 cancel；
- 单目标和批量目标；
- concurrency limit；
- fail-fast；
- panic recovery；
- RunOutput 合并和顺序；
- Result Builder；
- exit code。

Module 不重复实现这些逻辑。对于必须串行的底层工具，Capability 或 ModuleInfo 声明 MaxConcurrency=1，由 Executor 遵守。

批量执行不会共享可变 Operation.Options map；每个 worker 得到独立副本。

## 17. Renderer Common

```go
type RenderOptions struct {
	Format    OutputFormat // table, wide, json, yaml
	NoHeaders bool
	Columns   []v1alpha1.ColumnHint
}
```

职责边界：

- JSONRenderer/YAMLRenderer 输出完整 Result；
- TableRenderer 从 Item.Data 按 ColumnHint 读取值；
- Renderer 不重新计算 status；
- Renderer 不调用 Module；
- Renderer 不写日志；
- ColumnHint 不允许任意模板或代码执行；
- 格式化 bytes/duration/bool 的逻辑集中在 render/value package；
- 未知值统一显示 `-`；
- wide 只增加列，不改变行语义。

建议 ColumnHint：

```go
type ColumnHint struct {
	Header string     `json:"header" yaml:"header"`
	Path   string     `json:"path" yaml:"path"`
	Type   ColumnType `json:"type" yaml:"type"`
	Wide   bool       `json:"wide" yaml:"wide"`
	Order  int        `json:"order" yaml:"order"`
}
```

Path 只支持受限点路径，例如 `data.cpu.logicalCPUs`，不支持函数、管道、条件表达式或任意 JSONPath。

首期 ColumnType：

- `string`、`integer`、`boolean`：基础值；
- `decimal`、`percent`：固定两位小数，percent 增加 `%`；
- `bytes`：整数 bytes 转换为 IEC 单位；
- `bytes-per-second`：数值保持 bytes/s 语义并以 IEC 单位加 `/s` 展示；
- `duration-seconds`：整数或浮点 seconds 转换为可读 duration；
- `count`：数组元素数量；
- `named-bytes-list`：格式化对象数组中的 `name` 和 `sizeBytes`，例如 `sda:500.0 GiB,nvme0n1:1.8 TiB`。

`named-bytes-list` 是受限、通用的结构化列类型，不执行模板；数组元素缺少 name/sizeBytes 时跳过，全部无效时显示 `-`。

## 18. Diagnostic Common

CheckResult 是 doctor 的统一领域输出，Module 不直接构造 Result.Errors：

```go
type CheckResult struct {
	Name       string            `json:"name" yaml:"name"`
	Status     CheckStatus       `json:"status" yaml:"status"`
	Message    string            `json:"message" yaml:"message"`
	Suggestion string            `json:"suggestion,omitempty" yaml:"suggestion,omitempty"`
	Details    map[string]string `json:"details" yaml:"details"`
}
```

聚合 doctor 负责 pass/warn/fail/skip 的排序和总体退出码：默认只有 `fail` 生成 TargetError 并返回非零退出码；`warn` 仍保持成功退出码；`--strict` 下 `warn` 也生成 TargetError。`skip` 始终表示不适用或未检测到，不导致失败。

## 19. External Plugin 边界

外部插件协议响应使用 ExecutionResponse，而不是最终 Result：

```go
type ExecutionResponse struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"` // ExecutionResponse
	OperationID string       `json:"operationId"`
	Items      []Item        `json:"items"`
	Warnings   []Diagnostic  `json:"warnings"`
	Errors     []TargetError `json:"errors"`
}
```

ProcessRunner：

1. 发送 Operation；
2. 限制并读取 ExecutionResponse；
3. 校验 apiVersion、kind、operationId、Item Data 和错误码；
4. 转换为 RunOutput；
5. Executor 生成最终 Result。

插件不能设置最终 status、generatedAt、durationMs、module source 或 CLI exit code。

## 20. 并发和生命周期

- Registry 和 ResolvedConfig 创建后只读；
- Module 元数据只读；
- Runner 默认必须并发安全；
- 非并发安全 backend 通过内部 mutex 或 MaxConcurrency=1 处理；
- Result Builder 不修改调用者持有的 slices/maps；
- FileSystem 和 ProcessExecutor 必须支持并发；
- context 是取消的唯一通用通道，不增加全局 stop flag；
- App 进程结束时不保留后台 goroutine。

## 21. Common 层禁止事项

- 不添加业务名词，例如 raid controller、mysql replication；
- 不添加模块专用 parser；
- 不添加任意反射式 service locator；
- 不把 Cobra command 放入 Module 接口；
- 不暴露全局 mutable singleton；
- 不用 `map[string]any` 作为跨进程领域数据；
- 不允许模块决定最终 Result envelope；
- 不在多个 package 各自实现 bytes/duration/error 格式化；
- 不为单个调用点提前创建泛型 abstraction；
- 不把“暂时不知道放哪里”的代码放进 common。

## 22. 首期必须冻结的契约

进入 system/host 编码前确认并用测试固定：

1. Capability ID、Command.Path 和 alias 规则；
2. Operation JSON schema；
3. RunOutput 与 framework error 的边界；
4. Item.Kind/Data 和字段命名、单位规则；
5. Result envelope 和 status 计算规则；
6. ErrorCode、Diagnostic 和退出码映射；
7. Module、Runner、Backend 接口；
8. FileSystem、ProcessExecutor、Clock、IDGenerator 最小接口；
9. ColumnHint 和 table/wide 规则；
10. External ExecutionResponse；
11. Registry 冲突优先级；
12. nil collection 规范化和确定性序列化。

每项至少包含：Go 类型测试、JSON golden、非法输入测试和一条端到端 CLI 测试。

## 23. 可以延后冻结的内容

- gRPC/agent transport；
- streaming/NDJSON；
- plugin Go SDK；
- credential provider 扩展；
- custom columns；
- server-side task；
- cache；
- telemetry exporter；
- 声明式 Resource Spec/Status。

这些内容不能反向迫使首期 Module 绕过现有公共接口；确需破坏协议时升级 apiVersion。
