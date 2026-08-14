# ClusterGuard HA 平台认证设计

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../specs/2026-07-16-platform-authenticated-operations-design.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 目标

将 ClusterGuard HA Web 控制台中手动输入 approval-token 的方式替换为经过认证的平台会话。已登录的操作员只需单击一次即可启动 MySQL 操作；服务器内部创建并消耗与计划绑定的一次性审批授权。浏览器从不接收或存储该授权。

初始平台管理员为：

- 用户名：`admin`
- 密码：`generated-bootstrap-password`
- 角色：`admin`

引导密码仅以密码哈希形式存储。初始管理员必须在允许任何变更平台操作之前更改该密码。

## 选择的方法

ClusterGuard 将使用服务器端、Raft 复制的用户和会话。认证通过不透明的会话 cookie 表示；授权通过平台角色表示。浏览器变更还需要 CSRF token。高风险数据库操作在内部保留现有的单次使用审批授权，保持工作流不变，无需用户粘贴令牌。

这比以下方法更优：

1. 浏览器请求并重放审批授权。这虽然去除了手动提示，但仍将授权暴露给 JavaScript 和浏览器工具。
2. 使用永久的浏览器/API token。这更简单，但会丢失操作、目标、拓扑、计划、过期时间和单次使用绑定。

## 安全模型

### 密码

密码使用 Argon2id 进行哈希处理，每个密码使用随机盐。存储的用户记录中仅包含编码后的 Argon2id 哈希和密码元数据。密码明文从不写入元数据快照、日志、审计消息、API 响应或命令参数。

生产环境的 Argon2id 配置为：

- 内存：64 MiB
- 迭代次数：3
- 并行度：2
- 盐：16 个随机字节
- 密钥：32 字节

测试使用注入的低成本配置，但不更改生产默认值。

新密码必须满足以下条件：

- 至少包含 12 个字符；
- 与当前密码不同；
- 不等于引导密码 `generated-bootstrap-password`。

### 引导管理员

启动时，leader 确保存在一个标准化的 `admin` 用户。如果不存在该用户名的用户，ClusterGuard 会使用引导密码哈希、角色 `admin` 和 `must_change_password=true` 创建该用户。

引导过程是幂等的，并通过复制的元数据存储提交。重启时不会覆盖现有用户。

### 会话

成功登录后，`clusterguard_session` cookie 中返回一个不透明的随机会话令牌。该 cookie 的属性为：

- `HttpOnly`；
- `SameSite=Strict`；
- `Path=/`；
- 当请求为 HTTPS 时，`Secure`；
- 绝对生命周期限制为八小时。

仅存储会话密钥的 SHA-256 哈希。会话状态包括用户 ID、用户认证版本、签发时间、过期时间、撤销时间和 CSRF-token 哈希。会话不使用滑动过期，避免每次请求都写入复制的元数据。

更改密码会增加用户的认证版本并撤销该用户的现有会话。

### CSRF

登录时还会设置一个 `clusterguard_csrf` cookie，该 cookie 可被同源 JavaScript 读取。每个更改状态的浏览器请求必须在 `X-CSRF-Token` 中发送相同的值。服务器将令牌与会话中存储的哈希进行验证。

使用 Bearer 认证的服务 API 调用不使用 cookie，因此不需要 CSRF 验证。Bearer 控制凭证无法直接执行高风险数据库操作；它只能使用现有的显式 plan-and-grant API 合约。

### 角色

首次实现定义了三个稳定的角色：

- `admin`：平台的全部访问权限，密码管理，元数据，生命周期和所有数据库操作；
- `operator`：只读访问和支持的数据库操作；
- `viewer`：只读访问。

在此交付中，仅暴露默认管理员和自更改密码。模型和策略边界支持后续用户管理 API，而无需更改存储的身份或会话语义。

`must_change_password=true` 的用户只能调用以下操作：

- 登录；
- 当前会话检查；
- 更改密码；
- 注销。

在更改引导密码之前，所有其他 API 调用都会被阻止。

## 数据模型

### PlatformUser

```text
resource_id
username
display_name
role
password_hash
must_change_password
disabled
auth_revision
password_changed_at
created_at
updated_at
metadata_revision
```

用户名会被修剪并不区分大小写进行比较。`resource_id` 是不可变的身份。

### PlatformSession

```text
resource_id
user_id
token_hash
csrf_hash
user_auth_revision
issued_at
expires_at
revoked_at
created_at
updated_at
metadata_revision
```

这两个记录都存储在现有的复制元数据快照中，因此在进程重启和控制器故障转移后仍然存在。

## API

公共认证路由：

