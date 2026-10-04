# 操作日志与分页

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

上下文和筛选 → 第一页 → 用户加载更多 → 展开原始详情；参数、游标、迟到响应和最终计数一起核对。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[operation_page.go](../../../internal/api/operation_page.go)、[operations.go](../../../internal/api/operations.go) |
| 回归 | [console-log-pagination-acceptance.cjs](../../../tools/console-log-pagination-acceptance.cjs)、[console-log-scope-acceptance.cjs](../../../tools/console-log-scope-acceptance.cjs)、[console-log-toolbar-acceptance.cjs](../../../tools/console-log-toolbar-acceptance.cjs) |

## 需要时再读

- [log-cluster-scope-regression](../../zh-CN/log-cluster-scope-regression.md)
- [audit](../backend/audit.md)

## 必须保持与回归

- 顶部所选集群是当前工作上下文。日志默认跟随它；用户明确选择全部集群才跨集群显示。切换顶部集群必须清理旧行、旧游标和旧请求，重新查询所选集群。
- 日志先查询一页，点击加载更多才取下一页，展开才读原始详情。筛选查询全部历史，不能只筛当前页；包保留最近三个版本不等于日志只保留三条。
- 性能修复不能恢复全量日志阻塞页面刷新，也不能靠取消健康核验、观测一致性、签名、权限、fencing、writer lease、维护门禁或多数派检查变快。
- 第一页、加载更多、搜索/筛选、空结果、失败重试；旧请求晚到不得覆盖/追加到新上下文。
