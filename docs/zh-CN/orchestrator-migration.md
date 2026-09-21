# 从 Orchestrator 迁移到 ClusterGuard HA

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/orchestrator-migration.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

本文用于把已经由 Orchestrator 管理的 MySQL 集群，逐套迁移到 ClusterGuard HA。迁移对象是数据库集群的管理权，不是导入旧控制器的代码、API 或元数据。

ClusterGuard HA 是独立控制平台，不读取或兼容 Orchestrator 的后端表、配置文件、恢复记录、Raft 状态、Hook、CLI 或 API。现有 MySQL 数据不需要重装；平台通过权威清单和 MySQL `server_uuid` 重新发现并绑定已有实例。

## 1. 不可违反的迁移原则

同一套数据库集群在任何时刻只能有一个系统拥有以下变更权限：

- 自动故障切换
- 计划切换和提升主库
- 修改复制源
- 恢复旧主
- 绑定、漂移或撤销 VIP
- 执行数据库角色变更 Hook

迁移观察期可以同时运行两个控制器的只读发现，但不能让两者同时执行恢复或 VIP 变更。若无法证明旧控制器已经失去变更能力，ClusterGuard HA 必须保持只读观察或人工切换关闭状态。

## 2. 迁移前先确定应用接入方式

历史业务通常使用以下三种连接方式：

| 当前接入方式 | 迁移处理 |
| --- | --- |
| 应用连接 VIP | 保留原 VIP；切权前由旧系统管理，切权后只允许 ClusterGuard Agent 管理 |
| 应用连接独立代理或负载均衡 | 本次迁移只接管数据库 HA；代理配置另行变更，不能同时改两层 |
| 应用直连物理主库 IP | 先建立稳定 HA Endpoint，再分批修改连接串；不要把控制节点地址当数据库地址 |

如果保留原 VIP，业务连接串通常不需要修改。迁移的是 VIP 和数据库角色的控制权，不是业务访问地址。

## 3. 阶段 0：冻结和备份旧环境

逐套集群建立迁移清单，至少记录：

- 集群名称、MySQL 完整版本和端口
- 当前主库、全部副本、复制源和候选优先级
- 每台实例的 `server_uuid`、`server_id`、hostname、IP 和端口
- GTID、binlog、复制线程、延迟和 errant transaction 状态
- VIP、网卡、CIDR、当前 VIP Owner 和 ARP 行为
- Orchestrator 控制节点、Leader、后端数据库和恢复配置
- `PreFailoverProcesses`、`PostFailoverProcesses`、`PostGracefulTakeoverProcesses` 等 Hook
- 自定义 VIP 脚本、systemd service/timer、cron、Keepalived 或其他自动化
- Orchestrator 使用的数据库执行账号和来源地址

保存只读回退资料：

```bash
mysqldump --single-transaction <ORCHESTRATOR_BACKEND_DATABASE> \
  > orchestrator-backend-before-clusterguard.sql

tar -C / -czf orchestrator-config-before-clusterguard.tar.gz \
  etc/orchestrator.conf.json \
  etc/systemd/system
```

现场路径可能不同。备份中不得包含未加密外传的密码、私钥或控制令牌。

同时保存数据库证据：

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
SELECT @@global.gtid_mode, @@global.enforce_gtid_consistency;
SELECT @@global.read_only, @@global.super_read_only;
SHOW REPLICA STATUS\G
```

MySQL 5.7 使用 `SHOW SLAVE STATUS\G`。

## 4. 阶段 1：独立部署 ClusterGuard 控制面

对已有生产数据库，先只安装三节点控制面，不要调用数据库安装和数据同步流程：

```bash
./install_clusterguard.sh \
  -l 192.0.2.21,192.0.2.22,192.0.2.23 \
  -u root \
  -ld /var/lib/clusterguard \
  --engine none \
  --control-only \
  --known-hosts /secure/clusterguard/control-plane/known_hosts \
  --state-file /secure/clusterguard/control-plane/deployment-state.json \
  --secrets-file /secure/clusterguard/control-plane/deployment-secrets.env \
  --work-dir /secure/clusterguard/control-plane/site \
  --plan
```

核对计划后把 `--plan` 改成 `--execute`。控制节点必须为奇数且不少于 3 个。

已有数据库接管禁止执行以下行为：

- 不运行 MySQL 初始化
- 不清空或覆盖现有数据目录
- 不复制 `auto.cnf`
- 不自动重建复制
- 不自动绑定 VIP
- 不立即启用自动故障切换

## 5. 阶段 2：准备已有 MySQL 集群

### 5.1 验证数据库原生身份

在每台实例执行：

```sql
SELECT @@server_uuid, @@server_id, @@hostname, @@port, @@version;
```

要求：

- 每台实例 `server_uuid` 唯一
- 每台实例 `server_id` 唯一
- 只有一个可写主库
- 所有副本指向同一当前主库
- GTID 配置满足现场切换策略
- 旧 endpoint 与新 endpoint 都能追溯到同一 `server_uuid`

ClusterGuard 使用不可变平台 `resource_id` 保存节点，用 `server_uuid` 识别 MySQL 实例。hostname、IP 和 port 变化时只更新 endpoint 并保留 alias，不能创建新的逻辑节点。

### 5.2 创建用途分离的账号

推荐使用独立发现、执行和复制账号。以下来源网段必须替换为控制节点和数据节点的实际地址范围：

```sql
CREATE USER 'cg_discovery'@'192.0.2.%' IDENTIFIED BY '<DISCOVERY_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'cg_discovery'@'192.0.2.%';

