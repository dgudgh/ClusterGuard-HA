# 双网关固定VIP入口实现

2026-11-02。HA主线codex/2.2-postgresql、基线f23a6ef；独立gateway基线75b7860，两工作树进入时均干净。现场已授权候选为3.1.4.2/4f077a2，本任务不安装、不迁移现场VIP、不执行云切主；两台新网关VM地址待提供。

## 适用规则及旧版对照

已读validation-gate-workflow.md、upgrade-validation-chain.md全文、change-policy.md、operation-safety.md、后端operations/nodes及独立gateway AGENTS、design/protocol/development/deploy。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。本次SOURCE、行为/集成/变异及文档许可检查；签名ART与现场FIELD未执行。

对照gateway 6e4b5ca→75b7860：已有TCP授权路由、mTLS管理、每集群readyz，但无本地VIP配置生成或keepalived探针命令；独立Client.Renew可给存活节点续期，HA当前gatewayHTTP.Statuses及GatewayProvider.statuses强制全节点，OwnershipKeeper观察失败即无法续期，单网关故障导致所有路由到期。HA旧2024567不含网关，e2d6def→61cc5b3仍保持全体切主撤权屏障，不能为可用性移除该屏障。

## 实现范围和不变量

1. 独立网关新增本地就绪检查命令：只读现有网关配置、查询指定集群loopback readyz，超时/重定向/非200/未知集群/未明确启用均失败，不读取控制器私钥、不推断主库。
2. 严格双节点站点规格生成每节点gateway.json、Keepalived单播VRRP配置和systemd启用前置条件；校验VIP/节点不同且同子网、端口/身份/接口/优先级，拒绝注入与覆盖。155从数据库迁移到网关属于独立维护窗口，生成配置绝不自动绑定VIP或创建启用标记。探针须绑定当前集群启用标记且检查真实授权路由；不以进程存活代替就绪。
3. HA仅对已经存在的稳定主库授权开放部分观测/续期：真实多数派租约仍有效、原主库新鲜SQL证据、存活网关相同授权/任期/目标和不超出已发布水位。标记不完整并仅续现有租约；未知/矛盾身份、全部离线、首次bootstrap、撤权/切主流程仍拒绝。离线节点恢复时先完成完整屏障，不能盲目激活旧路由。
4. 没有网关自主选主、弱化TLS/签名或绕过隔离；网关入口VRRP只处理本地地址冗余，不授权数据库。网络分区下VRRP不保证唯一地址，路由授权仍限制同一真实writer，生产需额外完成分区/故障域验收。

## 验证计划

先写失败回归再实现：严格规格及输出、探针无授权/过期/错误后端/重定向/启用标记；真实TCP/mTLS网关路由和探针；HA单节点离线稳定续期及全离线/错身份/过期/换主拒绝。Go race/vet、源码严格门禁及变异、许可、Markdown台账/HTML；能运行的真实MySQL隔离集成单独记录。Linux实际VIP/VRRP、两台新VM、真实云/业务连接池/切主/回退未执行，不填passed。


## 范围修订与实现结果

用户随后提供一台既有Linux主机，明确“就先用一个”，并确认保留其现有MySQL、网关监听63306、仅映射155:3306。实际SSH确认该机已有MySQL/其他业务，不当作空白新VM处理，未停止或改写这些服务。现场IP/实例坐标仅进私有生成目录，不入公开示例。

独立组件新增cg-gateway-site和cg-gateway-ready、严格站点配置、单机验证/双机模式、集群绑定启用标记、Keepalived启动前探针及周期探针；端口映射生成本集群独立nft表、PREROUTING/OUTPUT精确VIP/端口DNAT、先映射后持VIP的systemd依赖。无启用标记或无有效路由不持VIP。标记root:cg-gateway/0640供非特权只读探针读取；没有让探针读取控制器私钥。默认生成不绑定VIP、不创建标记、不安装依赖或重载主机防火墙。

