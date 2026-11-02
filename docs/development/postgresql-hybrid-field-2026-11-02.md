# PostgreSQL 本地三节点与云节点部署测试

## 授权、门禁与范围

2026-11-02，用户明确要求在192.168.102.152/153/154及现有云主机部署PG并测试。进入主线codex/2.2-postgresql、HEAD5f0d76c，工作树干净。已读门禁执行流程、规则索引、操作授权、修改证据、PG HA和PG Agent文档；已完整读取CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。

现场只读前检：三本地为Rocky8.10，云为Alibaba Cloud Linux3；均无运行PG和5432监听，本地已有MySQL/HA，云当前承载MySQL。保持现有MySQL、155网关和三投票节点身份；部署独立5432 PG实例，PG数据/日志放/data/postgresql/5432下，禁止复用已有实例或覆盖目录。云内存仅约1.8GiB，使用适合测试的低内存PG设置。

## 旧实现对照与限制

当前PG原生Agent与VIP路径已有，PGDATA窄沙箱由systemd_sandbox.go生成；gateway.go预检、Agent gateway配置及稳定续期明确限制MySQL。现役Agent3.1.4.2，HA版本及配置能力须再核对。任务先部署真实PG并验证复制、角色与网络，按现有受控能力验证切换。云不具备本地VIP，不能拿TCP转发或手动SQL提升冒充PG企业网关联动。若实现阻断，记录准确缺口，不移除限制或手工伪造writer授权。

## 验证计划

1. 核对官方PG16可用介质、发行版兼容和签名摘要，记录实际版本，四节点一致；保留安装原始输出。
2. 独立路径/服务/随机凭据，SCRAM及限定地址；本地主、其余备，检查system_identifier、固定节点UUID、时间线、WAL与实际写入读取。
3. 按实际网络部署受限云通道，既有MySQL端口/复制/网关不改。分清复制连接成功与控制端查询/Agent可达。
4. 正式清单注册与页面可见、PG Agent真实systemd上下文及多数派授权按实际能力推进；变更控制面配置备份并逐节点核验。
5. 原生切换/回切、旧主隔离及复制恢复必须走产品授权；未经实现的云网关场景保持OPEN。测试前后核对MySQL健康和网关入口。
6. 私有原始证据放.build/pg-field-20261102；不入库凭据、私钥、连接密码或伪造现场证据。文档台账、许可、HTML按本次修改执行；若需源码/新包，另完成旧版对照及对应SOURCE/ART，不能覆盖现役包。

## 网关联动实施前补充（用户目标扩大为对标MySQL）

用户明确设置目标“完成pg数据库的一系列网关测试，对标mysql”，并指定PG入口156。此后范围包括必要的PG网关适配、对应源码/制品/现场分阶段验证。156经三次ARP冲突探测无响应；PG16.15官方PGDG三RPM四节点摘要/签名均通过，已部署独立一主三备，同一system_identifier，三备streaming。云端不新增Raft投票节点。

修改前对照：e2d6def引入MySQL gateway，4031a88增加显式单机/部分续期，当前5f0d76c仍在GatewayProvider.Precheck、RefreshStableRoute和Agent策略/协调中限制MySQL；PG原生execution已有旧主停止证明、WAL追平、晋升、重定向/rewind及EndpointProvider授权/Transfer/Verify，不能另写直接提升路径。将PG可写证据定义为健康且非恢复、transaction_read_only=false，沿用共识租约、水位、严格撤权屏障和过期撤权。PG gateway Agent不得操作宿主机VIP；失去授权沿用PG停主/保留流式备用的硬隔离，不能套用MySQL双只读。受控过渡只保留controller权威，不由Agent提升或开启写入；稳定授权另核对本地原生可写角色。

部署实测还暴露postgres用户继承root HOME时libpq寻找/root/.postgresql权限失败；需为PG工具设置受保护passfile所在目录作为独立HOME，并回归HOME不继承root。只修改这一实际执行上下文，不关闭SELinux或放宽数据库身份。新增测试先在旧实现重现PGgateway拒绝，再验证正反场景/过期、不同Owner/任期、读状态、Agent签名、丢失授权、旧连接关闭及MySQL回归；新功能用新签名身份交付，原3.1.5.1不重建。真实云往返必须等源码/ART及滚动升级验证完成后进行，不用手动SQL提升替代。

## 初步真实数据库与源码回归

