# 全页面与引擎能力边界

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

仅在跨页面、跨引擎能力或发布回归时读取本页；单页改动先读单页功能文档。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[server.go](../../../internal/api/server.go)、[adapters](../../../adapters) |
| 回归 | [console-engine-pages-audit.cjs](../../../tools/console-engine-pages-audit.cjs)、[console-bootstrap-audit.cjs](../../../tools/console-bootstrap-audit.cjs) |

## 需要时再读

- [source-layout-and-testing](../../zh-CN/source-layout-and-testing.md)

## 必须保持与回归

- 全页面检查必须包含总览、拓扑、操作、节点、指标、操作日志、关于、设置，不得只测六页。Oracle/SQL Server 必须核对后端真实能力及指标名称；未实现功能不能因通用 execute 能力可用而亮起。
