# 实例自动策略与升级连续性现场验收（2026-11-03）

当前阶段结论：MySQL 实例策略故障行为按下文版本分阶段实测；较早HA更新窗口的SELECT结果保留。**新增DeepSeek真实事务写入复测失败**：最后控制节点重启后，MySQL 716笔、PG 717笔确认提交后均提前退出。四节点逐键核对全部确认提交一致，只证明保留，不证明业务连续。PG停主后自动候选源身份丢失的独立缺陷也仍未修复。两个问题均不得借当前健康、源码或原升级FIELD结果代签。

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

## 3.1.7.2实际升级与实例故障新发现（修改前记录）

新包3.1.7.2/d0e5637，17522997字节，SHA256 76dd40d9bcaaa05037bc44a01dd3765d332edb94754fcdc0a80e44b3a4a1e06f，6件载荷含5程序与job脚本。ART13/篡改3/回退原语5通过；复用仅含二进制的验证器时两次错误假设脚本0755目标/载体一致，已对照nFPM修正为载体0755、签名目标0750，未重建包。私有台账初次旧声明superseded与新四段目录检查冲突，按明确撤回候选的frozen历史声明保留7.1完整审计和已知功能缺陷，当前7.2完整基线覆盖不变；全部43判据通过后才现场上传，纠正过一次提前的通过口述。

正式plan 6cc3f029-091c-4928-a013-6dc1ba031d59，execute 4bef8faf-5220-4220-9fc5-47bf1f2ab4f4，14:27:09~14:29:39成功。三节点实际进程及6文件摘要、job脚本root/root/0750、数据库PID不变、维护释放通过。升级窗口MySQL/PG各141次原生入口SQL全通过，145次VIP/就绪采样均正常且网关PID未变；此前受控回退窗口各82次SQL全通过、86次VIP正常。旧轮询器没识别rolled_back，后续本地状态文件被新plan读回覆盖；原append-only采样保留，以唯一rollback操作ID从原始采样提取真实终态，不改现场历史。

MySQL新策略保存与三节点读回成功：云副本权重1000但排除，153权重900、154权重100，152恢复后权重1000。仅停152数据库后自动选择了153，连续操作在old_primary_fenced预检阻断，没有提升或新的过渡lease。现场失败保留，先暂停自动、按只读启动保护恢复152，程序自行恢复唯一writer和入口，再恢复原自动开关/当前四实例权重100；显式policy revision3保留，不能恢复为不存在的历史。

事前对照d0e5637的internal/coordination/failover_safety.go与既有gateway手动切换：activeVIP已接受gateway资源，但Fence/probeIsolation/signedRequest仅允许LinuxVIP，导致已配置无本机VIP Agent的gateway故障接管无法使用数据库隔离。Agent本身已有无VIP self_isolate：MySQL持久只读与停库原生证据，PG原生停服；请求仍签名、engine/cluster/plan/lease绑定。拟仅将这条受限且可达的Agent直接隔离路径接入gateway，清空VIP/interface/prefix并校验Service端点；不开放K8s/external_command，不把gateway加入失联Agent的授权超时兜底，不跳过外部隔离要求。新增两引擎正向顺序/签名及无多数派、失联Agent、可写重启、仍运行PG等拒绝测试，先红后绿；新修复使用3.1.7.3，基线为现场7.2/d0e5637，不覆盖7.2签名字节。

新gateway直接隔离路径源码回归：全仓Go、coordination/recovery/workflow race、全仓vet通过；SOURCE54、161变异与双对照通过。两引擎10场景拒绝/成功矩阵通过，保留旧源码失败日志。仅源码阶段通过，尚未构建或现场验证3.1.7.3。

## Agent真实命令链第二缺口（修改前）

3.1.7.3/211c22e正式plan322d58dd-e130-45d6-8ead-730718c202e3、execute db506167-bf4e-4e1a-8a55-b8cec822dc87已成功。实际仅HA载荷，其余程序和DB PID不变；执行窗口两入口各117次全成功、121次VIP/就绪检查正常、网关PID未变。后验首次错误假定--version-json输出完整commit导致本地断言失败，按既有七位输出与清单full hash及实际文件摘要绑定后通过，未更改现场。

