# Docker Swarm MySQL 验证计划

## 目标与边界

在 `192.168.102.152-154` 上建立三 Manager Docker Swarm，部署 MySQL 8.0.44 一主两从。破坏性测试阶段先用临时隔离控制面避免影响已运行的 PostgreSQL；功能验收后必须迁入站点统一 ClusterGuard HA 控制面，由同一 API、Raft 和 Agent 同时管理两个独立数据库集群。

| 资源 | 验证值 |
| --- | --- |
| Swarm Manager / MySQL 节点 | `192.168.102.152-154` |
| MySQL | `8.0.44:3306` |
| 业务 VIP | `192.168.102.156/24` on `ens160` |
| 临时破坏性测试控制台 | `https://192.168.102.152:3100/`，仅测试阶段 |
| 临时测试 Raft | `11009/tcp`，验收后停用 |
| 最终统一控制面 | `https://192.168.102.152-154:3000/`，Raft `10009/tcp` |
| 控制台集群条目 | `pg16-ha`、`swarm-mysql-8.0` |

## 实施阶段

1. 固定 Docker 静态介质和 `mysql:8.0.44` linux/amd64 镜像摘要，制作三节点一致离线介质。
2. 安装 Docker，校验时钟、防火墙、SELinux、overlay 网络与三 Manager quorum。
3. 为 01、02、03 创建固定 slot，部署三个独立 MySQL Service 和持久化数据目录。
4. 先初始化 01，再初始化 02、03，确认 GTID 自动定位、复制双 ON 和验收数据收敛。
5. 启动独立 ClusterGuard Raft 三节点，发现 MySQL，固化 `server_uuid -> resource_id -> Swarm Service` 映射。
6. 登记 RuntimeTarget、WorkloadBinding 和 Linux VIP HAEndpoint，启动宿主机 Agent 和所有权租约收敛。
7. 完成计划切换、故障切换、旧主恢复、VIP 唯一性、数据一致性、审计和报告验收。
8. 将 Docker MySQL 资源、运行时绑定、HA 端点和 Agent 策略迁入 `3000` 统一控制面，验证两个集群均健康后关闭 `3100/11009` 临时控制面。

## 通过标准

- 三个 Swarm Manager 均为 `Ready/Reachable`，任一 Manager 下线后仍可管理 Service。
- 任意时刻只有一个 MySQL 可写，只有当前主库宿主机持有 VIP。
- `01 -> 02 -> 03 -> 01` 计划切换全部通过，切换后两个副本重指向新主库。
- 当前主库 Service 缩容为 0 后，ClusterGuard 只在 Raft 多数派和隔离证据成立时提升候选，目标 30 秒内恢复 VIP。
- 旧主 Service 恢复到 1 时首先以 `read_only=ON` 和 `super_read_only=ON` 启动，完成数据补齐后挂回新主。
- 双 VIP、双主、标签重复、容器身份不一致、Agent 不可达、Raft 无多数派均必须 fail-closed。
- 每次操作必须生成 Operation、Execution、Verification、AuditEvent 和 Report，页面、API 与实际 MySQL 角色一致。

## 证据清单

保存 Swarm node/service/task 输出、MySQL `server_uuid`、GTID、复制状态、只读状态、VIP 所有者、每次切换耗时、旧主恢复过程、ClusterGuard 审计/报告以及所有失败注入的阻断原因。未保存上述证据的场景不计为通过。

## 2026-08-27 实机结果

本轮使用 MySQL 8.0.44、三个 Swarm Manager、三个 ClusterGuard Raft 投票节点和宿主机 Agent 完成验证。测试结束时 01 为唯一可写主库并持有 `192.168.102.156/24`，02、03 均为 `read_only=ON`、`super_read_only=ON` 且复制线程运行。

| 场景 | 实测结果 | 结论 |
| --- | --- | --- |
| 计划切换 `01 -> 02 -> 03 -> 01` | 三次均约 8 秒，主库、两个复制源和 VIP 同步收敛 | 通过 |
| 计划重启当前主库 Service | 恢复冻结期间 VIP 不误漂移；任务以持久角色文件启动，稳定租约恢复后可写角色自动收敛 | 通过 |
| 突发停止 02 主库 Service | 21 秒内完成 02 到 01 的端到端恢复；工作流执行及验证约 12.2 秒 | 通过 |
| 故障前数据一致性 | 故障前标记 `abrupt-failover-1787762799` 在新主和两个回挂副本均存在 | 通过 |
| 02 旧主恢复 | 内置 `former_primary_rejoin` 在 3 秒内完成，4 项持久化验证全部通过 | 通过 |
| 成功操作重复验证 | `/operations/{id}/verify` 幂等返回原验证证据，不再使用旧计划误判新拓扑 | 通过 |
| 单个 Docker Manager 停止 | 03 不可达时 01 主库和 VIP 保持不变；Docker 恢复后 03 自动恢复为只读副本 | 通过 |
| ClusterGuard Raft 丢失多数派 | 切换计划返回 HTTP 503，未生成或执行数据库变更 | 通过 |
| 未授权重复 VIP | 在 153 人工添加 VIP 后，宿主机 Agent 在 3 秒内摘除，152 的合法 VIP 保持 | 通过 |

关键操作证据包括自动故障切换 `eb8cd08d-11ad-4cc5-9326-069ad2aa2a52` 和旧主回挂 `63c026dc-fc6a-49bd-b6f9-3fffa26aa68b`。全仓 `go test ./... -count=1` 与 `go vet ./...` 均通过。

本轮证明当前实现满足 Docker Swarm MySQL 第一阶段功能验收，不替代生产环境的长时间压测。整机断电、跨交换机网络分区、存储只读/满盘、连续百次切换和 MySQL 8.4 仍需在目标生产等价环境单独执行。

## 统一控制面迁移验收

同日完成从临时 `3100` 控制面到正式 `3000` 控制面的迁移。统一 `/api/v1/clusters` 同时返回健康的 `pg16-ha` 和 `swarm-mysql-8.0`；PostgreSQL VIP `192.168.102.155` 位于其当前主库宿主机，Docker MySQL VIP `192.168.102.156` 位于其当前主库宿主机。三台宿主机均由一份 Agent 配置同时处理两个集群。

迁移后执行 Docker MySQL `01 -> 02 -> 01` 受控切换回归，主库角色、两个副本、VIP 和切换前验收数据均收敛。临时 `clusterguard-swarm-lab.service` 与专用 Agent timer 已停用，三台主机 `3100` 均无监听，`3000` 均返回控制台。

迁移中发现并修复两项交付缺口。其一，容器标签中的实例 UUID 必须先更新为统一控制面的资源 UUID，否则 Agent 会因身份不匹配拒绝操作。其二，Agent systemd 单元采用 `ProtectSystem=strict` 时必须显式允许写入 `/etc/clusterguard/docker`，否则无法原子更新重启隔离文件。现在配置校验和回归测试共同锁定这两个运行契约。