HA增加显式allow_single_gateway配置（只允许一个URL，默认至少2），不改变Raft多数派；稳定主库部分网络失联只续旧租约和已知目标/任期，错误仍上报。TLS/协议/身份错误不当成普通离线；全部离线、冷启动、过期、重启、矛盾身份或换主仍拒绝部分覆盖。完整切主撤权屏障不变。API区分serving、unavailable_gateways及single_gateway，不将部分服务标为全体ready。

## 本轮实际验证

- HA go test ./...、go vet ./...通过；endpoint/coordination/config竞态回归通过。
- 独立gateway全量竞态测试与vet通过；就绪检查实测真实TCP后端、无授权/过期/后端故障，严格站点/单机选择、错误HTTP/重定向、外部地址和标记保护均有断言。
- 真实单/双网关进程+mTLS+HA多数派发布测试通过，包含两节点模式下停止一个进程、存活节点同授权续期及换主拒绝。数据库角色由夹具提供，不是真实MySQL切主。
- SOURCE严格47项、139条源码变异及2个控制通过；HA行为变异8/8捕获，独立gateway行为变异7/7捕获。标记变异首轮删掉唯一strings引用而编译失败，不记为捕获；改成合法表达式后由断言捕获，原首轮证据保留。
- 实际Rocky8.10 Linux上在全新临时网络命名空间使用192.0.2.0/24测试nft映射：远端和同机VIP3306都进入63306，普通主机3306仍进入旧测试数据库；单独停止网关而保留旧测试数据库后VIP连接失败，无回落。宿主机真实MySQL PID未变。不是修改真实业务VIP。
- 从系统配置的Rocky仓库下载Keepalived2.1.5和对应库，仅在私有目录解包，RPM签名/摘要验证通过，未安装系统RPM。真实Keepalived配置测试和两套网关/VRRP进程在隔离网络命名空间通过：缺标记/授权不持VIP、固定入口本地→云模拟后端、活动网关故障后VIP转至另一台、授权到期撤VIP。复测约1011ms完成本次故障漂移，只是实验观测，不是生产RTO承诺。另验证非特权用户只读探针可读取正确root管理标记且无需TLS私钥，root-only不可读标记会失败。
- 从用户指定网关主机使用现役发现凭据进行进程内只读SQL/TLS查询，四库均可达、UUID匹配、GTID一致；凭据未输出或写入证据。此项是网络/数据库身份验证，不是网关接管或云切主。

证据仅留本机.build/gateway-vip-20261102：source/source-mutations、ha/gateway race/vet与行为变异日志、process-integration-final.log、native-nat-result.json、native-vrrp-r2.log、151-real-database-connectivity.json。实际生成的站点配置在site-151/；包含真实清单，不公开。

## 交付与现场尚未完成

本轮新增代码尚不在现役3.1.4.2签名包内；新HA代码、Agent策略和独立网关须走兼容交付与ART门禁后才能启用。现有历史制品门禁未被这轮SOURCE回归关闭；未生成或覆盖任何已交付身份，未声明签名ART/FIELD通过。

仅做隔离Linux和网络只读验证，没有在用户主机启动生产网关/Keepalived、创建真实frontend-enabled或接管155，没有启用真实HA gateway profile、修改旧Agent VIP策略或执行云切主。下一步须完成PKI/可信Agent与云控制连通、新制品及维护窗口迁移；原数据库主库/VIP不因本轮实现而改变。单机模式不提供入口冗余；两机VRRP分区、真实云事务和连接池恢复仍须现场验收。


独立组件源码已提交a377a83（codex/main）；HA适配保留于唯一主线codex/2.2-postgresql。本轮全量测试和文档/许可结果按上述私有日志归档；未推送、打标签、生成新包或安装到生产路径。测试命名空间已清理，Linux宿主原有MySQL PID仍为936，系统Keepalived仍未安装；只保留私有测试解包与证据目录。
