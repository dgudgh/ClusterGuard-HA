# ClusterGuard HA 控制平面可操作性计划

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../../plans/2026-07-20-control-plane-operability.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

1. 为存活性、故障关闭准备就绪性、详细状态和请求关联性添加失败的 API 测试。
2. 为稳定的本地身份、角色、Leader、多数派仲裁、任期和索引报告添加失败的 Raft 测试。
3. 实现 Raft 状态快照和运行时状态提供程序。
4. 添加认证状态路由、公共探测、请求 ID 和操作计数器。
5. 添加控制平面 Prometheus 指标和 `cgctl status` 人类/JSON 输出。
6. 添加紧凑的控制台诊断面板和 systemd 预飞行配置验证。
7. 每次更改后运行聚焦测试，然后运行完整测试、构建、格式化、差异检查和范围扫描。
