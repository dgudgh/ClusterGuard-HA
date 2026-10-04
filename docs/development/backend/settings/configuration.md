# 设置：运行参数只读接口

[返回设置接口索引](README.md)

## 职责与作用范围

本接口回答“当前控制器进程实际使用什么参数、值来自哪里”。它展示单个控制器节点的启动配置，不修改配置文件，也不把文件变化应用到运行进程。节点之间的配置文件可以不同；集群策略则作为单独的 Raft 复制来源显示。

## 请求与实现流程

`GET /api/v1/control-plane/configuration` 的实际流程是：

1. [server.go](../../../../internal/api/server.go) 对已启用认证的 `/api/v1/` 请求先做身份校验，再把 GET 请求分派给 `configurationRoute`。
2. [configuration.go](../../../../internal/api/configuration.go) 设置 `Cache-Control: no-store`，给 provider 三秒超时并调用 `ConfigurationProvider.Configuration`。
3. [configuration_view.go](../../../../internal/runtime/configuration_view.go) 以进程启动时 `config.Load` 得到的内存配置构造有效值，同时读取当前配置文件的键存在性、修改时间和可读状态。
4. provider 读取当前集群策略，将其作为 `policy` 来源加入投影；它不会把节点文件误写成已经热加载。

## 值来源与重启边界

| `source` | 含义 |
| --- | --- |
| `file` | 该键在节点配置文件中出现；即使值等于平台默认值，也仍标记为文件来源 |
| `default` | 文件没有该键，进程启动时由平台补入默认值 |
| `policy` | 来自 Raft 复制的引擎级集群策略，不属于节点文件或平台默认值 |

- `reload_supported` 固定为 `false`。文件参数变更需要滚动重启控制面，刷新此接口不能替代重启。
- 文件当前不可读时仍返回进程已经生效的内存值，并在 `warnings` 说明文件状态；不得据此编造新的有效值。
- 禁用的引擎没有实际发现超时，投影不得为它虚构秒数。
- 凭据只显示环境变量名并标记 `credential_ref`；环境变量内容和内存中的明文不得进入响应。
- 集群策略可在运行中生效，但它的写入、清除和作用范围由[引擎级集群策略](cluster-policy.md)定义。

## 失败行为

- provider 未配置或返回错误时响应 `503`，不回退到猜测值；三秒是传入 provider 的 context 截止时间，provider 也必须遵守取消语义。
- 对外错误只说明视图不可用，不泄露 provider 的内部错误文本。
- 路由只接受 GET；此接口没有保存、重载或重启动作。

## 修改与验证入口

| 要修改的行为 | 实现入口 | 对应回归 |
| --- | --- | --- |
| HTTP 状态、超时、缓存与错误脱敏 | [configuration.go](../../../../internal/api/configuration.go) | [configuration_test.go](../../../../internal/api/configuration_test.go) |
| 启动配置、文件键来源、缺文件警告与凭据脱敏 | [configuration_view.go](../../../../internal/runtime/configuration_view.go) | [configuration_view_test.go](../../../../internal/runtime/configuration_view_test.go) |
| 配置默认值及合法范围 | [config](../../../../internal/config) | [config_test.go](../../../../internal/config/config_test.go) |

只有修改对应行为时才运行相应回归；这些入口不代表已经完成浏览器、真实集群或现场验证。前端展示任务再读[设置：运行参数只读视图](../../frontend/settings/configuration.md)。