第二轮MySQL停152服务后仍被old_primary_fenced阻断；签名只读Agent真实请求定位vip_status调用Linux ip dev空接口返回Device does not exist，而role_status正确证明停服、restart_read_only=true、persisted_read_only=true。此前对网关NoVIP实现存在的判断错误：只有gateway reconciler绕过VIP，生产newRuntimeControllers的命令路径仍直接LinuxVIPController。已有coordination夹具隔离了该真实构造链，所以源码通过不能证明FIELD。自动脚本遇Leader重选已退出，保留全部blocked操作；暂停自动、验证只读重启保护后恢复原152，再恢复等效策略，修订递增不清空历史。

事前git show d0e5637:internal/agent/vip.go与211c22e相同；拟对明确gateway、MySQL/PG且VIP/interface/prefix全空的数据库策略，使Status/Release不操作本机VIP，Acquire始终拒绝；错误混合VIP配置仍拒绝，Linux路径不变。新增生产newRuntimeControllers+实际Service+真实原生角色包装的签名命令链测试，两引擎先红后绿，防止再用纯transport stub遮蔽接线缺陷。新身份3.1.7.4，保留7.3失败和签名包，不放宽数据库隔离/多数派/租约。

真实命令链两引擎/启停4场景旧码全红，修改后通过；8项混合配置/引擎拒绝和既有原生VIP回归通过。全仓Go/vet、Agent生产构造与Agent/coordination race通过，SOURCE54、164变异及双对照通过；许可68/Markdown11/生成页重建通过。尚未把3.1.7.4源码检查当现场通过。

## 半同步真实插件方言缺陷（修改前）

3.1.7.4/c106a2f已通过正式通道完成两程序HA+Agent更新，13ART/54SOURCE/43私有台账通过；计划dd50ca36-bf76-4df1-80d0-b2b27c8893bb、执行ed0ae80a-b3d3-495b-bbaa-b0eebe2ab154成功，执行窗口两入口各120次全通过、124次VIP/就绪正常、网关和DB PID不变。第一次后验发生在任务尚运行的维护释放阶段，正确被断言拒绝；终态后重新取样通过，没有跳过维护检查。签名原生Agent实际vip_status/role_status均ok。

MySQL新故障已真正隔离152并按权重提升153，随后c1253ba3-4dfe-4f9a-890f-8c2db537420c在activate_failover_semisync_source收到1193，目标被保守重新隔离、入口withdraw，结果indeterminate；另一早期774d2095操作因正常观测变化stale_plan阻断，保留。原生SQL证明153/154/云均read_only/super_read_only=1，152仍停服且持久重启保护成立，没有双写。按正式review记录已核验关闭态且不修改indeterminate结果，暂停自动，准备签名代码修复后按原计划受控继续。

事前对照adapters/mysql/semisync.go的activateSemiSyncSource按dialectForVersion.ModernSource选source变量；parseSemiSync其实已接受master/slave真实变量。生产cnf与现场SHOW VARIABLES均只有master/slave插件变量，8.0.44不代表安装了source/replica插件。拟把启停变量名绑定到实际读取的完整source配置族，而非服务器版本；不改现场cnf、插件或半同步要求，不吞1193或放宽ACK判据。新增8.0旧插件和现代插件启停、配置族不完整/冲突拒绝及activation故障重新隔离回归，3.1.7.5新身份。

半同步变量修复全仓Go/vet、MySQL/workflow/recovery race通过，55SOURCE与167变异/双对照通过；旧8.0旧插件1193及双插件歧义先红后绿。许可68、Markdown11和生成页通过。3.1.7.5将在MySQL已确认隔离关闭、自动暂停、旧操作已正式复核且仍保留indeterminate、PG健康的明确基线中正式升级；不能把当前MySQL断连归入HA升级连续性样本，也不能当作全站健康基线。

## 网关旧主恢复身份错误（修改前）

