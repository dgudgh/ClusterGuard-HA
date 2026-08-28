# ClusterGuard HA 2.2.39 发布说明

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/release-2.2.39.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

发布日期：2026-08-28

`v2.2.39` 是 ClusterGuard HA 2.2 系列的首个正式版本。在保留 2.1 MySQL 高可用能力的基础上，本版本加入 PostgreSQL 16.4 原生高可用、Docker Swarm 数据库接管、Kubernetes 写入口提供器、图形化版本更新和节点运行时身份管理。

## 正式交付物

- `clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz`：新装、重装和离线部署介质；
- `clusterguard-ha-2.2-39.x86_64.rpm`：ClusterGuard 控制器、Agent、升级 Helper 和配套脚本；
- `clusterguard-ha-2.2-38_to_2.2-39.x86_64.cgupgrade`：仅供正式 `2.2-38` 集群滚动升级的签名升级包；
- 每个制品对应的 `.sha256` 摘要和本发布说明。

`.cgupgrade` 是完整的签名滚动升级包，不是二进制差分小补丁。它包含目标 RPM、回退 RPM、兼容合同、SHA-256 和发布签名。完整离线包用于新装或重装，不能直接上传到版本更新页面。

## 主要能力

- MySQL 5.7、8.0、8.4 和已审批兼容发行版的发现、计划切换、自动故障切换、VIP 唯一所有权、旧主恢复和节点生命周期；
- PostgreSQL 16.4 的原生身份、流复制、WAL/时间线判断、计划切换、故障切换、`pg_rewind`/`pg_basebackup` 恢复和旧主回挂；
- 三节点 Raft 控制面、双向 TLS、Leader 多数派校验、持久操作状态、审批、审计和 fail-closed Safety Guard；
- Docker Swarm MySQL 与 PostgreSQL 的固定服务槽、宿主机 Agent、宿主机 VIP 和容器运行时操作；
- Kubernetes selectorless Service/EndpointSlice 写入口、RBAC、运行时绑定和启动隔离守卫；
- 图形化签名升级、只读计划、Follower 到 Leader 的滚动顺序、断点续跑、自动回退和历史记录；
- 中文控制台、首次登录强制改密、角色权限、CSRF、操作复核和中英文离线文档。

## 验收边界

- 原生 MySQL 和 PostgreSQL 16.4 的三节点切换、故障、旧主恢复、网络分区、失去多数派和 VIP 唯一性已有实验室报告；
- Docker Swarm 模式已实现并完成三节点实验室验证，客户现场仍需按实际镜像、存储卷、端口映射和网络重新验收；
- Kubernetes 模式已有代码级自动化测试，尚无真实 Kubernetes 生产验收报告，不得直接作为生产认证结论；
- Oracle Data Guard Broker 和 SQL Server Always On 仍是独立后续产品线，不属于本版本正式支持范围。

## 安装与升级

新装使用完整离线介质：

```bash
tar -xzf clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-39-offline-linux-x86_64
./install_clusterguard.sh --help
```

正式 `2.2-38` 集群可在 **设置 → 版本更新** 上传同一 Release 中的 `.cgupgrade`，先生成计划，再执行滚动升级。

> **重要：系统升级期间无法进行自动切换，请注意关注。**

现场运行的 `2.2-38.field3` 是实验室热修二进制，RPM 数据库仍记录为 `2.2-38`，不满足正式升级包的精确来源版本合同。该环境必须先按桥接说明恢复到可追溯 RPM，或直接使用 `2.2.39` 重新部署；不得绕过版本检查强行升级。

完整步骤见[离线安装与部署手册](offline-rpm-install.md)和[版本升级与回退手册](update-and-patch.md)。

## 发布校验

正式附件必须从 GitHub Release 下载，并使用同目录 `.sha256` 校验。发布标签、附件和摘要不可覆盖；任何代码、脚本或文档变化都必须增加新版本。
