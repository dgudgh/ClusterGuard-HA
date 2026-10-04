# 后端设置接口

这里汇总设置页及关于页所用接口，职责分散在 API、认证、运行时和升级服务中。先确定是读取状态、读取配置、写策略还是执行升级。

| 职责与粗略路径 | 功能文档 |
| --- | --- |
| 状态 GET → 状态 provider → 当前控制面事实 | [控制面状态](status.md) |
| auth 路由 → 会话、CSRF、账户服务 | [认证与账户](account.md) |
| configuration GET → 当前节点配置投影 | [运行参数](configuration.md) |
| cluster-policy GET/PUT → 引擎级覆盖 → Raft | [全局引擎策略](cluster-policy.md) |
| version GET → buildinfo → 版本投影 | [版本信息](version.md) |
| updates 路由 → 升级服务 → 受限执行器 | [升级与热修](updates.md) |

浏览器语言和刷新频率由[前端显示偏好](../../frontend/settings/preferences.md)说明，没有对应写配置 API。

[返回后端功能入口](../README.md)
