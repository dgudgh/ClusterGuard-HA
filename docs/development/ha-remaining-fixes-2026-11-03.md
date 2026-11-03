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


## 新补丁ART（已完成，FIELD待授权）

源码提交6b059e7319ab2a2e7111a757c9022bc9fdbe7b86，单主线已提交；3.1.7.11/revision10以00a3946为base，仅本次生产提交列入fix_commits。正式构建输出release/3.1.7.11/clusterguard-3.1.7.11.x86_64.cgpatch，12088348字节，SHA-256 3230310ce9e9930da8c72076f795acf2043d3ebd714e13b89b0071137b112009。本轮三台只读版本重查仍3.1.7.10；未将此新包安装。

import图及nfpm精确映射实际替换HA、Agent、Helper三程序，重启HA/Helper；升级器与job脚本未变化，不强塞旧脚本制造完整栈假象，现役基线两脚本SHA逐项与源码相同且协议1。cgctl/k8s guard未受影响，不声称其被更新到11。没有数据库、配置或151网关载荷。

包验签、外内摘要、映射/权限/重启单元、生产提交血缘、实际生成回退原子恢复5场景、签名/载荷/工具3篡改拒绝和ART13及artifact strict通过。Linux152的root私有临时目录仅运行实际包内3程序的身份、现役未变脚本协议、合成状态下正向/旧不兼容/未来协议等9项只读载荷检查；没有启动Helper服务/job、写现役程序、改数据库或VIP，不能列为现场升级。此前65源码门禁仍通过。68份原签名包逐项SHA未变。

初轮ART复用了7.10的“5程序+2脚本”假设而失败，随后调用不存在的component-map scripts模式又失败；这两次是验收器准备错误，均保留原日志，修正为真实import图与production路径映射后才通过，未重建签名包。私有台账初轮漏更新patch_version、遗漏旧物理目录声明而红，修正私有输入：10冻结保留与当前11入口分开、旧声明仍完整审计，最终同一完整目录判据全通过，不改门禁/签名原件。Docker守护进程不可用的尝试保留，实际Linux载荷检查另走受控root临时目录。

新本地台账/目录及声明在.build/remaining-ha-fixes-20261103/catalog/；签名包及摘要仅release/3.1.7.11/，未复制下载目录、未公开GitHub。delivery-result/CURRENT-DELIVERY和更新说明明确SOURCE/ART完成、FIELD未做。已向用户提交可审查的包身份，询问是否安装及是否允许主动停PG主库的故障验收；等待明确选择前不安装、不注入故障、不修改策略或清理旧失败。


## FIELD授权与执行计划（开始现场动作前）

用户已明确回复“允许升级并执行故障验收”，覆盖三台3.1.7.11正式通道升级、真实事务写入、主动停止当前PG主库以验自动故障恢复；单网关151不更新/重启。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true；沿用已完整读取契约，重读门禁流程、操作授权和恢复发布验收清单。先刷新三台版本/多数派/维护与活动操作、四库主从/原生身份/两个入口、策略和审计，原配置/程序在各节点root私有目录备份，不复制热Raft目录。新证据目录.build/ha31711-field-20261103/，不复用旧FIELD为新通过。先新业务键有限低速写入覆盖升级，再按独立时间窗暂时启用PG自动策略及权重，停止原PG主库，记录实际自动操作/候选/隔离/提升/重挂和已确认事务。所有UNKNOWN与失败保留；策略以新revision恢复原等效值，不倒退/删除；只走正式恢复入口，不绕过多数派、lease、签名、fencing或维护锁。末尾核对原日志前缀、真实运行载荷、历史投影和50个新发现周期，真实测试不足项维持未完成。


## 3.1.7.11现场执行与反例（2026-11-03）

用户授权后按正式API上传同一签名包、真实Chrome生成只读计划并输入精确ID确认，未改浏览器文件权限。执行operation-7ce278bfe87df5116440ac8d3d0287bc于13:41:56Z开始、13:44:25Z成功，三台实际运行HA/Helper均为3.1.7.11/6b059e7，三载荷摘要匹配、另四程序保持原样，MySQL/PG进程在升级窗口未重启。Leader由154变153，维护释放。升级窗口主代理冻结事务工具覆盖全部时段：MySQL288、PG290个真实COMMIT ACK，无该窗口失败/UNKNOWN/重连，最大相邻ACK间隔0.553/0.531秒。后续主动故障造成的客户端失败单列，不把整个混合场景运行误记为无中断。