3.1.7.5/1dec471正式plan a21e27f2-3707-4df8-863d-12ba2c7d8922，execute1d855385-aded-4fc4-bdff-a6f95919f688成功；按事前隔离关闭基线核对三节点文件、其余文件/DB PID、PG入口及多数派通过。MySQL原c1253ba3自动操作在重新开启原策略后按原不可变计划受控续跑，真实完成半同步激活、入口迁移及最终验证，记录成为succeeded，原indeterminate报告/复核/失败原始证据保留。原生155 SQL证明153 UUID且可写，网关授权稳定，旧152经ON只读保护启动。

恢复旧152的正式approval仅规划、未授权执行：7d8bb6ac-2022-4bca-8711-15e8ad8e4eb7所有身份/只读/GTID/binlog检查通过，仅former_primary_endpoint_absent失败。根原文为former primary still owns a gateway route。事前对照1dec471:internal/endpoint/gateway.go，FormerPrimaryPrecheck错误比较resolved.Primary（当前新主），而MySQL rejoin和既有LinuxVIP约定resolved.Target才是待恢复旧主，故健康新主路由必然误拒。

拟要求有效且不同的当前主/旧主身份，完整观测仅当前主拥有路由、canonical/endpoint均为当前主，稳定多数派租约与所有网关精确grant验证；待恢复旧主仍不得拥有入口，未知/部分观测/无路由/第三方/过期/transition均阻断。补实际GatewayProvider与生产LeaseStore回归先红后绿，新身份3.1.7.6。没有直接改复制SQL、写权限或历史去绕过该预检。当前153为真实可写主、154和云健康副本，152保持只读未挂回；第一项完整验收仍未通过。

网关旧主恢复9场景先红后绿，完整Go/vet和endpoint/MySQL/PG race通过；56SOURCE、170变异与双对照、许可68、Markdown11/生成页通过。实际新基线为MySQL153可写且155入口已恢复、152只读等待挂回，PG152原主与156健康。后续HA滚动连续性样本将记录真实MySQL153身份，不能沿用152旧identity断言。

## 关闭自动后的手动故障源解析缺口（修改前）

7.6正式更新成功（plan d228167a-fad6-4431-9577-c883320a0439，execute5e8400bc-8ac8-4a37-b856-27ffd0171c51），两入口各122次原生可写身份SQL零失败、127次VIP/就绪正常、网关/DB PID不变。实际旧主恢复b33e7ce1与计划回切3719bb3f均成功、原策略修订10。完整新MySQL用例再次自动选择153，排除权重更高云节点，旧主恢复1789c7d0成功；40秒高权重152健康后不抢回和50秒关闭自动不生成切主均通过。

随后手动failover的approval在discover/resolve阶段失败，b3bb0caf-d531-45b3-8ab7-cb9a3db0f9ee未形成plan/approval/执行或步骤，不能假装手动分支通过。事前对照a2808e1:internal/workflow/resolver.go只为自动SourceID允许当前失败源；停止主库后发现清除Role为unknown，手动无SourceID还要求RolePrimary恰好1，必然拒绝。保持自动关闭修订12，通过ON启动配置及mysqld-auto.cnf检查只读启动153，等待产品既有稳定所有者恢复，不直接改写SQL权限或复制。

拟仅手动failover且零主库时，从唯一激活HA入口的权威OwnerID与Endpoint.InstanceID一致性确定故障源；源须是同集群清单UUID、不同于目标、明确当前database_unavailable探针和unhealthy/unknown，不能接受任意调用者SourceID、凭据错误、旧/缺失探针、冲突入口或另一主库。后续完整预检/授权/隔离/多数派/lease不变。新增两引擎正向和拒绝矩阵先红后绿，7.7新身份后重测手动分支；自动/不抢回/关闭不自动的既有FIELD单独保留。

