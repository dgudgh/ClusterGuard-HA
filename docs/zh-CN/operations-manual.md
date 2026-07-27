# ClusterGuard HA 运维操作手册

本文用于日常值守、变更、故障处理和审计。所有高可用操作都必须经过平台统一工作流：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK
-> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

Adapter 不得绕过该流程直接修改数据库。

## 1. 登录

访问：

```text
https://<任一控制节点>:8088/
```

新元数据存储的临时账号：

```text
用户名：admin
密码：admin123
```

首次登录必须修改密码。平台内发起的操作使用已认证会话、CSRF 和角色授权，用户不需要手工输入控制令牌或一次性审批令牌。外部 API 使用 Bearer control token；高风险服务调用仍需要与操作绑定的一次性 Approval。

忘记管理员密码时禁止修改后端 JSON 或密码哈希。按本地一次性恢复流程执行：

```bash
sudo -u clusterguard /usr/local/bin/clusterguard admin prepare-recovery
```

将生成的同一恢复工件以 `0600` 权限放到所有控制节点，滚动重启，并用一次性临时密码登录后立即修改。

## 2. 控制面健康

每日先看控制面，而不是直接点切换：

```bash
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:8088/healthz
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:8088/readyz
cgctl --server https://127.0.0.1:8088 \
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
cgctl --server https://127.0.0.1:8088 \
  --ca-file /etc/clusterguard/tls/ca.crt clusters
cgctl --server https://127.0.0.1:8088 \
  --ca-file /etc/clusterguard/tls/ca.crt topology <集群UUID>
cgctl --server https://127.0.0.1:8088 \
  --ca-file /etc/clusterguard/tls/ca.crt health <集群UUID>
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
cgctl --server https://127.0.0.1:8088 \
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

“旧主恢复”用于切换后重新把旧主加入当前主库。

1. 确认当前主库和 Writer endpoint 正常。
2. 选择已登记的离群旧主。
3. 平台先保持旧主只读。
4. 运行数据差异和身份检查。
5. 选择安全恢复方式。
6. 执行回挂。
7. 验证复制线程、延迟、只读状态和拓扑。

引擎策略：

- MySQL：GTID 无冲突时回挂；存在 errant GTID 时使用批准的重建流程。
- PostgreSQL：满足条件优先 `pg_rewind`，否则 `pg_basebackup`。
- Oracle：由 Data Guard Broker reinstate/validate。
- SQL Server：按 AG 副本状态恢复同步，不能把未同步副本伪装成健康。

## 8. 节点扩容与修复

控制台“节点”页面中的“添加或修复节点”使用同一生命周期流程：

1. 选择数据节点、控制节点或混合节点。
2. 输入永久固定节点名和平台 UUID。
3. 输入 SSH endpoint。
4. 选择数据库版本和端口。
5. 上传或选择批准的软件包。
6. 预检查磁盘、端口、依赖、身份和来源节点。
7. 安装数据库。
8. 选择 clone、xtrabackup、mysqldump、pg_basebackup 或 pg_rewind。
9. 同步数据并建立复制。
10. 验证后提交元数据。

数据节点数量不限制。Raft 控制节点必须保持奇数，常用为 3 或 5。物理损坏节点修复后沿用原平台 UUID；如果数据库原生身份因重建变化，必须经过明确的 replacement/reconcile 流程。

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

## 11. 备份与恢复

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

## 12. 应急处理

### 12.1 Leader 未知或失去 quorum

- 停止所有人工和自动数据库切换
- 检查 10009/TCP、证书、时钟和 voter 状态
- 恢复多数节点后确认 mutation authority
- 禁止在 follower 本地绕过门禁

### 12.2 双 VIP 或 Writer endpoint 错位

- 立即隔离业务写流量
- 对所有清单节点执行 endpoint owner 探测
- 在非主库移除 VIP/Listener 所有权
- 确认唯一可写主库后重新获取多数租约
- 完成 verify 后再恢复业务

### 12.3 操作超时或状态不确定

- 不要连续重复点击
- 用 operation UUID 或 idempotency key 查询
- 查看数据库真实角色和 endpoint owner
- 根据 verification failed checks 处理
- 只有确认前一次没有执行或已安全结束后才能重新发起

### 12.4 数据库客户端缺失

- 控制面应返回明确阻断
- 从批准的离线源安装匹配版本客户端
- 执行 `clusterguard --check-config`
- 重启 follower 验证后再滚动其他控制节点

### 12.5 管理员密码丢失

- 暂停控制面变更
- 备份元数据和 Raft
- 使用本地一次性恢复工件
- 保持多数节点使用相同工件
- 登录后立即修改密码并检查安全审计

## 13. 值守检查表

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
