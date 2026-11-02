# 网关真实MySQL与页面端到端测试

2026-11-02，用户指出上一轮仍缺实测。本轮补真实数据库、实际HA进程和控制台操作，不将模拟SQL/端点计作数据库验收。

## 修改前与门禁

- 主线codex/2.2-postgresql，HEAD 02cabe0，进入时工作树干净；独立网关75b7860。现场版本未读取，不以本地HEAD推定现场已升级。
- 已读validation-gate-workflow.md、upgrade-validation-chain.md全文、operation-safety.md、change-policy.md、前端operations.md/frontend-common.md、后端operations.md/persistence.md。
- CG-UPGRADE-CONTRACT；contract_version=2；contract_loaded=true。适用SOURCE与隔离真实数据库端到端测试；没有执行签名更新或云现场，不填ART/FIELD通过。
- 对照e2d6def与02cabe0：已有gateway_process_integration_test.go实际后端为tcpBackend字符串端点；gateway_switchover_test.go为switchoverSQLClient夹具；console-gateway-acceptance.cjs使用fixture API。它们不能证明真实复制、实际数据库身份或页面切换闭环。
- 只读发现本机MySQL 8.0.46 arm64可用；Docker daemon未运行。采用独立空目录和高位loopback端口，不读写Homebrew默认MySQL数据或用户集群。

## 本轮计划与边界

独立初始化真实MySQL主从，启动真实Raft控制面、独立双网关及mTLS管理通道，使用实际页面鉴权、发现/候选、操作锁和切换接口，验证固定入口往返切换、旧连接关闭、持续写入恢复、GTID/复制追平、唯一可写主、租约续期及失去授权停写。所有原始日志保存到本地.build/gateway-real-20261102；不存入公开文档。

测试不修改192.168.102.152等已用实例，不删除现场binlog，不使用已有数据库凭据。真实云链路、云LB及用户业务系统仍需另行现场验收；本地多个真实实例不能冒充跨云测试。

## 实测发现与修改前对比

首次真实运行建立了三台MySQL的GTID复制，但OwnershipKeeper发布首条网关授权时，真实Raft提交报metadata snapshot state digest does not match contents，路由保持关闭。02cabe0的snapshot把gateway_publications插在clusters前面，而既有snapshotPayload以clusters为载荷起点，导致非空网关记录计入编码摘要却被验签载荷截掉。先添加有网关授权的快照往返/篡改回归复现，再把可选扩展追加在旧载荷之后；不得删除或放宽摘要检查。

## 2026-11-02实际结果

环境：macOS arm64、原生MySQL 8.0.46，三台独立真实数据库、三进程Raft投票控制面（TLS）、双独立网关（管理通道mTLS）、真实Chrome。HA页面仅绑定loopback HTTP；没有云LB、跨云链路或Linux现场Agent进程。复制采用明确配置的GTID异步模式，不能据此声明半同步验收。

| 验证 | 本轮实际结果 |
| --- | --- |
| 摘要缺陷回归 | 修改前测试失败并点名digest mismatch；修复后网关授权快照往返与篡改拒绝通过，真实Raft成功提交授权 |
| 页面鉴权 | 真实admin首次登录、强制改密、重新登录、候选选择、解锁、原切换按钮；无fixture API或响应拦截 |
| MySQL往返切换 | 第一节点→第二节点→第一节点，两次操作均HTTP 200 / succeeded / verification.passed=true |
| 固定入口 | 每次切换后从两个网关分别查询真实server_uuid，均指向目标库 |
| 旧会话 | 两次切换前建立的真实MySQL长连接均报ERROR 2013并退出，未继续指向旧主 |
| 写入与中断 | 全新目录完整复跑155次已确认写入，三台均可查到；52次连接握手失败未自动重放。最长相邻成功写入间隔3093ms；包含页面及SQL核验的两次操作测量分别5144ms、5159ms |
| 主从一致性 | 最终唯一可写主，两从read_only/super_read_only均ON；两从IO/SQL均ON且错误号0；三台GTID集合一致，总行数均155 |
| 稳定续期 | 等待6秒后双网关授权Sequence均递增，目标不变 |
| 失去多数派 | 实际停止本实验两个跟随控制进程，剩余节点quorum_confirmed/mutation_authority均false；11.541秒内双路由关闭，两入口SQL连接均拒绝 |

客户端必须在旧连接关闭后重新建连；不能把短暂中断说成零中断，不能自动重放结果未知的事务。本实验只证明网关授权失效后的流量隔离；没有运行现场Agent，不能据此证明主机本地持久只读隔离。跟随节点页面保守显示authority_unconfirmed，切换验证使用真实Leader页面。

原始本地证据位于`.build/gateway-real-20261102-r3/`：`browser-real-result.json`、`real-safety-result.json`、`final-real-mysql.json`、`source-binding.json`、`real-before.png`、`real-after-*.png`及各进程日志。目录中含仅供实验使用的凭据和TLS私钥，禁止提交/公开。首次失败与修复中间复测保留于`.build/gateway-real-20261102/`和`-r2/`。源码门禁46/46、许可68/68、Markdown台账11/11通过；store/consensus/endpoint/coordination/api/mysql六包回归及前四包竞态通过，store的go vet与两个测试脚本语法检查通过。HTML重建118页，入库生成页无漂移。未执行签名安装包、远程升级或云现场验收。

## 复跑入口

使用全新`.build/<实验名称>/`，将当前平台的真实HA与独立网关二进制分别构建为`bin/clusterguard`和`bin/cg-gateway`。MySQL二进制可用`CG_TEST_MYSQL`、`CG_TEST_MYSQLD`指定；默认使用本机Homebrew路径。需要真实MySQL 8.0，不会降级为mock或静默跳过。

```sh
python3 tools/gateway-real-mysql-lab.py prepare .build/<实验名称>
NODE_PATH=<已安装Playwright的模块目录> node tools/console-gateway-real-mysql.cjs .build/<实验名称>
python3 tools/gateway-real-mysql-lab.py safety .build/<实验名称>
python3 tools/gateway-real-mysql-lab.py snapshot .build/<实验名称>
python3 tools/gateway-real-mysql-lab.py cleanup .build/<实验名称>
```

`prepare`保留数据并拒绝覆盖既有实验；若失败，先检查日志，按已记录PID执行cleanup后换一个全新目录。改密后复跑浏览器需设置`CG_LAB_PASSWORD_CHANGED=1`；该变量只选择本次实验生成的密码，不提供绕过鉴权。`safety`会停止两个本次实验的HA跟随进程；`cleanup`只停止本次记录且命令行仍匹配实验绝对路径的进程，保留全部数据和证据。
