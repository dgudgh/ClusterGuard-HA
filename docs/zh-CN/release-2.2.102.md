# ClusterGuard HA 2.2-102 完整安装介质

本版是**新装用全量离线安装介质**，不是控制台可上传的 `.cgupgrade` 升级补丁。
它沿用 2.2-101 的界面、安装器、MySQL/PostgreSQL 恢复与升级保护逻辑，本轮不新增
数据库引擎能力，只纳入 101 之后已提交的验证证据强化、域模型修正、死代码清理和
回归测试归并。

## 相对 2.2-101 的变更

| 提交 | 类型 | 内容 |
| --- | --- | --- |
| `7b36461` | 修复 | 强化验证证据：新增 `model.Verification.Successful()`，成功必须由非空且全部为 pass/warn 的 checks 支撑，缺证据不能判定成功；成功操作要求 verification 的 `operation_id` 与操作一致；持久化证据不完整或不一致时降级为 `indeterminate` 并提示需复核。 |
| `7b36461` | 修复 | `EndpointAddress` 改用 `netip` + `net.JoinHostPort`，正确规范化 IPv6 字面量（`[::1]:3306`），并统一端口与空主机处理；此前裸拼 `host:port` 对 IPv6 会生成非法地址。 |
| `7b36461` | 修复 | 新增 HTTP 边界投影 `presentOperationResponse`：仅在 HTTP 响应中省略零值 `started_at`/`finished_at`，模型层 JSON 编码保持不变，避免改变快照摘要的字节兼容性。 |
| `33033fa` | 重构 | 归并重复的终止状态判定（`terminalReportStatus`/`terminalProgressStatus` → 共用 `terminalOperationStatus`/`durableTerminalStatus`）；合并碎片化回归测试文件，测试语义保留。 |
| `7e9717d` | 清理 | 删除 Oracle、SQL Server、Raft、endpoint、store 中未被引用的内部 helper；见 [死代码清理记录](dead-code-cleanup-2026-09-11.md)。 |
| `0a04ee4` | 文档 | 校正 2.2-101 发布记录与升级器执行区证据的交叉引用，不改变已发布的 101 产物。 |

本次域模型修正的来源与逐项结论见 [域模型评估记录](domain-model-review-2026-09-11.md)。
该记录同时列出**尚未处理**的模型缺陷，本版不宣称域模型已全部收敛。

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制（minimal 不含调试
  符号，保留数据库运行程序）。
- PostgreSQL：16.4 官方源码，并附 Rocky Linux 8.10 x86_64 编译依赖 RPM 和仓库
  元数据；安装器支持在首个数据节点编译一次并分发，不是 PostgreSQL 预编译二进制包。
- ClusterGuard Linux x86_64 RPM、tar 运行程序、多节点 `install_clusterguard.sh`、
  MySQL/PostgreSQL 安装接入脚本、示例配置和中文手册。
- 静态 Linux `jq` 与内外两层 SHA256 校验清单。**不包含签名私钥、现场凭据或生产
  补丁签名公钥。**

新装数据库也可通过 `-r` 指定已批准的 MySQL 二进制包或 PostgreSQL 二进制/官方
源码包；PostgreSQL 另需 `--database-version`。安装器默认只生成计划，显式
`--execute` 才修改主机。完整参数见[离线安装与部署手册](offline-rpm-install.md)。

## 使用入口

```bash
sha256sum -c clusterguard-ha-2.2-102-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.2-102-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-102-offline-linux-x86_64
sha256sum -c SHA256SUMS
bash install_clusterguard.sh --help
```

已有集群不要用新装命令覆盖部署状态、Raft 数据、数据库目录或身份文件。本压缩包
不能上传到升级弹窗；需要现场升级请使用对应来源版本的签名 `.cgupgrade` 补丁。

## 与 2.2-101 的兼容性

- 2.2-102 不是二进制差分，也不替代 101 的升级补丁。101 → 102 没有发布签名
  `.cgupgrade`，本介质只用于新装。
- 模型层 JSON 编码未改变（时间戳投影只发生在 HTTP 边界），101 写入的元数据快照
  与操作记录可继续被 102 读取，不需要迁移。
- 端口、目录、systemd 单元、控制台默认端口和安装参数与 101 一致。

## 验证边界

包内 `RELEASE-INFO` 记录版本、构建提交、干净工作树状态和数据库包数量；RPM 内
`BUILD-INFO` 记录同一版本与提交。构建前执行 `go test ./...`、`go vet ./...`、
脚本语法检查和差异检查；包内两个控制程序必须嵌入与测试一致的 HTML。

以下事项**在本版中未完成，不因本包生成而视为已通过**：

1. 未在目标 Linux 主机执行 102 的安装、PG 源码编译或滚动升级。
2. 未执行真实 MySQL/PostgreSQL 灾难恢复、VIP 接管、Raft Recovery Commit 或旧主
   回挂的现场验收。
3. 未确认与现场信任公钥匹配的生产补丁签名私钥，故未生成生产 `.cgupgrade`。
4. 目标操作系统上的依赖 RPM 签名信任仍需在目标机验证。
5. 域模型评估记录中的其余缺陷（例如 `ResourceID` 生成失败直接 panic、
   `EngineIdentity` 未统一归一化、endpoint 字段双写、`ReplicaLink` 延迟语义、
   labels/所有权维度缺失）未在本版修复。
6. PXC 专用恢复、Oracle/SQL Server 原生灾难恢复和历史泄露凭据轮换仍不在已完成
   范围内。

真实构建回归、摘要、包内容与安装器 `--help` 检查结果记录在同目录构建报告和交付
目录的 `verification.json`；这些检查不等同现场验收。
