# toolctl v1

`toolctl` 是面向 Linux 服务器的单文件基础设施工具集。v1 聚焦服务器初检、实时性能、网络诊断、RAID、MySQL/Redis 快速指标、容器与远程批量执行、CPU/内存/磁盘/网络压测，以及新服务器综合评估。

设计目标：

- 一个二进制覆盖常用服务器排查和验收场景；
- 核心查询优先使用 Go、`/proc`、`/sys` 和 Linux syscall，不依赖常驻服务；
- 可选外部工具缺失时只影响对应能力，并可用 `toolctl doctor` 统一检查；
- 所有命令使用统一的 table、wide、JSON 和 YAML 输出模型；
- v1 仅支持 Linux `amd64` 和 `arm64`，只维护一套 standard 功能集。

> 注意：`bench` 和 `burn` 会产生实际负载；`bench device` 的写测试会不可逆覆盖裸盘数据。执行前请阅读对应章节。

## 快速开始

### 构建

需要 Go 工具链。在项目根目录执行：

```bash
make build-linux
```

生成：

```text
dist/toolctl_linux_amd64
dist/toolctl_linux_arm64
```

构建时可写入版本信息：

```bash
make build-linux \
  TOOLCTL_VERSION=v1.0.0 \
  TOOLCTL_COMMIT="$(git rev-parse --short HEAD)" \
  TOOLCTL_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

将对应架构的文件复制到服务器并赋予执行权限，例如：

```bash
chmod +x toolctl_linux_arm64
sudo install -m 0755 toolctl_linux_arm64 /usr/local/bin/toolctl
toolctl version
```

### 新服务器建议执行顺序

```bash
# 1. 检查平台和可选依赖
toolctl doctor -o wide

# 2. 查看机器身份、磁盘、网卡和当前健康状态
toolctl host -o wide
toolctl disks
toolctl nics -o wide
toolctl health
toolctl disk health -o wide

# 3. 查看 RAID 和实时性能
toolctl raid doctor -o wide
toolctl raid status
toolctl raid disks
toolctl perf cpu -c 5
toolctl perf io -c 5
toolctl perf net -c 5

# 4. 运行无外部依赖的快速评估并保存结果
toolctl assess -o json > assess-quick.json
```

需要完整压测时，再准备 `fio`、空闲数据盘或数据目录，以及另一台运行 `bench net serve` 的服务器。

## 通用行为

### 通用参数

所有业务命令统一支持：

```text
-o, --output table|wide|json|yaml   输出格式，默认 table
    --no-headers                    table/wide 不显示表头
    --timeout duration              整个操作超时，默认 30s
