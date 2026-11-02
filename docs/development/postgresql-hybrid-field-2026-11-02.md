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
