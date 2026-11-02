# 节点同步、生命周期与受限执行

[返回模块](README.md)


修改操作入口、预检、授权或执行链前，必须读[高风险操作授权规则](../rules/operation-safety.md)。

## 调用链与边界

节点目标 → planner → gates → executor/Agent → lifecycle 终态；关机/启动/修复与普通切换是不同流程，执行成功和后续读取失败分开记录。

## 定位实现与回归

| 职责 | 入口 |
| --- | --- |
| 实现 | [nodes.go](../../../internal/api/nodes.go)、[node_sync.go](../../../internal/api/node_sync.go)、[power.go](../../../internal/api/power.go)、[lifecycle](../../../internal/lifecycle)、[agent](../../../internal/agent)、[endpoint](../../../internal/endpoint) |
| 回归 | [nodes_test.go](../../../internal/api/nodes_test.go)、[node_sync_test.go](../../../internal/api/node_sync_test.go)、[lifecycle](../../../internal/lifecycle) |

## MySQL Agent 的原厂 RPM 权限兼容

SSH 中的只读角色检查通过，不代表实际 systemd 协调计时器具备相同能力。云副本首次提升后出现 `inspect MySQL restart defaults`、`EPERM` 和反复自隔离时，按[MySQL Agent systemd 单功能指南](../../zh-CN/mysql-agent-systemd.md)核对厂商文件能力与服务边界；只对已确认需要的节点追加能力，不扩大所有节点权限。

指南保留现有实现不会自动探测该 MySQL 差异的限制，并要求真实计时器、持续固定入口 SQL 与复制验证；不以任务 succeeded 代替持续 writer 验收。

## PostgreSQL Agent身份与文件系统边界

PG使用专属策略、默认setpriv身份切换、受保护passfile，以及由实际PGDATA生成的20-postgresql-recovery.conf。按[PG Agent单功能指南](../../zh-CN/postgresql-agent-systemd.md)核对真实协调单元；不能用SSH完整root结果替代，也不能套用MySQL文件能力修复。本轮源码新增Linux PG无本机VIP的gateway策略和原生可写检查，须部署匹配HA/Agent；源码、制品和真实往返分别验收，见[部署记录](../postgresql-hybrid-field-2026-11-02.md)。

## 需要时再读

- [节点与生命周期弹窗](../frontend/nodes.md)