手动源解析28场景先红后绿；首轮正向夹具漏填复制用户名导致解析后的既有凭据门禁正确拒绝，已补夹具，未放宽凭据要求。源还限定unknown、无复制上游、当前同批次健康时间、15秒新鲜拓扑与单探针。全Go/vet及workflow/recovery/API race通过，SOURCE57/173变异与双对照通过，许可68/Markdown11/生成页通过。153按ON保护启动后产品恢复为唯一可写主，四节点/两入口健康，准备7.7正式升级；保持自动关闭12，不用开启自动来冒充手动恢复。

7.7正式执行前补同一SQL会话验收：152上MySQL native CLI关闭自动重连，PG psql以verify-full TLS持续查询，分别记录CONNECTION_ID/pg_backend_pid、客户端PID和原生主库ID不变；同时保留每秒新连接查询、151 VIP与探针/网关PID监测。前几轮新连接成功不能推出旧连接未断，本次用真正持久会话填补该证据。只执行SELECT，不写业务数据。此基线MySQL153/PG152均可写、四节点复制健康、自动MySQL关闭12；升级后再停库做手动分支前结束升级会话窗口，避免把刻意故障混入连续性。

## 3.1.7.7 实际结果与 PG 阻塞

正式 plan 9521a4fe-87e4-46d4-ad33-edc4cdffd839，execute b40a57ad-f6e3-490d-9b2a-be029a68f38d 于15:25:59~15:28:13成功。三台 HA 为9bb5047/3.1.7.7；本次仅 HA 载荷，Agent仍为含无VIP命令修复的3.1.7.4，其余程序保持此前身份。窗口两入口各125次新连接原生SQL零失败，130次VIP/就绪采样正常，网关及数据库PID不变。两条持久SQL会话各307次查询，其中升级窗口各133次，原生session ID与writer身份不变、零失败、MySQL自动重连关闭。以上仅证明该窗口的连接和查询，不是写入/事务连续性证明。

MySQL：7.6真实自动任务25ff682c-2533-44cd-8587-8f40536f6c5a按权重选153、排除更高权重但不参与的云实例；旧152经产品恢复后40秒18个独立周期未抢回。7.7在自动关闭时停153，50秒19个独立周期无自动操作；正式手动failover b28a79e1-85b6-4bb8-a5d3-4411ae25b8fa成功，旧153通过eef4b839-3e3c-4f62-8828-9b6aaf96f140恢复。原152主库、自动开启、四实例参与/权重100恢复，revision13保留。不同阶段版本分开记录，不把7.6样本冒充全由7.7执行。

PG：保存显式权重与排除云节点后仅停152数据库服务。330秒有界等待内未创建任何自动操作。153/154/云原生均仍处于recovery、只读且receive/replay一致，但pg_stat_wal_receiver在源停服后无记录；adapters/postgresql/topology.go坚持本轮双端证据，清除SourceIdentity，candidates.go原本允许的源失联分支因此无法成立。原primary_node_id配置仍在，不能拿它单独代替真实复制来源证据。PG权重自动升主未通过；其后恢复高权重不抢回、关闭自动后手动故障接管的完整链条未执行，不能填通过。

恢复时先经CAS暂停PG自动切换，启动原152被Agent无有效授权保护再次停服，原日志保留。随后在保护持续启用的情况下启动同一原主并同步执行正常discover，现有keeper取得新多数派租约，入口恢复；未停Agent/timer、未伪造grant或修改可写权限。最终原PG自动关闭、四实例参与/权重100恢复，revision3保留；MySQL不受此恢复影响。两套四库分别取得至少50个独立健康发现周期、各三条零延迟复制链，原生155/156身份与可写状态正确。原205项审计前缀与20个原操作ID在三台均保留，三投票节点ready、唯一Leader、维护释放、无活动/待复核操作。实际Chrome重新加载当前前端后核对7.7升级成功、两套实例规则及PG四节点健康；旧打开页面曾仍保留升级前前端，未把它当作新前端验收。

归档时钟说明：archive-result.json记录末目录比对完成14:07:43，而网关重启回执记录14:04:24，历史时钟/记录存在矛盾。此前时间叙述不构成精确先后或耗时证明；完整tar compare、SHA、指定源目录移除与实际空间恢复均已独立核对。后续当前时钟样本两机差57ms，不据此追认旧时间。

