# ClusterGuard HA 运维操作手册

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/operations-manual.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


本文用于日常值守、变更、故障处理和审计。所有高可用操作都必须经过平台统一工作流：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK
-> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Adapter 不得绕过该流程直接修改数据库。

版本边界：`2.1-45` 是 MySQL 封板版；PostgreSQL 操作从 2.2 开始。本文中的
Oracle 和 SQL Server 内容只有在对应正式版本发布并完成现场验收后才可执行。

## 1. 登录

访问：

```text
https://<任一控制节点>:3000/
```

新元数据存储的临时账号：

```text
用户名：admin
首次密码：admin123
```

首次登录必须修改密码；改密前，除认证接口外的集群、拓扑、审计读取和所有变更都会被拒绝。默认密码只以 Argon2id 哈希保存，不会写入配置、环境文件、审计或报告。平台内发起的操作使用已认证会话、CSRF 和角色授权，用户不需要手工输入控制令牌或一次性审批令牌。外部 API 使用 Bearer control token；高风险服务调用仍需要与操作绑定的一次性 Approval。

忘记管理员密码时禁止修改后端 JSON 或密码哈希。按本地一次性恢复流程执行：

```bash
sudo -u clusterguard /usr/local/bin/clusterguard admin prepare-recovery
```

将生成的同一恢复工件以 `0600` 权限放到所有控制节点，滚动重启，并用一次性临时密码登录后立即修改。

## 2. 控制面健康

每日先看控制面，而不是直接点切换：

```bash
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/healthz
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/readyz
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt status
```

正常标准：

- 只有一个 Raft Leader
- voter 数为奇数
- quorum confirmed 为 yes
- mutation authority 为 yes
- active/indeterminate operation 数符合预期
- 三节点 metadata revision 最终一致

Follower 收到写请求时会向当前 Leader 转发。Leader 不明或失去多数时必须阻断写入，不能在本机静默执行。

## 3. 集群接入

控制台进入“集群管理”：

1. 选择数据库类型。
2. 输入固定集群名称。
3. 添加完整实例清单。
4. 为每个节点选择固定节点名和平台 UUID。
5. 填写 hostname、IP、端口和数据库原生身份。
6. 配置 VIP、Oracle 服务或 SQL Server Listener。
7. 保存后立即执行发现和健康检查。

数据库清单是控制边界。平台不得操作清单外节点。

命令行查看：

```bash
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt clusters
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt topology <CLUSTER_UUID>
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt health <CLUSTER_UUID>
```

## 4. 元数据变更

修改 hostname、IP、port 时：

1. 在“拓扑”右上角打开“修改元数据”。
2. 选择已有平台资源，不新建节点。
3. 输入新的 endpoint 和变更原因。
4. 运行元数据预检查。
5. 确认数据库原生身份与已有资源一致。
6. 执行 reconcile。
7. 验证旧 endpoint 已保存为 alias，`resource_id` 未变化。

发现相同 engine identity 时必须合并到已有资源。遇到 identity mismatch、重复资源或跨集群节点串用时，先阻断操作，再由管理员合并 alias 或退役错误 endpoint。

## 5. 计划切换

适用于当前主库健康、需要受控切换的场景。

1. 在左上角选择目标集群。
2. 确认当前主库、候选主库、VIP/服务、延迟。
3. 从候选列表选择目标节点。
4. 点击“解锁操作”，锁只在当前会话和短时间窗口有效。
5. 点击“执行切换”。
6. 页面应显示预检查、计划、安全门禁、执行、验证阶段。
7. 等待最终状态为成功，不要把“已提交”当作成功。

成功必须同时满足：

- 新主库可写
- 旧主库只读或已隔离
- 副本跟随新主库
- VIP/Oracle 服务/SQL Server Listener 指向新主库
- 操作锁已释放
- 审计和报告已落库

如果状态为 `indeterminate` 或“需复核”，立即停止重复点击，按操作 UUID 查询当前状态：

```bash
cgctl --server https://127.0.0.1:3000 \
  --ca-file /etc/clusterguard/tls/ca.crt operation <操作UUID>
```

## 6. 故障切换

故障切换用于原主库不可达或失去写入能力的场景，不等同于计划切换。

执行前确认：

- 控制节点仍有 Raft 多数
- 原主库已通过 fencing、网络隔离或电源隔离证明不能继续写
- 候选节点复制状态和数据丢失风险已评估
- Writer endpoint 不会留在旧主
- 当前没有同集群操作锁

自动故障切换可以不要求人工输入令牌，但仍必须通过 Safety Guard、Raft Leader、多数、幂等键、Operation Lock、验证和审计。任何一项不满足都应阻断。

