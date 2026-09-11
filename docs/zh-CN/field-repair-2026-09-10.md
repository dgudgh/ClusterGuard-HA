# 2026-09-10 现场修复执行卡

## 授权与范围

用户在三台现场只读审计之后明确要求“修复呀”，此前已允许 SSH 到
192.168.102.152、153、154。本轮由当前任务自行执行，不调用其他模型。
不改变页面样式，不安装控制面 RPM，不删除数据库文件，不影响健康 MySQL。

## 修改前证据与旧版对照

- 源码工作树：platform-auth-session，分支 codex/2.2-postgresql，HEAD 19d02bc。
- 旧代码基线 bc0546a，恢复功能落库提交 c8795a3；已检查 git log、旧版
  internal/agent/config.go、当前 internal/api/disaster_recovery.go、
  internal/store/disaster.go、internal/store/recovery_freeze.go、
  internal/runtime/disaster_driver.go、Agent recovery_rebuild.go 和 Docker PG 驱动。
- 发布记录 release-2.2.99.md 明确 99 沿用 98 的受控恢复实现；现场三台都是
  2.2-98，不能因构建提交包含 dirty 就假定没有新功能。执行前再通过现场 plan 验证能力。
- 旧版与当前 Agent 都按 agent.json 的本地 policies 工作，不会因为控制面目录
  删除集群就自动删除本地策略。此次孤立 5432 策略需要有备份的精确清理，
  不能放宽授权，也不能通过修复凭证让孤立集群重新获得写入口。
- 本轮新快照：.build/field-repair-20260910/before，2026-09-10 03:34 UTC。
  MySQL 健康、两条复制链路、VIP156 唯一在153；Swarm PG55432 的152服务0/0，
  153和154都是 standby、无 WAL receiver，VIP157不存在，API一致 degraded/0links。
  因此本轮实际问题是无主库，不能伪造拓扑或放宽 healthy 条件。
- 宿主机5432是另一个旧系统：system_identifier 不同，旧集群ID
  baf1e092-0e44-4e74-889e-8092d92df831 不在控制面目录，但三台 Agent 仍含它。
  其凭证父目录权限导致读取失败，产生重复日志；不把它与 Swarm55432 混同。

## 执行顺序

1. [x] 复核版本、多数派、两个活动集群、服务绑定和原生数据库状态。
2. [x] PG 恢复计划只读预检；冻结目标集群，停止三个指定 Swarm PG 服务。
3. [x] 三台服务器各自保存私有配置备份和离线 PGDATA 备份，校验归档与 SHA-256。
4. [x] 每个确认版本仅提交一次；首轮明确失败后定位并修正旧只读 fence，再用新 revision 受控重试成功。
5. [x] 备份并精确移除孤立旧集群的 Agent policy，保留 MySQL/Swarm PG 两条策略。
6. [x] 原生复制、身份、槽、VIP、Recovery Commit 与三节点 API 一致性复核。
7. [x] 连续至少50个不同后台 discovery 时间戳：两集群 healthy、每个两条链路。

## 安全与回退

- 只允许 PG 集群 8938f553-c7ac-4589-aaf2-5d6823c4cc7e，名称 swarm-pg16，
  服务 cgpg16_postgresql01/02/03，宿主数据目录 /data/clusterguard-swarm/postgresql/55432。
- 备份只保留在原服务器唯一私有目录，不导出密码、token、passfile 或完整连接串。
- 恢复流程必须全员 fencing，依据系统标识、Timeline、WAL/COMMIT 证据选主。
  独立提交分叉、证据不足、身份或多数派失败时停止并保留冻结；不得改成 max(LSN) 强选。
- 只有 Recovery Commit 和最终验收后才恢复 VIP、writer lease 和自动恢复。
- 超时或断线只查询任务，不重复提交。任何失败都记录原始脱敏原因。
- 配置备份可在再次核对当前哈希后原子恢复；数据库备份不能直接在线覆盖。
  数据回退必须重新全员冻结/停止并核对权威分支，禁止自动启动旧主。
- 不停止控制面或全局 Agent timer，不改正常 MySQL 服务，不清理历史操作日志。

## 结果

### 新发现：受控恢复遗漏旧只读 fence

第一次任务 b4807cd8-dfb5-407d-a2ff-3019c8550b12 已完成选主、两个从库重建，
但在 03:44:06 UTC 被 CommitRecovery 拒绝：members=3/3 links=0/2。
保护未释放，失败后所选主库停止，未人为添加 links。

现场 154 postgresql.auto.conf 第4行为 default_transaction_read_only='on'；
本轮主库最新引擎证据为 in_recovery=false、transaction_read_only=true、Timeline14。
两个从库均已记录重建和上游身份验证通过。

修改前再次对照：bc0546a 的 adapters/postgresql/execution.go 已有
postgresqlActivateWritesSQL（ALTER SYSTEM RESET），正常切换明确释放旧只读配置；
c8795a3 新增的 internal/agent/recovery_start.go 只在已验证 HBA guard 和授权之后
清空上游，并未处理持久化 default_transaction_read_only，遗漏这一恢复条件。
当前 Link Builder 明确要求主库 transaction_read_only=false，因此拒绝正确，
不修改健康判断、Identity Resolver 或 Raft links 校验。