## 追加真实写入验收（用户明确要求）

用户要求“还要使用deepseek做数据写入，证明业务的连续性”。已在DSH Desktop新建隔离工作区.build/deepseek-business-continuity-20261103，使用该应用已有DeepSeek-V4.1-Flash/High配置实际派发事务写入器与只读对账工具任务，未向模型发送口令/连接串或业务数据，未改变模型与权限。第一阶段仅实现工具和本地测试，完成后由主代理审查，再派发有界真实写入。

计划在独立合成测试表使用run UUID、连续业务键、确定性payload摘要与同事务主从表，先持久化intent、再记录COMMIT确认；断线未获确认记UNKNOWN，按同一业务键回查后才能重试。固定155/156入口、TLS、低速有界写入；记录重连、提交间隙、失败和未知，不用静默重试隐藏中断。独立逐键核对当前主及全部副本的确认提交、payload、明细与总额，区分尝试未提交和已确认事务丢失。HA回退/重升、数据库切主与稳定写入分开标记；PG当前自动故障缺陷仍为未通过，不能用稳定写入或计划切换代签。正式执行与结果待本节追加，尚未声明写入连续性通过。

真实写入追加进度：DeepSeek首轮本地91项测试通过后，主代理为处理已复现审查问题暂停该轮收尾，不能声称该轮自行完成全部交付。主代理冻结review-frozen-v1源码并进行独立PG稳定烟测：经156/verify-full TLS在独立cg_bct_20261103表实际COMMIT20笔订单、40条明细、无未知结果。其本机直连三个副本的verify失败保留；随后由既有152管理通道使用原生psql逐库只读导出，四库20笔确认提交、40明细、payload SHA及总额全部一致。该10秒稳定样本没有HA升级或切主，不证明故障期业务连续。

另有独立纯单元反例：已有确认前缀后发生connect_failed并以connect_unavailable提前退出，初版仍报continuity_clean=true，且max_successful_commit_gap计算的是序号缺口而非时间。保留counterexample.json、旧verifier摘要和原始账本。第二轮已实际向同一DeepSeek会话提交R1–R6局部修正：MySQL明确TLS模式、失败/终止与时间间隙判据、有界重连预算、原生后端身份、实际执行口径及外部场景标记；仍不允许该阶段读取凭据或连接现场。修正后再执行升级与切主中的事务样本。


## DeepSeek真实写入结果（16:14—16:37，连续性失败）

第二轮工具由主代理冻结为review-frozen-r2，独立本地120项测试通过，再允许真实执行。通过DSH Desktop正常会话使用DeepSeek-V4.1-Flash / High，DeepSeek实际执行固定launcher的prepare、start、status和reconcile；主代理独立读取原始账本与原生四库结果。口令仅存在可信桥与数据库客户端进程内存，不传给DeepSeek，不改授权、HBA、安全组或TLS。只使用cg_bct_20261103合成订单/明细，每事务唯一run UUID/业务键、两条明细及确定性payload SHA，低速目标2事务/秒。MySQL强制TLS REQUIRED并在认证前拒绝无TLS服务器；PG使用verify-full。

先行10秒稳定烟测：DeepSeek实际运行MySQL/PG各20笔COMMIT ACK、各40明细，四库原生核对全部一致，无失败、UNKNOWN或重连。它只证明这两个稳定小样本，不认证升级或切主。

随后DeepSeek启动两套各420秒写入器；主代理在正式更新通道执行受控回退c131a18e-6e08-45cd-938c-00a2d97311cf、计划cd18e9b7-ec1d-4147-af81-b2dbe332bf67与重新升级813fcdac-f9b2-4d7b-b28c-f56d03bdec35。本轮顺序153→154→152，152同时是Raft Leader和两套数据库原主，最后重启；与较早查询窗口的节点顺序不同，不能互相覆盖。升级自身终态succeeded不表示业务连续通过。

