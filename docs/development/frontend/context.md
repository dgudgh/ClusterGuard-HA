# 集群上下文与导航

[返回模块](README.md)

修改顶部集群切换、刷新、请求世代或会话变化对操作确认和锁定状态的影响前，必须读[高风险操作授权规则](../rules/operation-safety.md)；只改导航样式或非操作文案时无需展开该规则。

## 调用链与边界

顶部集群选择 → 清理旧行/游标/请求 → 加载所选集群 → 更新当前页。排查异步问题时搜索 loadSelectedCluster、请求版本与会话失效处理。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html) |
| 回归 | [console-context-acceptance.cjs](../../../tools/console-context-acceptance.cjs)、[console-session-boundary-audit.cjs](../../../tools/console-session-boundary-audit.cjs) |

## 需要时再读

- [日志集群联动回归：旧版对比与修复证据](../../zh-CN/log-cluster-scope-regression.md)

## 必须保持与回归

- MySQL 与 PostgreSQL 分别验证；顶部切换、页面跳转、同集群刷新、快速连续切换。

## 既有集群接入已有实例

集群管理 → 接入已有实例：名称和引擎只读，端点追加到打开入口时的当前集群，不创建新集群。提交前重新核对权限和所选集群；请求发出后切换集群，不得将晚到结果强制切回原集群。

登记成功后发现；发现失败提示已登记但首次发现未完成，保留端点并允许重试。页面明确该功能不安装/重建数据库或Agent，发现成功不代表具备切换能力。英文文案沿用语言设置。桌面/窄屏点击、已有实例显示、发现失败与上下文撤销回归：`tools/console-existing-instance-acceptance.cjs`（隔离夹具，不代表云现场验收）。
