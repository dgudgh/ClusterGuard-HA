# 设置：运行参数、编辑与节点下发

[返回设置接口索引](README.md)

## 职责与边界

自产品功能版 `3.1.2.1` 起，控制台可编辑白名单中的25项整数调优参数，按参数作用范围选定控制节点后下发并滚动重启。HTTP/TLS、节点身份、Raft成员、数据目录、凭据、执行路径和隔离启停不能通过通用表单修改，必须进入对应专用变更流程。动态策略已经覆盖的参数不能通过启动默认值编辑器修改，须使用策略面板；策略摘要绑定预检且下发期间冻结，避免重启后继续被另一值覆盖。故障窗口须容纳所需观测，Agent命令超时不得超过变更超时。已有[引擎级集群策略](cluster-policy.md)仍通过Raft逐轮动态生效，不需要重启。

`GET /api/v1/control-plane/configuration` 仍只展示当前节点进程启动时加载的配置；“重新读取文件”只刷新显示。`reload_supported=false`，不能将文件保存或任务提交描述成运行时热加载。

## 按需进入实现

| 要修改的行为 | 实现与回归 |
| --- | --- |
| 有效值、凭据引用与来源投影 | [configuration.go](../../../../internal/api/configuration.go)、[configuration_view.go](../../../../internal/runtime/configuration_view.go)与各自测试 |
| 25项编辑白名单、范围、候选校验与原子覆盖 | [distribution.go](../../../../internal/config/distribution.go)、[distribution_test.go](../../../../internal/config/distribution_test.go) |
| 预检、节点顺序、实际生效回执、失败重试及回退 | [configuration](../../../../internal/configuration)、[manager_test.go](../../../../internal/configuration/manager_test.go)、[真实Raft回归](../../../../internal/configuration/raft_test.go) |
| Raft任务、CAS、不可变主体与原子审计 | [configuration_tasks.go](../../../../internal/store/configuration_tasks.go) |
| HTTP鉴权、严格解码、Leader转发和门禁 | [configuration_distribution.go](../../../../internal/api/configuration_distribution.go)、[对应测试](../../../../internal/api/configuration_distribution_test.go) |
| 固定控制面服务重启与软件升级互斥 | [configuration_restart.go](../../../../internal/platformupdate/configuration_restart.go)、[对应测试](../../../../internal/platformupdate/configuration_restart_test.go) |

## 参数作用范围与节点版本

字段元数据的`scope=cluster`要求全部当前投票节点：引擎发现、自动切换、Agent命令/变更时限、隔离等待和控制证书有效期，共23项。`scope=node`仅允许`consensus.apply_timeout_seconds`与`agent.max_concurrent_sessions`两项选择子集；任一时刻其他在册投票节点未就绪仍禁止启动任务。不涉及数据面Agent自身配置刷新。

新计划要求每个投票节点的配置接口`schema_version=2`；混用旧接口拒绝预检。已持久化旧任务仍保留原主体、历史和180秒时限，不把旧任务偷偷改成新的全节点范围。

## 请求与任务

所有新接口位于 `/api/v1/control-plane/configuration/`：

- `GET node`：本节点真实启动参数、启动时加载的覆盖任务ID、就绪与重启能力。
- `GET distribution`：当前Leader提供实时成员和任务历史。页面另读本节点`node`，不把Leader的本地值冒充当前节点。
- `POST candidate`：每个节点只读候选校验。保留管理员/控制令牌及会话CSRF校验，仅不要求本节点是Leader；没有写入或重启动作。
- `POST plan`：选定`node_ids`和类型为整数的`changes`，Leader确认多数派，检查所有当前投票节点的新接口、配置摘要、就绪和Helper能力，逐节点完整校验候选配置，返回差异、逐节点超时`step_timeout_seconds`及`hash`。
- `POST dispatch`：携带同一`changes/node_ids/plan_hash`及UUID `request_id`；重新预检再CAS创建任务。同一UUID与原主体重复提交返回同一任务，主体变化拒绝。
- `POST permit`：节点保存和重启前向当前Leader确认任务ID、revision、目标及其他投票节点就绪。缺少多数派、任务/成员变化或软件维护中不得保存或重启。
- `POST tasks/{task_id}/retry`或`rollback`：明确任务及当前`revision`。只恢复保留门禁的失败任务，不能恢复完成任务或按“最新记录”猜主体。

## 持久化与生效