| 真实用例 | 客户端实际时长/退出码 | 确认事务 | 失败尝试/重连 | 四节点原生对账 | 业务连续性 |
| --- | --- | --- | --- | --- | --- |
| ha-mysql，run UUID 7e9a6cc1-bccf-4da0-8360-6198a1d51613 | 371.006秒，exit=2，attempts_exhausted | 716 | 5 / 4 | 每库716订单、1432明细；已确认丢失/重复/payload或总额不符均0 | 失败 |
| ha-pg，run UUID 9db85e63-3f4b-44bd-8886-e4716165b9b5 | 369.052秒，exit=2，database_error | 717 | 1 / 0 | 每库717订单、1434明细；已确认丢失/重复/payload或总额不符均0 | 失败 |

两套UNKNOWN均0，失败业务键分别717/718，均在发送COMMIT前失败。connect_failed为0并不等于无断连：MySQL是在执行事务时失败并产生4次重连。客户端明确有限预算不能遮盖提前结束；其重试并未维持到原定420秒。工具错误包装丢失原驱动错误码，不能从null错误码推导具体SQLSTATE。其终态scenario_phase仍显示baseline，升级阶段依据独立持久化scenario-events及monotonic时间和操作ID关联，不能假称每笔ACK都有正确阶段标签。

16:22:20—21最后节点HA重启，16:22:24原生Agent日志记录两套release_and_read_only：PG writer被原生停服、MySQL持久只读，随后MySQL经已有多数派bootstrap恢复；两套写入器在同秒结束。16:28签名诊断显示PG无有效多数派租约，原主仍是权威Owner；这只能证实直接隔离链，**尚未定位到完整底层原因**，不能把状态读取/重放时序或租约写入竞争假设当已证实修复。

暂停后续故障注入，保留第一次恢复后再次被隔离的日志。16:33正常启动原PG并同步discover，既有keeper产生新多数派租约后Agent返回keep_vip；没有停止Agent/timer、强挂VIP、伪造grant、手工开放SQL写入或删除数据库文件。原生四库对账在PG恢复后完成；DeepSeek随后又实际执行两套status/reconcile，明确判exit=2及continuity FAILED，只把四库一致记为retention。

新增独立机器回执.build/instance-failover-field-20261103/deepseek-business-write-verdict.json，绑定冻结工具、原始receipts、真实四库读回、Agent/服务日志、现场快照与DSH会话截图SHA。原始失败、未完整的PG第一次读回、先行成功烟测均保留。恢复后的健康观察与较早FIELD报告只说明各自窗口，不覆盖本轮失败；不更改旧报告或原签名包。私有delivery-result/current说明同步失败口径。当前业务连续性及PG自动故障接管均未通过，不公开发布、不把3.1.7.7认证为可交付的连续性修复。


恢复收尾：追加观察MySQL连续64、PG连续50个不同发现周期，均为四实例健康、三条复制链零延迟。较早一次严格观察因MySQL瞬时1秒延迟退出的原输出保留，新观察另存v2不覆盖。最终原生SQL验证两套原主仍152、云副本仍在复制、155/156就绪、网关PID3828513/3828542未变、三台HA3.1.7.7/9bb5047全部ready且唯一Leader，无维护/活动/待复核操作；原等效策略MySQL自动开启/revision13、PG自动关闭/revision3、全部实例参与/权重100保留。最终健康回执deepseek-business-write-restoration.json与写入失败总回执分离，前者不改变后者FAIL结论。文档/许可门禁和118页生成重建通过；本追加阶段未改业务源码、未新构建包或公开推送。


## 保存策略后不兼容降级补测计划（执行前记录）

用户明确要求补测保存策略后的降级拒绝。已完整读取validation-gate-workflow、CG-UPGRADE-CONTRACT（contract_version=2，contract_loaded=true）、rules索引/change-policy/operation-safety、backend/settings/instance-failover与update-actions、delivery入口。实际源码仍主线9bb5047，工作树保留进入时四份暂存文档；现场最后核实HA3.1.7.7、MySQL策略revision13自动开启、PG revision3自动关闭。本轮不把兼容7.7→7.6回退代替7.1→6.3不兼容回退的拒绝证据。