DSH/DeepSeek-V4.1-Flash实际调用新可信launcher启动MySQL900秒写入并status；其13:42:51Z才启动，覆盖部分升级窗口181个ACK，不能冒充覆盖全部准备阶段。PG启动在DSH工作区沙箱审批等待期间错过窗口，主代理取消该未执行命令，无扩权限或绕过；PG实际写入由主代理使用冻结DeepSeek工具独立执行。签名、凭据和受保护文件未发送模型，环境只在可信桥内存中使用。

升级后各至少50个不同健康发现周期完成，PG三从均已有真实verified_upstream绑定。策略由原revision3/自动关闭临时CAS至4/开启：153权重900、云库权重1000但不参与，其他100。13:46:54Z仅停152 PG服务；约7秒后产品自行建立9b498513-82ce-447e-bcd4-24dd68439686，选择153，完成旧主隔离、授权、提升可写、156网关与两个从库重挂，13:47:24Z查询到终态成功。没有手工提升/开放只读、改lease或绕过多数派。故障断开旧连接，两套PG客户端分别635及41个ACK、各1个COMMIT前失败后退出；已结束运行经原生四库逐键、payload/两明细/合计核对全部保留，UNKNOWN0，不等于零中断。

策略以CAS新revision5恢复自动关闭/全部参与/全部权重100。正式former_primary_rejoin a171a4e1-a93b-466e-8a11-ec4c2413d6a8完成152 pg_rewind并回挂；新连接经156真实向153写入309笔，四库全部一致。另行正式switchover 49add41f-2996-40e6-9ede-64c0dc2ca222将主库切回152并自动rewind153，四库健康恢复；151网关/隧道PID、启动时间及155/156地址逐字相同。三台原审计前缀/原操作集合保留，原签名包不覆盖。

### 不能关闭的两项

1. **跨Leader完整历史仍失败**：154原requests/status保留旧计划7bd02acb-b5a8-487c-a1fb-e9b334a1984d及失败aa3e442f-2c72-46c4-b9eb-fddfb2622a75；新Leader153仅有更早root operations，API/UI显示3.1.7.1的旧rollback 0c695210-c64d-4d3d-a566-eb202f856147，并缺这两次尝试。读投影在同一Leader一致不等于全节点受理历史完整。原文件未丢但跨节点传播缺口未修，FIELD-011/012不认证通过；首轮校验过早假定全部旧job ID不变的失败亦保留。
2. **旧主回挂时入口二次中断**：309笔新PG运行中13:48:08.928Z发生COMMIT前ConnectionLost，4次连接失败，13:48:29.979Z同一客户端重连至仍为主库的153并继续提交；最大ACK间隔须以最终账本为准。新主保持运行。源码controlledPostgreSQLEndpointMutationActive将former_primary_rejoin纳入整集群暂停Keeper的分支，因而跳过稳定路由续期，与观测窗口相符；这是一条待回归的原因线索，不把时序直接当完整因果证明。没有通过延长授权或手工续租掩盖该失败。

本次只证明对应升级写入窗口和PG自动选择/隔离/提升/复制/路由链。新反例使3.1.7.11整体FIELD不能通过，不公开发布、不宣称全部修好；原ART报告保留，新FIELD报告单独记录失败。.build/ha31711-field-20261103保留各窗口/角色/策略/原生对账和截图，交付目录同步已安装但验收未全通过。

最终恢复后第一次50周期检查在PG累计35个新观察时失败：13:53:56Z云从链路healthy=true但lag_seconds=181；严格断言未放宽，原stability-final.log与失败前JSON保留。13:55:16Z实际新观察三从receive=replay、lag=0；重新启动相同严格条件的50个新周期，不能把首次运行改记通过。这条短暂观测需要继续区分真实WAL排空时间与空闲事务时间差，尚未确认完整底层原因。

收尾：相同严格断言复跑MySQL62/PG50个新健康周期通过，首次失败不改。六套有界客户端均已退出；MySQL1743+1162+DeepSeek实际1742=4647笔、PG635+41+309=985笔ACK，六个独立run UUID均经原生四库逐键/两明细/摘要/合计复核无缺失，UNKNOWN0。PG故障/回挂失败仍不算无中断。新FIELD报告field-acceptance-20261103.json保留FIELD-011/012失败，strict实跑exit1（65源码项，报告拒绝及未完成义务），未把FIELD刷绿；MD11/许可68及生成页构建通过。交付状态是已安装内部测试候选、整体验收未通过，签名包字节不变。
