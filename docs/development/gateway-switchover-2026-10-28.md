# 页面主库切换接入独立网关

2026-10-28，用户授权实现页面本地/云切换与网关联动。只修改源码和隔离验收，不安装现场、不改真实数据库、不发布或重建既有包。

## 修改前依据

- 已读：validation-gate-workflow.md、upgrade-validation-chain.md全文、rules/README.md、change-policy.md、operation-safety.md、frontend-common.md、后端operations.md与前端operations.md。
- CG-UPGRADE-CONTRACT；contract_version=2；contract_loaded=true。适用source阶段；ART/FIELD未执行，不用源码通过宣称部署完成。
- 主线codex/2.2-postgresql，HEAD a018235，进入时干净。目标现场运行版本本轮未读取；不据本地HEAD推断现场版本。
- 对照132b35b及其父版本：此前ProviderRouter只注册VIP/Kubernetes；132b35b增加external_command，但其AuthorizeTransition自行拼接LeaseID，没有真实LeaseStore，且没有稳定续期钩子。a018235只增实例登记，不接网关。
- 独立网关6e4b5ca提供mTLS route/status API与排序水位，未包含HA适配。协议、design与AGENTS已读；通过网络协议对接，不将独立实现复制入AGPL仓库。
- 现有控制台executeOperation与操作管理保留锁/预检/审批/执行/验证；MySQL适配器已在提升前AuthorizeTransition、提升后Transfer/Verify/Finalize。复用该调用链，禁止另加绕过隔离的直接改路由按钮。
- 当前store.CommitHAEndpointOwner只接收VIP，导致已有Service端点提供者不能提交所有权；本次按明确provider/kind校验扩展，不允许任意Service充当writer。

## 实现与验证计划

新增gateway端点提供者、静态受信mTLS配置，使用真实多数派LeaseStore；路由TTL不得超过数据库租约，先全体撤权再提升/激活。稳定续期仅在OwnershipKeeper完成当前SQL/唯一主库/观测/多数派租约检查后执行，失去授权不得无限续期。所有网关缺失、旧目标、乱序或部分失败均阻断/撤权。

页面继续用“操作→主库切换”，增加网关入口及授权/目标状态，保留原确认与操作锁。接入已有数据库不等于新建复制；新候选仍走引擎原生预检和数据追平。

验证：Go单测/竞态、真实网关进程与隔离数据库适配器调用链、真实浏览器双语/窄屏/锁/迟到响应、相关变异、source strict/self-test、许可、Markdown台账与HTML生成。真实MySQL、云LB、网络和现场切换不执行，不填通过。

## 实现范围与使用路径

新增`gateway`端点提供者，复用原页面“操作→主库切换”的锁、候选、提交、原生MySQL编排与验证。没有新增直接改路由的高风险按钮。独立网关仍是外部进程，本仓只实现mTLS协议客户端，不链接或复制该项目代码。

- `ReserveGatewayGrant`在同一个共识快照提交中比较真实Lease身份/有效期并分配epoch/sequence；先落盘后发网络，TTL最大15秒且不超过真实Lease剩余时间。重启/换Leader沿用共识水位；失去多数派不发布。
- AuthorizeTransition先用原`authorizeTransitionLease`取得多数派授权，再全体withdraw。MySQL执行原隔离、GTID追平、提升、复制重挂；Transfer激活目标并提交Service所有权，Finalize核对全部网关后稳定原租约。部分激活失败尝试更高序号withdraw，不恢复旧写主。
- OwnershipKeeper原有SQL健康、唯一可写主、观测与多数派租约检查完成后，调用RefreshStableRoute；再核对新鲜SQL证据，禁止只读/bootstrap阶段就开放网关。换任期或网关重启重新做全体屏障；全部网关须可观测，离线时保守阻断，不宣称单网关故障无影响。
- 受mTLS保护的网关status新增`targets`和随机进程`instance_id`。HA先核对静态后端实例ID与注册的精确主机/IP、端口，并拒绝把一个进程的两个URL算双节点。它不证明网关位于不同物理故障域。
- Agent新增显式`endpoint_provider=gateway`策略及签名响应域。MySQL持有有效真实租约时维持writer、保留控制器主从切换；失联仍持久只读隔离，不执行宿主机VIP操作。新旧模式混用拒绝。现阶段只开放MySQL受控切换，其他引擎与灾难恢复不由本功能宣布支持。
- 页面新增默认折叠的网关路由状态：逐网关实际目标/到期、路由和多数派租约是否一致；跟随语言设置，刷新立即清除旧状态，迟到响应不能跨集群显示或解锁。
- 写租约仍有效时禁止重新绑定gateway端点/配置，避免另一条路由域被留在撤权屏障外。既有VIP不会自动迁移或删除，必须安排维护窗口按部署步骤处理。

