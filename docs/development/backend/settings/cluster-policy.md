# 设置：引擎级集群策略

[返回设置接口索引](README.md)

## 必须先读

修改策略写入、清除或对应前端交互前，必须先读[高风险操作授权规则](../../rules/operation-safety.md)。本页只记录现有后端门禁，不暗示控制台已经实现额外的风险确认弹框。

## 职责与作用范围

集群策略是自动切换参数的 Raft 复制覆盖层。策略按引擎保存，适用于**所有同引擎集群**，与控制台顶部当前选择的集群无关；它不是单集群设置，也不是节点配置文件编辑器。目前只接受 `mysql` 和 `postgresql`。

## 请求、中间件与真实写入流程

`GET/PUT /api/v1/cluster-policy` 的实际流程是：

1. [server.go](../../../../internal/api/server.go) 先完成平台身份校验。PUT 还必须通过管理员角色与 CSRF，或有效 control bearer，并经过维护门禁及当前 Leader/quorum 校验；需要时由 mutation RPC 转发到 Leader。
2. [cluster_policy.go](../../../../internal/api/cluster_policy.go) 的 `clusterPolicyRoute` 只负责 GET/PUT 分派。GET 从 store 返回 `policy` 和稳定排序的 `summary`；PUT 解码并规范化引擎键，再调用 `PutClusterPolicy`。
3. [store/cluster_policy.go](../../../../internal/store/cluster_policy.go) 校验引擎、自动切换时间边界、备注及审计字段，将策略和审计事件放进同一个 snapshot 后提交 Raft。
4. [runtime](../../../../internal/runtime) 在每轮自动切换判断时重新读取对应引擎策略，无需重启。

管理员、CSRF、维护状态、Leader 和多数派检查属于 `server.go` 的通用变更中间件，不在 `clusterPolicyRoute` 内；修改路由时不得漏掉或绕过这些门禁。

## 默认值、覆盖与清除

- 没有策略时，每个节点继续使用自身启动配置。
- 数值字段为零表示该字段未覆盖，运行时回退到该节点的启动配置；维护抑制为 `true` 时暂停该引擎的自动切换，但继续记录失败证据。
- PUT 是整个策略记录的替换，不是 PATCH。请求中省略原有引擎会移除该引擎覆盖。
- `engines` 为空会清除全部覆盖并恢复节点配置；本来就为空时不产生无意义的复制提交。
- 实际写入或清除产生变更时记录操作者与审计事件，策略与审计必须一起提交或一起失败；已无覆盖的清除是 no-op，不新增复制提交或审计。

## 校验与失败行为

- 空白引擎键、规范化后重复的键、不支持的引擎、越界时间参数或超过 512 字符的备注返回 `400`，不得保存策略或审计成功事件。
- store 不可用或 Raft 持久化失败返回 `503`；失败提交不得把半份策略或半份审计发布到内存。
- GET 和 PUT 都返回 `Cache-Control: no-store`。对外路由只分派这两种方法；其他方法由路由拒绝，不能把 handler 内部的 405 当成所有外部请求的固定响应。

## 修改与验证入口

| 要修改的行为 | 实现入口 | 对应回归 |
| --- | --- | --- |
| HTTP 读写、输入规范化与错误映射 | [api/cluster_policy.go](../../../../internal/api/cluster_policy.go) | [api/cluster_policy_test.go](../../../../internal/api/cluster_policy_test.go) |
| 通用鉴权、维护门禁、Leader/quorum 与转发 | [server.go](../../../../internal/api/server.go) | [server_test.go](../../../../internal/api/server_test.go) |
| 校验、全量替换、清除、审计原子性、持久化与复制 | [store/cluster_policy.go](../../../../internal/store/cluster_policy.go) | [store/cluster_policy_test.go](../../../../internal/store/cluster_policy_test.go) |
| 每轮读取、热覆盖和清除后回退 | [runtime](../../../../internal/runtime) | [runtime/cluster_policy_test.go](../../../../internal/runtime/cluster_policy_test.go) |

只指明与改动对应的验证入口；未执行的浏览器、真实数据库、真实集群或现场验证不得记为通过。前端交互任务再读[设置：集群策略编辑](../../frontend/settings/cluster-policy.md)。