拟改范围：仅受控 PG 主库启动时，在完整离线证据、HBA guard 与有效授权检查后，
把旧的数据库只读默认值改为 off；最终仍要重新校验授权、启动后的 guard、复制和
Recovery Commit。普通 standby、未授权请求和无 guard 的请求均不得执行该步骤。
先给真实三节点 PG 启动/重建集成测试加入遗留只读 fence，复现失败，再修实现。
现场不换二进制：对已冻结、已停止、已选定且有相同 task guard 的154做同等配置修正，
保留配置备份；随后通过原受控 API 重新取证/执行，不直接启动数据库或添加 VIP。

### 实际修复与验证

- 真实三节点测试在修改前失败：selected primary retained the old write fence: on。
  修复后真实 PG 启动、重建、两条 streaming、两个身份绑定活动槽均通过。
- 补充7种授权/guard/指纹/撤销场景，检查无授权、旧证据或缺失 guard 不改只读配置；
  启动前撤销不启动，启动后撤销必须停止。业务 HBA 拒绝与释放的真实 PG 用例通过。
- 全仓重跑2104项测试及子测试通过（378.659秒），Agent/Disaster/Store/Runtime
  race 536项通过（105.749秒），go vet通过。全仓运行器未带Node路径而跳过的
  TestConsoleUpdateStateUsesLatestExecutionAndLiveMaintenance 已带正确PATH单独补跑通过。
  5个本地容器跳过项在前一轮现场隔离验证记录中已通过，本轮未重复执行它们，不能冒充重跑。
- 第二次受控执行于03:49:16 UTC受理；03:51:44完成 Recovery Commit；
  03:51:53完成业务入口验证并解除恢复保护，task revision42，stage=succeeded。
  首轮与第二轮是修复前后的两个明确尝试，不是断线后的盲目重提。
- 现场154为唯一 PG primary，152/153为 standby；三台Timeline14，采样LSN均为
  0/9032D30。应用名分别绑定152和153的原生ID，两个物理槽active，VIP157只在154。
  MySQL容器、UUID、GTID、复制线程、只读标志与修复前完全一致，VIP156仍只在153。
- 50轮自然发现用时116.929秒，覆盖03:52:36至03:54:33 UTC；两集群每轮均healthy、
  三实例healthy、2条healthy且lag=0链路。三控制节点最终返回的完整拓扑快照相同。
  PG所采接口无错误；MySQL candidates有1次409 topology observation changed，
  如实保留此跨快照冲突，不将所有请求写成200或取消一致性保护。
- 三台Agent只移除了旧5432集群的精确policy，其余字段和值逐项比较一致。
  未停止/删除宿主机旧PG、未修其权限使其重新参与仲裁。03:51起的后续日志中，
  旧cluster ID及认证失败均为0；三个reconcile timer active，最近执行success/0。

### 服务器备份

共同父目录：/var/backups/clusterguard/field-repair-20260910，权限0700。

| 主机尾号 | 配置备份 | 离线PGDATA归档 | 归档SHA-256 |
| --- | --- | --- | --- |
| 152 | before-fktbykb6 | pgdata-f_t4_riq/pgdata.tar | fd0f7f928e188ea5098104c9701664d9ce122fbae1e472087bbddfbbd037cc4e |
| 153 | before-ocqjp5d5 | pgdata-b_p_o3lm/pgdata.tar | c5a04004c48e5612f05409095359f03664c2619edbd24d3c0c2da2f5810bc946 |
| 154 | before-4bwqztn1 | pgdata-8_wszvhu/pgdata.tar | ffc90d26b61cd68013da2293e85f5e3c5299e24eee1c960e977389551b1c5725 |

另保留policy-nb4qhi01（152）、policy-ag7zt3el（153）、policy-6u4b18x8（154），
154的旧只读配置与guard清单在sql-fence-txbwrsbb。备份不外传，不自动删除。

### 补丁与边界

生成新的2.2-98到2.2-100签名补丁，不覆盖99。三台现场受信公钥DER SHA-256均为
46ac59a2234263c1410d79e6e60d3961a5835257ea3cc6b0e1383803cae95c65，与签名私钥匹配。
本地签名、篡改拒绝、源/目标RPM、回退载荷、引导脚本与所有SHA通过，包内HTML逐字节
匹配已测试HTML。相对98只改变5个版本化二进制和BUILD-INFO，其余66个载荷文件不变。
152用现场已安装升级器及受信公钥 --inspect 成功，检查后仍为2.2-98。

**未执行补丁安装、页面上传和滚动升级。** 现场恢复通过配置修正及原受控流程完成；
永久的RecoveryStart源码修复和前面审计的前端修复要由用户安装100后生效。
不把本轮结果扩大成PXC/Oracle/SQL Server已完成灾难恢复验收，也未进行业务断电演练。
历史已泄露复制凭据尚未在本轮轮换，不能将此安全待办标成完成。

可复核证据：.build/field-repair-20260910/ 的before、blocked、retry、after目录，
field-verification.json、regression/results.json、artifacts/package-verification.json，
以及带时间戳的plan/execute/status/backup/配置变更/现场inspect/reconcile-check记录。

最终04:00 UTC再次检查三台：两集群仍healthy/2links，六个Swarm服务全部1/1，
MySQL原生状态和容器未改变，测试容器/网络无残留，SELinux模式未改变。
现场临时验签文件已删除，三条本次SSH复用连接及本地空socket目录已关闭清理；服务器备份保留。