-h, --help                          查看帮助
```

示例：

```bash
toolctl host -o wide
toolctl disks --no-headers
toolctl health -o json
toolctl raid status -o yaml --timeout 1m
```

`table` 用于日常终端查看；`wide` 增加路径、固件、配置来源等扩展字段；`json`/`yaml` 适合归档和自动化。JSON/YAML 都使用统一 Result 外壳，包含 `status`、`operation`、`items`、`warnings`、`errors` 和 `metadata`。

结果状态为：

- `success`：命令成功；
- `partial`：获得部分结果，但部分目标或检查失败；
- `failure`：整体失败。

退出码约定：

| 退出码 | 含义 |
| --- | --- |
| `0` | 成功 |
| `1` | 执行失败或部分失败 |
| `2` | 参数或配置错误 |
| `3` | 平台、能力或依赖不满足 |
| `4` | 超时或取消 |

### 实时输出

支持 `-l/--live` 的命令会持续追加快照，按 `Ctrl-C` 结束。无限实时流只支持 `table` 和 `wide`，不支持将无限数据伪装成单个 JSON/YAML 文档。

`perf` 的 `-c/--count` 只控制有界采样；开启 `-l` 后忽略 count。`bench net -l` 不同：它仍受 `-d/--duration` 限制，只在测试期间增加进度快照。

### 可选外部依赖

| 工具 | 影响的命令 | 是否必需 |
| --- | --- | --- |
| `fio` | `bench disk`、`bench device` | 仅磁盘压测必需 |
| `smartctl` / `nvme` | `disk health` 深度 SMART/NVMe 数据 | 可选，基础 sysfs 检查仍可用 |
| `storcli` / `perccli` / `ssacli` / `arcconf` | 对应厂商硬件 RAID 查询 | 对相应控制器必需 |
| `ssh` | `batch ssh` | 必需 |
| `nsenter` | `batch containers` | 必需 |
| `ssar` + sresar 历史数据 | `perf history ...` | 仅历史查询必需 |
| `mysql` / `mariadb` client | `mysql` | 仅 MySQL/MariaDB 查询必需 |
| `redis-cli` | `redis` | 仅 Redis 查询必需 |

## 命令总览

```text
toolctl doctor
toolctl host
toolctl disks
toolctl nics
toolctl net dns|route|connect
toolctl health
toolctl top [cpu|mem|io]
toolctl disk health
toolctl perf doctor|cpu|io|net|tcp retrans
toolctl perf history doctor|cpu|io|net|tcp retrans
toolctl raid doctor|status|controllers|volumes|disks
toolctl init raid
toolctl mysql
toolctl redis
toolctl containers [all|cpu|mem|io]
toolctl batch ssh|containers -- <command>
toolctl bench cpu|mem|disk|device|net
toolctl burn [cpu|mem|mixed]
toolctl assess [quick|full]
toolctl assess compare
toolctl version
toolctl completion bash|zsh|fish|powershell
```

下面逐个说明 v1 的每个可执行命令。

## 环境与主机信息

### `toolctl doctor`

统一检查当前服务器是否满足各项能力的运行条件。面向用户的 component 包括 `core`、`perf`、`bench`、`raid`、`batch`、`mysql` 和 `redis`，不会输出内部 package 名称。

检查内容包括 Linux/ARCH、procfs、可选 SMART/NVMe 工具、ssar 历史后端、fio、OpenSSH、nsenter、MySQL/MariaDB client、redis-cli，以及已检测 RAID 控制器匹配的厂商工具。状态包括 `pass`、`warn`、`fail`、`skip`。默认只有 `fail` 返回非零；`--strict` 下 `warn` 也返回非零。

```bash
toolctl doctor
toolctl doctor -o wide
toolctl doctor --component core,raid
toolctl doctor --component perf,bench,batch --strict
toolctl doctor --component mysql,redis -o wide
```

`doctor` 只检查本机环境。例如 SSH 检查通过不代表所有远程主机均可连接。

### `toolctl host`

显示当前服务器的 hostname、物理机/虚拟机类型、硬件型号、虚拟化技术、发行版、内核、架构、CPU、内存、根文件系统、顶层磁盘摘要、网卡数量和 uptime。

```bash
toolctl host
toolctl host -o wide
toolctl host -o json > host.json
```

该命令不执行外部程序，适合作为拿到服务器后的第一份身份快照。

### `toolctl disks`

列出操作系统可见的顶层块设备，包括设备名、类型、容量、型号和是否为旋转介质；`wide` 额外显示 removable。

```bash
toolctl disks
toolctl disks -o wide
toolctl disks -o json
```

它展示的是 Linux 块设备视图。硬件 RAID 后的物理盘槽位请使用 `toolctl raid disks`。

### `toolctl nics`

列出网卡名称、类型、状态、MTU、速率和双工模式；`wide` 额外显示 carrier 和是否为虚拟网卡。

```bash
toolctl nics
toolctl nics -o wide
```

v1 默认不采集 MAC 和 IP，避免初期盘点输出唯一网络标识。路由选源地址可使用 `net route`。

### `toolctl health`

检查当前服务器运行状态，而不是工具依赖。它读取 1 分钟 load、可用内存、swap 使用、根文件系统占用和非 loopback 网卡 down 数量，并给出 `pass`、`warn` 或 `fail`。

```bash
toolctl health
toolctl health -o json
```

这是一个瞬时健康快照，不替代长期监控、BMC、ECC/MCE 或硬件巡检。

### `toolctl top [cpu|mem|io]`

从 `/proc/<pid>` 采集进程快照，默认按 CPU 排序，也可按内存或 I/O 排序。CPU 和 I/O 需要两次快照计算速率。

默认：模式 `cpu`、间隔 `1s`、最多 `10` 个进程。

```bash
toolctl top
toolctl top cpu -n 20
toolctl top mem -n 20
toolctl top io -i 2s
toolctl top cpu -l
```

输出的是 `/proc/<pid>/stat` 中的短 command，不是完整 cmdline。采样期间退出的进程会被正常忽略。

### `toolctl disk health`

检查顶层块设备的 sysfs state、只读状态、scheduler、queue depth 和 timeout。若存在 `smartctl`，会解析 SMART JSON；NVMe 在 smartctl 不可用或不支持时回退 `nvme smart-log -o json`。

```bash
toolctl disk health
toolctl disk health -o wide
toolctl doctor --component core -o wide
```

SMART failed 或 critical warning 会形成失败，历史 media error 会形成 warning。硬件 RAID 控制器后的物理盘仍应以 `raid disks` 和厂商工具结果为准。

## 网络诊断

### `toolctl net dns <name>`

使用系统解析配置解析主机名，输出全部 A/AAAA 地址和整次 lookup 耗时。

```bash
toolctl net dns example.com
toolctl net dns server.example.com -o json
```

### `toolctl net route <name>`

判断访问目标时内核会选择哪个目标地址、本地源地址和出口网卡。IPv4 下还会从 `/proc/net/route` 补充 gateway。

```bash
toolctl net route 10.0.0.2
toolctl net route example.com -o wide
toolctl net route 2001:db8::2
```

实现上使用无 payload 的 UDP connect 触发内核路由选择，不是在探测目标端口，也不会证明目标可达。需要验证端口时使用 `net connect`。

### `toolctl net connect <host:port>`

进行一次真实 TCP connect，输出连接状态、本地/远端 socket 和连接耗时。目标必须包含端口；默认连接超时为 `3s`。

```bash
toolctl net connect 10.0.0.2:9234
toolctl net connect server.example.com:22 --connect-timeout 5s
toolctl net connect 10.0.0.2:9234 --bind 10.0.0.1
toolctl net connect '[2001:db8::2]:443'
```

`--bind` 指定本地 IPv4 或 IPv6 源地址。

## 无依赖实时性能

实时 `perf` 直接读取 `/proc/stat`、`/proc/diskstats`、`/proc/net/dev` 和 `/proc/net/snmp`。它读取基线快照，等待 `-i/--interval`，再根据累计计数器差值计算速率和百分比。

共同默认值：`-i 1s`、`-c 1`、不开启 live。

### `toolctl perf doctor`

检查实时性能采样所需的 Linux procfs 数据源是否可读，不检查可选历史后端。

```bash
toolctl perf doctor
toolctl perf doctor -o json
```

### `toolctl perf cpu`

显示 CPU user、system、iowait、idle 和 busy 百分比。默认返回汇总 CPU，可用 `--cpu` 选择逻辑 CPU。

```bash
toolctl perf cpu
toolctl perf cpu -i 1s -c 5
toolctl perf cpu --cpu cpu,cpu0,cpu1 -c 5
toolctl perf cpu -l
```

### `toolctl perf io`

显示逐块设备的读写 IOPS、读写带宽、平均等待时间和 util。

```bash
toolctl perf io
toolctl perf io --device sda,nvme0n1 -c 5
toolctl perf io --device nvme0n1 -i 2s -l
```

设备名使用内核名称，不带 `/dev/` 前缀。

### `toolctl perf net`

显示逐网卡的收发带宽、包速率、错误和丢包速率。

```bash
toolctl perf net
toolctl perf net --interface eth0,ens3 -c 5
toolctl perf net --interface eth0 -l
```

### `toolctl perf tcp retrans`

根据 `/proc/net/snmp` 中 TCP 累计计数器计算每秒 OutSeg、每秒重传和重传比例。

```bash
toolctl perf tcp retrans
toolctl perf tcp retrans -i 1s -c 10
toolctl perf tcp retrans -l
```

Linux 的 `MaxConn=-1` 是合法的“无限制”哨兵值，不参与无符号 TCP 计数器计算。

## 可选历史性能

`perf history` 是 ssar adapter：通过 `ssar -P --api` 查询历史数据并归一化输出，不解析 tsar2 表格，也不经过 shell。它要求安装 ssar，并由 sresar 在 `/var/log/sre_proc` 留存历史数据。

历史命令默认查询最近 `5h`，聚合间隔 `5m`。可使用 `--range`，或使用 RFC3339 格式的 `--from`/`--to` 指定绝对范围。

### `toolctl perf history doctor`

检查 ssar 可执行文件、版本以及 `/var/log/sre_proc` 历史数据目录。

```bash
toolctl perf history doctor
toolctl perf history doctor -o wide
```

### `toolctl perf history cpu`

查询历史 CPU 指标，可用 `--cpu` 过滤。

```bash
toolctl perf history cpu
toolctl perf history cpu --range 1h -i 5m
toolctl perf history cpu --cpu cpu0,cpu1 --range 30m
toolctl perf history cpu --from 2026-08-06T10:00:00+08:00 --to 2026-08-06T11:00:00+08:00
```

### `toolctl perf history io`

查询历史块设备 I/O 指标，可用 `--device` 过滤。

```bash
toolctl perf history io
toolctl perf history io --device sda,nvme0n1 --range 2h -i 10m
```

### `toolctl perf history net`

查询历史网卡流量指标，可用 `--interface` 过滤。

```bash
toolctl perf history net
toolctl perf history net --interface eth0 --range 1h -i 5m
```

### `toolctl perf history tcp retrans`

查询历史 TCP 输出和重传指标。

```bash
toolctl perf history tcp retrans
toolctl perf history tcp retrans --range 6h -i 15m
```

## RAID

RAID 使用统一数据模型，并按控制器选择 backend。v1 支持无外部依赖的 Linux MD，以及 `storcli`、`perccli`、`ssacli` 和 `arcconf`。所有 RAID 查询均为只读。

### `toolctl raid doctor`

通过 sysfs PCI class、vendor 和 subsystem vendor 识别 RAID/SAS 控制器，判断匹配工具是否存在、可执行和已经登记。

```bash
toolctl raid doctor
toolctl raid doctor -o wide
```

`wide` 中的 `SCOPE` 表示工具来源：`auto` 为 PATH/常见目录自动发现，`user` 为用户配置，`system` 为系统配置，`explicit` 为 `TOOLCTL_CONFIG_DIR`，`builtin` 为 Linux MD。

### `toolctl raid status`

汇总 RAID 健康、控制器/虚拟盘/物理盘数量，以及 degraded、failed、rebuilding 数量。

```bash
toolctl raid status
toolctl raid status -o wide
toolctl raid status -o json
```

`COMPLETE=false` 表示清单不完整，例如检测到控制器但缺少厂商工具。此时已知局部正常不会被误判为整体 `optimal`。

### `toolctl raid controllers`

列出控制器 ID、backend、厂商、型号和健康状态；`wide` 额外显示 PCI 地址和 firmware。

```bash
toolctl raid controllers
toolctl raid controllers -o wide
```

### `toolctl raid volumes`

列出硬件 RAID virtual drive 和 Linux MD array，包括 RAID level、状态、容量、名称和设备路径。

```bash
toolctl raid volumes
toolctl raid volumes -o wide
```

### `toolctl raid disks`

列出 RAID 物理盘，重点显示 enclosure、slot、状态、容量、介质类型、接口和型号。

```bash
toolctl raid disks
toolctl raid disks -o wide
toolctl raid disks -o json
```

这是定位“哪个槽位的哪块盘异常”的主要入口。可见字段取决于厂商工具能提供的清单。

### `toolctl init raid`

登记本机已经安装的 RAID 管理工具。该命令只保存可执行文件路径，不复制、不下载工具；登记后 RAID 命令和 `doctor -o wide` 会显示其配置来源，后续无需每次指定路径。

```bash
# 自动发现并登记所有支持的工具
toolctl init raid

