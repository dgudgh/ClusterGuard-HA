# ClusterGuard HA 接管 Docker Swarm MySQL

## 1. 交付范围

本阶段支持 ClusterGuard HA 接管运行在 Docker Swarm 上的 MySQL 8.0 主从集群，并使用宿主机 VIP 对外提供稳定地址。数据库容器由 Swarm 管理，ClusterGuard 控制面和受限 Agent 继续运行在宿主机。

本模式不把 VIP 放进容器，也不允许数据库 Adapter 直接调用 Docker API。Kubernetes MySQL 已由独立的 Service/EndpointSlice Provider 接管，部署方式见 `kubernetes-mysql.md`，不能与本页的宿主机 VIP 模式混用。

## 2. 统一控制面与集群选择

主机、Docker Swarm 和 Kubernetes 是数据库集群的运行时属性，不是拆分控制面的依据。一个站点只部署一套奇数节点 ClusterGuard Raft 控制面，所有数据库集群共用同一组 HTTPS API/控制台端口，默认是 `3000`。控制台按集群资源展示独立条目，例如 `pg16-ha` 与 `swarm-mysql-8.0`，运维人员在顶部集群选择器中切换。

同一宿主机上的 `clusterguard-agent` 也只运行一份。`/etc/clusterguard/agent.json` 的 `clusters` 数组可以同时包含宿主机 PostgreSQL、宿主机 MySQL、Docker MySQL 和 Docker PostgreSQL 策略；每条策略用不同的 `cluster_id`、`instance_id` 和 `runtime_kind` 区分。数据库端口可以相同，不能据此推断运行时或集群身份。

`3100` 等第二控制端口只允许用于有明确销毁计划的隔离破坏性实验。验收结束后必须把已验证集群登记到统一控制面、迁移 Agent 策略和所有权租约，并停用临时 Raft、Agent timer 与监听端口。生产交付不得长期运行两套控制面管理同一站点。

## 3. 主机层与容器层的边界

ClusterGuard 所说的“主机层”包括物理机和虚拟机。两者都由宿主操作系统直接运行数据库进程。“容器层”表示数据库进程由 Docker Swarm Service 管理，但 Agent、VIP 和隔离证据仍位于宿主机。两种模式不能只看数据库 IP 或端口判断，必须由 RuntimeTarget 和 WorkloadBinding 明确登记。

| 边界 | 主机层（物理机/虚拟机） | 容器层（Docker Swarm） | Kubernetes |
| --- | --- | --- | --- |
| 数据库进程 | 宿主机上的 systemd 服务或受控进程 | 固定 Swarm Service 的 task/container | StatefulSet Pod |
| 稳定数据库身份 | `resource_id` + MySQL `server_uuid` | 同左，容器重建不能改变数据库身份 | 同左，Pod UID 不是数据库身份 |
| 可变运行位置 | 主机名、IP、进程 PID | 宿主节点、task ID、container ID | Node、Pod UID |
| 数据持久化 | 宿主机数据目录或块存储 | 明确登记的持久卷，禁止容器可写层 | PVC UID |
| 生命周期管理 | systemd/主机命令 | Docker Swarm Service | Kubernetes 控制器 |
| ClusterGuard Agent | 安装在数据库主机 | 安装在容器宿主机，不进入数据库容器 | 控制面通过受限 Kubernetes API 和 Pod 启动守卫执行 |
| 业务入口 | 宿主机网卡上的 VIP | 仍是当前主库宿主机网卡上的 VIP | Service/EndpointSlice，不使用宿主机 VIP |
| 角色与隔离 | 修改本机数据库角色并管理 VIP | 写宿主机 fence 文件，再修改固定 Service 内的数据库角色 | StatefulSet 持久角色、scale 0 隔离和 EndpointSlice 切换 |
| 当前支持状态 | 支持 | MySQL 8.0 Docker Swarm 接管 | MySQL 首批开放，约束见 Kubernetes 文档 |

容器层不等于把 VIP 绑进容器。业务始终连接 `VIP:数据库端口`，VIP 在宿主机之间迁移；Swarm 只负责容器期望状态。ClusterGuard 控制面继续作为独立宿主机服务运行，数据库容器停止不应导致管理面同时消失。

## 4. 资源与身份

平台同时保存三层身份：

| 层级 | 稳定身份 | 可变属性 |
| --- | --- | --- |
| 数据库实例 | `resource_id` + MySQL `server_uuid` | hostname、IP、port、角色 |
| 运行时目标 | `RuntimeTarget.resource_id` | Docker endpoint、标签、凭据引用 |
| 工作负载 | `WorkloadBinding.resource_id` | container ID、task ID、观测时间 |

容器删除并重建后，`container_id` 可以变化，数据库 `resource_id` 和 `server_uuid` 不得变化。相同实例只能有一个活动 WorkloadBinding。

## 5. 高可用边界

1. Swarm 负责容器进程重启和固定节点调度。
2. MySQL 原生 GTID 复制负责数据同步。
3. ClusterGuard 负责候选评估、切换流程、旧主隔离、VIP、审计和验证。
4. Raft 多数派决定谁有权执行切换和签发 VIP 所有权租约。
5. 宿主机 Agent 通过本机 Docker socket 管理固定 Service，不接受 API 传入的容器 ID 或任意命令。

数据库 Service 使用 `mode: host` 发布 3306。VIP 始终绑定到当前主库所在宿主机网卡，因此业务地址在切换前后保持不变。

## 6. 双主防护

