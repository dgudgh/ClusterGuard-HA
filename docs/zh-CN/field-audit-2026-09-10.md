# ClusterGuard SSH 现场检查记录

2026-09-10。范围：192.168.102.152、153、154。用户明确允许 SSH 检查；本次只读检查现有集群，并在独立临时容器中验证恢复路径。不委派其他模型。

后续用户已授权修复：PG现场恢复、旧policy清理、新发现的只读fence源码修复及50轮验收，
见[现场修复执行记录](field-repair-2026-09-10.md)。本文保留修复前的审计证据，不代表修复后的当前状态。

## 结论

**现场仍有问题，不能宣称全部修复或现场升级已验收。**

| 项目 | 当前结论 |
| --- | --- |
| 现场版本 | 三台实际运行 2.2-98，控制器与 Agent 二进制各自一致；不是 99，也不包含本轮源码修复 |
| MySQL 8.0.44 | 153 为唯一可写主库；152/154 为只读副本，复制线程 ON，GTID 一致；50 个不同后台观测均 healthy、2 links |
| Swarm PostgreSQL 16.4 | **当前无主库**：152 服务为 0/0；153、154 都是 standby，均无 WAL receiver；50 个观测均 degraded、0 links |
| PostgreSQL VIP | 三台均未持有 192.168.102.157；MySQL VIP 192.168.102.156 仅在 153。未放松 fencing 或人为恢复入口 |
| Raft / API | 152 为 Leader，三台最终返回相同观测时间及主从/健康/link 状态；没有观察到本轮 API 或 follower 丢失已生成 link 的证据 |
| 宿主机旧 PG 配置 | Agent 保留一个已不在活动集群清单中的 5432 集群；152/154 周期性认证失败。凭据文件父目录 root:postgres、0700，postgres 无遍历权限 |
| 已定位代码缺陷 | PG 无可写主库时错误进入 MySQL reboot bootstrap，产生误导的“实例范围无效”；已在源码中修正，现场未安装 |

## 现场数据库对照

Swarm PG 集群：`8938f553-c7ac-4589-aaf2-5d6823c4cc7e`，system_identifier `7678901569924304935`。

| 节点 | 原生证据 | API 证据 |
| --- | --- | --- |
| 152 / pg01 | Swarm 0/0，停止态只读取证通过；Timeline 13，redo 0/9032AF0，end 0/9032B68 | unreachable / unhealthy；历史引擎元数据不能当当前可写证据 |
| 153 / pg02 | pg_is_in_recovery=true；Timeline 13；receive 0/9000000，replay 0/9032A78；无 receiver | standby / degraded |
| 154 / pg03 | pg_is_in_recovery=true；Timeline 13；receive/replay 0/9032B68；无 receiver | standby / degraded |

pg02/pg03 的运行期 primary_conninfo 都指向已停止的 `152:55432`，application_name 分别匹配其原生节点 ID；连接配置未内嵌明文口令，使用受保护凭据文件。只导出 host/port/application_name 等允许字段，未输出原始连接串。

本轮不能沿用之前“数据库已恢复但 links 为空”的现场结论。当前没有主库和 streaming，空 links 与原生现状相符。不能仅凭最大 LSN 启动节点，也不能把历史 Recovery Commit 成功当作当前恢复完成。

另外，宿主机 `5432` 的 system_identifier 是 `7677239728205718297`，与 Swarm `55432` 不同，必须分别处理。旧 Agent 集群 ID 为 `baf1e092-0e44-4e74-889e-8092d92df831`。本轮未擅自删除它或修权限使其重新参与自动收敛。

## 性能与一致性

50 轮使用自然后台观测，不调用手动 discover，不做写操作。共用时 116.776 秒，每种引擎收集 50 个不同的 observed_at。指标为服务端通过 TLS 请求自身 API 的耗时，不包含用户浏览器网络、扩展或绘制时间。

| API | MySQL P95 / 最大 | PG P95 / 最大 |
| --- | --- | --- |
| topology | 207 / 255 ms | 27 / 34 ms |
| health | 179 / 217 ms | 18 / 30 ms |
| candidates | 44 / 189 ms | 22 / 33 ms |
| operation context | 21 / 58 ms | 22 / 28 ms |
| 日志首屏 | 26 / 37 ms | 31 / 44 ms |

PG candidates 返回 409 的原因明确为“需要恰好一个当前主库”，不是超时。MySQL 个别观测跨越后台发布边界时返回 `topology observation changed`，这是同一快照检查，不能删除。三个控制节点最终返回的两个集群 observed_at 完全一致。

另对三台各进行 10 轮详情、metrics、power/status、operation context，共 240 次 GET：全部 HTTP 200，最大 193 ms。**本轮没有复现“PG 后端接口一直很慢”，不等于历史卡顿不存在。** 首屏被无关后台请求阻塞的前端缺陷见总计划 AUDIT-02，已在源码修复，但现场仍为 98。

用脱敏现场拓扑回放实际 HTML：1440/390 两个宽度均通过。PG 无主显示 partial/degraded、按钮锁定、候选只请求一次而不反复重试；切回 MySQL 得到 2 links。共 4 项通过，0 个修改请求；这是本地浏览器回放，不冒充现场浏览器部署。

## 修复与测试

### AUDIT-04：PG 误入 MySQL 重启检查

对照 `bc0546a` 与 `19d02bc`，相同缺陷均存在于 `internal/coordination/ownership_keeper.go`。先添加失败复现：PG 两个 standby、完整 VIP 观测且 canonical 一致时，原逻辑报 `reboot bootstrap instance scope is invalid`。