网络分区恢复后，旧主不得自动恢复为可写。它只能以待修复节点进入旧主恢复流程。

## 7. 旧主恢复

“旧主恢复”用于切换后重新把旧主加入当前主库。MySQL 8.0、8.4 和 9.7
使用同一个“一键恢复为从库”入口，操作员不需要先判断应当增量回挂还是全量重建。

1. 确认当前主库和 Writer endpoint 正常。
2. 选择已登记的离群旧主。
3. 启动已修复的数据库服务；旧主必须保持 `read_only=ON` 和
   `super_read_only=ON`，不得手工恢复写权限或绑定 VIP。
4. 在控制台解锁操作，选择旧主并点击“一键恢复为从库”。
5. 控制台先主动刷新集群发现，避免服务刚启动时使用旧的健康快照。
6. 平台校验固定资源身份、当前主库、只读状态、GTID 集合、所需 binlog、
   集群清单和 Writer endpoint 唯一性。
7. 平台自动选择增量回挂或全量重建。
8. 最终验证复制线程、延迟、只读状态、复制源、单主和 VIP 唯一性。

### 7.1 MySQL 自动恢复决策

| 证据 | 自动动作 | 说明 |
|---|---|---|
| 旧主 GTID 是当前主库 GTID 的子集，且追平所需 binlog 仍存在 | GTID 增量回挂 | 重新指向当前主库并启动复制，速度最快 |
| 当前主库已经清理旧主追平所需的 binlog | 全量重建 | 从当前主库重新同步完整数据，再建立复制 |
| 旧主存在当前主库没有的 errant GTID | 全量重建 | 不尝试跳事务或强行合并分叉数据 |
| 旧主仍可写、原生身份不匹配、不在集群清单、当前主库或 Writer endpoint 不唯一 | 阻断 | 修复事实后重新执行，不做猜测性变更 |

“丢失 binlog”指当前主库已经 purge 了旧主追平必需的二进制日志。平台不能也
不会伪造缺失事务；此时安全恢复方式是保留审计证据，清理旧主的数据副本，使用
当前主库作为 donor 完成全量同步，然后以只读副本身份重新加入。全量重建要求
平台管理员权限、节点生命周期执行能力和完整验证；任一条件不满足都会 fail-closed。

恢复完成必须同时看到：当前主库唯一可写、恢复节点只读、复制 IO/SQL 线程运行、
延迟归零、数据校验通过、VIP 仅在当前主库、操作报告状态为 succeeded。仅看到
“服务已启动”不等于恢复成功。

引擎策略：

- MySQL：GTID 和 binlog 完整时增量回挂；binlog 缺口或 errant GTID 时自动进入审批的全量重建流程。
- PostgreSQL：满足条件优先 `pg_rewind`，否则 `pg_basebackup`。
- Oracle：由 Data Guard Broker reinstate/validate。
- SQL Server：按 AG 副本状态恢复同步，不能把未同步副本伪装成健康。

## 8. 节点扩容与修复

控制台“节点”页面中的“添加或修复节点”使用同一生命周期流程：

1. 选择数据节点、控制节点或混合节点。
2. 输入永久固定节点名和平台 UUID。
3. 输入 SSH endpoint。
4. 选择数据库版本和端口。
5. 上传或选择审批的软件包。
6. 预检查磁盘、端口、依赖、身份和来源节点。
7. 安装数据库。
8. 选择 clone、xtrabackup、mysqldump、pg_basebackup 或 pg_rewind。
9. 同步数据并建立复制。
10. 验证后提交元数据。

以上步骤均为 ClusterGuard 内置工作流，不要求运维人员在目标节点手工执行安装或复制命令。任务详情会分别显示“预检查、安装、数据同步、复制配置、验证、元数据提交”；任一步失败都会停止后续提交，并保留脱敏日志和报告供复核。

数据节点数量不限制。Raft 控制节点必须保持奇数，常用为 3 或 5。从 3 个控制节点扩容时，控制台要求一次填写两个不同的固定节点名、平台 UUID、主机名和 IP，并在同一受控任务中完成：

1. 校验两台目标机 SSH 身份、固定节点清单和地址唯一性。
2. 安装 ClusterGuard 控制服务与所选数据库 Adapter Runtime。
3. 为每台控制节点签发独立的 API 与 Raft mTLS 证书。
4. 启动服务并验证节点身份。
5. 由当前 Leader 依次执行 Raft `AddVoter`，最终成员数必须为 5。
6. 验证多数派、Leader、成员清单和元数据 revision 收敛后提交节点元数据。