## 首次接入（现场未执行）

已有数据库无须重装，但本地与云端必须为同一套真实主从复制。先在“集群管理→接入已有实例”登记两端并发现，取得真实集群UUID及实例UUID；不能把两套独立数据库登记后当作复制已经成立。

1. 准备至少两个独立网关进程/主机及固定业务域名/LB，确认应用→入口→两端MySQL和控制器→网关管理端的网络可达。
2. 使用扩展status协议的新网关源码/新候选；原`0.1.0-preview.1`包字节不变，缺少targets/instance_id会明确阻断。网关每个cluster的`id`用真实ClusterGuard集群UUID，backends键用真实实例UUID，值必须与登记的数据库主机/IP和端口一致。管理端使用独立CA及TLS1.3双向证书，客户端CN加入allowed_controller_names。
3. 所有控制节点配置同一个受信profile，证书/私钥只在节点本地，不经过浏览器提交：

```json
{
  "gateway": {
    "enabled": true,
    "profiles": {
      "hybrid-prod": {
        "management_urls": ["https://gateway-a.example:9443", "https://gateway-b.example:9443"],
        "ca_file": "/etc/clusterguard/gateway/ca.crt",
        "cert_file": "/etc/clusterguard/gateway/controller.crt",
        "key_file": "/etc/clusterguard/gateway/controller.key"
      }
    }
  }
}
```

4. 本地与云端Agent都升级为支持签名gateway域的新代码，每个相关cluster策略填写`endpoint_provider: "gateway"`、`engine: "mysql"`，不配置VIP/interface/prefix（为空/0），保留原实例UUID、数据库客户端及持久只读保护配置。不能对仍处于VIP模式的现场直接覆盖Agent策略。
5. 若原来有VIP，先维护窗口停业务写入，保存原配置与证据；通过已有HA端点接口停用旧VIP元数据，让旧Agent完成VIP释放和只读隔离，读回确认旧VIP实际消失，并确认旧租约到期。之后才改Agent策略和创建gateway端点。禁止只删元数据/租约文件或仅等待固定秒数就假称旧权威已消失。
6. 通过现有管理员/CSRF保护的`POST /api/v1/clusters/<cluster-uuid>/ha-endpoints`登记唯一活动Service端点（已有活动VIP不得同时保留）：

```json
{
  "kind": "service",
  "provider": "gateway",
  "provider_ref": "hybrid-prod",
  "hostname": "database.example",
  "port": 63306,
  "owner_id": "当前可信主库的真实实例UUID",
  "active": true
}
```

此配置是首次受控接入，不是绕过写租约的切主入口。正常运行时重新绑定会被活租约拒绝。新路由只在SQL证明可写、唯一主库、多数派租约及全部网关状态满足时开放；全体只读的恢复仍走原受控启动授权和新鲜发现，不通过手工关只读绕过。

7. 首次把应用连接串改为固定域名/端口并验证TLS主机身份、连接池重连和测试读写。之后在页面选集群→操作→主库切换→选择云端候选→解锁→执行；沿用原安全门禁。旧TCP会话关闭，在途COMMIT可能结果不确定，不自动重放SQL。

