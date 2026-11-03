# 实例自动策略与升级连续性现场验收（2026-11-03）

## 授权、门禁和范围

用户在确认剩余前两项后明确要求“前2个测试下”，授权本轮测试集群实例策略保存、真实数据库故障与切主，以及控制面受控回退/重新升级中的入口连续性。网关本身重启允许断连；HA滚动期间应保持健康writer的授权与入口。测试后恢复原业务主库和等效策略，不删除操作/失败证据。

已完整读取validation-gate-workflow、upgrade-validation-chain、操作规则、修改规则、backend operations、instance-failover、update-actions、update-and-patch和release-recovery-acceptance-checklist。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。适用阶段为既有不可变包的ART复核、现场升级/回退与独立策略行为FIELD；不构建新身份，不将本地回归代替现场。

## 事前基线及验证计划

仓库主线codex/2.2-postgresql，HEAD b03ef95，开始时工作树干净；生产源码c2cc78a/HA3.1.7.1，网关preview.4/af5b0d3。先重读现场策略修订和恢复材料，保存三节点身份、摘要、原生SQL/复制、入口/网关、原策略和审计前缀。与3.1.6.3/2d4b4aa对比：实例策略为新功能，未保存策略仍沿启动默认，旧控制器不支持显式策略，保存后禁止降级旧控制器。因此先在revision0时核验3.1.7.1签名回退备份，正式rollback到3.1.6.3，再正式plan/execute同一3.1.7.1包，避免伪造新修复版本或绕过installed禁止重装。任何备份/基线/门禁不符立即停止，保留失败。

升级连续性：155/156每秒原生TLS SQL、VIP唯一归属、网关进程、授权续期、数据库PID/角色、三投票节点ready/唯一Leader/多数派、维护进入释放与追加历史；逐节点实际程序摘要、最终不同发现周期>=50。网关与数据库不重启。

策略行为：MySQL和PG分别设定显式参与/权重；排除最高权重副本，在安全等价副本中验证较高权重自动升主；旧主经产品恢复后高权重不抢回。自动关闭时停止主库并等待完整证据窗口，不应生成自动切主，随后通过正式手动流程恢复入口。仅停止独立数据库服务，不停止控制器/Agent/整机，不删除数据库文件/日志，不绕过fencing、writer lease或多数派。若出现不确定操作，先按操作ID核对并停止后续故障。最后恢复152原主与原自动开关/参与/权重；显式策略revision保留递增，不能恢复成不存在的历史。

原始证据保留在.build/instance-failover-field-20261103/。当前仅制定计划，尚未执行本轮回退、升级或故障测试。

## 预检实际阻断（13:57起，尚未注入故障）

155/156入口原生SQL失败，151仅有自身IP、两套网关/Keepalived仍active；探针503 gateway_closed。154多数派和数据库writer租约仍有效，路由最后授权停在12:46:19，12:46:24起网关拒绝续期，Keepalived撤VIP。本轮未执行任何回退、策略保存、停库或切主，故障先于本轮测试。

151根分区64GB已满（剩20KB），inode540654/540776；/osr44GB，其中test_out29GB、data10GB。/home为独立32GB分区，剩约15GB。代码Gateway.Apply中持久化Save失败会关闭路由并要求重启，不能通过强挂VIP或复用旧grant恢复。

用户要求先核对/osr并提供可审查方案。只读核对两个历史目录full_coverage_151_20260701_212958与full_coverage_151_20260701_213630各约4.30GB逻辑字节，共77064个文件，最新修改时间分别2026-07-01 21:35:04和21:41:34；全/proc fd/cwd/exe扫描未发现/osr打开引用。候选方案为完整归档到/home/clusterguard-test-archive/，压缩包与源逐文件比对、SHA256成功后才移除指定源目录；不处理数据库/data/mysql/pgdata。清理不属于原故障注入范围，已提交具体方案等待用户确认，尚未归档或删除。首次read-only详情使用bash heredoc因根分区无空间失败，原失败保留，改用python -c只读取得明细。

