# 操作日志与分页

[返回模块](README.md)

修改 API 分页参数、游标、服务端筛选或列表投影前，必须读[后端日志与审计查询](../backend/audit.md)；只有提交、复制、CAS 或存储保留出错时，才从后端功能页继续进入持久化基础设施。

## 调用链与边界

上下文和筛选 → 第一页 → 用户加载更多 → 展开原始详情；参数、游标、迟到响应和最终计数一起核对。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[operation_page.go](../../../internal/api/operation_page.go)、[operations.go](../../../internal/api/operations.go) |
| 回归 | [console-log-pagination-acceptance.cjs](../../../tools/console-log-pagination-acceptance.cjs)、[console-log-scope-acceptance.cjs](../../../tools/console-log-scope-acceptance.cjs)、[console-log-toolbar-acceptance.cjs](../../../tools/console-log-toolbar-acceptance.cjs) |

## 需要时再读

- [日志集群联动回归：旧版对比与修复证据](../../zh-CN/log-cluster-scope-regression.md)

## 必须保持与回归

- 顶部所选集群是当前工作上下文。日志默认跟随它；用户明确选择全部集群才跨集群显示。切换顶部集群必须清理旧行、旧游标和旧请求，重新查询所选集群。
- 日志先查询一页，点击加载更多才取下一页，展开才读原始详情。筛选查询全部历史，不能只筛当前页；包保留最近三个版本不等于日志只保留三条。
- 第一页、加载更多、搜索/筛选、空结果、失败重试；旧请求晚到不得覆盖/追加到新上下文。
