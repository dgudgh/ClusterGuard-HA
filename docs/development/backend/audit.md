# 后端日志与审计查询

[返回后端功能](README.md)

日志查询的业务路径为 `API 分页参数 → OperationLogPage → 页边界/游标 → 列表投影`。这页处理查询和展示；Raft、CAS 等提交问题另进[持久化基础设施](persistence.md)。

## 定位

- [operation_page.go](../../../internal/api/operation_page.go)：`operationLogPage`、查询参数与游标的输入/输出。
- [operation_page.go](../../../internal/store/operation_page.go)：`OperationLogPage`、排序位置与过滤范围。
- [operations.go](../../../internal/api/operations.go)：公开列表/结果投影；原始操作记录和展示信息有不同职责。

## 请求与查询边界

分页请求使用 `GET /api/v1/operations?view=page`；按需附 `cluster_id`、`limit`、`kind`、`status`、`q` 与 `cursor`。遗漏 `view=page` 会进入其他查询投影，不能把旧全量路径当成分页实现。

- 集群、kind、status、q 与游标属于同一查询上下文；改条件时旧游标返回 400。limit 接受 1..100；返回 items、total、record_count、remaining 与 next_cursor。游标不能代替授权或查询范围。
- 保留已有日志及原始证据，旧运行中/待复核记录仍参与安全判断。
- 保留策略作用于材料或存储类别，不能把最近三个安装版本误当成三条日志。

## 验证与跨层排查

分页/排序/过滤先看[分页测试](../../../internal/store/operation_page_test.go)。仅当 API 正确而页面仍混行、计数或筛选不对时，读[前端操作日志](../frontend/logs.md)。仅当提交、复制、CAS 或存储保留出错时，读[持久化基础设施](persistence.md)。
