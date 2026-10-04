# 设置：运行参数只读视图

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

settings-configuration-panel → loadConfiguration → /api/v1/control-plane/configuration → 值、来源、重启标记。重新读取文件不等于应用配置。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../../internal/api/console.html)、[configuration.go](../../../../internal/api/configuration.go) |
| 回归 | [configuration_test.go](../../../../internal/api/configuration_test.go)、[console-engine-pages-audit.cjs](../../../../tools/console-engine-pages-audit.cjs) |

## 需要时再读

- [configuration](../../backend/settings/configuration.md)
