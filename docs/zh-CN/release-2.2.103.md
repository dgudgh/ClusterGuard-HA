# ClusterGuard HA 2.2-103 完整安装介质

本版是**新装用全量离线安装介质**，不是控制台可上传的 `.cgupgrade` 升级补丁。

2.2-103 取代 2.2-102 候选包。取代的原因不是新增功能，而是**2.2-102 的包内文档快照
取早于本轮全部修正**：那份介质里的《离线安装与部署手册》《版本升级与回退手册》
《运维操作手册》和《Docker-Swarm-MySQL》仍带着后经查实的错误。已修好的源码文档与
已发出去的包内文档不能是两套，所以带修正重新出包。

本版**含一处新装行为变更**（首次管理员口令），不只是文档更新。装维流程需要相应调整，
详见下文「本版的安全行为变更」。

## 相对 2.2-102 的变更

2.2-102 的代码基线是 `7b36461`（包内 `RELEASE-INFO` 记录打包时 HEAD `2b9a449`，该提交
只新增发布说明与索引、不含代码改动，两者代码等价）。本版基线为 `eb741ee`。

| 提交 | 类型 | 内容 |
| --- | --- | --- |
| `eb741ee` | 测试 | 文档契约测试 `TestAuthenticationDeliveryDocumentationCoversBootstrapAndRecovery` 的断言，由字面量 `admin123` 改为引导机制 `bootstrap_admin_password_env` / `bootstrap-admin-password`。该测试原先钉住的正是本版改掉的旧行为；测试意图（装维仅凭随包文档即可引导并恢复管理员账号）不变，且仍是真实内容检查——四个文件任一丢掉机制说明即失败。 |
| `772fbfb` | **安全修复** | 新装首次管理员口令不再回退到公开字面量 `admin123`。未设置 `bootstrap_admin_password_env` 的部署，改由控制面自身的生成器生成随机口令，写入 `metadata.json` 同目录的 root-only 0600 文件 `bootstrap-admin-password`。 |
| `772fbfb` | 修正 | 派生路径与清理路径统一由 `metadata_path` 推出，创建与清理由此不可能再指向不同文件；此前清理的是一个运行时从未创建的固定路径。 |
| `772fbfb` | 修正 | MySQL 适配器 `CapabilityNodeSync` 的 `reason` 由「node synchronization is not implemented」改为说明「适配器 node-sync 接口未实现，MySQL 节点生命周期由平台任务引擎执行」。控制台把该映射当作能力清单渲染，旧措辞会被读成「MySQL 根本不能重建节点」。能力开关本身正确，未改动。 |
| `772fbfb` | 工具 | 新增 `tools/verify-release-records.cjs`：逐条校验 `release/*/RELEASE-INFO` 的提交是否在本仓库、是否被标签或远端引用到达、该提交内是否含本版发布说明。 |
| `772fbfb` | 流程 | `AGENTS.md` 打包前阻断清单新增「交付记录可追溯」一项。 |
| `772fbfb` | 文档 | 证据窗口「三次观测」的**第二轮**遗漏点：两个根 README 与英文迁移手册仍有旧值，本版修正。 |
| `ef4d72b` | 文档 | 第三轮（72 文件）：按各文档自身证据表改正数值类缺陷；补齐英文侧对齐（`kubernetes-mysql` 89→157 行、`docker-swarm-mysql` 67→116 行）。 |
| `cb10460` | 文档 | 第二轮（87 文件）：改正安全证据窗口参数；文档中心改为从文件系统发现发布说明，65→86 页；非策展文档链接解析为真实源文件，不再产出悬空 `.md`。 |
| `9c314ca` | 文档 | 残留的英文 `three observations`。 |
| `3a39c4d` | 文档 | 第一轮（51 文件）：2.2 生产文档集与实现对齐，含多处「照做会失败」的命令、路径与参数错误。 |
| `562c537` | 记录 | 把 2.2-102 的交付记录并回文档分支（该提交此前只活在一次性的构建克隆里）。 |

另恢复 `docs/assets/wechat-covers/` 下 11 张封面图 —— 它们在本轮工作之前就已从工作区
缺失，一直是 Markdown 链接门禁里那 11 个断链的来源。

### 本版修正的、且存在于 2.2-102 包内文档中的错误

| 项 | 2.2-102 包内文档（错） | 2.2-103（正） | 依据 |
| --- | --- | --- | --- |
| 自动故障切换证据窗口 | 《离线安装与部署手册》「连续 **3** 次得到当前主库失败观测」 | 「连续 **4** 次……时间跨度不少于 3 秒」 | `internal/runtime/runtime.go` 的四次观测注释、`runtime_test.go` 的「少于四次必须拒绝」用例、`coordination/failure_window.go` 首个观测只作种子不计入 checks |
| 版本查询命令 | 《版本升级与回退手册》`cgctl version --json` | `cgctl --json version` | `cgctl` 的全局标志必须在子命令之前 |
| VIP 迁移计时 | 《Docker-Swarm-MySQL》「VIP 在 30 秒内迁移」 | 「实验室实测端到端约 21 秒、其中工作流执行及验证约 12.2 秒；30 秒是目标值，非产品承诺时限」 | 该文档自身引用的 2026-09-11 实验室记录 |
| 首次管理员口令 | 《运维操作手册》「首次密码：admin123」；《离线安装与部署手册》两处 | 部署设置 `bootstrap_admin_password_env` 时取该变量，否则读取控制节点上 root-only 0600 的 `bootstrap-admin-password` | `internal/auth/recovery.go`、`internal/runtime/runtime.go` |

