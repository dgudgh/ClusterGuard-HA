# 前端页面入口

**修改前必读：[门禁执行与更新流程](../../zh-CN/validation-gate-workflow.md)。** 涉及控制台更新流程、操作管理或其他契约范围时，先完整读[强制契约 v2](../../zh-CN/upgrade-validation-chain.md)；完成必读后再选页面。

页面实现集中在 `internal/api/console.html`。先读[前端通用交互规则](../rules/frontend-common.md)，再按问题选择页面；此处的目录是阅读导航，不代表源码组件划分。

| 页面或问题 | 功能文档 |
| --- | --- |
| 设置：状态、账户、偏好、参数、策略、版本更新 | [设置功能](settings/README.md) |
| 顶部集群切换、刷新、请求晚到 | [集群上下文](context.md) |
| 总览和拓扑展示 | [总览与拓扑](overview.md) |
| 切换与恢复的按钮、确认、进度 | [操作与恢复交互](operations.md) |
| 节点添加、修复、生命周期弹窗 | [节点交互](nodes.md) |
| 指标名称、数值和引擎支持 | [指标展示](metrics.md) |
| 日志范围、筛选、分页、原始详情 | [操作日志](logs.md) |
| 版本与关于信息 | [关于页面](about.md) |
| 共同壳层、跨引擎能力或全页面验收 | [跨页面与引擎检查](engines.md) |