CREATE USER 'cg_operator'@'192.0.2.%' IDENTIFIED BY '<OPERATION_PASSWORD>';
GRANT PROCESS, REPLICATION CLIENT, CONNECTION_ADMIN,
  SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.*
  TO 'cg_operator'@'192.0.2.%';

CREATE USER 'cg_replication'@'192.0.2.%' IDENTIFIED BY '<REPLICATION_PASSWORD>';
GRANT REPLICATION SLAVE ON *.* TO 'cg_replication'@'192.0.2.%';
```

MySQL 5.7 的执行账号需要受限来源的 `SUPER`。完整版本差异和 TLS 配置见[数据库接入手册](database-preparation.md)。

密码写入控制节点受保护的环境文件，并由 `/etc/clusterguard/clusterguard.json` 中的 `password_env` 引用。浏览器登记集群时不输入数据库密码，发现 API 也拒绝请求体覆盖凭据。

## 6. 阶段 3：登记权威清单

登录控制台后进入 **集群管理 -> 新增集群**：

1. 数据库类型选择 MySQL。
2. 输入固定且唯一的集群显示名称。
3. 登记全部主库和副本 endpoint，不能只登记当前主库。
4. 为每台物理主机登记固定节点名和平台 UUID。
5. 保存后执行一次发现。
6. 核对发现到的 `server_uuid`、角色、复制源、版本和延迟。

等效 API 示例：

```bash
curl -sS -X POST https://<controller>:3000/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-mysql",
    "engine":"mysql",
    "endpoints":[
      {"hostname":"mysql-a","ip_address":"192.0.2.31","port":3306},
      {"hostname":"mysql-b","ip_address":"192.0.2.32","port":3306},
      {"hostname":"mysql-c","ip_address":"192.0.2.33","port":3306}
    ]
  }'
```

刷新发现时只发送空对象：

```bash
curl -sS -X POST \
  https://<controller>:3000/api/v1/clusters/<CLUSTER_UUID>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

不要导入 Orchestrator 后端表，也不要把 `hostname:port` 当成 ClusterGuard 资源主键。

## 7. 阶段 4：并行只读观察

在旧系统仍拥有执行权时，ClusterGuard 只做发现、健康检查、拓扑和候选评估。建议至少覆盖一个业务高峰和一个备份窗口。

每轮比较以下结果：

| 检查项 | 必须一致或可解释 |
| --- | --- |
| 当前主库 | 两边识别同一 `server_uuid` |
| 副本集合 | 不缺节点、不串集群 |
| 复制源 | 全部指向当前主库 |
| 复制状态 | IO/SQL 线程、GTID、延迟一致 |
| 候选排序 | 版本、延迟、GTID 和维护状态可解释 |
| VIP Owner | 只有当前主库持有 |
| 元数据 | hostname/IP/port 与权威清单一致 |

若出现重复节点、身份不匹配、清单外 endpoint、多个可写主库、VIP 多 Owner 或观察覆盖不完整，先修正元数据或数据库状态，不得进入切权。

## 8. 阶段 5：切换唯一执行权

在维护窗口内按以下顺序执行，每次只迁移一套集群：

1. 暂停业务变更和计划切换。
2. 确认 ClusterGuard Raft 只有一个 Leader 且多数派健康。
3. 停止所有 Orchestrator 实例的恢复能力。
4. 停止并禁用旧 VIP Hook、timer、cron、Keepalived 或其他漂移脚本。
5. 验证旧系统不能再提升主库、修改复制源或变更 VIP。
6. 保持当前 MySQL 主从关系和 VIP 位置不变。
7. 在数据节点安装并配置受限 ClusterGuard Agent，只登记已有数据库路径和 service，不执行数据库初始化。
8. 在 ClusterGuard 登记原 VIP、网卡和 CIDR，启用 HA Endpoint 所有权协调。
9. 重新发现并执行 VIP 唯一性、主库可写、副本跟随和 Agent 覆盖验证。
10. 先开放人工受控切换；现场验收通过后再单独启用自动故障切换。

旧系统的 service 名称和 Hook 位置因部署而异。停止前先盘点：

```bash
systemctl list-unit-files | grep -Ei 'orchestrator|vip|reconcile|keepalived'
systemctl list-timers --all | grep -Ei 'orchestrator|vip|reconcile'
crontab -l
ps -ef | grep -Ei '[o]rchestrator|[v]ip.*(move|reconcile)'
```

典型停止命令需要按现场名称调整：