《Docker-Swarm-MySQL》与两处《生产验收报告》里其余的「30 秒」是被阻断操作的重试退避
语义，经核对正确，未改动。

## 本版的安全行为变更（装维需注意）

未设置 `bootstrap_admin_password_env` 的**全新安装**，首次管理员口令来源变了：

- 2.2-102 及以前：固定为文档中的 `admin123`，首次登录后强制改密。
- 2.2-103 起：控制面生成随机口令，写入 `metadata.json` 同目录的 `bootstrap-admin-password`
  （权限 0600，仅 root 可读）。首次登录后仍强制改密。

不变量：

- **显式配置优先。** 部署设置了 `bootstrap_admin_password_env` 时仍使用该值
  （例如 `admin123`），不生成任何文件，已有站点不会被锁在外面。
- 口令从不以明文落库；元数据快照里是 Argon2id 哈希。
- 改密前控制台与 API 只允许认证与改密，不能查看或操作集群数据。
- 首个完成改密的控制器会清除该 root-only 文件。

已在真实控制器上验证（非仅单测）：全新单机实例生成 `bootstrap-admin-password`（0600）、
日志中无口令；用生成口令登录返回 200 且 `must_change_password=true`，用 `admin123`
登录返回 401；元数据含 Argon2id 哈希且不含两个口令的任何明文。另起一个经
`bootstrap_admin_password_env` 配置的实例，可用自己的口令登录且**不创建**任何口令文件。

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制（minimal 不含调试符号，
  保留数据库运行程序）。
- PostgreSQL：16.4 官方源码，并附 Rocky Linux 8.10 x86_64 编译依赖 RPM 和仓库元数据；
  安装器支持在首个数据节点编译一次并分发，不是 PostgreSQL 预编译二进制包。
- ClusterGuard Linux x86_64 RPM、tar 运行程序、多节点 `install_clusterguard.sh`、
  MySQL/PostgreSQL 安装接入脚本、示例配置和中文手册。
- 静态 Linux `jq` 与内外两层 SHA256 校验清单。**不包含签名私钥、现场凭据或生产补丁
  签名公钥。**

新装数据库也可通过 `-r` 指定已批准的 MySQL 二进制包或 PostgreSQL 二进制/官方源码包；
PostgreSQL 另需 `--database-version`。安装器默认只生成计划，显式 `--execute` 才修改主机。
完整参数见[离线安装与部署手册](offline-rpm-install.md)。

## 使用入口

```bash
sha256sum -c clusterguard-ha-2.2-103-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.2-103-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-103-offline-linux-x86_64
sha256sum -c SHA256SUMS
bash install_clusterguard.sh --help
```

首次登录前先取得初始口令：设置了 `bootstrap_admin_password_env` 就用该值；未设置则到
控制节点读取 `/var/lib/clusterguard/bootstrap-admin-password`（0600，需 root）。

已有集群不要用新装命令覆盖部署状态、Raft 数据、数据库目录或身份文件。本压缩包不能
上传到升级弹窗；需要现场升级请使用对应来源版本的签名 `.cgupgrade` 补丁。

## 与 2.2-102 的兼容性

- 2.2-102 不是二进制差分，也不替代升级补丁。102 → 103 没有发布签名 `.cgupgrade`，
  本介质只用于新装。
- 元数据快照、操作记录与 HTTP 响应结构与 102 一致，102 写入的数据可继续被 103 读取，
  不需要迁移。
- 端口、目录、systemd 单元、控制台默认端口和安装参数与 102 一致。
- 唯一影响装维流程的差异是上面那节首次口令来源。**已在跑的集群升级到 103 后不会
  被改口令**，只有全新安装受影响。

## 验证边界

包内 `RELEASE-INFO` 记录版本、构建提交、干净工作树状态和数据库包数量；RPM 内
`BUILD-INFO` 记录同一版本与提交。构建前执行 `go test ./...`、`go vet ./...`、脚本语法
检查和差异检查；包内两个控制程序必须嵌入与测试一致的 HTML。

以下事项**在本版中未完成，不因本包生成而视为已通过**：

1. 未在目标 Linux 主机执行 103 的安装、PG 源码编译或滚动升级。
2. 未执行真实 MySQL/PostgreSQL 灾难恢复、VIP 接管、Raft Recovery Commit 或旧主回挂
   的现场验收。
3. **新引导口令流程的多控制器现场验收未做。** 新装现场操作手册已从「用 `admin123`
   登录」改为「读取控制节点上的 root-only 文件」，装维流程必须重跑一遍才能算通过。
4. 未确认与现场信任公钥匹配的生产补丁签名私钥，故未生成生产 `.cgupgrade`。
5. 目标操作系统上的依赖 RPM 签名信任仍需在目标机验证。
6. 域模型评估记录中的其余缺陷（例如 `ResourceID` 生成失败直接 panic、
   `EngineIdentity` 未统一归一化、endpoint 字段双写、`ReplicaLink` 延迟语义、
   labels/所有权维度缺失）未在本版修复。
7. PXC 专用恢复、Oracle/SQL Server 原生灾难恢复和历史泄露凭据轮换仍不在已完成范围内。
8. 本版未上传 GitHub。

真实构建回归、摘要、包内容与安装器 `--help` 检查结果记录在同目录构建报告和交付目录的
`verification.json`；这些检查不等同现场验收。
