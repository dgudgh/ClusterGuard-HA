# 设置：认证与账户接口

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

/api/v1/auth/login、me、logout、password → authRoute → 会话/CSRF → auth Service → 持久化与审计；凭据必须脱敏，密码变更不等于前端授权可以继续复用。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [auth.go](../../../../internal/api/auth.go)、[auth](../../../../internal/auth)、[auth.go](../../../../internal/store/auth.go) |
| 回归 | [auth_test.go](../../../../internal/api/auth_test.go)、[auth](../../../../internal/auth) |

## 需要时再读

- [account](../../frontend/settings/account.md)
