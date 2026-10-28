# 既有集群接入已有数据库实例

日期：2026-10-28。分支 codex/2.2-postgresql，基线 132b35b，初始工作区干净。现场版本仅有此前3.1.3.6记录，本次未连接现场确认。

## 修改前对照与门禁

- 已读 validation-gate-workflow、rules/change-policy、frontend-common、operation-safety、frontend/context、frontend/overview、backend/clusters 及强制契约全文。
- CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。本次适用SOURCE；不构建、签名、发布或执行现场操作。
- 对照 git show 0cc7619:internal/api/clusters.go 的 registerCluster 与当前132b35b：均只支持创建新集群及其初始端点，无向既有集群追加实例的入口。store.UpsertEndpoint已有唯一地址与失效观测规则，但不提供用户入口或批次原子性。
- 旧发现只遍历已登记 active database endpoints；手工配置复制不会自动纳管。复现：已登记三端点集群，第四个库即使正常复制也不在发现清单。
- 范围：新增既有集群端点批量原子登记API、管理员控制台入口、双语文案、存储/API/浏览器回归。保持数据库、Agent、Raft、VIP、授权与维护门禁不变。
- 验证计划：存储原子性/冲突/幂等/持久化；API权限/坏请求/登记后发现；真实隔离浏览器桌面及窄屏点击、切换上下文与发现失败；source strict/self-test、许可、Markdown台账及生成页。

## 行为

POST /api/v1/clusters/{id}/database-endpoints，body.endpoints与创建集群的地址字段一致。服务器固定集群、kind和active，不接受实例身份/凭据覆盖。批次冲突全部拒绝，相同集群的重复地址复用原端点。新增后使旧拓扑失效，并记录库存登记审计。登记与发现分开，发现失败仍保留登记，允许再发现。

仅为可观测性接入，不安装、播种、重建数据库或Agent，也不授予writer lease。跨NAT需所有控制节点均可访问的真实地址，单机loopback隧道不能当共享云地址。

## 验证结果

- Go：store/api/discovery 全包回归通过；登记、权限、发现链定向 race 通过；全仓 `go test ./...` 通过。全仓首轮因精简PATH缺少node失败；将现有node/go链接到隔离工具目录，保持系统工具PATH后重跑通过，未修改测试以规避失败。
- 浏览器：实际Chrome + Playwright、隔离fixture API，1440/390两视口共28断言通过，覆盖新端点→发现→副本展示、发现失败保留登记、当前集群撤销、PG默认端口与英文。没有请求真实数据库或现场API。
- 本功能变异：在临时源码副本分别取消跨集群冲突、取消幂等短路、保留过期拓扑，3/3被行为回归捕获，非编译报错。工作树生产源码未用于变异。
- source strict 45项通过；门禁self-test 128变异全捕获、两个对照正常；语言5项、许可68项、Markdown台账11项通过。生成器重建118页，无生成页净变化。
- 新UI只追加端点到所选已有集群，不登记Raft控制成员，不安装Agent。源码完成不代表服务器页面生效：本次没有构建/签名补丁，也没有部署、现场登记或数据库验收。现场仍需安装包含此入口的新版本，并提供对控制节点可达的云数据库地址。
