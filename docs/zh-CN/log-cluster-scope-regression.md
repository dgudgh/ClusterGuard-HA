# 日志集群联动回归：旧版对比与修复证据

日期：2026-09-09。仓库工作树：`clusterguard-ha/.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`。

## 流程记录

本轮开始修改联动逻辑前，只检查了当前实现并复现问题，没有先完成历史正常版本的代码对照。用户明确要求后才补做 Git 旧版对比。这是流程违例，不能倒填成“已在修改前完成”。本次暂停新包发布并补齐证据；这次补救不是以后允许先改后查的例外。后续修改必须遵守项目根目录 `AGENTS.md` 的前置规则。

随后用户明确要求继续修复并交付补丁。继续执行时，旧版对比已经完成；本轮没有新增业务代码修改，重新运行回归并构建 2.2-94 → 2.2-95 签名包。此前未事先比较的事实不改为通过，后续前置规则保持不变；交付详情见 [2.2-95 发布说明](release-2.2.95.md)。

## 可信基线

| 对象 | 标识 | 含义 |
| --- | --- | --- |
| 正常旧行为源码 | `dc1a57ad8f8224ffdb267ec64c155405f5409447` | `bc0546a` 的父提交，日志按顶部集群查询 |
| 引入回归的提交 | `bc0546a3e91ac86c61017d6d57c7ceeedd3d114f` | 2026-08-31，`release: prepare ClusterGuard HA 2.2-68 upgrade flow` |
| 当前 Git HEAD | `bc0546a3e91ac86c61017d6d57c7ceeedd3d114f` | 当前工作树还有后续未提交修改，不能用 HEAD 代表已发布 94 内容 |
| 2.2-94 来源 RPM | `d7659bcd8c8a214ae1b32df66657bd8bee8fe0cc369f54ec33453009b77adae6` | 先前已交付的 RPM SHA-256；版本/字段内容应以包内代码再次核验 |

继续交付前已再次核验上述 RPM 摘要，并提取实际控制台 HTML（不是程序中另一个报告模板），其 SHA-256 为 `c2c9dc5741588fd89e283a8ea843497440bc6a5ec0325d0fa7552c1a1cb29022`。包内确实存在 `logClusterId: 'all'`，与当前修复后代码的完整差异保存在交付目录 `console-94-to-95.diff`；其它页面的静态样式和结构未变。

这里验证的是本地 Git 历史，不声称已核对远程分支最新状态，也不将截图当成现场运行版本查询。

## 旧代码证据

文件：`internal/api/console.html`。行号对应指定提交，不对应当前工作树。

- `dc1a57a` 第 3346–3350 行的 `loadOperationLog`：无 `selectedClusterId` 就清空日志，有选择则调用 `/api/v1/operations?cluster_id=${state.selectedClusterId}`。
- `dc1a57a` 第 3765–3769 行的 `loadSelectedCluster`：随集群刷新取 `/api/v1/operations?cluster_id=${clusterId}`，日志作用范围与顶部一致。
- `bc0546a` 第 1440 行新增 `logClusterId: 'all'`；第 3573–3579 行的日志下拉框保留独立选择，默认全部集群。
- `bc0546a` 第 3645–3648 行将 `loadOperationLog` 改为 `/api/v1/operations` 全局读取；`filteredOperations` 改用 `state.allOperations`，不再以顶部当前集群作为默认日志范围。
- 本轮修复前的工作树已经使用 20 条服务端分页，但仍继承独立 `logClusterId: 'all'`。顶部 `change` 只触发 `loadSelectedCluster`，没有同步日志范围，因此不是分页按钮能解决的问题。

复核命令：

```sh
git log --all --format='%h %ad %s' --date=short -G 'logClusterId|log-cluster-filter|operations\?cluster_id' -- internal/api/console.html
git show dc1a57a:internal/api/console.html | sed -n '3346,3351p;3765,3771p'
git show bc0546a:internal/api/console.html | sed -n '1436,1441p;3573,3593p;3645,3649p'
git diff dc1a57a bc0546a -- internal/api/console.html
```

结论：用户记忆正确，旧版确实按顶部集群过滤。回归在 `bc0546a` 将日志改成独立全局范围，不是 PostgreSQL 返回 MySQL 数据，也不是数据库复制拓扑的问题。

## 保留与修复范围

恢复旧版的默认联动，不恢复旧版每次刷新都等待全量日志的性能问题：

1. 初次进入日志页按顶部所选集群请求第一页；尚未选择集群时不静默查询全局日志。
2. 顶部变化立即同步日志范围，取消旧请求、清除旧记录和游标。新请求明确带 `cluster_id`，晚到响应不得串入新列表。
3. 相同顶部集群的刷新不重置已加载页。日志页隐藏时只同步范围，进入页面才发出分页请求。
4. 保留用户手动选择全部集群的入口；下一次顶部集群切换重新按顶部过滤。
5. 服务端响应若含不属于请求集群的条目，前端拒绝展示并显示错误，不把它当作正常列表。
6. 保留 20 条分页、原始详情按需读取、全历史筛选、完整日志保留、独立操作安全上下文和已确认的白色单行工具栏。

## 本地验证

`tools/console-log-scope-acceptance.cjs --baseline` 在修改前的实际控制台 HTML 上复现两个失败：首次默认没有集群参数；顶部切换没有重新查询对应日志。证据为 `.build/log-scope/before/result.json` 和 `mixed-history.png`。

修复后的 `tools/console-log-scope-acceptance.cjs` 验证 9 项场景，包括初始只请求一次、慢日志不阻塞拓扑、同集群刷新保留翻页、隐藏页不预取、旧页迟到不混入、显式全局范围、12 次 MySQL/PG 切换、错误范围响应拒绝、无集群不查全局。证据为 `.build/log-scope/after/result.json`、`mysql-only.png`、`pg-only.png`。

同时保留并通过分页交互回归、1,000 条日志的刷新隔离回归、320–1920px 的 11 个布局检查及全量 Go 测试。上述浏览器数据均是隔离 fixture，不是现场验收；未 SSH，未部署，未执行数据库操作。