四节点PG16.15已运行一主三备（本地152为初始主），system_identifier一致；100行测试数据及摘要四库相同，三备streaming且只读。三个控制节点到四库共12条verify-full TLS/发现权限查询通过。MySQL原四库及三控制节点仍健康，原155网关配置未改。部署原始失败保留：PG就绪socket竞态、云runuser继承root HOME的客户端证书权限错误、SELinux init_t直接exec ssh被拒；对应使用显式就绪等待、专用HOME和已有受控脚本启动方式修正，没有关闭SELinux。

PGgateway旧实现先复现预检和正常writer被拒；新增PG原生可写/多数派续期、Linux无VIP策略及停主隔离。Go全仓、受影响包race/vet通过；native PG BuildPlan/Execute/Verify配真实LeaseStore及隔离SQL/网关传输测试通过，证明withdraw→旧主停止证明→WAL追平→提升→激活→rewind。48项SOURCE严格门禁、143项门禁变异与2个控制通过；12/12独立行为变异由断言捕获（含PG恢复/只读、本机写证据、HOME），编译失败不算捕获。隔离Chrome网关40项及原操作锁18场景通过，均为fixture API，不是现场页面执行。签名包和PG真实网关切主此时尚未执行。

## 首次真实Agent验收发现的运行时分发缺口

3.1.6.1已验签并通过13项ART，正式通道按153→154→152更新成功，维护门禁释放；云端使用相同签名原件更新Agent。四节点源/新包摘要一致检查另存。私有制品目录完整检查在升级排队后才完成，顺序不符合先检查后执行的计划，已保留此时序事实，不能倒填成排队前已跑；该次完整目录检查实际通过。

PG正式登记与12条控制器TLS查询通过，网关进程在156尚未启用时取得多数派稳定租约。首次只在152追加PG Agent策略，实际systemd却停止PG主库：cmd/clusterguard-agent的newRuntimeControllers实际传入RuntimePostgreSQLController；24c1c88只为PostgreSQLLocalController实现Writable，分发器未转发可选接口，源码夹具直接注入本地控制器遗漏该接线。首个补丁未通过PG现场验收，字节永久保留，不重建或改写通过。

纠正前对照：读取24c1c88的main.go/newRuntimeControllers、docker_postgresql.go的RuntimePostgreSQLController.Selected/Status和gateway_reconcile.go；运行时分发器缺Writable，导致!ok保护触发。原15分钟内新PG尚无应用入口，三备仍为备用，未出现第二writer；152的PG策略已单独撤回保留其他MySQL策略，MySQL实际协调恢复Result=success。PG端点将经正式接口撤权并等待租约自然失效后再按初始部署恢复PG，禁止伪造writer/grant或删水位。修复须增加实际newRuntimeControllers→Reconciler的测试与分发器失败路径，使用3.1.6.2新身份及同封板修订链重新ART/现场验证；原6.1失败日志保留。

分发修复回归：新增cmd/clusterguard-agent测试使用实际newRuntimeControllers构造和运行时分发器，先重现正常writer被停，修复后正常writer保持且只读writer被停；分发器缺实现/缺运行时仍拒绝。受影响包unit/race/vet、48项SOURCE、144项门禁变异与2控制、13/13行为变异通过。PG端点已正式设inactive，旧授权自然到期；没有删租约或路由水位，没有启用156。新身份将为3.1.6.2、revision=1，原6.1字节保留。

## 3.1.6.2 制品与实际部署

修复提交63c99bd，3.1.6.2/revision=1以24c1c88为基线；签名原件SHA-256为e62ec41dde2beea2fd09bf9c4813ad5fa921496bd5a9debaf5927a50b155f901。仅更换HA与Agent；ART-001..013和私有完整台账检查均在本次排队之前通过，随后正式升级三控制节点，云端应用同一验签原件。原3.1.6.1保留并标注PG现场失败。没有公开上传签名包，也未重建既有身份。

6.2部署后四节点Agent实际systemd协调通过，PG仍只有三控制投票节点。PG网关使用既有preview.3原件，在151另建独立服务/状态目录、65432数据端口、9543管理端口、8180健康端口和VRID56，将156:5432精确映射至PG网关；151原有5432、MySQL3306和155/63306/VRID55保留。三控制节点至四库12条verify-full TLS查询通过。云连接走专用限定SSH通道，没有开放公网5432。

部署脚本一次错误地查询154这个Follower并等待authoritative ready，随后外层未启用set-e而继续配置；此等待不记为通过。已把只读验收工具改为动态寻找唯一ready且有多数派的Leader，并单独复核实际Agent和路由。Follower的authority_unconfirmed不能当作网关实际停止或跳过Leader检查的理由。