修复仅限制 fallback 的引擎范围：只有 MySQL 进入其 reboot bootstrap；PG 无可写主库时继续拒绝 writer/VIP 授权，并明确要求受控恢复。测试同时覆盖健康 PG 正常续租、异常 PG 无 lease、无 owner commit。没有新增“自动选最大 LSN”、修改 fencing 或恢复现场数据库。

### 原先跳过的五项已补执行

| 用例 | 结果 | 边界 |
| --- | --- | --- |
| MySQL ActualGuardedClone | 通过，60.07 s | 物理克隆、业务入口隔离、重启及 UUID/GTID 验证 |
| MySQL ActualThreeNodeRelayDrainAndSelection | 通过，53.37 s | 追平 relay、持久化只读、重启保持 fence、独立提交冲突、prepared XA 拒绝 |
| MySQL ActualExecutorRebuild | 通过，59.18 s | 真实执行器完成 clone、验证身份/GTID并保留测试业务提交 |
| PostgreSQL ReadOnlyStoppedDockerEvidence | 通过，6.74 s | 152 上已停止 pg01，读取稳定指纹/控制文件/WAL并拒绝旧指纹；不启动数据库，挂载只读 |
| EntrypointPreservesDynamicPostgreSQLRole | 通过，0.67 s | 已有备库保留运行期 upstream，已提升主库不被旧 Bootstrap Env 恢复为从库 |

前三项在 153 创建随机命名的 MySQL 网络/容器；使用现有镜像，不发布端口、不挂载业务目录、不注册进平台。Docker Clone 的自动重启返回 3707 后，测试继续验证实际克隆、受控启动和复制结果，并非把 3707 直接记为成功。

启动脚本测试第一次失败于 SELinux 拒绝临时 bind 目录。修复的是测试夹具：复制脚本到专用临时文件、仅给临时挂载设置独立标签、禁网、禁止拉取镜像、限制 CPU/内存。未关闭宿主 SELinux，未重标记源码或业务目录；首轮失败日志保留。

修复后再次运行全仓：2097 个测试及子测试通过，耗时 364.993 秒；本地仍跳过的五个容器用例已按上表分别在服务器执行通过，另外四个 package skip 为无测试文件。API/coordination race 423 项通过，耗时 11.499 秒；go vet 通过。结果记录于 `field-regression/results.json`，不与重复执行的测试数相加。诊断脱敏 3 项、现场数据浏览器回放 4 项另有记录。此前前端全套验收记录仍保留；HTML SHA-256 仍为 `b94a46042fac672a0dce56ea8cfef73238f5743b6faa81cb96e98420811b0309`。

## 安全与环境问题

- 5432 凭据文件本身是 postgres:postgres、0600，但父目录是 root:postgres、0700；三台 `runuser ... test -r` 均失败。现有安装/同步脚本明确设 0750 并检查可读性，尚未证明是谁随后改变了目录模式，不归因于某次提交。需先确认该旧集群是否应退役，再修配置或停用旧 policy。
- 152 使用本地时钟源且 NTPSynchronized=no；153/154 跟随 152，采样偏差微秒级。不能把时区不同直接当时钟漂移；外部基准与失去时钟源场景仍需验证。
- 检查时 154 的 SELinux 为 Disabled，152/153 为 Enforcing。本轮未调整任何主机安全模式。
- 诊断输出包含结构化及自由文本脱敏，新增 3 项测试通过；历史已泄漏的复制凭据本轮未旋转。完成状态核对后仍需安排轮换。
- 旧 99 安装包是历史制品，不包含本轮修复；不覆盖旧版本，也不把此次只读审计当滚动升级验收。

## 清理与证据

临时 MySQL 容器/网络、entrypoint 容器均已清理；三台检查返回零残留。152/153 上传的测试二进制和专用临时目录已删除，三条本次建立的 SSH ControlMaster 连接已关闭。结束时 MySQL 服务仍均为 1/1，PG 仍为 0/0、1/1、1/1，与检查前一致。现有 MySQL GTID、VIP 未改变。

证据根目录：`.build/end-to-end-audit-20260910/`：

- `field/{152,153,154}-{inventory,api,native,bindings,logs,refresh,cleanupcheck}.json`：脱敏原始只读证据；重采集前版本保留时间戳副本。
- `field/152-observations.json`：50 轮观测与最终跨控制节点对照。
- `field/mysql-isolated.json`、`mysql-isolated.log`：真实 MySQL 执行记录。
- `field/pg-stopped-readonly.log`、`pg-entrypoint-isolated.log`、`pg-entrypoint-isolated-after.log`：PG 验证及夹具失败证据。
- `field/pg-bootstrap-before.log`：AUDIT-04 修复前失败复现。
- `field/browser-replay.json`、`pg-no-primary-{1440,390}.png`：浏览器现场数据回放。
- `field-regression/results.json`：修复后全仓/race/vet 重跑。

## 未闭环项

1. 现场 PG 尚未执行灾难恢复。需要全节点维护冻结、完整 Timeline/WAL/COMMIT 证据、唯一权威分支及可回退方案，再执行受控恢复；最后验证 Recovery Commit、VIP/lease 和至少 50 轮健康。
2. 旧 5432 Agent policy 与活动清单不一致，需确认保留或退役；不能只修目录权限就让旧实例重新参与仲裁。
3. Linux 全新安装、现场上传验签、滚动升级、断电/网络分区没有在本轮对业务集群执行。要在可丢弃环境验证，或另行确认维护窗口与回退边界。
4. Oracle/SQL Server 本轮仅完成页面/适配器契约测试，无真实数据库恢复验收；PXC 专用恢复尚未实现。

只有这些边界明确后才能给出对应范围的交付结论，不能用单测总数代替现场恢复成功。