# 自动发现指定 backend
toolctl init raid --backend storcli

# 登记明确路径
toolctl init raid --backend storcli --path /opt/MegaRAID/storcli/storcli64

# 写入系统级配置，需要 root
sudo toolctl init raid --system --backend perccli \
  --path /opt/MegaRAID/perccli/perccli64
```

用户配置位于用户配置目录下的 `toolctl/raid-tools.json`；系统配置为 `/etc/toolctl/raid-tools.json`。同 backend 下用户配置覆盖系统配置。设置 `TOOLCTL_CONFIG_DIR` 时使用该显式配置目录。配置文件权限为 `0600`。

## 容器与批量执行

### `toolctl containers [all|cpu|mem|io]`

扫描 `/proc/<pid>/cgroup` 发现 Docker、containerd、CRI-O 和 Podman 容器，直接读取 cgroup v2 的累计 CPU、当前/限制内存、PID 数量以及累计读写 bytes/IO 次数，不在容器内执行命令。

默认模式为 `all`。

```bash
toolctl containers
toolctl containers cpu
toolctl containers mem --runtime containerd
toolctl containers io --container abcd1234
toolctl containers all --runtime docker,podman -o json
```

`--container` 接受一个或多个容器 ID 前缀，`--runtime` 用于过滤 runtime。v1 在 cgroup v1 主机返回明确 warning；CPU 和 I/O 是累计值，不是实时速率。

### `toolctl batch ssh -- <command> [args...]`

使用系统 OpenSSH client 在多台主机并行执行同一命令，不依赖 Python pssh。目标来自 `-H/--hosts` 和/或 `--host-file`；默认并发 `16`、连接超时 `10s`、单主机命令超时 `30s`。

```bash
toolctl batch ssh -H node1,node2 -- uptime
toolctl batch ssh --host-file hosts.txt -u root -j 32 -- df -h
toolctl batch ssh -H node1 -i ~/.ssh/id_ed25519 -- uname -a
toolctl batch ssh -H node1 --accept-new -- systemctl is-active nginx
toolctl batch ssh -H node1 -- sh -c 'systemctl status nginx | head'
```

`--` 后的内容原样作为远端 argv。认证、ProxyJump、HostName 和默认 Port 沿用 `~/.ssh/config`；只有显式 `-p` 才覆盖 Port。命令启用 `BatchMode=yes`，不会等待交互密码。默认严格校验 host key；`--accept-new` 只接受首次出现的 key，仍拒绝变化的 key。

每个目标默认最多捕获 128 KiB stdout 和 128 KiB stderr，可用 `--max-output` 调整，上限 16 MiB。任一目标失败会返回 partial/failure 和非零退出码。

### `toolctl batch containers -- <command> [args...]`

使用系统 `nsenter` 对本机发现的容器批量执行命令。默认 `--scope net`：只进入容器 network namespace，保留宿主机文件系统并使用宿主机命令，适合 minimal 容器。

```bash
# 默认 net scope：宿主机 ip 在每个容器网络 namespace 中执行
toolctl batch containers -- ip -br address
toolctl batch containers -- ss -lntp