## 验证记录

本轮通过项以实际测试日志为准；日志位于`.build/gateway/`。真实MySQL、云LB、跨云网络、现场Agent升级和最终业务验收未执行。源码功能不是已部署功能，不能让现有旧页面直接获得新能力。

### 验证中发现的既有SQL超时问题

本轮全仓回归与单独重跑均复现`TestCLIQueryRunnerAppliesDefaultQueryDeadline`：50ms超时实际约2.05s，要求小于1s。修改前核对HEAD的`adapters/mysql/runner.go`同样使用exec.CommandContext、未设置WaitDelay；shell子进程持有stdout管道导致等待超过context。该问题早于本轮网关变更，但影响其SQL探针时限，局部增加100ms管道等待上限，仍返回原context错误，不放宽测试断言或查询时限。

### 本轮已执行的本地验证

| 检查 | 实际结果与限制 |
| --- | --- |
| `go test ./...` | 修复既有查询管道超时后全仓通过；原失败输出单独保留，不改写历史。 |
| 受影响包Go竞态、go vet | 通过；新增网关、API、Agent、存储、原生MySQL编排测试通过。 |
| 实际网关进程联调 | 两个真实进程、真实mTLS、真实多数派LeaseStore代码（隔离权威夹具）、固定TCP入口本地→云、旧会话关闭、稳定续期通过；SQL拓扑为模拟，非真实复制/云LB。 |
| 原生MySQL适配器 | 实际BuildPlan/Execute/Verify配隔离SQL和网关传输夹具通过，验证先撤权、原只读隔离/GTID等待/提升/复制、后激活及稳定租约；非真实MySQL。 |
| 新页面浏览器 | 1440/390像素共40项通过；默认折叠、双语切换、目标绑定、真实点击提交、拒绝后锁定、503、未就绪、迟到响应、所有列窄屏可见。API夹具拒绝真实数据库操作。 |
| 原操作锁浏览器 | MySQL/PostgreSQL原18场景通过，含强制点击、候选/集群变化、迟到预检、重复提交、会话撤权。 |
| 源码门禁 | source strict 46项通过；ART/FIELD为DEFERRED，不声明发布/部署。 |
| 门禁自测 | 136项变异全部捕获，未修改与注释两个no-bite控制均通过。 |
| Go行为变异 | 5/5断言捕获：查询管道超时、路由超出租约、部分激活未撤权、只读SQL主库续期、Agent混用VIP授权；不把编译失败算捕获。超时夹具等子进程已启动后才取消，避免启动慢掩盖问题。 |
| 许可/文档 | AGPL许可68项、Markdown台账11项通过；生成HTML按本轮文档重建。 |

现场仍未改动，本地实现不等于旧服务器页面已经支持。独立网关需新兼容候选，HA与Agent需本轮源码的新交付；首次旧VIP迁移、证书、真实复制与业务恢复须维护窗口验收，不自动开展。

### 网关状态刷新补充

普通拓扑刷新可为60秒，长于15秒路由授权，因此网关状态在操作页独立每5秒只读刷新；不改变操作锁，不触发数据库或路由写操作。独立读取代次和集群请求代次共同拒绝迟到响应；缓存授权过期时显示“需重新读取”，不能继续以旧证据显示就绪。

### 未实现恢复能力的边界检查

修改前另读backend/recovery.md并对照e2d6def的disasterRecoveryRoute：原接口只按MySQL/PG引擎开放计划/执行，未区分端点提供者。本轮gateway仅实现受控切换，不能让它误入仍依赖宿主机VIP的灾难重建流程。因此gateway集群的历史状态仍可读，新的恢复计划/执行在建任务与Driver调用前明确返回unsupported；不删除旧任务或降低既有恢复门禁。

网关只读状态在Follower或无法取得当前多数派授权时仍显示已读的网关目标，但明确标注“写入授权未确认”，不把Follower身份误报成路由故障，也不据此发放新授权。实际切换仍由既有Leader/多数派执行链决定。
