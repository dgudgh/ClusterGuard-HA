# ClusterGuard HA 一次性审批与自动恢复授权设计

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../specs/2026-07-16-clusterguard-one-time-approval-design.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 状态

已于 2026-07-16 审批实施。

## 问题

ClusterGuard HA 当前使用两个长期有效的共享密钥：

- `CG_CONTROL_TOKEN` 用于对每个变更型 HTTP 请求进行身份验证。
- `CG_APPROVAL_TOKEN` 用于审批每个工作流执行，包括自动故障转移。

这混合了 API 身份验证、人工审批和内部控制器权限。它还迫使浏览器控制台持有长期有效的密钥，并导致页面刷新后正常的切换操作失败。

## 目标

1. 手动高风险操作使用短期、一次性的审批授权凭证。
2. 仅特权管理员或审批者可颁发授权凭证。
3. 授权凭证绑定到单个集群、操作类型、目标和不可变计划。
4. 明文授权凭证仅返回一次，且永不存储。
5. 授权凭证的颁发、验证、消费、拒绝和过期均被审计。
6. 自动故障转移不需要人工审批授权凭证。
7. 自动故障转移在 Leader、多数派仲裁（quorum）、事件、隔离（fencing）、安全卫士（Safety Guard）、操作锁、验证、审计和报告之后保持故障安全（fail-closed）。
8. 公共 HTTP 请求无法声明自动恢复身份。
9. 标准 Web 控制台不再需要长期有效的控制令牌来执行已审批的操作。

## 非目标

- 在此增量版本中构建外部 IAM、LDAP、OIDC 或 SSO 提供商。
- 允许匿名变更操作。
- 从管理 API 中移除身份验证。
- 削弱安全卫士、操作锁、拓扑固定（topology pinning）、VIP 租约、验证、审计或报告持久性。
- 使 PostgreSQL、Oracle 或 SQL Server 的变更操作可执行。

## 授权模型

### 管理主体

现有的控制凭据转变为管理 API 凭据。它可用于注册清单、触发特权维护端点以及颁发审批授权。该凭据不输入到常规操作控制台。

在将来的外部身份提供商替换它之前，管理凭据仍作为部署密钥保留。

### 手动操作员

手动操作员在执行高风险操作时提供一次性审批授权。该授权仅用于授权其绑定的意图；它不会成为通用的控制 API 凭据。

HTTP API 仅在匹配的执行端点上接受该授权。授权不能用于注册、任意发现发布、授权颁发或其他操作。

### 自动恢复主体

自动恢复使用运行时创建的内部类型化系统授权。它不由请求参数或 HTTP 头表示。

只有在 ClusterGuard 进程内组装的恢复控制器才能调用自动执行入口点。入口点验证以下内容：

- 已启用自动故障转移；
- 调用者持有当前的变更权限；
- 事件处于稳定且当前状态；
- 操作为 `failover`；
- 请求的执行者是固定的自动恢复执行者；
- 请求包含内部生成的事件标识。

自动路径在进入安全卫士之前进入相同的工作流，并且不能跳过人工审批之后的任何关卡。

## 审批授权模型

添加一个 Raft 复制的 `ApprovalGrant` 资源：

```text
resource_id
token_hash
operation_id
cluster_id
engine
operation_kind
target_id
plan_digest
observation_digest
issued_by
issued_at
expires_at
consumed_at
consumed_by_operation_id
status
metadata_revision
```

规则：

- 授权令牌格式：`cgag_<grant-id>.<256-bit-random-secret>`。
- 仅存储随机令牌的 SHA-256 哈希值。
- 默认 TTL：5 分钟。
- 最大 TTL：15 分钟。
- 状态为 `active`、`consumed`、`expired` 或 `revoked`。
- 明文令牌仅在签发响应中返回。
- 验证使用恒定时间哈希比较。
- 集群、引擎、类型、目标、计划摘要和观测摘要必须匹配。
- 在集群操作锁下，授权令牌的消耗与 APPROVE 阶段转换原子性地执行。
- 已消耗、已过期、已撤销、格式错误或不匹配的授权令牌将导致失败并关闭（fail closed）。
- 适配器变更之前会消耗授权令牌。除非持久化幂等键解析为已运行或终态操作（该操作已消耗此令牌），否则重试需要新的授权令牌。

## 计划绑定

授权令牌的签发接收预期的集群、操作类型和目标。服务器创建或复用持久的计划操作，捕获当前的拓扑观测数据，解析适配器请求，构建不可变计划，并将生成的操作 ID、计划摘要和观测摘要绑定到授权令牌上。

执行过程独立地重建或恢复持久化计划。审批消耗将持久化记录与授权令牌进行比较。拓扑或计划的变更会阻止执行，并要求新的授权令牌。

## API

### 签发授权令牌

```text
POST /api/v1/approvals
Authorization: Bearer <administrative-control-token>
```

请求：

```json
{
  "cluster_id": "uuid",
  "engine": "mysql",
  "operation_kind": "switchover",
  "target_id": "uuid",
  "issued_by": "dba-admin",
  "ttl_seconds": 300
}
```

响应返回授权令牌元数据以及明文 `approval_token`（仅一次）。

### 检查授权令牌

```text
GET /api/v1/approvals
GET /api/v1/approvals/{id}
```

响应中从不包含 `token_hash` 或明文令牌。

### 手动执行