修复环境后才能重新取得健康基线；前两项目前均未进入实际测试，不记通过。

## 新暴露的源码缺陷及事前对照（修改前记录）

14:00用户明确同意归档、恢复入口并继续。两目录经GNU tar完整数据/属性compare与SHA验证才移除，原件保留/home/clusterguard-test-archive/20261103-full-coverage，根分区剩8.3GB；14:04两网关重启后接受新HA授权、155/156与原生SQL恢复，未重置水位。受控rollback操作0c695210-c64d-4d3d-a566-eb202f856147已完成，三台实际3.1.6.3/2d4b4aa，数据库和入口健康。新plan 7bd02acb-b5a8-487c-a1fb-e9b334a1984d成功，execute aa3e442f-2c72-46c4-b9eb-fddfb2622a75在建立维护锁前被拒；私有status实际failed，公共status仍queued。

旧码对照git show 7b643f4^与7b643f4/current：旧版不导入复制历史；v2新增scripts/clusterguard-update-job.sh import_private_history，无条件要求本机status.operation_id等于复制status.operation_id。实际根私有新计划与复制旧回退各有独立合法ID，但完整根历史能证明新计划继承旧回退，仍被无条件拒绝。保留未知/活跃外包、同操作不同终态/模式阻断；拟只允许根历史能证明的已知终态祖先，合并日志而不改旧操作结果。

第二缺陷internal/platformupdate/helper.go HelperHandler.done在wrapper安装trap前退出时，recordLaunchFailure只写根私有目录，没有发布给原先已固定打开的公共目录，导致API/UI永久排队。拟在失败回调中校验同一patch/mode/operation，使用已固定目录和原子状态发布真实failed；不得覆盖外部/迟到另一操作，不把旧成功改成通用失败。新增实际Bash函数历史测试与Helper私有/公共目录、迟到身份和目录替换行为测试，先红后绿；全Go/race/vet、source/self-test、许可/md后再新身份3.1.7.2构建ART。本轮因此不能重用旧源码绑定报告，也不能将原3.1.7.1失败改成通过或覆盖旧包。完成失败状态的真实发布后，使用新包正式升级，再继续实例策略实测。

## 修复回归与当前恢复步骤

Bash原函数回归证明两条合法终态继承被旧版拒绝，修复后7项通过，仍拒绝未知祖先、另一活动操作、同ID不同模式/终态与外包历史。Helper首次本机夹具被根属主边界拒绝，没有当成缺陷复现；改为Linux root/私有TMPDIR运行同一测试，旧二进制matching和replaced-parent真实留下queued，新二进制3场景通过。补测公共状态保留控制台组可读权限；补测首轮Stat_t类型断言夹具panic已保留并修正，最终Linux3场景和GID检查通过。无安全规则放宽。

为了让新修复候选可走正式升级，按同包同操作ID核对根私有真实failed与公共queued，确认无更新进程及维护锁后，备份原公共/私有文件，用既有runuser原子发布函数同步真实failed、完整根操作历史与原始错误输出；根日志是公共日志的超集。控制台用户实际可读失败，没有伪造成功或清除历史。三台仍3.1.6.3，准备从该真实基线封板新3.1.7.2；本轮尚未保存实例策略。

本次保留历史同操作缺失mode的旧兼容读取，但缺失mode不得证明不同操作的终态祖先；不同已知mode和同ID矛盾终态仍拒绝。既有同操作跨Leader导入回归已复跑通过；新增Bash8项覆盖未知mode不能充当新祖先。SOURCE53、158变异及双对照通过。原Go全仓首轮仅历史夹具分支失败，修正兼容边界后重新跑完整仓库，不把旧失败改绿；最终结果以独立日志为准。

最终源码回归全仓go test ./...退出0、platformupdate race与全仓vet退出0、shell语法/diff检查通过；本机root专属用例明确跳过，但同一用例的Linux root实际旧/新、3分支及公共GID验证已执行并通过，不能把本机跳过算通过。许可68、Markdown11、HTML重建通过。代码将从此已测提交构建3.1.7.2新候选，旧制品59份摘要已冻结。
