# 关于与版本展示

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

平台版本 API → 关于页展示；区分安装 RPM、运行二进制、源码提交和热修身份。关于页不能代替验包或生产版本核实。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [console.html](../../../internal/api/console.html)、[control_plane.go](../../../internal/api/control_plane.go)、[buildinfo](../../../internal/buildinfo) |
| 回归 | [console-engine-pages-audit.cjs](../../../tools/console-engine-pages-audit.cjs) |

## 需要时再读

- [version](../backend/settings/version.md)