# 完整容器执行环境
toolctl batch containers --scope exec -- cat /etc/os-release

# 容器文件系统视图
toolctl batch containers --scope fs -- df -h

# 容器进程视图
toolctl batch containers --scope process -- ps aux

# 过滤目标
toolctl batch containers --runtime containerd --container abcd1234 -- ip route
```

scope 语义：

| scope | namespace/视图 | 典型用途 |
| --- | --- | --- |
| `net` | 仅 network namespace | `ip`、`ss`、`ping`；默认 |
| `exec` | 完整容器执行环境 | 执行镜像内命令 |
| `fs` | mount、root、working directory | `df`、`du`、文件检查 |
| `process` | PID、mount、root、working directory | 容器进程视图 |

`--network-only` 是 `--scope net` 的兼容写法。exec/fs/process 从容器文件系统查找命令，minimal 镜像缺少命令时通常返回 exit 127。该能力通常需要 root 或相应 capabilities。默认并发 `16`、单容器超时 `30s`、每个 stdout/stderr 上限各 128 KiB。

## 数据库快速指标

MySQL 和 Redis 以两个独立内置 Module 接入，遵循与其他模块相同的 Capability、Runner、Doctor 和 Result 接口。它们只执行查询，不修改数据库配置。客户端缺失只影响对应命令，并由 `toolctl doctor` 显示为 warning。

为避免凭据出现在 shell history 和进程列表中，两条命令都不提供明文 `--password` 参数。

### `toolctl mysql`

通过本机 `mysql` 或 `mariadb` client 一次读取 GLOBAL STATUS/VARIABLES，并尝试从 performance_schema 获取已插桩内存。默认连接 `127.0.0.1:3306`；user 和认证信息仍沿用客户端 option file，或由 `--defaults-extra-file` 提供。

```bash
# 使用 ~/.my.cnf 中的认证信息连接本机 3306
toolctl mysql

