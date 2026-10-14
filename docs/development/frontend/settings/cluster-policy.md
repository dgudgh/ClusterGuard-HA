# 设置：全局引擎策略编辑

[前端页面](../README.md) → [设置功能](README.md) → 策略

修改策略写入、清除或自动切换联动前，必须读[操作授权规则](../../rules/operation-safety.md)及[后端策略语义](../../backend/settings/cluster-policy.md)。当前页面没有额外风险确认弹框，不能在文档中假设它存在。

## 页面入口与调用链

运行参数面板 → `configuration-policy-tab` → `configuration-policy-panel` 内的唯一 `configuration-policy` → `fetchClusterPolicy()` → `GET /api/v1/cluster-policy` → `state.clusterPolicy` → `renderClusterPolicy()`。

保存按钮检查浏览器数值合法性后，`clusterPolicyPayload(engine)` 组装当前策略记录 → `submitClusterPolicy()` → PUT → 重新读取配置与策略。清除按钮直接提交 `{ engines: {} }`。

## 作用范围与状态

- 策略作用于本控制面管理的**所有同引擎集群**，不随顶部选中集群改变。
- `cluster-policy-engine` 选择 mysql 或 postgresql；它选择覆盖层，不选择数据库节点。
- 观测次数、证据窗口、操作预算留空时沿用节点启动配置；维护抑制暂停该引擎自动切换。
- PUT 是整个记录替换；组装当前引擎字段时保留其他引擎的已有记录。清除按钮会移除全部引擎覆盖。
- `state.clusterPolicySaving` 防止重复保存；`state.clusterPolicy` 未取得时禁止保存/清除。
- 页面数值范围检查不能替代后端引擎、范围、角色、CSRF 和 Raft 校验。

## 失败边界

策略 GET 失败会令 `state.clusterPolicy` 为空并显示读取失败；配置 GET 成功不应让策略按钮恢复。PUT 的成功与后续重新读取分属两个请求，排查时分别核对服务端已提交结果和页面刷新，不重复提交来修显示。

## 定位与验证

实现位于 [console.html](../../../../internal/api/console.html) 的 `fetchClusterPolicy`、`renderClusterPolicy`、`clusterPolicyPayload`、`submitClusterPolicy` 和两个按钮事件。

[策略浏览器检查](../../../../tools/console-cluster-policy-audit.cjs)覆盖读取失败、字段输入/保存、可用性及布局；后端验证在[策略功能页](../../backend/settings/cluster-policy.md)。只改运行参数显示时读[配置视图](configuration.md)，不用预读升级交付文档。

## 独立视图与当前值

`state.configurationMode=policy` 时仅显示策略编辑器，控制节点选择与固定参数下发底栏同时隐藏。原 `cluster_policy` 摘要保留在后端投影中，前端不再作为重复节点参数组展示。读失败、异步刷新、语言切换及保存后的配置重读不能重新露出节点操作栏。

引擎选择同时刷新作用范围、维护开关、覆盖草稿、本节点生效值和来源。正数覆盖来自已读取的策略；未覆盖字段从对应 `engine:<引擎>` 的节点配置投影读取。缺字段、策略读取失败或旧投影读取失败时明确未确认，不猜默认值或宣称三台一致。

“清除所有引擎的策略覆盖”仍提交 `{ engines: {} }`，说明明确同时移除 MySQL/PostgreSQL 覆盖并恢复各节点配置；保存仍保留未编辑的另一引擎。会话清理清空策略输入及维护选中状态，恢复 MySQL 和节点视图。整页布局回归见[配置双视图验收](../../../../tools/console-configuration-layout-acceptance.cjs)。