## 实际页面与业务读回发现

真实Chrome登录现有控制台，未使用fixture API；只对预先核对证书的公钥指纹做临时浏览器固定，不关闭系统或全局TLS验证。页面切主仍通过原操作锁、一次性审批、安全门禁和原生编排。

首轮本地→云操作成功，页面约20.7秒；云→本地约16.6秒，原TCP连接按撤权关闭，原生UUID/角色/三备streaming均通过。但是返回本地后实际156入口SQL被HBA拒绝，不能据此宣称业务闭环：pg_rewind复制源端配置，云端原HBA缺少151的限定规则，该配置随重绕返回本地。四个本轮新建测试实例已统一窄规则hostssl cg_gateway_acceptance cg_test 192.168.102.151/32 scram-sha-256并仅reload；原文件逐机备份。统一后HBA摘要d98154e646308c72fda834bb6f65ed694ec4a2ccb31b8c85a7b3ef3b220ffae1。未声称支持异构节点本地配置的自动保留。

完整重测又发现切主激活后15秒授权可能在稳定续期接续前到期，156会按安全规则短暂撤下，再由稳定授权恢复。页面原生任务成功时间不能代替业务RTO；首次严格连续SQL脚本失败和入口超时均保留。本轮后续验收明确测量操作完成后的重连窗口，再检查连续SQL，不能把加等待写成零中断或掩盖失败。云端旧主rewind耗时也须从实际操作记录计算；正在执行的任务不算通过。

私有原始证据：.build/pg-field-20261102/browser、browser-back、browser-final、portable-hba-configuration.json、stable-cloud-after-convergence-stability.json。前两套页面流程曾发生验收脚本等待/继续超时，已经成功的服务端操作不盲目重放；脚本失败与数据库失败分别保留。

## 长耗时切换的路由续期缺陷（修复前对照）

6.2现场回切时pg_rewind持续数分钟，旧主已隔离、新主已提升且多数派过渡租约仍在5秒续期；但是15秒路由grant没有续期，OwnershipKeeper按设计跳过受控PG操作，导致156在恢复期间到期撤下。原生任务与业务中断分别保留。真实浏览器测试脚本180秒等待亦超时，不等于后端30分钟受控执行已失败；不能重放或中断正在恢复的旧主。

修改前核对63c99bd与24c1c88/e2d6def的gateway.AuthorizeTransition/Transfer、lease_authorization.go及ownership_keeper.go：过渡授权只续期DB lease，没有续期已激活的gateway grant；稳定续期必须待操作结束。拟增加与同一授权生命周期绑定的5秒路由续期：只续期同一operation/lease/owner/epoch、已观测active的路由；withdraw阶段绝不激活，失去多数派/变更身份/撤权/错误时取消过渡上下文并到期关闭。保留严格全网关屏障，Cancel/Abort停止并等待续期退出。新增长于15秒的行为回归和丢失授权/取消/撤权反例，再走新不可变候选3.1.6.3与SOURCE/ART/现场，6.2不可覆盖。

长耗时复现结果：7800a73f-5745-445c-82f0-a08fe4147597实际执行约283秒，四库角色/复制检查通过，writer_endpoint_owner失败，原结果为indeterminate，保留不改成成功。稳定keeper在任务结束后恢复156。新增真实时间回归在旧6.2源码上21秒重现15秒grant到期，不能再将此问题只解释为重连窗口。

审查留痕纠正：一次复核脚本的断言失败后，外层shell未set-e，后续review仍被提交。复核API实际返回plan_integrity失败（批准后集群revision已变化），review说明中的“API re-verification now passes”不实。review本身为不可变acknowledged_indeterminate，未将操作改成成功，不能删改原审计；以本段及私有long-rewind-final-verification.json纠正口径，后续只读/部署脚本必须显式检查每一步状态后才写入。本次人工已核对新主152、云备和稳定路由，但它不能替代该批准计划的API复验。

长过渡续期修复已完成同一真实时间回归：withdraw阶段保持关闭，激活路由连续保持超过原15秒有效期，epoch/operation/lease/target均不变，仅sequence推进；实际授权拒绝使上下文取消，Cancel后不再发布。修复前21秒失败，修复后endpoint race约27秒通过；PG原生编排/coordination/store回归与受影响vet通过。49项SOURCE、147项门禁变异和2个控制通过。首轮自测复制清单漏新测试，控制样本因此失败，修正复制清单后重测，首轮不算通过。许可68、Markdown11和HTML重建通过。6.3制品及新现场结果仍须随后单独记录。