# 查询远端实例
toolctl mysql -H 10.0.0.10 -p 3306 -u observer

# 使用只读凭据文件，避免密码出现在 argv；默认仍连接本机 3306
toolctl mysql --defaults-extra-file /run/secrets/toolctl-mysql.cnf

# 指定 Unix socket
toolctl mysql -S /run/mysqld/mysqld.sock

# 查看缓存和连接扩展指标
toolctl mysql -o wide
```

建议凭据文件权限为 `0600`：

```ini
[client]
user=observer
password=your-secret
host=127.0.0.1
port=3306
```

默认表格重点显示：版本、uptime、当前可用内存计数、`innodb_buffer_pool_size`、buffer pool 使用率、当前/运行中/上限/历史峰值连接数和 buffer pool 命中率。`wide` 额外显示内存来源、buffer pool data/instances、thread cache、table cache、累计连接和 aborted connects。

这里的“连接池/POOL”指数据库服务端的 InnoDB buffer pool、`max_connections` 和 thread cache。应用侧 HikariCP、Druid 等连接池大小只存在于应用进程配置中，MySQL 服务端无法可靠反推出其配置值；toolctl 展示的是应用最终在服务端形成的实际连接数和历史峰值。

`MEMORY` 优先采用 `performance_schema.memory_summary_global_by_event_name` 的已插桩内存；若权限、版本或插桩配置不支持，则回退到服务器可用的 memory status，最后回退到 InnoDB buffer pool data，并通过 warning 和 `MEMORY SOURCE` 明确说明。因此该值不应直接等同于 mysqld 进程 RSS。

### `toolctl redis`

通过本机 `redis-cli INFO ALL` 获取重点指标。默认连接 `127.0.0.1:6379`、DB 0。需要密码时使用 `REDISCLI_AUTH`，不要把密码放在命令行中。MySQL 和 Redis 指定 `--socket` 时都会忽略默认 host/port。

```bash
# 本机默认实例
toolctl redis

