# 设置：控制面状态展示

[前端页面](../README.md) → [设置功能](README.md) → 状态

状态设置面板使用 `state.controlPlane`，由 `renderControlPlaneStatus()` 展示 Leader、Raft 多数派、元数据版本、运行时长、活动/待复核操作和节点任务。

## 加载与作用范围

登录后辅助读取以及集群观测刷新会请求 `GET /api/v1/control-plane/status`。升级轮询也更新控制面维护事实。此数据描述控制面整体，不是顶部单个数据库集群的复制健康。

- mode/role/leader_known/quorum_confirmed/readiness_reason 一起解释，不能只看 Leader 地址。
- 活动操作与待复核操作分别展示；历史成功、失败与待复核不是同一计数。
- 部分读取失败路径会保留已有状态；排查迟到或旧状态时核对请求/会话版本，不能凭旧 UI 数值认定当前多数派已确认。
- 状态辅助接口失败不应冒充登录失效；会话变化的旧响应仍必须丢弃。

## 定位与验证

展示实现看[console.html](../../../../internal/api/console.html)的 `renderControlPlaneStatus`、登录后辅助读取和 `loadSelectedCluster`；响应来源问题才读[后端状态接口](../../backend/settings/status.md)。

[启动辅助接口检查](../../../../tools/console-bootstrap-audit.cjs)覆盖部分失败隔离；[引擎页面检查](../../../../tools/console-engine-pages-audit.cjs)覆盖设置可见与布局。这些入口不等于状态刷新、Leader 变化和现场 quorum 语义全部已验收。
