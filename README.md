# ClusterGuard HA

### 企业级数据库高可用与故障接管平台

**让数据库故障接管更安全，让业务恢复更可控。**

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](README.en.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

ClusterGuard HA 是一套面向 **MySQL 与 PostgreSQL** 的数据库高可用管理平台，提供从数据库集群监测、主备拓扑管理、故障检测、安全仲裁、自动或受控故障切换，到写入口迁移、故障节点恢复和操作审计的一体化高可用管理能力。

它的核心目标不是简单地将备库提升为主库，而是确保数据库发生故障或进行计划维护时，能够在满足安全条件的前提下，**将数据库写入服务从原主库安全地转移到新的主库，同时避免多个节点同时拥有合法写权限。**

## 为什么需要 ClusterGuard HA？

企业的核心业务系统通常依赖数据库持续提供服务。

即使已经部署了数据库主从复制，仍然存在几个无法仅靠复制解决的问题：

- 主数据库发生故障后，由谁判断是否需要切换？
- 多个备库中，应该选择哪一个接管业务？
- 如何确保原主库不会重新上线造成脑裂？
- 数据库主备切换后，业务访问入口如何同步迁移？
- 主库恢复之后，如何重新加入集群？
- 如何确认切换真正成功，并追溯整个故障处理过程？

**数据库复制解决数据同步问题，而 ClusterGuard HA 解决数据库故障后的安全接管与高可用管理问题。**

## ClusterGuard HA 能做什么？

**统一管理数据库集群**

持续发现和监测 MySQL、PostgreSQL 的节点状态、主从拓扑、复制关系、复制延迟和集群健康。

**安全执行主备切换**

通过 Raft 多数派仲裁、复制安全检查、候选节点评估和操作门禁，在满足条件后执行计划切换或启用后的自动故障切换。

**自动迁移数据库写入口**

将数据库角色变更与业务写入口迁移纳入同一受控流程，支持 Linux VIP、Docker Swarm 宿主机 VIP，以及已实现适配的 Kubernetes 写入口机制。

**降低脑裂及错误接管风险**

通过节点隔离、写者租约、端点租约与安全校验，防止旧主和新主同时成为合法写入节点。当无法证明切换安全时，平台拒绝执行高风险操作。

**管理故障后的恢复流程**

提供旧主回挂、节点重建、复制修复、操作验证与结果报告，减少人工操作风险。

**统一操作和审计**

通过 Web 控制台、CLI 和 HTTP API 管理高可用操作，并对关键变更记录执行阶段、验证证据与审计信息。

## ClusterGuard HA 的核心理念

**故障可以发生，但错误的故障接管不应该成为第二次事故。**

ClusterGuard HA 将数据库高可用从分散的脚本和人工操作，升级为具备集群仲裁、安全门禁、受控执行和结果验证的统一管理体系。

它不是数据库代理，也不是数据库备份软件。应用流量直接访问数据库写入口，ClusterGuard HA 负责管理写入口的所有权以及数据库角色转换。

## 适用场景

适用于企业核心业务数据库、需要自动或受控故障接管的主从集群、生产环境计划维护、数据库主备迁移，以及需要明确操作权限和故障审计的运维场景。

当前主要支持 MySQL 与 PostgreSQL，Oracle Data Guard 和 SQL Server Always On 属于后续受控扩展方向。

**ClusterGuard HA —— 让每一次数据库故障接管，都有依据、有控制、有验证。**

## 控制台预览

高可用操作工作台把集群上下文、主库与候选节点、VIP 状态、受控执行、旧主恢复、拓扑和审计证据组织在同一套运维流程中。

![ClusterGuard HA 高可用操作工作台](docs/assets/screenshots/ha-operation-workbench.png)

拓扑页按不可变资源身份组织，直接回答四个运维问题：当前被观测到的主库是哪一个、复制方向如何、VIP 由谁持有、哪些节点不可达或已降级。

![ClusterGuard HA 集群拓扑](docs/assets/screenshots/cluster-topology.png)

节点生命周期和操作日志页面见中英文[产品导览](docs/zh-CN/product-tour.md)。

## 与其他高可用方案的区别

它和 MySQL 的 MHA / Orchestrator、PostgreSQL 的 Patroni / repmgr 属于同一类工具：一个常驻运行的组件，保证始终只有一个主库可写、写入口跟着主库走。

| | MHA / Orchestrator / Patroni / repmgr | ClusterGuard HA |
| --- | --- | --- |
| 引擎覆盖 | 一个工具对应一种数据库 | 同一套控制平面覆盖 MySQL 与 PostgreSQL |
| 判定依据 | 脚本或进程状态；退出码 0 即视为"切过去了" | 每个动作都以**当轮证据**为门槛：当前拓扑、健康与复制关系 |
| 结果证明 | 日志供人翻看 | 每一步留下可核验的证据、审计和报告；未通过验证的变更不会显示为成功 |
| 身份标识 | hostname:port、主机名，或会变化的名称 | 不可变的平台 UUID 加引擎原生身份（`server_uuid`、`system_identifier`）；主机名、IP 和端口是可修正的坐标 |

## 架构与安全机制

控制平面由奇数个（三台）Raft 控制器组成：元数据在控制器间复制，每一项权威判定——写者租约、端点租约、操作锁——都需要当前多数派。数据库节点可以与控制器分离，也可以混布；节点生命周期任务会强制奇数控制器并核验 Raft 成员关系。

```text
运维 / 自动化
        |
        v
[ ClusterGuard HA 控制平面 ]
  控制台 | HTTP API | cgctl
  3 台 Raft 控制器；每项权威判定
  都需要当前多数派
        |
        | mTLS / HTTPS，仅限白名单动作
        v
[ 受限节点 Agent，每台主机一个 ]
        |
        v
[ 数据库节点：主库 + 备库 ]
[ 写入口：Linux VIP、Kubernetes Service、Swarm 宿主机 VIP ]

客户端 ------> 写入口（直连；流量不经过控制面）
```

客户端直连写入口，流量不经过 ClusterGuard。控制面负责迁移写入口的**所有者**，并在迁移后核验它确实发生了。

每个已实现的变更都走同一条工作流，未通过验证的变更不会显示为成功：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

支撑这条工作流的安全机制：

- **先证据、后动作。** 真正的角色或写入口变更，只有在集群具备完整 HA 端点清单、奇数 Raft 控制器、当前 Leader 支持的多数派权威、专用数据库凭据、Agent 签名材料、操作锁和审批时才可执行。缺少或未知的证据会阻断操作。
- **宁可暂时不可用，也不要第二个写者。** 平台宁可出现一段不可用，也不要出现第二个写者或第二个写入口所有者。过期授权即失效，旧节点 fail-closed 到无写入口并持久只读，Leader 在提升前会再次校验本次转换租约的确切身份。
- **数据路径保持直连。** 应用自己连写入口；ClusterGuard 不代理、不缓冲、不路由数据库流量，也从不直接编辑数据库数据文件。
- **自动故障切换是显式开启的。** 开启后它要求连续 4 次、跨度不少于 3 秒的当前故障观测、排名第一的合格候选、当前控制器多数派、旧主已隔离，以及独占写入口租约。外部 BMC、PDU、云平台或虚拟化隔离器仍是可选的更强一层。3 秒是控制器确认稳定故障的**证据窗口，不是端到端 RTO 承诺**；受限 Agent 另有 15 秒授权失效隔离宽限，之后失联旧主不能再持有写角色或写入口。
- **高可用不等于零中断、零数据丢失的承诺。** 设计上明确选择安全优先：当无法证明旧主已隔离或候选安全时，平台会**拒绝**切换。这种"拒绝"是产品的安全特性，而不是"任何故障都能无条件自动恢复"的宣传。
- **数据库、数据与备份仍然归你。** 平台不接管数据库软件、数据目录、复制关系或备份策略，升级平面同样不碰它们。
- **身份不可变，坐标可变。** 改主机名、改 IP、改端口只更新端点，平台实例身份不变。
- **不捆绑数据库。** 完整安装介质不自带 MySQL / PostgreSQL 二进制，数据库介质由现场自备。

## 支持矩阵与版本

| 引擎 | 状态 | 范围 |
| --- | --- | --- |
| MySQL | 已支持 | 已封板的 2.1 系列就是 MySQL HA 产品线，MySQL HA 在 2.2 及后续版本继续保留。发现覆盖 MySQL 5.7、8.0、8.4、9.x；经测试的 MySQL 5.7/8.x/9.x 变更语法运行在独立适配器与写入口契约之后。9.x 方言分支只有针对 9.7.0 的单元测试，尚无 9.x 现场验收记录。Kubernetes 写入口接管当前仅限 MySQL。 |
| PostgreSQL | 2.2 起交付 | PostgreSQL 16.4：清单内发现、`system_identifier` 集群绑定、主备拓扑、流复制健康、时间线与 WAL 证据、确定性候选评估、受保护的切换与故障切换、`pg_rewind` 旧主恢复、白名单低风险修复，以及 `pg_basebackup`/`pg_rewind` 节点生命周期。 |
| Oracle | 受控未来线 | Data Guard Broker 的发现、健康、拓扑、备选候选评估、预检查、计划、执行，以及受控切换的双节点验证。使用专用密码文件 `SYSDG` 账户而不是 `SYS`；本地 Agent 以 Oracle 操作系统账户运行 DGMGRL，且只接受配置的 Broker 成员。在配置旧主隔离之前，故障转移保持阻断。代码可以存在于能力门之后，但不构成生产支持声明。 |
| SQL Server | 受控未来线 | Always On 只读发现、健康、拓扑、同步次级候选评估、预检查、计划、执行、验证，以及用于"计划故障转移到同步提交次级"的发送/重做队列指标。只有 `sqlcmd` 可用或注入了 SQL Server 执行器时才对外标记发现与执行；默认阻断强制故障转移，除非配置了显式的数据丢失审批策略。 |

运行形态：物理机或虚拟机（systemd 托管数据库）、Docker Swarm（固定单副本 Service）、Kubernetes（独立单副本 StatefulSet）。只有当受限 Agent、操作凭据、端点提供器、控制器多数派和所需隔离证据全部配置完成时，相关变更能力才会对外标记为可用；这些能力不会回溯添加到 `v2.1.45`。

### 当前完整安装介质：3.1.3.3（本地候选）

完整 Linux x86_64 安装介质位于本地 `release/3.1.3.3/`，产品版本 **3.1.3.3**，RPM 兼容基线 **2.2-111**。数据库介质由现场自备。见[本版说明](docs/zh-CN/release-3.1.3.3.md)与[完整安装命令参数](docs/zh-CN/offline-installer-parameters.md)。源码与文档提交 GitHub；本版尚未发布 GitHub Release，ART/FIELD 仍 OPEN。

### 已记录的正式发布版本

发布策略和支持边界：

- `v2.1.45` 是 2.1 系列的不可变最终发布版本。
- 支持的 2.1 数据库引擎是 MySQL，包括通过站点验收矩阵验证的已审批兼容
  MySQL 发行版。
- `v2.2.39` 是 2.2 系列首个正式版本，交付 PostgreSQL 16.4、
  Docker Swarm 接管和 Kubernetes 写入口基础能力。
- 任何已发布的功能或行为更改都会增加包的发布版本；一个
  已发布的 RPM、离线包、标签或 GitHub Release 都不会被覆盖。
- **GitHub Release 只承载完整离线安装介质。** 签名 `.cgupgrade` 升级包（含旧
  `.cgpatch`）只对签约企业客户交付，**绝不**上传 GitHub 或任何其他公开渠道；
  公开渠道出现副本即视为越权分发，一经发现立即删除。见
  [发版规范 §3.1](docs/zh-CN/version-release-policy.md)。
- Oracle 和 SQL Server 代码可能存在于能力门后，但不是
  已封板 2.1 系列的生产支持声明。

| 系列 | 状态 | 生产支持边界 |
| --- | --- | --- |
| `2.1.45` | 稳定封板 | MySQL HA 控制平面 |
| `2.2.39` | 正式发布 | PostgreSQL 16.4、Docker Swarm，以及保留的 MySQL HA 能力 |
| 后续系列 | 路线图 | Oracle Data Guard Broker 和 SQL Server Always On 在单独认证后 |

从 [ClusterGuard HA 2.2.39](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39) 下载已记录的正式 `2.2.39` 版本：

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.39/clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.2.39/clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz.sha256
```

2.1 MySQL 产品线的封板版本仍可从
[ClusterGuard HA 2.1-45](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.1.45) 下载：

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.1.45/clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v2.1.45/clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
```

已发布的 SHA-256 值：

```text
d4a46bdfa4c95bb641a7d19f063d2f43219177658b01b914a31a1cd5d06ec590  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
1bc70109b556e973744bb05b5be0f53b075260910ae5def25518631ff8629a1c  clusterguard-ha-2.1-45.x86_64.rpm
```

> **认证状态：** 自动测试和实验室验收不能
> 替代现场认证。在生产使用前，验证确切的 MySQL
> 包、操作系统、存储、网络、隔离策略、VIP 提供商和
> 三控制器部署。PostgreSQL 验收属于 2.2 系列
> 并在
> [PostgreSQL 高可用](docs/zh-CN/postgresql-ha.md)中单独记录。

## 快速开始

ClusterGuard HA 需要 Go 1.22 或更新版本。

创建一个仅具有文档中只读查询所需权限的专用 MySQL 账户。将秘密保存在环境变量中，而不是 JSON 配置中。

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
export CG_POSTGRESQL_DISCOVERY_PASSWORD='replace-with-the-pg-monitor-secret'
export CG_POSTGRESQL_OPERATION_PASSWORD='replace-with-the-pg-operation-secret'
export CG_POSTGRESQL_REPLICATION_PASSWORD='replace-with-the-pg-replication-secret'
export CG_ORACLE_DISCOVERY_PASSWORD='replace-with-the-dgbroker-monitor-secret'
export CG_ORACLE_OPERATION_PASSWORD='replace-with-the-dgbroker-operation-secret'
export CG_SQLSERVER_DISCOVERY_PASSWORD='replace-with-the-ag-monitor-secret'
export CG_SQLSERVER_OPERATION_PASSWORD='replace-with-the-ag-operation-secret'
# Optional, only on controllers that execute these engines:
# export PATH="/opt/oracle/product/bin:/opt/mssql-tools18/bin:$PATH"
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

从源配置直接启动时，默认情况下控制台和 API 由 `http://127.0.0.1:8088/` 提供。支持的 RPM/离线
部署在端口 `3000` 上提供 HTTPS。

浏览器控制台使用存放在复制元数据快照中的平台用户认证。全新安装会创建 `admin`，并带
`MustChangePassword=true`。除非部署设置了 `bootstrap_admin_password_env`，Leader 会生成首次登录
口令并写入与元数据文件同目录、root 专属的 `bootstrap-admin-password` 文件（权限 0600）；在它出现的
控制节点上读取。首次改密成功后平台会删除该文件。首次改密是强制的：在成功之前，所有集群与元数据
API 读取以及一切变更都会被阻断。口令只以 Argon2id 哈希保存，绝不写入配置文件、环境文件、审计事件
或报告。会话采用 8 小时绝对有效期，使用 HttpOnly SameSite Cookie 加 CSRF 校验，并在改密或注销时
失效。

这份示例配置保持受限节点代理和自动故障转移为禁用状态，因此在配置之前它只是一个只读控制平面。

服务管理器应使用 `/healthz` 进行活性检查，使用 `/readyz` 进行
失败时阻断的控制平面就绪性检查。已认证的操作员可以使用
`cgctl status` 或 `/api/v1/control-plane/status` 进行 Raft 角色、Leader、多数、
元数据修订、活动工作和运行时间诊断。每个 API 响应都有一个 `X-Request-ID`，当跟随者将变更转发到
当前 Leader 时，该值会被保留。

生产二进制文件是 `clusterguard`；CLI 是 `cgctl`。该发行版
使用 `/etc/clusterguard/`、`/var/lib/clusterguard/` 和
`/var/log/clusterguard/`。systemd 单元是
`packaging/systemd/clusterguard-ha.service`。

`scripts/build-clusterguard-bundle.sh` 生成控制器、CLI、受限
代理、生命周期助手、systemd 单元、日志轮转、配置示例，
以及完整的 SHA-256 清单。`scripts/clusterguard-install.sh` 是每个节点的
生命周期助手；它不是初始多节点生产引导工具。

使用 `scripts/install_clusterguard.sh` 和完整离线包完成首次生产部署。
安装流程会在同一条可审计工作流中建立控制平面、固定资源身份、证书、
数据库拓扑、Agent 配置和 VIP 收敛策略。见
[中文离线安装](docs/zh-CN/offline-rpm-install.md) / [English offline installation](docs/en-US/offline-rpm-install.md)。

### 注册和刷新 MySQL 集群

注册会建立权威端点清单。主机名、IP 和
端口是坐标，而不是资源标识符。

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-mysql",
    "engine":"mysql",
    "endpoints":[
      {"hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},
      {"hostname":"mysql-b","ip_address":"192.0.2.11","port":3306},
      {"hostname":"mysql-c","ip_address":"192.0.2.12","port":3306}
    ]
  }'
```

使用返回的平台集群 UUID 刷新已注册清单。
正文正好是一个空的 JSON 对象；凭据和端点覆盖被拒绝，因为凭据在服务器上解析。

```bash
curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

### 注册和刷新 PostgreSQL 集群

每个 PostgreSQL 实例必须具有唯一的不可变
`clusterguard.node_id`。每个备库节点还需在 `clusterguard.primary_node_id` 中声明当前主节点的节点
UUID。ClusterGuard 将集群绑定到
`pg_control_system().system_identifier`；更改主机名、IP 或端口会更新
端点并保留相同的平台实例 UUID。

启用 `postgresql` 配置块，设置专用发现、
操作和复制凭据环境，并仅注册权威端点：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-postgresql",
    "engine":"postgresql",
    "endpoints":[
      {"hostname":"pg-a","ip_address":"192.0.2.20","port":5432},
      {"hostname":"pg-b","ip_address":"192.0.2.21","port":5432}
    ]
  }'

curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

控制台提供 PostgreSQL 拓扑、健康、原生指标、候选证据、受控操作、节点同步、审计和报告。对应的运行时能力与安全证据未就绪前，变更操作保持禁用；控制台不会显示模拟成功。数据库授权、受限 Agent 策略、生命周期、隔离和生产验收要求见[PostgreSQL 高可用](docs/zh-CN/postgresql-ha.md)。

### CLI

```bash
go run ./cmd/cgctl engines
go run ./cmd/cgctl clusters
go run ./cmd/cgctl topology <cluster-uuid>
go run ./cmd/cgctl health <cluster-uuid>
go run ./cmd/cgctl candidates <cluster-uuid>
go run ./cmd/cgctl metrics <cluster-uuid>
go run ./cmd/cgctl refresh <cluster-uuid>
go run ./cmd/cgctl operation <operation-uuid>
go run ./cmd/cgctl approval issue --cluster <cluster-uuid> --engine mysql \
  --kind switchover --target <instance-uuid> --issued-by <administrator> --ttl 5m
go run ./cmd/cgctl approval list
go run ./cmd/cgctl approval show <grant-uuid>
```

`cgctl` 使用来自 `CG_CONTROL_TOKEN` 的控制令牌进行认证读取
和写入。在命令前使用
`--token-env <name>` 选择其他环境变量；密钥不接受命令行明文参数。审批签发同样使用此管理员凭据。返回的审批令牌只打印一次，且 `cgctl` 不会持久化。

