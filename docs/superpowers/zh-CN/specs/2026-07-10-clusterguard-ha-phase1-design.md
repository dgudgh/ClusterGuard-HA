# ClusterGuard 高可用性阶段1设计

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../specs/2026-07-10-clusterguard-ha-phase1-design.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 目标

构建一个独立的多数据库高可用性控制内核，适用于MySQL、PostgreSQL、Oracle和SQL Server。阶段1将提供稳定的资源身份、只读MySQL发现和健康状态、四个注册的适配器、元数据同步、受保护的工作流、审计记录、报告以及版本化的HTTP API。

## 范围

- 该仓库不包含任何来自冻结原型的兼容层、导入包、API路由、配置键、表、二进制文件名或服务名。
- 每个持久化资源使用平台UUID。网络坐标是可变的端点数据，且从不作为主键。
- 适配器在没有工作流发出的受保护执行上下文的情况下，不允许修改数据库。
- 未实现的修改能力会明确返回`unsupported`响应。

## 资源与身份模型

平台拥有`Platform`、`Controller`、`DatabaseCluster`、`DatabaseNode`、`DatabaseInstance`、`Endpoint`、`EndpointAlias`、`ReplicationLink`、`HAEndpoint`、`Operation`、`OperationPlan`、`Execution`、`Verification`、`AuditEvent`和`Report`资源。

`DatabaseInstance`包括平台UUID、集群UUID、引擎、引擎原生身份、显示名称、主机名、IP地址、端口、别名、角色、健康状态、元数据版本和生命周期时间戳。

引擎原生键包括：

- MySQL: `server_uuid`。
- PostgreSQL: 集群使用`system_identifier`；节点使用平台UUID。
- Oracle: `DBID + DB_UNIQUE_NAME`，RAC实例单独表示。
- SQL Server: 可用性组`group_id`和副本`replica_id`。

当重新发现相同引擎身份但主机名、IP或端口发生变化时，元数据仓库将更新该资源，增加其版本，并将旧端点记录为别名。它永远不会创建第二个资源。

## 适配器SDK

`DatabaseHAAdapter`提供`Engine`、`Capabilities`、`Discover`、`Topology`、`Health`、`Precheck`、`BuildPlan`、`Execute`、`Verify`、`NodeSyncPrecheck`、`BuildNodeSyncPlan`、`ExecuteNodeSync`、`MetadataPrecheck`和`ReconcileMetadata`。

注册表包含MySQL、PostgreSQL、Oracle和SQL Server适配器。MySQL适配器在阶段1中仅使用密码安全的本地客户端调用进行发现和健康检查。其他适配器是骨架，它们声明不支持的功能，并对每个未实现的操作关闭失败。

## 统一工作流

每个修改路径都遵循以下固定状态机：

```text
DISCOVER -> PRECHECK -> PLAN -> SAFETY_GUARD -> LOCK -> APPROVE -> EXECUTE -> VERIFY -> AUDIT -> REPORT
```

工作流核心负责锁获取、审批验证、安全检查、审计记录、验证、报告创建和终端状态。适配器仅提供引擎特定的评估和操作。

## API和控制台

HTTP服务暴露了所需的`/api/v1`引擎、能力、集群、拓扑、健康、操作、节点同步和元数据同步路由。一个紧凑的嵌入式控制台显示引擎、所选集群健康、拓扑和元数据异常。它在同一个API工作流之外没有直接的修改路径。

## 验证

测试覆盖UUID创建、原生身份键、端点同步、适配器注册、MySQL发现/健康、不支持的适配器、工作流门控、审计/报告生成和API响应。仓库扫描验证独立代码库中没有遗留依赖或兼容性命名。