```bash
systemctl stop orchestrator.service
systemctl disable orchestrator.service
systemctl stop <OLD_VIP_TIMER_OR_SERVICE>
systemctl disable <OLD_VIP_TIMER_OR_SERVICE>
```

仅从负载均衡摘除旧控制器不等于关闭恢复能力。必须在每个旧控制节点验证进程、Hook、timer 和 cron 都不能再执行变更。必要时临时撤销旧控制器数据库执行账号或限制其来源地址，形成第二道防线。

## 9. 阶段 6：执行首次受控切换验收

首次切换应在业务低峰进行：

1. 刷新拓扑并锁定同一观察快照。
2. 选择无 GTID 缺口、复制线程正常且延迟满足策略的候选节点。
3. 解锁操作并执行“主库与 VIP 同步切换”。
4. 等待完整工作流结束，不把“请求已提交”当作成功。
5. 检查新主可写、旧主只读、全部副本跟随新主。
6. 检查 VIP 全集群只有一个 Owner 且 Owner 为新主。
7. 检查操作锁释放、审计和报告完成。

成功标准：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK
-> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

任一阶段为 `blocked`、`failed` 或 `indeterminate` 时，不要重复点击。使用操作 UUID 查询原始返回，先确认数据库真实状态。

## 10. 阶段 7：启用自动故障切换

自动故障切换应作为独立变更启用。至少确认：

- 三个或更多奇数控制节点已形成稳定 Raft 多数
- Agent 在全部数据节点启用并能完成本地失多数只读隔离
- 当前主库、候选和 VIP Owner 识别正确
- 默认连续 4 次且跨 3 秒的稳定故障证据窗口，以及独立的 15 秒 Agent 隔离宽限均已现场验证
- 已使用有界客户端连接超时和重试，从应用写入口测量实际 RTO
- 网络分区、Agent 失联和控制节点失多数时均 fail-closed
- 旧主恢复只能走“恢复为当前主库的从库”流程
- 外部 BMC、PDU、云 API 或虚拟化 fencing 已按现场风险决定是否启用

启用前保留 `automatic_failover_enabled=false`。验收通过后再修改配置并滚动重启控制节点。不要通过重新运行数据库安装器来切换该开关。

## 11. 回退策略

### 11.1 ClusterGuard 尚未执行角色变更

如果仍处于只读观察期：

1. 关闭 ClusterGuard 自动故障切换和 HA Endpoint 协调。
2. 停止 ClusterGuard Agent 的 VIP reconcile。
3. 确认 VIP 和数据库角色仍与切权前一致。
4. 恢复 Orchestrator service、Hook 和 VIP 自动化。
5. 验证旧系统重新发现完整拓扑后再恢复其执行权。

### 11.2 ClusterGuard 已经切换过主库

不能直接启动旧 Orchestrator。旧系统可能仍把历史主库当作恢复目标或写入端点。

先完成以下任一方案：

- 保持 ClusterGuard 当前拓扑，清空旧系统陈旧发现并重新发现全部节点；或
- 通过 ClusterGuard 受控切回原拓扑，再验证 VIP 和复制关系；或
- 在维护窗口人工修正旧系统元数据，并确认它识别当前真实主库。

只有在数据库角色、复制源、VIP Owner 和旧系统视图完全一致后，才能恢复旧系统执行权。回退过程中仍然只能有一个变更控制器。

## 12. 迁移验收清单

- [ ] ClusterGuard 与旧系统没有共享代码、后端表或运行状态
- [ ] 所有实例按 `server_uuid` 绑定到唯一平台 `resource_id`
- [ ] hostname、IP、port 只是可变 endpoint，历史地址进入 alias
- [ ] 权威清单包含全部实例，没有清单外控制
- [ ] 旧控制器、Hook、timer、cron 和 VIP 自动化已停止
- [ ] 当前只有一个可写主库
- [ ] 所有副本跟随当前主库且复制线程正常
- [ ] VIP 只有一个 Owner 且位于当前主库
- [ ] ClusterGuard Leader 和 quorum 健康
- [ ] 首次计划切换通过并生成完整审计和报告
- [ ] 旧主可以受控恢复为从库
- [ ] 自动故障切换在独立现场验收后才启用
- [ ] 回退步骤和负责人已经演练

## 13. 旧能力与新能力对应关系

| 历史 Orchestrator 能力 | ClusterGuard HA 对应能力 |
| --- | --- |
| 拓扑发现 | 权威清单内发现和不可变身份绑定 |
| 候选排序 | Adapter 候选评估和风险证据 |
| Graceful takeover | 受控主库与 VIP 同步切换 |
| Recovery | 统一故障切换工作流和旧主回挂 |
| Hook | 受限 Agent 和签名执行契约 |
| Audit | Raft 复制审计、操作日志和报告 |
| hostname:port 标识 | 平台 UUID + 数据库原生身份 + endpoint alias |

迁移不是接口替换，也不存在原地升级。历史项目需要做的是让 ClusterGuard 重新发现数据库、验证身份和拓扑，然后在明确的维护窗口转移唯一执行权。