# 远端实例和 ACL 用户
REDISCLI_AUTH='your-secret' toolctl redis -H 10.0.0.20 -p 6379 -u observer

# Unix socket
REDISCLI_AUTH='your-secret' toolctl redis -S /run/redis/redis.sock

# TLS
REDISCLI_AUTH='your-secret' toolctl redis -H redis.example.com --tls \
  --cacert /etc/ssl/redis-ca.pem

# 查看策略、碎片率、拒绝连接、淘汰和 key 数
toolctl redis -o wide
```

默认表格重点显示：版本、role、uptime、used memory、RSS、maxmemory、内存上限使用率、当前连接、maxclients、OPS/s 和 keyspace hit rate。`wide` 额外显示 maxmemory policy、内存碎片率、blocked clients、累计/拒绝连接、evicted keys 和所有 DB 的 key 总数。

较旧 Redis 未在 INFO 中提供 `maxclients` 时，toolctl 会只读执行 `CONFIG GET maxclients` 作为回退；ACL 不允许 CONFIG 时其余指标仍正常返回，`MAX CLIENTS` 显示为 0/未知。

## CPU、内存与稳定性测试

### `toolctl bench cpu`

使用固定 SHA-256 workload 测量 CPU 吞吐，不依赖外部程序。默认运行 `10s`，`-j 0` 表示使用全部可见逻辑 CPU。

```bash
toolctl bench cpu
toolctl bench cpu -j 1 -d 10s
toolctl bench cpu -j 8 -d 30s --timeout 40s
```

该结果适合相同 toolctl 版本和参数下做相对比较，不是跨工具通用跑分。

### `toolctl bench mem`

测量内存复制带宽和随机读延迟，不依赖外部程序。默认工作集 `64M`、线程 `1`，复制和随机访问两个阶段各运行 `5s`。

```bash
toolctl bench mem
toolctl bench mem -s 1G -j 4 -d 30s --timeout 2m
```

`-s/--size` 是总工作集大小，不是每线程大小。

### `toolctl burn [cpu|mem|mixed]`

运行有界 CPU、内存或混合稳定性负载。默认模式 `mixed`、持续 `20s`、内存 `256M`，CPU 线程 `0` 表示全部逻辑 CPU。

```bash
toolctl burn
toolctl burn cpu -d 10m --timeout 11m
toolctl burn mem -s 1G -d 10m --timeout 11m
toolctl burn mixed -s 1G -d 30m --timeout 31m
```

它验证负载能否持续执行，不监控温度、降频、ECC/MCE 或 BMC SEL，不能替代硬件稳定性平台。

## 磁盘压测

### `toolctl bench disk doctor`

检查 PATH 中的 `fio` 是否存在、能否执行，以及是否为预期的 Linux fio。

```bash
toolctl bench disk doctor
toolctl bench disk doctor -o wide
```

### `toolctl bench disk <directory>`

在指定已挂载目录创建独占临时文件并调用 fio。默认 profile `rand-write`、文件 `1G`、正式测试 `10s`、预热 `2s`、job `1`、queue depth `16`。随机 I/O 默认 block size `4K`，顺序 I/O 默认 `1M`。

```bash
toolctl bench disk /data
toolctl bench disk /data -p seq-write
toolctl bench disk /data -p rand-read -s 4G -d 30s --timeout 1m
toolctl bench disk /data -p rand-rw -j 4 -q 32
toolctl bench disk /data --dry-run -o wide
```

profile 支持 `seq-read`、`seq-write`、`rand-read`、`rand-write`、`rand-rw`。读和混合 workload 会先写满文件，避免读取稀疏文件得到虚假结果。默认结束后删除临时文件，`--keep-file` 可保留。

命令会检查目录、剩余空间和安全余量。目标属于根文件系统时，真实写测试必须增加 `--force`：

```bash
toolctl bench disk / --dry-run
toolctl bench disk / --force --timeout 1m
```

`--dry-run` 只验证依赖、参数、目标和 fio 计划，不创建文件、不产生 I/O 负载。

### `toolctl bench device <device>`

直接对未使用的 Linux 整盘块设备运行 fio。默认 profile 为只读的 `seq-read`，区域 `1G`、正式测试 `10s`、预热 `2s`、job `1`、queue depth `16`。

```bash
toolctl bench device /dev/nvme1n1 --dry-run -o wide
toolctl bench device /dev/nvme1n1 -p seq-read
toolctl bench device /dev/nvme1n1 -p rand-read -s 10G -d 30s
```

写 profile 会不可逆覆盖设备数据，必须显式增加 `--destroy-data`：

```bash
toolctl bench device /dev/nvme1n1 -p rand-write --destroy-data
```

命令拒绝分区、存在子分区的整盘、sysfs holders、mount、swap 或 Linux MD 正在使用的设备。`--destroy-data` 只确认不可逆风险，不会绕过占用检查。始终先核对 `--dry-run` 输出中的设备、容量、profile 和 fio 参数。

## 网络压测

网络压测使用 toolctl 内置 TCP 协议，同一轮测试两端应使用相同 toolctl 版本。服务端一次只处理一个有界 session，完成后退出；吞吐和 latency 都要各自重新启动服务端。

### `toolctl bench net serve`

启动一次性 TCP 压测服务端，默认监听 `:9234`。

```bash
toolctl bench net serve --timeout 2m
toolctl bench net serve --listen 10.0.0.2:9234 --timeout 2m
toolctl bench net serve --listen '[::]:9234' --timeout 2m
```

`--timeout` 应长于等待客户端和实际测试的总时间。

### `toolctl bench net <peer>`

连接 toolctl 压测服务端并测量 TCP 吞吐。默认单流、客户端发送、持续 `10s`、端口 `9234`。peer 可直接带端口。

```bash
toolctl bench net 10.0.0.2
toolctl bench net 10.0.0.2 -P 8 -d 30s --timeout 1m
toolctl bench net '[2001:db8::2]:9234' -d 30s --timeout 1m
toolctl bench net 10.0.0.2 -P 8 --direction receive -d 30s
toolctl bench net 10.0.0.2 -P 8 --direction both --bind 10.0.0.1 -d 30s
toolctl bench net 10.0.0.2 -P 4 --rate 1Gbps -d 30s
toolctl bench net 10.0.0.2 -P 8 --direction both -d 30s -l -i 1s
```

`--direction` 以客户端视角支持 `send`、`receive`、`both`；`-P/--parallel` 支持 1～128 流。`--rate` 是每个方向跨全部 stream 的合计限制，接受 bps/Kbps/Mbps/Gbps 或 Kibps/Mibps/Gibps。`-l` 只增加有界测试的累计平均速率快照，仍在 `-d` 到期后结束，并只支持 table/wide。

### `toolctl bench net latency <peer>`

复用同一个服务端协议执行固定 8-byte TCP echo，输出 min、avg、P50、P95、P99 和 max。默认 `100` 次、间隔 `10ms`、端口 `9234`。

```bash
# 先在对端重新启动一次 server
toolctl bench net serve --timeout 2m