如果配对安装中的任意节点失败，平台会逆序停止本次新安装的控制角色；如果 Raft 成员提交中途失败，已加入的 voter 会被逆序移除，不能留下偶数成员。目标状态不明确时任务标记为“需复核”，不会伪报成功。

控制节点动态签发材料通过离线安装资产目录交付：

```text
assets/pki/api-issuer.crt       0644
assets/pki/api-issuer.key       0640 root:clusterguard
assets/pki/raft-issuer.crt      0644
assets/pki/raft-issuer.key      0640 root:clusterguard
```

API issuer 必须被 `tls_ca_file` 指向的信任链信任，Raft issuer 必须被 `consensus.tls_ca_file` 指向的信任链信任。四个路径必须同时配置；私钥不得放入浏览器、任务请求、审计、报告或普通日志。物理损坏节点修复后沿用原平台 UUID；如果数据库原生身份因重建变化，必须经过明确的 replacement/reconcile 流程。

## 9. 操作日志

“操作日志”是独立菜单，不嵌入操作页面。每条记录至少显示：

- 时间
- 集群名称和 UUID
- 原主库
- 目标主库
- 操作类型
- 操作人或自动化身份
- 执行模式
- 风险等级
- 状态
- 操作 UUID、报告 UUID

原始返回默认折叠，点击后显示。原始返回不得泄漏密码、session、Bearer token、一次性 Approval 或 Agent secret。

日志中集群字段必须显示固定集群名称，不能错误显示 hostname:port。历史记录应按资源 UUID 关联，即使主机名或端口后来变化也能正确还原。

## 10. 指标与监控

只读监控使用独立 monitoring token，不使用控制令牌。监控至少覆盖：

- 控制服务、Leader、quorum、metadata revision
- 集群健康、主库、候选节点
- 复制延迟和线程/进程状态
- VIP/服务/Listener owner
- 活跃、失败和 indeterminate 操作
- Agent reconcile 状态

Prometheus 可以读取平台暴露的监控接口，不要求额外部署数据库 exporter。Zabbix 通过只读 JSON API 拉取同一健康和性能数据。

## 11. 计划关机与自动恢复

适用于机房维护、整机迁移、业务窗口等需要安全关闭整个 MySQL 主从集群的场景。关闭前平台自动加保护，重启后由 systemd 单元自动恢复，全程无需人工干预。

### 11.1 两种关机模式

| 模式 | 行为 | 适用场景 |
|------|------|----------|
| `service` | 仅停止 MySQL 服务（先副本、后主库），主机保持开机 | 数据库软件升级、配置变更、短暂停机窗口 |
| `poweroff` | 所有节点并行整机下电 | 机房断电维护、整柜迁移 |

### 11.2 执行关机

日常操作使用控制台 **拓扑 -> 电源生命周期 -> 一键关机**。默认选择
`service`，只停止数据库服务，服务器保持开机。已登录的平台管理员由后端签发并
消费短时一次性审批，浏览器中不暴露审批密钥。

自动化命令同样走统一 Power API；真实执行必须显式提供一次性审批令牌：

```bash
cgctl cluster shutdown --cluster <集群显示名> --mode service|poweroff \
  --approval-token <一次性令牌>
cgctl cluster shutdown --cluster <集群显示名> --mode service --dry-run
```

`--dry-run` 仅执行预检查，随后自动取消临时生命周期，不改数据库或主机状态。
不存在绕过 Safety Guard、锁、审批、审计和 Agent 白名单的直连脚本入口。

完整执行流程（任何一步失败都会中断并保留保护，见 11.4）：

1. 刷新拓扑（discover），由每台签名 Agent 原子写入
   `/etc/clusterguard/power-snapshots/<CLUSTER_UUID>.json`（目录 0700、文件 0600）。
   同一主机上的多个集群不会互相覆盖快照。
2. 冻结自动恢复（recovery-freeze）：自动故障切换、重启引导、手动切换全部阻断。
3. 对所有实例打维护标记（maintenance）。
4. 对所有节点执行 `SET PERSIST_ONLY read_only=ON; SET PERSIST_ONLY super_read_only=ON`：运行期不生效，重启后持久化，防止误写。
5. 按模式停止：`service` 先停副本、最后停主库；`poweroff` 全部节点并行下电。

### 11.3 重启后的自动恢复

两个 systemd 单元随安装默认启用。开机时扫描每集群快照目录；目录为空时直接跳过：