配置任务、节点原值和摘要进入Raft复制存储，操作审计与任务原子提交。配置任务与软件升级、运行中数据库操作、节点生命周期、灾备执行和电源生命周期互斥；自动切换在配置门禁期间暂停。Follower先重启，当时的Leader后重启；Leader变化继续原任务，成员变化停止并保留门禁。

原配置文件不修改；服务将普通整数覆盖保存到`metadata_path`同目录的`runtime-configuration.json`。覆盖层有严格schema、白名单和摘要，原子写入并同步；每个任务的前态备份不可覆盖。`config.Load`在启动时合并原文件与覆盖层，再执行正常默认值、凭据读取和配置校验。非法覆盖阻止启动，不能忽略后声称正常。

参数下发复用软件升级Manager实际选定的Helper客户端，支持其自定义socket，不另选默认路径；非Unix/缺失能力拒绝下发。受认证Unix Helper仅接受固定`clusterguard-ha.service`重启，不接受任意单元、命令、路径或载荷。Leader读到新进程实际加载的任务ID与参数，且节点已就绪后才确认该节点完成。仅写文件、发送重启请求或提交Raft不是完成证据。

写后再次用真实`config.Load`及运行时验证器检查完整候选；本次发布的候选失败时，先保存`configuration-<task_id>.rejected-<digest>.json`，再恢复之前覆盖（原先不存在则恢复为不存在），不重启，仍保留失败门禁。恢复前再次核对文件归属；外部修改、证据保存失败或恢复失败时拒绝覆盖，保留证据和错误。已存在的坏文件不因这一修复被忽略。

失败或逐节点重启/核验超时保留配置门禁。启动配置显式支持`configuration_distribution.step_timeout_seconds`，整数30..3600秒，缺省180秒；加载后提供实际默认值。该项属于本地引导配置，不能通过25项下发表单自改。计划摘要包含时限，任务持久化后时限不可变，Leader切换、重试和回退不改时限。确认框显示计划实际时限；修改本地启动配置需要专用滚动重启。旧任务缺少该字段时仅为兼容使用原180秒。

```json
{"configuration_distribution":{"step_timeout_seconds":600}}
```

上例是合入原配置的片段，不是完整配置。现场负责人应按systemd停止、启动、选举、追平和读回耗时确定窗口；默认数值不代表已通过现场验证。该启动键仅由新版本识别；回到不识别该键的旧二进制前，应恢复其兼容的原配置备份，不能放宽未知字段校验。

维护门禁不仅锁住表单，也阻止其他控制面变更；仅任务恢复/permit/幂等dispatch等明确路径例外。重试继续同任务；回退恢复各节点各自覆盖前态，未触碰节点保持原值，实际读回核验后才解除门禁。原始文件被外部改变、备份损坏、成员变化或回退不完整不得强行清锁。

## 来源与验收

`file`表示原文件包含该键；`default`表示启动默认值；`policy`表示Raft动态策略；`distributed`表示新进程已经加载的节点覆盖。凭据仅显示环境变量或引用路径，永不返回秘密内容。

源码单元、真实Raft/HTTP隔离回归、浏览器、签名制品和生产现场分别记录。Raft测试中的进程配置重载不等于生产systemd重启验收。前端任务再读[参数编辑与下发页面](../../frontend/settings/configuration.md)；生产验收仍需节点实际重启、服务就绪、多数派和逐节点参数读回证据。

## 首次现场验收（FIELD未执行时保持OPEN）

1. 确认全部控制节点已安装支持schema 2的新版本，无升级/配置维护或待复核操作；以真实节点多数派及服务健康为准。
2. 在窗口开始前备份原配置、存在的覆盖文件及任务前态文件，记录“覆盖不存在”而不创建空文件；备份保存在受保护的节点本地目录。
3. 在实际配置的Helper socket通过认证路径读取`GET /v1/configuration-restart`；不能用fixture输出替代。记录systemd停止/启动、Raft选举/追平的耗时。
4. 先选一项允许的本地调优测试，核对计划差异和时限，授权下发；逐节点保存真实进程启动时间、任务ID、数值、服务状态。集群级字段必须全体投票节点。
5. 独立确认多数派、VIP、数据库writer与复制健康、最终维护释放；失败时保留门禁，按指定任务revision执行retry或rollback，并逐节点核验前态。

开发及制品验收命令见[私有制品门禁与可复跑浏览器](../../delivery/hotfix-catalog-validation.md)。生产节点重启、参数读回与数据库健康必须另留FIELD原始证据。