现有执行负载保留 `approval_token`。该路由接受匹配的临时授权，无需通用控制凭据。其他变更路由仍受管理身份验证保护，除非显式转换为基于授权的执行。

### CLI

添加：

```text
cgctl approval issue --cluster <uuid> --kind switchover --target <uuid> --ttl 5m
cgctl approval list
cgctl approval show <grant-id>
```

签发操作从配置的环境变量中读取管理凭据。CLI 仅打印一次密钥，并警告该密钥无法恢复。

## 工作流接口

将仅字符串的审批契约替换为授权信封：

```go
type ExecutionAuthorization struct {
    Mode          AuthorizationMode
    ApprovalToken string
    IncidentID    string
}
```

公共 API 仅能构造 `AuthorizationManual`。恢复包使用单独的 `ExecuteAutomatic` 方法，在内部构造 `AuthorizationAutomatic`。JSON 输入绝不控制 `Mode`。

审批验证器接收持久化操作记录，以便在原子性消费授权之前比较目标、计划摘要和观察摘要。

## 存储与共识

审批授权存储在仓库快照中，因此使用现有的 Raft 快照 CAS 协议。签发、消费、撤销和过期操作均为 Leader 独占的变更操作。

授权消费与操作的 APPROVE 状态转换必须作为单次仓库变更原子执行。Follower 节点绝不会独立消费授权。

过期授权可在查找期间惰性标记，或通过有界清理流程进行标记。过期判断始终使用 Leader 时钟，并在消费时重新检查。

## Web 控制台

- 从常规控制台设置中移除控制令牌（control-token）字段。
- 将剩余字段重命名为 `一次性审批令牌`。
- 说明该令牌由管理员生成，且仅可消费一次。
- 在每次执行尝试后清除该字段及内存中的值。
- 如果 API 报告已消费、已过期、不匹配或无效，则显示确切的中文原因并保持开关锁定状态。
- 自动故障转移状态为只读，且从不请求令牌。

现有的显式解锁按钮仍作为本地防误操作控件；它并非授权机制。

## 自动故障转移

从自动恢复启动流程中移除静态审批令牌（approval-token）要求。
控制器调用 `ExecuteAutomatic`，而非公共手动执行合约。

每次自动操作均记录以下内容：

- 执行者 `clusterguard-automatic-recovery`；
- 触发器 `stable_primary_failure`；
- 事件标识符及首次稳定观测时间；
- Leader 标识符及当前任期（若可用）；
- 选定的目标节点及不可变的计划摘要；
- 安全守卫、锁定、隔离（fencing）、执行、验证、审计及报告的结果。

任何 HTTP 端点均不接受自动授权模式。

## 迁移

1. 现有的 `CG_CONTROL_TOKEN` 仍作为管理凭据。
2. 现有的 `CG_APPROVAL_TOKEN` 已弃用，在新执行中被忽略。
3. 自动故障转移不再需要 `CG_APPROVAL_TOKEN`。
4. 安装程序停止在面向浏览器的说明中放置审批密钥。
5. 首个发布版本在检测到已弃用的变量存在时记录明确的警告日志。
6. 无兼容性回退机制接受旧的静态审批令牌。

## 错误语义

- 缺少手动授权：`approval grant is required`。
- 授权格式错误：`approval grant is invalid`。
- 授权已过期：`approval grant has expired`。
- 授权已消耗：`approval grant has already been consumed`。
- 作用域不匹配：`approval grant does not match this operation`。
- 计划不匹配：`approval grant plan is stale`。
- 非主节点问题/消耗：现有主节点/多数派仲裁阻止响应。
- 尝试通过 HTTP 选择自动模式：作为无效输入被拒绝。

错误在变更之前保持 `blocked`，并在不暴露令牌明文的情况下进行审计。

## 测试策略

单元测试：

- 随机令牌格式和仅哈希持久化；
- 精确的作用域和计划匹配；
- 过期、撤销和一次性消耗；
- 原子消耗加上 APPROVE 状态转换；
- 重启和 Raft 复制持久化；
- 快照、审计、报告或日志中无明文令牌；
- 自动执行仅通过内部 API 在无授权的情况下成功；
- 公共 HTTP 无法伪造自动授权；
- 旧的静态审批令牌被拒绝。

API 和控制台测试：

- 特权颁发需要管理凭据；
- 颁发响应仅一次揭示密钥；
- 手动执行接受一个匹配的授权并拒绝重复使用；
- 控制台没有控制令牌字段；
- 控制台在执行后清除一次性令牌；
- 从节点响应保留主节点信息。

集成测试：

- 计划切换消耗一个授权并移动主节点和 VIP；
- 使用相同授权的第二次切换被阻止；
- 自动故障转移在主节点和多数派仲裁下无授权完成；
- 多数派仲裁丢失、缺少隔离、重复 VIP 和陈旧拓扑仍然阻止自动故障转移。

## 验收标准

1. 手动切换不再因浏览器缺少 `CG_CONTROL_TOKEN` 而失败。
2. 若无匹配的生效一次性授权，高风险手动操作无法执行。
3. 授权不可重复使用，亦不可应用于其他集群、目标、类型或计划。
4. 自动故障转移无需人工令牌，且无法通过 HTTP 以自动方式调用。
5. 所有现有的安全、锁、隔离（fencing）、验证、审计、报告、Raft 及幂等性测试均保持通过状态。
6. 三节点测试环境成功完成一次手动切换、一次被拒绝的授权复用尝试以及一次自动故障转移演练。