每个 MySQL 节点在宿主机保存 `/etc/clusterguard/docker/mysql-fence.cnf`。整个 `/etc/clusterguard/docker` 目录只读挂载到容器的 `/etc/mysql/conf.d`，确保 Agent 原子替换角色文件后容器看到的是新文件，而不是文件级 bind mount 固定的旧 inode。旧主隔离按以下顺序执行：

1. 先把隔离文件原子写为 `read_only=ON` 和 `super_read_only=ON`。
2. 再写入 Agent 本地持久角色记录并执行 MySQL `SET PERSIST_ONLY`。
3. 最后设置当前进程的全局只读状态并摘除 VIP。

即使 Swarm 在隔离后重新创建容器，新任务也会先读取宿主机隔离文件，以只读方式启动。Docker daemon 不可达、匹配到多个容器、容器标签不一致或运行中的数据库无法确认角色时，故障切换全部 fail-closed。

## 7. Swarm 约束

- 生产至少 3 个奇数 Manager，Manager 仲裁与 ClusterGuard Raft 仲裁相互独立。
- 每个数据库 Service 固定到一个带 `clusterguard.mysql.slot` 标签的节点。
- 数据目录使用本地持久卷或经过验证的块存储，不使用容器可写层。
- Agent 运行在宿主机，仅允许 root 或受限 sudo 账号访问 Docker socket。
- 计划停库通过 `docker service scale SERVICE=0` 修改期望状态，禁止使用 `docker stop`。
- 数据库故障不等同于宿主机故障。3306 探测失败可触发数据库恢复，整机关机仍走单独的电源工作流。

## 8. 部署顺序

仓库交付文件位于 `deploy/docker-swarm/`。

1. 把官方 Docker x86_64 静态包和 MySQL 8.0.44 镜像 tar 复制到离线环境。
2. 三台宿主机运行 `install-docker-static.sh`。
3. 在 01 初始化 Swarm，把 02、03 以 Manager 身份加入。
4. 每台宿主机运行 `mysql/prepare-host.sh SLOT NODE_IP`。
5. 每个控制节点运行 `mysql/install-mysql-client.sh MYSQL_TAR`，安装独立客户端。控制面不得借用本机数据库容器中的 `mysql`，否则本机容器停止时会同时失去探测和恢复能力。
6. 在 Manager 创建 `cg_mysql_root_password` 和 `cg_mysql_operator_cnf` 两个 Swarm secret。
7. 导出集群 UUID 和三个临时实例 UUID，执行 `docker stack deploy -c mysql-stack.yml cgmysql`。
8. 先在 01 执行 `bootstrap-replication.sh 01`，再分别在 02、03 执行 `bootstrap-replication.sh 02|03`，建立账号、GTID 复制和验收数据。该脚本只操作所在宿主机的本地任务，不会把远程容器误当成本地容器。执行前必须在对应宿主机导出 `CG_MYSQL_ROOT_PASSWORD`、`CG_MYSQL_DISCOVERY_PASSWORD`、`CG_MYSQL_OPERATION_PASSWORD`、`CG_MYSQL_REPLICATION_PASSWORD` 四个变量（任一缺失即直接退出，复制口令不得超过 32 字符）；`CG_MYSQL_SOURCE_HOST` 默认 `192.168.102.152`，不在该网段时必须显式覆盖。
9. 在站点现有的统一 ClusterGuard 控制面登记一个新的 Docker MySQL 集群、三个宿主机 endpoint 并执行发现；不要新建第二套 API/Raft 端口。
10. 使用发现后的真实实例 UUID 重新部署 Service 标签，把 Docker 策略合并进三台现有 `/etc/clusterguard/agent.json` 的 `clusters` 数组。
11. 创建 RuntimeTarget、WorkloadBinding 和 HAEndpoint，运行预检查后启用自动故障切换。MySQL 自动故障切换还要求可用的隔离证据，否则控制面会拒绝启动：设置 `"fencing": { "agent_quorum_enabled": true, "agent_quorum_grace_seconds": 15 }`（取值 15–60 秒），或配置外部 fencer，或启用 `kubernetes`。

## 9. 验收计划

| 场景 | 预期结果 |
| --- | --- |
| 计划切换 01→02→03→01 | 主库、复制源和 VIP 同步收敛，业务地址不变 |
| 停止主库容器 | 旧主持久隔离，候选提升，VIP 迁移（实验室实测端到端约 21 秒、其中工作流执行及验证约 12.2 秒；30 秒是目标值，非产品承诺时限） |
| 重启旧主 Service | 容器先以只读状态启动，再自动挂回复制 |
| Docker daemon 停止 | 不伪造容器状态；未满足隔离证据时阻断切换 |
| 两个容器标签指向同一实例 | Agent 报身份冲突并阻断变更 |
| 失去 ClusterGuard Raft 多数派 | 禁止主库切换和 VIP 获取 |
| 失去 Swarm Manager 多数派 | 禁止 Service 扩缩；现有数据库与 VIP 不被误改 |

本次 `192.168.102.152-154` 实验环境的可执行步骤、通过标准和证据清单见
[`docker-swarm-mysql-validation-plan.md`](docker-swarm-mysql-validation-plan.md)。

## 10. Kubernetes 独立模式

Kubernetes 使用同一 `DatabaseInstance`，工作负载绑定改为 StatefulSet ordinal、Pod UID、Node 名和 PVC UID。ClusterGuard 只在 Raft 授权后修改 selectorless Service 的 EndpointSlice；外部地址可由云 LoadBalancer 或 MetalLB 提供。Pod 标签、Service selector 或普通 Operator 不得自行决定写端点所有权。完整部署见 [`kubernetes-mysql.md`](kubernetes-mysql.md)。
