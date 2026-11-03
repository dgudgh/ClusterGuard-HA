# 三项HA缺陷修复（2026-11-03）

CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。已完整读取契约v2、validation-gate-workflow、change-policy、operation-safety及后端operations/recovery/settings实例策略、升级动作与历史功能页。任务：修复升级断写、PG源失联自动候选、跨Leader历史摘要；用户明确暂不处理单网关。

进入唯一主线codex/2.2-postgresql、干净HEAD22c5338，现场上一轮三台3.1.7.10/00a3946，RPM2.2-113，未将本轮源码当现场已升级。适用SOURCE：先红后绿的真实API/发现/候选与Raft时序集成、相关race/vet、全Go、门禁strict/self-test变异和文档/许可；如进入新制品或现场，另外读交付/恢复验收清单并使用全新身份完成ART/FIELD。旧包/失败证据不改。原主停库、切主或重启等现场故障验收须在明确维护授权及健康预检后执行，不能以源码通过关闭业务FIELD。

## 编辑前旧版对照

`git diff 9bb5047..22c5338`确认coordination/ownership_keeper.go、lease.go、api/agent_reconcile.go、postgresql/topology.go和platformupdate/history.go相关行为未因7.10改变。历史c8795a3引入的PG双端同轮streaming核验在主失联时清空SourceIdentity且不保留可核验来源，不能恢复为仅信primary_node_id的旧配置推断。旧7.7实测停主330秒无自动操作，原始记录instance-failover-field-2026-11-03.md及.build/instance-failover-field-20261103/保留。

升级：旧7.7真实事务期间最后HA重启后两库Agent release_and_read_only；PG失去租约后停服，MySQL持久只读。完整底层原因当时未确证。当前RequireMutationAuthority只VerifyLeader、不Barrier；agentReconcileRoute在其后立即读仓库并签隔离，OwnershipKeeper亦先读再在写入时同步。新Leader继承已提交但未应用的租约/发现状态可出现空读，拟以受控滞后FSM/实际API复现后修复一致读时序；不延长旧permit、忽略撤权或让维护标记替代多数派。不能把这一可复现缺陷直接宣称为旧现场全部根因。

历史：`git show 36fbf19^:internal/platformupdate/history.go`与当前均按文件枚举覆盖同ID快照、只按StartedAt排序；Manager.Job读本机status投影，Operations读追加文件，终态时间可与原事件不同。7.10现场12旧包事件内容/顺序保留，但7.1旧status选择回退而新Leader选择较后失败，完成时刻亦不同。拟使用同一已验证操作历史投影、同身份终态事件的确定性时间，保留原始记录与部署独立状态，拒绝同ID矛盾终态，不凭文件顺序/墙钟选权威。

## 不变量

维护仅阻断新自动操作；原有效租约正常续期。失去多数派、授权过期、撤权、角色/身份/拓扑冲突继续隔离。PG源失联候选必须绑定真实历史双端证据、当前库存/原生系统/时间线/WAL及源失败事实；不能凭GUC、未知lag或未追平WAL提升。历史追加原件不改，不用任意最新包替代用户对象。


## SOURCE修复与实测

1. **一致授权读取**：Agent API与OwnershipKeeper在读租约/拓扑前要求真实Raft Barrier及前后term/Leader/quorum核验，失败返回不可用，不签发基于重放前空状态的隔离决定；请求有效期在同步后重验，原10秒permit及真实租约期限不延长。实际旧API两引擎红例均200签self_isolate；新API继承租约应用后签keep，Barrier失败503。真实三节点Raft的FSM被故意阻塞时，多数派校验仍通过而一致读等待并受调用者deadline限制，解除阻塞后通过，断多数派仍拒绝。Keeper对应滞后读与失败不观测/不续租回归通过。这修复已复现的时序路径，不能直接等同旧16:22事故全部底层原因已确认。
2. **PG来源丢失**：同轮真实双端streaming成功时记录来源绑定。探针只在数据库内对primary_conninfo计算SHA-256，返回指纹，不读取连接串/口令原文；实现使用PostgreSQL原生sha256/encode（[PG16官方函数](https://www.postgresql.org/docs/16/functions-binarystring.html)）。失联后仅保留原生身份、系统、timeline、来源/目标注册端点与配置指纹均相同的历史绑定，并重查当前SQL只读recovery、replay未暂停、receive=replay、lag=0、WAL不倒退；端点移动/删除或重新绑定不复用历史。不伪造healthy或在线链路，不单信primary_node_id。实际执行前再次读目标SQL及指纹。新增10组发现/候选边界与执行期配置变化正反例通过。
3. **历史投影**：原文件不改；按根operations与受理requests的首见顺序约束组成因果顺序，不让晚到旧快照改变操作先后。本机status仅在明确因果证明新操作较后时替换投影；无关系旧记录的墙钟仅作显示排序，不能换当前对象。相同操作的原终态事件时间用于Job/Operations统一显示，wrapper较晚完成时间仍留原始记录。矛盾模式/终态或因果循环进入verification_required并阻断新任务；原部署结果独立。正常轮询的历史折叠按文件inode/大小/mtime/权限缓存，动作前仍重扫，不引入每次轮询全日志扫描。真实不同peer请求子集、逆向墙钟、晚到快照、时间投影、原件不改、冲突不启动Helper等回归通过。

原生隔离PG16三实例以pg_monitor普通账号完成50个不同发现周期、暂停/恢复WAL；随后真实停掉本地主库，等两从pg_stat_wal_receiver消失，两从排空WAL后连续3轮均通过真实发现→候选评估。空与错误bootstrap GUC不影响绑定；未在该原生实验提升从库或触碰现场四库。三节点Raft、API和原生PG是本机隔离运行，不是三台现场升级/故障验收。

最终验证日志在.build/remaining-ha-fixes-20261103/：全仓Go（显式CG_PG16_BIN，原生PG实际运行）、6包race、vet通过；SOURCE65通过、静态变异201全捕获/双对照，8项真实Go行为变异全捕获/5个原样对照及无关注释对照通过。编译/夹具准备失败、自测漏复制执行源码导致原样对照失败、初轮变异unused变量失败均保留，修正准备后才计行为红/绿，不把编译失败当安全变异捕获。历史因果实现迭代先暴露原Helper失败投影回归，最终补因果DAG与完整/部分节点日志对照修复，未删旧失败历史。

已读delivery/README、release-checklist、public-release、licensing、版本/热修命名规范及恢复发布验收清单。拟新身份3.1.7.11（revision10），以现场7.10/build00a3946为基线；通过源码门禁后才构建，ART/现场另记，旧签名7.10不重建、不公开上传签名包。真实写入升级窗口、PG自动停主/提升/重挂、升级后历史多Leader页面一致性仍须新版本FIELD，不用本次源码/原生候选通过关闭。