如果管理员密码丢失，平台没有在线绕过或重置 API。备份元数据并暂停变更后，使用 `clusterguard admin prepare-recovery` 创建私有的一次性恢复文件，将同一文件以所有者 `clusterguard:clusterguard`、权限 `0600` 分发到所有控制器，并
逐一重启。Raft Leader 只应用一次恢复 ID，撤销现有会话，强制修改密码，记录审计，并在每个节点删除恢复文件。详细流程见[操作说明](docs/zh-CN/operations.md)。活动 Raft 集群中禁止直接编辑密码哈希或创建共享默认密码。

HA 矩阵支持浏览器等效的会话执行：

```bash
export CG_PLATFORM_USERNAME='admin'
export CG_PLATFORM_PASSWORD='<current-platform-password>'
scripts/clusterguard-ha-matrix.sh \
  --api http://127.0.0.1:8088 \
  --clusters '<cluster-uuid>' \
  --platform-session
```

在新的元数据存储中，设置 `CG_PLATFORM_NEW_PASSWORD`。省略
`--platform-session` 会保持服务自动化测试的显式一次性授权模式。

将全局标志放在命令之前：

```bash
go run ./cmd/cgctl --json topology <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 clusters
```

完整 API 工作流和操作员流程见[操作说明](docs/zh-CN/operations.md)。资源身份、适配器、持久化、候选评估和安全设计见[架构说明](docs/zh-CN/architecture.md)。

