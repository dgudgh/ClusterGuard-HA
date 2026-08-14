# 控制台集群管理实施方案

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-17-console-cluster-management.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

> **对于智能代理工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐项实现此计划任务。步骤使用复选框（`- [ ]`）语法进行跟踪。

**目标：** 向已认证的 ClusterGuard HA 控制台添加安全的集群注册和退役功能。

**架构：** 重用现有的原子集群注册 API。添加一个仓库退役事务，并通过现有的已认证、CSRF 保护、quorum-leader 变更路由器暴露。添加一个单个工具栏模态框，调用这些 API 并刷新控制台状态。

**技术栈：** Go 1.19+，`net/http`，复制的 JSON 元数据仓库，嵌入的 HTML/CSS/JavaScript 控制台。

## 全局约束

- 集群退役从不接触数据库主机或删除数据库数据。
- 活动操作、操作锁、所有权租约和生命周期任务会阻止退役。
- 审计、报告、操作和终端生命周期历史必须保留。
- 浏览器变更需要管理员会话授权和 CSRF 验证。
- 仅使用现有的 `/api/v1` 命名空间和 ClusterGuard HA 命名。

---

### 任务 1：原子集群退役

**文件：**
- 创建：`internal/store/clusters.go`
- 测试：`internal/store/clusters_test.go`

**接口：**
- 生成：`Repository.RetireCluster(clusterID model.ResourceID, confirmDisplayName, actor string) (ClusterRetirement, error)`.
- 生成：`ClusterRetirement` 包含退役的集群和移除资源的数量。

- [ ] **步骤 1：编写失败的存储测试**

添加测试以证明精确名称确认、未知集群处理、运行中的操作/锁/租约/生命周期任务的阻塞、活动资源清单的原子删除、历史记录的保留、退役审计的创建以及跨仓库重新打开的持久性。

- [ ] **步骤 2：运行聚焦测试并确认失败**

运行：`go test ./internal/store -run 'TestRetireCluster' -count=1`

预期：由于 `RetireCluster` 不存在，构建失败。

- [ ] **步骤 3：实现仓库事务**

实现验证和一个克隆快照提交。仅删除活动集群资源清单映射，保留历史映射，并追加一个净化后的审计事件。

- [ ] **步骤 4：运行聚焦存储测试**

运行：`go test ./internal/store -run 'TestRetireCluster' -count=1`

预期：通过。

### 任务 2：认证退役 API

**文件：**
- 修改：`internal/api/clusters.go`
- 修改：`internal/api/clusters_test.go`
- 修改：`internal/api/auth_test.go`

**接口：**
- 使用：`Repository.RetireCluster`.
- 生成：`DELETE /api/v1/clusters/{resource_id}` 带有体 `{"confirm_display_name":"<name>"}`.

- [ ] **步骤 1：编写失败的 API 测试**

覆盖 200 成功、400 错误确认、404 未知集群、409 活动工作、查看器/操作员拒绝、管理员成功和Leader门控行为。

- [ ] **步骤 2：运行聚焦 API 测试并确认失败**

运行：`go test ./internal/api -run 'Test.*Cluster.*(Retire|Delete)' -count=1`

预期：失败，因为方法不允许或缺少处理程序。

- [ ] **步骤 3：实现 DELETE 分发和响应映射**

解码一个有界 JSON 体，推导认证的执行者，调用存储，并映射验证/未找到/冲突/持久性错误，而不暴露内部路径或秘密。

- [ ] **步骤 4：运行聚焦 API 测试**

运行：`go test ./internal/api -run 'Test.*Cluster.*(Retire|Delete)' -count=1`

预期：通过。

### 任务 3：集群管理模态框

**文件：**
- 修改：`internal/api/console.html`
- 修改：`internal/api/console_test.go`

**接口：**
- 使用：`POST /api/v1/clusters`，`POST /api/v1/clusters/{id}/discover` 和 `DELETE /api/v1/clusters/{id}`.
- 生成：工具栏按钮 `open-cluster-management-modal` 和模态框 `cluster-management-modal`.

- [ ] **步骤 1：编写失败的控制台契约测试**

断言工具栏条目、对话框控件、动态端点行、精确名称退役确认、API 调用、仅管理员控件、所选集群刷新和响应式固定操作页脚。

- [ ] **步骤 2：运行控制台测试并确认失败**

运行：`go test ./internal/api -run 'TestConsoleClusterManagement' -count=1`

预期：失败，因为模态框标记和 JavaScript 不存在。

- [ ] **步骤 3：实现响应式模态框和操作**

使用现有的对话框和按钮系统。将注册和退役保留在两个清晰的区域，避免嵌套卡片，显示一个明确的非破坏性作用域警告，并在确认匹配之前禁用退役。

- [ ] **步骤 4：运行控制台测试**

运行：`go test ./internal/api -run 'TestConsoleClusterManagement' -count=1`

预期：通过。

### 任务 4：验证和部署

**文件：**
- 修改：`docs/operations.md`

**接口：**
- 文档化集群注册和退役行为和操作员安全边界。

- [ ] **步骤 1：运行完整验证**

运行：`gofmt -w internal/store/clusters.go internal/store/clusters_test.go internal/api/clusters.go internal/api/clusters_test.go internal/api/auth_test.go`

运行：`go test ./...`

运行：`go build ./...`

运行：`git diff --check`

预期：所有命令成功。

- [ ] **步骤 2：部署到三个测试控制器**

构建 Linux 二进制文件，部署跟随者在当前Leader之前，重启 `clusterguard-ha.service`，并验证所有三个节点都处于活动状态，有一个 Raft Leader和两个跟随者。

- [ ] **步骤 3：执行浏览器验收**

验证管理员注册、自动发现、有活动工作的阻塞退役、精确名称确认、临时测试集群的成功退役、集群选择器刷新和小屏幕对话框可用性。
