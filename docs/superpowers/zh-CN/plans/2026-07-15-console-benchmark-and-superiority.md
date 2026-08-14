# ClusterGuard HA Console 对标超越实施计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-15-console-benchmark-and-superiority.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


> 按 TDD 执行；每个生产页面改动前先加入失败的契约测试。

**目标：** 在不改变后端 API 的前提下，将自包含控制台升级为全局资源优先、证据驱动、真实可执行的企业级高可用工作台。

**技术约束：** Go `embed` 的单页 HTML/CSS/JavaScript；无 CDN、无 Node 构建依赖；所有 API 数据使用安全 DOM API 渲染。

---

## 任务 1：建立超越基线测试

**文件：**
- 修改：`internal/api/console_test.go`

1. 新增全局资源筛选、观测新鲜度和集群行跳转契约测试。
2. 新增五阶段切换进度、一次性解锁和不确定结果禁止重试测试。
3. 新增紧凑拓扑、稳定连接线和异常摘要测试。
4. 新增节点流程分组、日志筛选和完整国际化测试。
5. 运行 `go test ./internal/api -run Console`，确认新测试失败。

## 任务 2：重构控制台壳层和总览

**文件：**
- 修改：`internal/api/console.html`

1. 统一 8px 视觉网格、侧栏、页头和内容宽度。
2. 改善导航语义和当前页状态。
3. 增加全局资源搜索、健康筛选、集群行操作和观测时间。
4. 保持总览不泄漏所选集群拓扑。
5. 运行总览相关测试。

## 任务 3：重构拓扑和元数据体验

**文件：**
- 修改：`internal/api/console.html`

1. 将拓扑改为紧凑圆角矩形和稳定逐行连接结构。
2. 固定节点信息为三行，VIP 和首选候选使用小型 badge。
3. 增加异常摘要和证据新鲜度。
4. 元数据弹窗分离不可变身份和可变 endpoint，增加变更说明。
5. 运行拓扑、身份和响应式测试。

## 任务 4：重构受控操作工作台

**文件：**
- 修改：`internal/api/console.html`

1. 保留五项上下文和一个切换按钮。
2. 增加 PRECHECK、PLAN、GATES、EXECUTE、VERIFY 阶段指示器。
3. 把后端状态映射为 succeeded/blocked/unsupported/indeterminate/failed。
4. 只对 `stale_plan` 重建计划；不确定结果绝不自动重试。
5. 执行结束自动锁回，并刷新拓扑和操作日志。
6. 运行操作契约测试。

## 任务 5：完善节点、指标、日志、设置

**文件：**
- 修改：`internal/api/console.html`

1. 节点表单按动作、身份、连接、数据库四组组织。
2. 任务进度显示阶段、状态、目标和报告。
3. 指标显示观测时间，缺失值不伪装为零。
4. 操作日志增加关键词、类型和状态筛选；保留折叠原始证据。
5. 设置增加内存态刷新间隔，补齐静态和动态中英文文案。
6. 运行页面全量测试。

## 任务 6：回归和交付

**文件：**
- 修改：`docs/mysql-feature-parity-acceptance.md`
- 修改：`docs/operations.md`

1. 运行 `gofmt`（如 Go 文件有改动）。
2. 运行 `go test ./internal/api`。
3. 运行 `go test ./...`。
4. 运行 `go vet ./...`、`git diff --check` 和 clean-room 扫描。
5. 构建 Linux 二进制并部署到 `192.168.102.152-154`。
6. 通过真实 API 验证集群、拓扑、候选、操作、节点、指标和日志页面数据源。
7. 对可控测试集群执行一次真实切换和一次旧主回挂，验证 VERIFY 后成功语义。
8. 更新验收文档并提交本地分支。
