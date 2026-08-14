# MySQL 旧主一键恢复生产前验收报告

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/mysql-former-primary-recovery-qualification-2026-08-09.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


- 日期：2026-08-09
- 节点：`192.168.102.152`、`192.168.102.153`、`192.168.102.154`
- 版本：MySQL 8.0.44、MySQL 8.4.10、MySQL 9.7.1
- 结论：进程故障后的 GTID 增量回挂、binlog 缺口后的全量重建、errant GTID
  分叉后的全量重建均通过真实三节点验证。

## 1. 一键恢复行为

控制台“一键恢复为从库”执行以下固定流程：

1. 主动刷新集群发现，读取旧主刚启动后的最新只读和复制状态。
2. 校验旧主固定资源 ID、MySQL `server_uuid`、集群清单和当前主库身份。
3. 比较旧主与当前主库的 GTID 集合。
4. 判断旧主追平所需 binlog 是否仍可从当前主库取得。
5. GTID 是子集且 binlog 完整时执行增量回挂。
6. binlog 已清理或存在 errant GTID 时，自动执行全量重建和复制恢复。
7. 验证旧主只读、复制线程、复制源、延迟、单主和 VIP 唯一性。
8. 写入审计、验证和报告；任何关键证据缺失时阻断。

平台不会用 `sql_slave_skip_counter`、GTID 注入或强制重置来掩盖数据分叉。

## 2. 真实测试矩阵

| 版本 | 故障与数据条件 | 恢复路径 | 结果 |
|---|---|---|---|
| 8.0.44 | 当前主库进程直接停止 | 自动故障切换后 GTID 增量回挂 | 通过；约 65-70 秒形成唯一新主，旧主启动后保持只读并完成增量回挂 |
| 8.4.10 | 故障期间继续写入，并 purge 旧主追平所需 binlog | 自动判定 `required_binlog_available=fail`，执行全量重建 | 通过；未尝试不安全增量回挂，全量同步、复制恢复和验证完成 |
| 9.7.1 | 隔离旧主上写入当前主库不存在的 errant GTID | 自动判定 GTID 非子集，执行全量重建 | 通过；分叉行未带入新拓扑，旧主按当前主库重建 |
| 8.4.10 | 主库数据目录变为只读，systemd 处于 `activating/auto-restart` 且无存活 PID | Agent 认定数据库已停止，允许已隔离故障切换 | 通过；修复空 auto-restart 状态误阻断后完成切换和旧主回挂 |
| 8.0/9.7 | 当前主库与其余两节点网络隔离 | 无外部 fencing 时停止写入，不提升无法确认已隔离的旧主 | 通过；全节点只读且 VIP 释放，网络恢复后收敛为唯一主库 |
| 8.0 | 当前主机整机断电 | 无 VMware/BMC fencing 证据时阻断自动提升 | 通过安全门禁；恢复虚拟机后服务自动启动并保持旧主只读 |

## 3. 并发与数据一致性

同一集群两个并发切换请求中，仅一个取得操作锁并成功，另一个以 HTTP 409
阻断。不同集群的 8.0 与 8.4 切换同时执行并均通过：

- 8.0 operation：`7b112de1-537a-4157-ae83-74d2e4372063`
- 8.4 operation：`e34b49d2-36b1-4948-b493-f202a051786b`
- 两个 execution 均为 `succeeded`
- 两个 verification 均为 `passed=true`
- 每个操作均通过源只读、目标可写、复制重挂、半同步、单主和 VIP owner 验证

最终三副本确定性数据校验。摘要统一按
`SUM(CRC32(CONCAT(id,marker,HEX(payload))))` 计算，便于在任一副本直接复算：

| 集群 | 每节点行数 | 每节点摘要 | 当前主库 | VIP owner |
|---|---:|---:|---|---|
| 8.0 / 3306 | 2007 | `4323827613648` | `.153` | `.153` 持有 `.155` |
| 8.4 / 3384 | 2006 | `4341221981770` | `.154` | `.154` 持有 `.160` |
| 9.7 / 3397 | 2005 | `4304498633408` | `.152` | `.152` 持有 `.164` |

三个集群其余节点均为 `read_only=1`、`super_read_only=1`，测试结束时无双主、
无双 VIP、无只读测试挂载、无网络故障规则。

最终收口阶段又执行 60 秒、7 个采样点的连续观察。每一轮都满足：每集群恰好
1 个 primary、2 个 replica、复制 IO/SQL 线程运行、复制链路健康、最大延迟 0 秒，
且主库身份没有后台漂移。

## 4. 本轮代码修复

1. MySQL probe 采集 `gtid_purged`，恢复预检查增加
   `former_primary_gtid_subset`、`required_binlog_available` 和 `rebuild_required`。
2. 控制台根据预检查自动选择增量回挂或节点全量重建。
3. 控制台在恢复决策前主动执行 discover，消除旧主刚启动时第一次点击读取旧快照的问题。
4. MySQL 9.7 复制状态采集使用兼容的垂直输出，不向客户端发送 `\\G`。
5. Agent 将“systemd auto-restart、MainPID=0、ControlGroup 为空”识别为已停止；存在
   任意存活 PID 或 cgroup 时仍 fail-closed。

## 5. 构建与测试

```text
go test ./... -count=1                                      PASS
go test -race ./adapters/mysql ./internal/api \
  ./internal/agent ./internal/coordination \
  ./internal/lifecycle ./internal/workflow -count=1          PASS
go vet ./...                                                 PASS
go build ./...                                               PASS
git diff --check                                             PASS
```

三台控制面滚动部署后的 Linux 二进制 SHA-256：

```text
clusterguard       d9a4d49c2e02d624307b8d2219cc70911cde5bbf63a7e52f8a9d4e70af4c1214
clusterguard-agent 29791923d37286172f23c923a61d9227c1d9d98360959069ba843baa959434fb
```

滚动部署期间始终保持 3 voters、quorum confirmed、mutation authority 和
`commit_index=applied_index`。

## 6. 生产边界

该结果证明软件在当前实验室对上述故障的安全处理，不表示基础设施风险为零。
完全失联或整机断电的主库若无法从独立通道确认已关机，ClusterGuard HA 会选择
停止写入而不是冒险产生双主。生产环境要在保持此门禁的前提下接入独立
BMC/IPMI、虚拟化平台或存储 fencing，才能同时取得自动接管和脑裂防护。

上线前还必须使用生产存储复测 I/O hang、磁盘满、inode 满、fsync 高延迟，并用
生产备份完成一次裸机恢复和时间点恢复。复制恢复不能替代备份恢复验收。