## 文档

[中英文文档中心](docs/README.md)列出了每份英文文档及其简体中文对应版本。
先读适用规则，再读场景手册；发布与验收记录用于追溯：

- [阅读顺序与适用规则](docs/development/rules/README.md) / [开发模块](docs/development/README.md)
- [升级与热修强制契约](docs/zh-CN/upgrade-validation-chain.md) / [门禁执行流程](docs/zh-CN/validation-gate-workflow.md)
- [English documentation](docs/en-US/README.md) / [中文文档](docs/zh-CN/README.md)
- [English product tour](docs/en-US/product-tour.md) / [中文产品导览](docs/zh-CN/product-tour.md)
- [English offline installation](docs/en-US/offline-rpm-install.md) / [中文离线安装](docs/zh-CN/offline-rpm-install.md)
- [English operations manual](docs/en-US/operations-manual.md) / [中文运维手册](docs/zh-CN/operations-manual.md)
- [English update guide](docs/en-US/update-and-patch.md) / [中文版本升级与回退手册](docs/zh-CN/update-and-patch.md)
- [English release policy](docs/en-US/version-release-policy.md) / [中文版本规范](docs/zh-CN/version-release-policy.md)

## 许可

ClusterGuard HA 采用 **GNU Affero 通用公共许可证第 3 版（仅此版本，`AGPL-3.0-only`）** 授权，
全文见 [LICENSE](LICENSE)。