# 再在客户端测试
toolctl bench net latency 10.0.0.2
toolctl bench net latency 10.0.0.2 -c 1000 -i 5ms
toolctl bench net latency 10.0.0.2 --bind 10.0.0.1
```

这是应用层 TCP RTT，不等同于 ICMP ping；v1 尚不提供 UDP、jitter 或 packet loss 压测。

## 新服务器综合评估

### `toolctl assess [quick|full]`

将多个现有能力按固定参数组合为一份服务器评估结果。默认模式为 `quick`。

quick 包含：环境依赖、host、RAID 状态、空闲 CPU、2 秒单核 CPU、2 秒全核 CPU，以及 64M 内存测试。

```bash
toolctl assess
toolctl assess quick
toolctl assess -o json > assess-quick.json
```

full 在 quick 基础上增加 3 秒 mixed stability；提供 `--disk` 时增加默认 1G/10s rand-write 文件压测，提供 `--network-peer` 时增加 3 秒单流 TCP send 测试。未提供的外部测试会明确显示 `skip`。

```bash
# 对端先启动一次服务
toolctl bench net serve --timeout 2m

# 待测服务器运行完整评估
toolctl assess full --disk /data --network-peer 10.0.0.2 \
  --timeout 1m -o json > assess-full.json
```

JSON/YAML 会保留每个 section 的原始 item、warning 和 error。磁盘、网络和稳定性测试都计入全局 `--timeout`，应预留足够时间。

### `toolctl assess compare`

比较两份已保存的 `toolctl assess -o json` 中路径一致的已知 benchmark 数值。默认退化 `10%` 为 warn、`25%` 为 fail；吞吐/IOPS 越高越好，延迟越低越好。

```bash
toolctl assess compare \
  --baseline baseline.json \
  --current server.json

