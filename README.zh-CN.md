# ClusterGuard HA

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](README.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

ClusterGuard HA 需要 Go 1.22 或更新版本。

**ClusterGuard HA 多数据库企业级高可用控制平台**

**ClusterGuard HA — Multi-Database High Availability Control Plane**

ClusterGuard HA 是一个独立、洁净室的高可用控制平面。
稳定版 2.1 系列是 MySQL HA 产品线。PostgreSQL 的交付从
2.2 开始。Oracle Data Guard Broker 和 SQL Server Always On 仍分别作为
未来产品线，且不属于 2.1 支持范围。

## 控制台预览

![ClusterGuard HA 高可用操作工作台](docs/assets/screenshots/ha-operation-workbench.png)

控制台将集群上下文、主库与候选节点、VIP 状态、受控执行、旧主恢复、
拓扑和审计证据组织在同一套运维流程中。拓扑、节点生命周期和操作日志等
页面见中英文[产品导览](docs/zh-CN/product-tour.md)。

## 当前发布版本

发布策略和支持边界：

- `v2.1.45` 是 2.1 系列的不可变最终发布版本。
- 支持的 2.1 数据库引擎是 MySQL，包括通过站点验收矩阵验证的已审批兼容
  MySQL 发行版。
- `v2.2.39` 是 2.2 系列首个正式版本，交付 PostgreSQL 16.4、
  Docker Swarm 接管和 Kubernetes 写入口基础能力。
- 任何已发布的功能或行为更改都会增加包的发布版本；一个
  已发布的 RPM、离线包、标签或 GitHub Release 都不会被覆盖。
- Oracle 和 SQL Server 代码可能存在于能力门后，但不是
  已封板 2.1 系列的生产支持声明。

| 系列 | 状态 | 生产支持边界 |
| --- | --- | --- |
| `2.1.45` | 稳定封板 | MySQL HA 控制平面 |
| `2.2.39` | 正式发布 | PostgreSQL 16.4、Docker Swarm，以及保留的 MySQL HA 能力 |
| 后续系列 | 路线图 | Oracle Data Guard Broker 和 SQL Server Always On 在单独认证后 |

从 [ClusterGuard HA 2.2.39](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.2.39) 下载当前正式版本：

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

当前的 MySQL 适配器提供：

- 面向 MySQL 5.7、8.0、8.4 和 9.x 的清单内发现（9.x 方言分支只有针对 9.7.0 的单元测试，尚无 9.x 现场验收记录）；
- 全局唯一不可变节点名称、平台 UUID 和原生 MySQL
  `server_uuid` 身份；
- 持久化的拓扑、健康、复制链接、探测证据和指标；
- 候选提升评估，包括 GTID 和复制安全检查；
- 通过主节点和 VIP 所有权耦合的受保护的三节点计划切换；
- 旧主重新加入和白名单内复制修复；
- 连续 3 次且跨越 3 秒的稳定故障检测和可选自动故障转移；
- 通过受限节点代理的 Raft 支持操作锁、端点租约、本地自我隔离和
  重启时的 VIP 收敛；
- 带固定节点插槽重用、分阶段安装、同步、验证、审计和报告输出的添加/重建生命周期任务；
- 不改变不可变资源身份的端点元数据修正；
- JSON、Prometheus 和监控安全的健康输出；
- 失败时阻断的存活/就绪检查、Raft 角色和多数派诊断、有界历史、分页操作日志和限速事件提醒；
- 带基于角色访问、强制引导密码更改、CSRF 保护和 `cgctl` 服务 CLI 的认证中文控制台；
- 持久操作 UUID、幂等键、阶段进度、审计和报告；
- 用于手动高风险数据库操作的计划绑定、单次使用审批授权（默认五分钟、上限十五分钟）；
- 在独立适配器和写入端点契约下验证的 MySQL 5.7/8.x/9.x 变更语法。

## 2.2 发布范围

2.2 的 PostgreSQL 适配器提供清单内发现、不可变节点身份、`system_identifier` 集群绑定、主/备拓扑、
流复制健康、时间线和 WAL 证据、原生指标、确定性候选评估、受保护的计划切换和故障切换、旧主
`pg_rewind` 恢复、允许列表低风险修复、写入器-VIP 耦合，以及
`pg_basebackup`/`pg_rewind` 节点生命周期，加上可选的 3 秒稳定故障自动故障转移。变更仅在
受限 Agent、操作凭据、端点提供器、控制器多数派和所需隔离证据全部配置完成时，相关能力才会对外标记为可用。这些功能不会
回溯添加到 `v2.1.45`。

Oracle 适配器是未来受控集成，用于签名代理 Data Guard Broker 发现、健康、
拓扑、备选候选评估、预检查、计划、执行和受控切换的双节点验证。使用专用密码文件
`SYSDG` 账户，而不是 `SYS`；本地代理以
Oracle 操作系统账户运行 DGMGRL，并且只接受配置的 Broker 成员。
ClusterGuard 从不直接编辑 Oracle 数据文件。在配置旧主隔离之前，故障转移仍被阻止。

SQL Server 适配器是未来受控集成，用于 Always On 只读发现、健康、拓扑、同步次级候选评估、
预检查、计划、执行、验证和计划故障转移到同步提交次级的发送/重做队列指标。
仅当 `sqlcmd` 可用或注入 SQL Server 执行器时才进行发现和执行。默认情况下，强制故障转移仍被阻止，除非配置了未来的显式数据丢失审批策略。

## 安全边界

示例配置保持受限节点代理和自动故障转移禁用。真正的 MySQL 角色或 VIP 变更只有在
集群具有完整的 HA 端点清单、奇数个 Raft 控制器、当前 Leader 支持的多数派权威、专用 MySQL 凭据、
代理签名材料、操作锁和审批时才可执行。缺少或未知的证据会阻止操作；它永远不会产生模拟成功。

自动故障切换需要显式启用。它要求连续观察到 3 次故障且时间跨度不少于 3 秒、存在排名第一且满足条件的候选节点、控制器保持多数派、旧主已隔离，并取得独占 VIP 租约。MySQL 可以使用短期 Raft 多数派 Agent 授权：授权过期即失效，旧节点会移除 VIP 并持久保持只读，Leader 在提升前再次验证本次转换租约。外部 BMC、PDU、云平台或虚拟化隔离器仍可作为更强的第二层隔离。平台宁可暂时不可用，也不允许出现第二个写节点或 VIP Owner。

3 秒是控制器确认稳定故障的证据窗口，不是端到端 RTO 承诺。受限 Agent 另有 15 秒授权失效隔离宽限，用于保证失联旧主不能继续持有写角色或 VIP。PostgreSQL 16.4 实验室验收在客户端连接超时为 2 秒时测得写入口中断 17.973 秒；每个生产现场仍需按自己的网络、存储、数据库包、客户端超时和隔离策略重新测试。

浏览器控制台会针对存储在复制元数据快照中的平台用户进行身份验证。全新安装会创建 `admin`，使用文档中指定的首次登录密码 `admin123` 和 `MustChangePassword=true`。首次密码更改是强制性的：直到成功，所有集群和元数据 API 读取和每个变更都会被阻止。默认值仅存储为 Argon2id 哈希；它永远不会写入配置、环境文件、审计事件或报告。会话具有八小时的绝对生命周期，使用 HttpOnly SameSite Cookie 加 CSRF 验证，并在密码更改或注销时被撤销。

对于已认证的管理员或操作员，服务端会生成确定的计划，并在内部签发和消费一次性授权；浏览器不会接触审批密钥。外部服务自动化继续使用显式审批 API：复制存储中只保存授权哈希，明文只返回一次，消费动作与工作流审批阶段原子提交。自动故障切换使用内部事件授权路径，不依赖可重复使用的人工令牌。

常见工作流程保持不变：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

所有已实现的变更都通过同一工作流。除非配置了原生 Broker/AG 命令执行器，并且平台具备当前身份、角色、健康、锁、审批和验证证据，否则 Oracle 和 SQL Server 执行路径保持阻断。

## 启动

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
部署在端口 `3000` 上提供 HTTPS。在新的元数据存储中，以
`admin` 和 `admin123` 登录。控制台立即要求新密码，并且
直到首次密码更改成功之前不会加载集群数据。

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
数据库拓扑、Agent 配置和 VIP 收敛策略。

## 文档

[中英文文档中心](docs/README.md)列出了每份英文文档及其简体中文对应版本。
推荐入口：

- [English documentation](docs/en-US/README.md) / [中文文档](docs/zh-CN/README.md)
- [English product tour](docs/en-US/product-tour.md) / [中文产品导览](docs/zh-CN/product-tour.md)
- [English offline installation](docs/en-US/offline-rpm-install.md) / [中文离线安装](docs/zh-CN/offline-rpm-install.md)
- [English operations manual](docs/en-US/operations-manual.md) / [中文运维手册](docs/zh-CN/operations-manual.md)
- [English update guide](docs/en-US/update-and-patch.md) / [中文版本升级与回退手册](docs/zh-CN/update-and-patch.md)
- [English release policy](docs/en-US/version-release-policy.md) / [中文版本规范](docs/zh-CN/version-release-policy.md)

## 注册和刷新 MySQL 集群

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

## 注册和刷新 PostgreSQL 集群

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

## CLI

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