- `clusterguard-cluster-restore.service`：先向在线控制面确认该快照仍对应进行中的
  计划恢复，再启动本地数据库服务并等待就绪。MySQL 仅允许快照中不可变实例 ID
  指定的主库解除持久化与运行期只读；普通重启遇到陈旧快照会在修改角色前拒绝执行。
  随后上报 boot/recovering 并触发拓扑发现。
- `clusterguard-cluster-finalize.service`：等待控制面健康（`/healthz`，最长 60 秒），
  随后轮询拓扑直至主库实例健康（每 5 秒一次，最长 600 秒）；确认健康后完成验证、
  解除恢复冻结和集群维护保护，并只在对应集群快照写入 `recovered_at`。

注意：`poweroff` 只能安全下电，软件无法让已经物理断电的机器凭空开机。自动开机需
VMware 自动启动/API、IPMI/iDRAC/iLO、Wake-on-LAN 或 BIOS 来电自启；操作系统一旦
启动，ClusterGuard 会自动完成数据库、拓扑、复制和保护状态恢复。

### 11.4 超时与失败兜底（fail-closed）

- finalize 超时且主库仍未健康：**保护不解除**，脚本输出 CRITICAL 日志并正常退出，等待人工处理。
- 人工确认问题已解决后，可手动解除保护：

```bash
curl -sk -X POST -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"freeze":false}' https://127.0.0.1:3000/api/v1/clusters/<CLUSTER_UUID>/recovery-freeze
```

- 删除快照后两个恢复单元自动变为 no-op；对已 finalize 的快照重复执行 restore/finalize 也是幂等 no-op。

### 11.5 查看恢复状态

```bash
cgctl cluster restore-status [--cluster <CLUSTER_UUID>]
```

输出快照是否存在、集群信息、`recovered_at`、恢复冻结状态（`frozen`/`active`）、每个实例的角色/健康/复制延迟/维护标记，以及按状态给出的恢复建议（advice）。未指定 `--cluster` 时默认使用本机快照中的集群 UUID。

## 12. 备份与恢复

每日备份：

- `/etc/clusterguard/`，密钥和密码文件单独加密
- `/var/lib/clusterguard/metadata.json`
- Raft 数据目录
- 操作审计和报告导出
- 当前 RPM、SHA-256、配置版本和 BUILD-INFO

数据库本身仍必须使用各引擎的正式备份体系；控制面元数据备份不能替代数据库备份。

恢复要求：

- 单控制节点配置恢复可使用 `config-backups`
- Raft 元数据恢复必须停止全部控制节点并使用一致恢复点
- 不得把一个节点的旧 Raft 目录热复制到运行中的集群
- 恢复后先验证 Leader/quorum，再启用任何自动切换或 VIP reconcile

## 13. 应急处理

### 13.1 Leader 未知或失去 quorum

- 停止所有人工和自动数据库切换
- 检查 10009/TCP、证书、时钟和 voter 状态
- 恢复多数节点后确认 mutation authority
- 禁止在 follower 本地绕过门禁

### 13.2 双 VIP 或 Writer endpoint 错位

- 立即隔离业务写流量
- 对所有清单节点执行 endpoint owner 探测
- 在非主库移除 VIP/Listener 所有权
- 确认唯一可写主库后重新获取多数租约
- 完成 verify 后再恢复业务

### 13.3 操作超时或状态不确定

- 不要连续重复点击
- 用 operation UUID 或 idempotency key 查询
- 查看数据库真实角色和 endpoint owner
- 根据 verification failed checks 处理
- 只有确认前一次没有执行或已安全结束后才能重新发起

### 13.4 数据库客户端缺失

- 控制面应返回明确阻断
- 从审批的离线源安装匹配版本客户端
- 执行 `clusterguard --check-config`
- 重启 follower 验证后再滚动其他控制节点

### 13.5 管理员密码丢失

- 暂停控制面变更
- 备份元数据和 Raft
- 使用本地一次性恢复工件
- 保持多数节点使用相同工件
- 登录后立即修改密码并检查安全审计

## 14. 值守检查表

每日：

- Leader、quorum、readyz
- 所有集群主库和 Writer endpoint 一致
- 复制延迟和异常线程
- 失败或 indeterminate 操作
- Agent reconcile 和证书有效期

每周：

- 执行只读健康检查和候选评估
- 导出审计、报告
- 验证备份可读取
- 检查数据库账号和 SSH 密钥到期时间

每月：

- 在测试环境执行计划切换和旧主恢复
- 验证控制节点滚动重启
- 验证元数据 hostname/IP/port reconcile
- 恢复演练一份控制面备份和一份数据库备份
- 复核最小权限、网络 ACL 和自动切换策略