toolctl assess compare \
  --baseline baseline.json \
  --current server.json \
  --warn-percent 5 \
  --fail-percent 15 \
  -o json
```

只比较数值性能指标，不比较服务器身份、环境检查和健康文本。应保证两次使用相同 toolctl 版本、模式、profile、线程、工作集、空闲状态和电源策略。

## 版本与补全

### `toolctl version`

显示构建时写入的 version、commit 和 build time。

```bash
toolctl version
toolctl version -o json
```

未注入构建参数的本地构建通常显示 `dev`、`unknown`。

### `toolctl completion bash`

生成 Bash 补全脚本：

```bash
toolctl completion bash > /etc/bash_completion.d/toolctl
```

### `toolctl completion zsh`

生成 Zsh 补全脚本：

```bash
mkdir -p ~/.zfunc
toolctl completion zsh > ~/.zfunc/_toolctl
```

确保 `~/.zfunc` 位于 `fpath`，再执行 `compinit`。

### `toolctl completion fish`

生成 Fish 补全脚本：

```bash
toolctl completion fish > ~/.config/fish/completions/toolctl.fish
```

### `toolctl completion powershell`

生成 PowerShell 补全脚本：

```powershell
toolctl completion powershell | Out-String | Invoke-Expression
```

toolctl 本体只支持 Linux amd64/arm64；该命令用于需要 PowerShell shell 补全语法的 Linux 环境。

## 开发与验证

```bash
make fmt
make test
make test-race
make vet
make verify
```

`make verify` 会依次执行普通测试、race 测试、vet，并构建 Linux amd64/arm64 两个发行物。

## 设计文档

- [`toolctl_design.md`](toolctl_design.md)：总体设计；
- [`toolctl_technical_design.md`](toolctl_technical_design.md)：技术实现方案；
- [`toolctl_common_design.md`](toolctl_common_design.md)：Common 层与模块扩展契约。

当前 Go module 路径暂为 `toolctl`。发布公共插件 SDK 前需要替换为最终代码仓库地址。
