# 认证与账户接口

[后端功能](../README.md) → [设置接口](README.md) → 认证

认证属于平台 auth 域，在设置页和登录页共同使用；会话身份、CSRF 与高风险操作批准有不同职责。

## 接口与流程

| 接口 | 职责 |
| --- | --- |
| POST `/api/v1/auth/login` | 验证凭据，建立会话与 CSRF Cookie |
| GET `/api/v1/auth/me` | 校验当前会话并返回账户信息 |
| POST `/api/v1/auth/logout` | 撤销会话及清除 Cookie |
| POST `/api/v1/auth/password` | 校验当前会话/旧密码，修改密码并撤销相关会话 |

`authRoute` 在通用业务路由前分派这些请求 → `internal/auth.Service` → 用户/会话存储及安全审计。认证服务未配置返回 503；登录失败、限流、会话不存在和 CSRF 拒绝必须分别检查，不能都解释为密码错误。

## 安全边界

`clusterguard_session` 是 HttpOnly Cookie；`clusterguard_csrf` 与变更请求头配对。凭据和令牌不进入日志、文档样例或浏览器持久化。平台角色允许哪些变更由通用请求鉴权再判断，登录成功不表示已获得具体操作批准。

## 定位与验证

- 路由、Cookie、CSRF、错误与审计：[auth.go](../../../../internal/api/auth.go)、[auth_test.go](../../../../internal/api/auth_test.go)。
- 登录、撤销、改密与限流：[认证服务](../../../../internal/auth/service.go)及该包测试。
- 复制持久化：[认证存储](../../../../internal/store/auth.go)，仅在账户/会话记录读写问题时展开。

退出按钮时机、旧请求晚到或新登录混缓存问题读[前端账户与会话](../../frontend/settings/account.md)；变更执行授权问题必须读[操作授权规则](../../rules/operation-safety.md)。