一句话：你可以自由使用、修改、收费分发；但一旦你把修改后的版本对外提供——**包括只作为网络
服务提供**——就必须按同一许可开放对应源码。原封不动地自用不产生对外开源义务。签约客户同样
按此条款取得软件；"升级包不进公开渠道"是交付策略，不是许可限制。

链接进二进制的 MPL-2.0 模块、以及离线介质中捆绑的第三方安装包，其许可与来源逐项列在
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)。各类场景下的具体义务见
[许可与合规](docs/zh-CN/licensing.md) / [Licensing and Compliance](docs/en-US/licensing.md)。

任何依赖或打包改动后，校验 `LICENSE`、`go.mod`、RPM 元数据与打包脚本仍然一致：

```bash
node tools/verify-license-consistency.cjs
```

## 维护者须知

### 文档与门禁流程

修改前先读[根规则](AGENTS.md)、[门禁执行与更新流程](docs/zh-CN/validation-gate-workflow.md)和[阅读顺序与适用规则](docs/development/rules/README.md)，再选[开发模块](docs/development/README.md)。涉及升级契约范围的修改或动作前，完整读取[强制契约 v2](docs/zh-CN/upgrade-validation-chain.md)。安装与运维先满足适用规则，再使用对应版本的场景手册。任何 Markdown 变更都必须在 [Markdown 变更记录](docs/development/markdown-change-record.md) 登记，并重建入库的生成页。

### v2 源码修复与验收记录

记录中的源码修复基线为 `7b643f4`（2026-10-03），唯一主线为 `codex/2.2-postgresql`。该次修复的升级与热修执行 v2 契约：热修失败使用独立 `retry`，滚动升级失败使用 `resume`。该次修复的新包与现场 ART/FIELD 验收记录仍为 **OPEN**，未产生新包或生产部署。见[实现与验收记录](docs/zh-CN/upgrade-validation-chain-implementation-status.md)；该记录不替代本次源码、制品和现场核验。