```text
POST /api/v1/auth/login
```

认证路由：

```text
GET  /api/v1/auth/me
POST /api/v1/auth/logout
POST /api/v1/auth/password
```

登录请求：

```json
{
  "username": "admin",
  "password": "generated-bootstrap-password"
}
```

登录和当前会话响应从不包含密码哈希、会话哈希或审批秘密。它们返回：

```json
{
  "status": "ok",
  "result": {
    "user": {
      "resource_id": "...",
      "username": "admin",
      "display_name": "Administrator",
      "role": "admin",
      "must_change_password": true
    }
  }
}
```

登录失败使用一个通用消息，以避免用户名枚举。

## 请求授权

根控制台 HTML 和登录端点在没有会话的情况下仍然可访问。监控端点保留其专用的监控令牌。代理协调保留其签名的代理协议。

所有其他 `/api/v1` 路由需要以下之一：

- 有效的平台会话；或
- 该路由明确支持服务自动化的现有 Bearer 服务凭证。

在路由执行之前评估角色策略。Cookie 认证的变更请求需要 CSRF 验证。Leader 和 quorum 变更权限检查保持独立，并在认证之后运行。

## 平台拥有的审批

对于会话认证的数据库执行：

1. 解析认证用户和角色。
2. 要求为 `admin` 或 `operator`。
3. 执行持久的 `PRECHECK` 和 `PLAN`。
4. 创建一个五分钟、与计划绑定的 `ApprovalGrant`，其中 `issued_by=<username>`。
5. 仅在服务器内存中保留明文授权。
6. 进入现有工作流。
7. 在操作锁下，原子地消耗授权并推进 `APPROVE`。
8. 执行、验证、审计和报告。
9. 在返回之前丢弃明文授权。

操作 API 响应包含操作和验证状态，而不是授权令牌。

显式服务自动化保留：

```text
POST /api/v1/approvals
POST /api/v1/operations/{id}/execute
```

这些路由继续需要管理员授权加上显式的单次使用授权。它们不会成为浏览器快捷方式。

自动故障转移保持独立。它使用现有的内部事件身份，无法通过 JSON 输入选择。

## Web 控制台

控制台新增：

- 专用的登录屏幕；
- 首次登录更改密码屏幕；
- 壳层中的当前用户名和角色；
- 更改密码和注销命令；
- 认证过期后返回登录流程。

控制台移除：

- 管理员控制令牌字段；
- 手动一次性审批令牌对话框；
- 审批令牌 JavaScript 状态。

现有的操作员锁仍作为有意操作的保护措施。解锁后点击 `执行切换` 会发送所选集群和候选；服务器拥有审批的发放和消耗。

## 错误处理

- 登录无效：`401`，通用的凭据无效信息。
- 会话缺失/过期/被撤销：`401`。
- 缺失或无效的 CSRF 令牌：`403`。
- 权限不足：`403`。
- 引导密码未更改：`403`，并带有
  `password_change_required=true`。
- 引导登录重复竞争：幂等地返回已持久化的用户。
- 审批创建或持久化失败：在数据库变更之前失败。
- 会话持久化或 Raft 失败：登录或变更操作关闭时失败。
- 密码更改持久化失败：保留旧密码和会话。

## 审计

操作审计记录使用已认证的用户名作为 `requested_by`。
审批审计记录标识该授权是由平台为会话用户发出的。
登录成功、登录失败、注销和密码更改记录为安全事件，不包含密码、cookie、CSRF 或 IP-密钥数据。

## 测试

测试必须覆盖以下内容：

- 引导管理员创建和重启的幂等性；
- Argon2id 哈希和明文数据的缺失；
- 正确和错误的登录；
- 禁用用户和强制密码更改；
- 会话重启和 Raft 复制；
- 会话过期、撤销和认证修订无效；
- cookie 认证变更需要 CSRF；
- 角色授权；
- 密码更改会撤销所有会话；
- Web 执行创建并使用一个内部授权；
- 浏览器响应中从不包含授权密钥；
- Bearer 控制令牌无法绕过高风险授权；
- 自动故障转移保持无令牌且仅限内部使用；
- 控制台包含登录/更改密码/注销，但不包含授权令牌输入；
- 企业禁用和不支持的适配器行为保持拒绝访问；
- 完整的 MySQL 工作流程、验证、审计和报告回归测试覆盖率。

## 范围

本次交付完成了当前 MySQL 控制体验的平台认证。PostgreSQL、Oracle 和 SQL Server 仍作为已注册的适配器，保留其现有的不支持执行能力。用户管理界面、SSO、LDAP、OIDC、MFA 和个人 API 令牌被有意延迟。
