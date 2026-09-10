# ClusterGuard HA 2.2-69：PostgreSQL 上游身份修复

构建日期：2026-09-07。来源版本：2.2-68。架构：Linux x86_64。

## 修复范围

修复 PostgreSQL 已恢复 streaming、lag=0，但两台 standby 上游身份缺失导致 `links=[]` 和集群 degraded 的后台发现链路。

1. 同一次只读 SQL 额外采集主库 WAL sender 的原生 application_name、状态、replay LSN，以及 standby receiver 的 sender_host / sender_port。不会读取完整 primary_conninfo、conninfo 或 passfile。
2. 在本轮全部端点探测完成后，使用原生 UUID、注册上游端点、唯一主库、system_identifier、Timeline 和双向 streaming 证据进行关联，再交给现有 Store / Raft 发布。
3. 后台拓扑不再依赖旧 primary_node_id GUC 的值。缺失、旧主或误填平台 UUID 的 GUC 不会覆盖经双方证实的当前原生上游身份，也不修改数据库 GUC 或 Docker Env。
4. 重复应用名、缺失主库证据、地址/端口不匹配、系统或 Timeline 冲突、断流、暂停重放等情况仍拒绝健康建边。多数派授权、fencing、writer lease 和持久化 watermark 规则保持不变。
5. WAL sender 原始观察只在本轮 Adapter 内传递，不进入 Topology JSON 或 Raft 元数据。
6. RPM 构建时，对未提交源码显式标注 `-dirty`，不再把当前工作树构建冒充 HEAD 的干净构建。
7. 同一个原生 UUID 出现在不同网络端点时，报告身份冲突并拒绝健康建边，不能将复制出来的可写节点合并成一个健康主库。
8. 受控切换前检查、正常 streaming 场景的 failover 复核、standby 恢复后验证、Timeline/WAL 实时复核和恢复 replay 后验证，同样复查已固定的两端实时证据，不能仅凭旧 GUC 放行。WAL 追赶保留原有固定采样目标与超时。

字段来源参考：[PostgreSQL 16 官方统计视图](https://www.postgresql.org/docs/16/monitoring-stats.html#MONITORING-PG-STAT-REPLICATION-VIEW)、[WAL receiver 视图](https://www.postgresql.org/docs/16/monitoring-stats.html#MONITORING-PG-STAT-WAL-RECEIVER-VIEW)。关联规则是 ClusterGuard 自身的安全约束，不是 PostgreSQL 提供的身份认证功能。

## 验收记录

- 已通过：Adapter / Discovery / Store 50 轮离线回放，原生 ID 与平台 ID 分离、错误 GUC 对照、持久化往返和旧时间戳拒绝。
- 已通过：重复应用名、错误上游端口、系统与 Timeline 冲突、双主、断流、暂停重放、probe / metrics 失败反例。
- 已通过：从 PostgreSQL 官方源码校验 SHA-256 后构建 16.4，启动三个本地隔离实例；真实 SQL、CLIQueryRunner、Discovery Scheduler 与 Store 连续 50 个不同的后台周期均健康且有 2 条链路。旧/空 GUC 未被修改；实际暂停 replay 后对应节点降级、链路收回。测试结束已停止全部隔离实例。
- 已通过：最终 `go test -p 1 ./... -count=1`、相关 Adapter/Discovery/Store 的 race、`go vet ./...`、RPM 与签名包构建、独立验签和包内 SHA-256。
- 待核验：现场三节点受信公钥指纹，以及当前 Leader 的 Chrome 文件上传校验；不得把本地验签称为现场验收。

## 候选制品

- 目录：`/Users/zhaolongjie/codex/clusterguard-ha/release/2.2-69-pg-topology`。
- 升级包：`clusterguard-ha-2.2-68_to_2.2-69.x86_64.cgupgrade`，36014297 字节，约 34.3 MiB。
- SHA-256：`1896bf8b7dab43510035a51e7a9b9a13e6050b3998e8587708c636f70c48a50d`。
- 构建时间：`2026-09-07T03:50:04Z`；签名包创建时间：`2026-09-07T03:50:39Z`。
- 构建提交标记：`bc0546a3e91ac86c61017d6d57c7ceeedd3d114f-dirty`。代码未提交、未推送。
- 详细证据与未完成门禁见该目录 `ACCEPTANCE.md`；含 17 个变更代码/测试文件的源码快照及摘要。
- 当前状态为待现场验收，不能标记可上线：SSH 公钥登录被拒绝；Leader `.153` Chrome 证书告警等待用户手动处理。本轮没有上传新包、生成计划或执行生产升级。

## 交付限制

本次是拓扑身份修复，不包含原子 Recovery Commit、Timeline DAG 灾难恢复或全链路秘密脱敏，也没有执行 replication secret 旋转。原始第一卡报告保留为修复前证据。

本次不修改 Docker entrypoint：首次诊断中发现的重启时 passfile 使用旧 Env 的独立路径仍需后续处理，不能把本包称为已完成全部 Bootstrap Env 隔离。

现场未执行滚动升级，不把隔离环境的 50 周期当作生产验收。灾难恢复后的历史 incident、启停状态和各维护门禁仍需后续卡按原子恢复协议收口。

本次不授权凭新拓扑绕过操作审批、主库隔离或恢复后验证。主库不可探测且旧 GUC 缺失时，不从历史信息猜测上游权威，继续保守阻断；source-loss 场景的持久化权威需要后续 Recovery Commit 卡处理。原有满足安全证据的 disconnected/source-loss 分支未放宽。