首先对当前HEAD复制到隔离源码目录，保存真实策略后通过实际API→Manager→签名归档Inspector发起rollback；Helper只作为“已到执行边界”的记录器，绝不执行节点变更。MySQL/PG各测自动开启与关闭、另加无策略对照；预期不兼容时409且Helper未调用、策略不变。使用原签名3.1.7.1归档及对应原3.1.6.3回退身份，不重建签名包。若当前实现已经受理，则登记真实失败并停止现场破坏性验证；源码/隔离集成结果不叫三台现场拒绝通过。再按实际Runner路径核查，只有已有可证明的前置拒绝时才允许现场负向验证；不依靠清除策略或只读/隔离保护兜底。任务只增加测试证据，不在本轮未经回归修改生产实现。适用SOURCE/隔离集成和文档许可阶段，不制作/发布新包。


## 保存策略后不兼容降级补测结果：失败

在HEAD9bb5047的隔离源码副本实际运行测试，生产源码未改。真实API保存策略、真实Manager和CommandInspector重新验签原不可变3.1.7.1归档；签名清单base_commit为2d4b4aa/3.1.6.3。历史fixture为已安装3.1.7.1、原版本3.1.6.3且无已安装后继，是首次保存策略后直接回退的合法场景；并非在当前7.7站点提交已经被替代的旧任务后，把其他拒绝当策略保护。Helper只记录已到执行边界，不执行命令；身份/健康/锁等外部依赖使用隔离fixture，不冒充现场多数派或实际三节点回退。

| 引擎 | 实际保存状态 | 预期 | API实际 | Helper调用 | 结论 |
| --- | --- | --- | --- | --- | --- |
| MySQL | 保存开启，revision1，实例排除/权重913 | 409、不得启动Helper | 202 accepted | 1 | 失败 |
| MySQL | 先保存开启、再保存关闭，revision2 | 409、不得启动Helper | 202 accepted | 1 | 失败 |
| PG | 保存开启，revision1，实例排除/权重913 | 409、不得启动Helper | 202 accepted | 1 | 失败 |
| PG | 先保存开启、再保存关闭，revision2 | 409、不得启动Helper | 202 accepted | 1 | 失败 |
| MySQL/PG各一项对照 | 从未保存，policy=nil | 不被策略限制误拒 | 202 accepted | 各1 | 两项对照通过 |

四份真实负向断言均红，两个无策略对照绿；所有fixture策略保持不变。源码协议检查在保存策略时执行，当前startSoftwareUpdate/Manager.StartWithOperationID没有把已存策略与回退目标能力绑定，因此不能用“保存前全体voter已支持”代替“后续降级会被拒绝”。进一步提取原Runner run_hotfix_update、rollback_hotfix_nodes、restore_hotfix_on_node原函数文本，保持严格Shell行为，以健康/锁/节点transport fixture替代外部I/O，六个场景均实际执行到三个节点rollback.sh恢复边界；四个保存策略场景未被拒绝。节点文件/服务仅用临时版本标记模拟，生产路径与节点完全未动，该结果是SOURCE隔离集成而非FIELD。

原始测试位于.build/policy-downgrade-20261103/，verdict.json绑定源码、Go/Bash测试、真实签名检查和每例结果SHA。首次从父模块启动Go测试的setup失败及Runner空数组fixture在macOS Bash的setup失败均保留，修正测试启动/混合节点fixture后再运行，未将这些准备错误当产品失败。实际回归红来自HTTP202/Helper已调用及Runner恢复边界可达。

结论：**已保存策略后降级到不支持策略的版本，保护未实现到执行链，补测未通过，尚未修复。** 没有在现场强行执行3.1.6.3降级，没有修改三台策略/程序/数据库/VIP。此前7.7→7.6保存策略回退成功仅为兼容版本证据；本轮不关闭不兼容降级FIELD义务。后续修复需在job、维护锁及文件恢复前核验实际回退目标能力与备份身份，关闭自动也不得放行；不得删除策略、覆盖旧归档或仅以事后旧进程启动失败充当提前拒绝。
