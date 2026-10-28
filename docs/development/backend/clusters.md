# 集群注册、发现与拓扑

[返回模块](README.md)

## 调用链与边界

集群登记 → 引擎适配与原生身份 → discovery → 保存观测 → API 投影；区分配置、实际观测和用户工作上下文。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [clusters.go](../../../internal/api/clusters.go)、[discovery](../../../internal/discovery)、[runtime](../../../internal/runtime)、[adapters](../../../adapters) |
| 回归 | [clusters_test.go](../../../internal/api/clusters_test.go)、[discovery](../../../internal/discovery) |

## 需要时再读

- [集群上下文与导航](../frontend/context.md)
- [ClusterGuard HA 数据库接入手册](../../zh-CN/database-preparation.md)

## 接入已有数据库实例

`POST /api/v1/clusters/{id}/database-endpoints` 接收 `{"endpoints":[{"hostname":"cloud-db","ip_address":"192.0.2.40","port":3306}]}`，每批1至32项。仅管理员会话（含CSRF）或已授权服务端凭据可修改，沿用Leader/多数派与维护门禁。

登记只追加当前集群的发现清单，批次冲突全部拒绝；原地址原字段的重复请求复用端点身份，不重复审计。地址重叠但坐标不同需走元数据对账，不能隐式覆盖。新登记使旧观测失效；再调用该集群discover，成功后才产生实例/拓扑。发现失败仍保留登记，允许再次发现。

不安装数据库或Agent，不重建/播种，不修改Raft成员、VIP或writer lease。MySQL复制正常不代表已经登记。地址须从所有控制节点可达；不要把单机loopback隧道地址当共享云入口。回归见 database_endpoints_test.go 与 console-existing-instance-acceptance.cjs，真实数据库/现场部署独立验收。
