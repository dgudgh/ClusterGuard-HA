# ClusterGuard HA 版本与发版规范

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/version-release-policy.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


## 1. 版本格式

ClusterGuard HA 的安装包使用：

```text
主版本.功能版本-发行序号
```

例如：

```text
2.1-45
2.2-1
2.2-2
```

对应 Git 标签使用点分格式：

```text
v2.1.45
v2.2.1
v2.2.2
```

规则如下：

- 主版本变化表示控制内核、数据模型或兼容边界发生重大变化。
- 功能版本变化表示开始新的产品能力线，例如 PostgreSQL 从 2.2 开始。
- 发行序号在同一功能版本内严格递增。修复、功能、安装器、配置、依赖和交付
  文档发生影响用户的变化，都必须增加发行序号。

## 2. 当前版本线

| 版本线 | 状态 | 说明 |
| --- | --- | --- |
| `2.1-45` | 已封板 | MySQL 稳定线，不再增加功能 |
| `2.2-N` | 开发线 | PostgreSQL 能力及 2.1 之后的新功能 |

`v2.1.45` 永久指向提交
`28425925a321655f38686942c6b081d235b0d288`。后续修复不得移动该标签。

## 3. 不可变发布规则

正式发布完成后，以下对象都不可覆盖：

- Git 标签；
- GitHub Release；
- RPM；
- 完整离线包；
- 签名 `.cgupgrade` 升级包（兼容旧 `.cgpatch`）；
- PostgreSQL 独立依赖包；
- SHA256 文件；
- Release Notes。

同名文件内容发生任何变化都视为新版本。即使只修改安装脚本或包内操作手册，
也必须增加发行序号，因为交付包已经发生变化。

禁止使用以下做法：

- 删除旧标签后重新创建同名标签；
- 用 `--clobber` 替换正式 Release 附件；
- 修改 RPM 内容但保留相同 NEVRA；
- 手工替换离线包中的脚本而不更新摘要；
- 把未提交工作树构建成正式发布并让 Release 指向另一个提交。

### 3.1 发布渠道分层（2026-09-22 起）

交付产物分两层，**上传渠道不同**：

| 产物 | 渠道 |
| --- | --- |
| 完整离线安装介质 `clusterguard-ha-<版本>-offline-linux-x86_64.tar.gz`、ClusterGuard RPM、各自 `.sha256`、`RELEASE-INFO`、`verification.json` | GitHub Release（公开渠道） |
| 签名 `.cgupgrade` 升级包（含旧 `.cgpatch`）及其 `.sha256` | **仅对签约企业客户交付，绝不上传 GitHub 或任何公开渠道** |

公开渠道只有完整安装介质。升级包是企业交付物，**出现在公开渠道即视为越权分发**，
必须立即删除并按发布事故处理。反过来同样成立：**公开渠道上的每个 Release 都必须带完整
离线介质**，只有 RPM 或只有说明的 Release 没有存在意义，必须删除。

- 升级包仍受本节不可覆盖规则约束，只是不出现在公开渠道。
- 构建脚本与本地 `release/` 目录照常保留升级包，**不得**把它们加进 GitHub Release 附件。
- 删除越权附件时**必须同步删除其 `.sha256`**，否则会留下指向不存在文件的摘要。
- 删除整个 Release 时**保留 Git 标签**：标签是源码出处，不是分发产物；且仍被远端分支可达，
  交付记录门禁不受影响。只有确认无人引用该标签时才用 `--cleanup-tag`。
- 检查命令：`node tools/verify-public-release-assets.cjs`。它执行三组规则，命中任一即退出码 1：
  ① 按 `.cgupgrade` / `.cgpatch` 后缀与 `<来源>_to_<目标>` 命名规则检查全部 Release（含草稿）的附件；
  ② 未带完整离线介质的**已发布** Release 视为违规（草稿默认豁免，它是上传中的暂存区；
  加 `--include-drafts` 可一并检查）；
  ③ 介质本身必须可用：有同名 `.sha256` 伴侣、体积不小于 10 MiB、上传状态为 `uploaded`。
  只按文件名匹配挡不住空包、截断上传或缺摘要文件的发布，所以这三项单独成规。
  缺少 `RELEASE-INFO` / `verification.json` 记为 warning（早期版本本就没有），加 `--strict`
  才升级为失败。上传前后各跑一次；要核对字节本身再加 `--verify-download`（会下载介质）。
- 历史先例一：`v2.2.68` 曾发布 `clusterguard-ha-2.2-66_to_2.2-68.x86_64.cgupgrade`
  （36,005,513 字节），已于 2026-09-22 删除；本地副本 `release/2.2-68-user-e2e/`
  摘要 `402256420e44473a3c37206ab2d3b53535cda00fd717c33bc78e1dd024772565` 一致，未丢失。
- 历史先例二：同一 `v2.2.68` Release 只有 `clusterguard-ha-2.2-68.x86_64.rpm` 及其 `.sha256`，
  没有完整离线介质，已于 2026-09-22 **整个删除**（保留标签，指向 `bc0546a`）。删除前已下载核对：
  远端副本与本地 `release/2.2-68-user-e2e/` 摘要同为
  `9c20ca1823706c0bdeccca239c563658645cfefdd748b9e5fb2f27544a5c8a58`，未丢失。
  删除后 GitHub 的 Latest 标记回到 `v2.2.39`（它带完整介质）。

## 4. 分支规则

- `main` 保存已审查、可追溯的正式代码。
- `codex/2.2-postgresql` 是 2.2 PostgreSQL 开发起点，基线为
  `v2.1.45`。
- 功能和修复先形成独立提交，再合入对应版本线。
- 构建正式包前工作树必须干净；`RELEASE-INFO` 中的提交必须与发布标签一致。

不得把 2.2 的 PostgreSQL 改动回写到 `v2.1.45`，也不得用 2.1 文件名交付
2.2 代码。

## 5. PostgreSQL 2.2 交付规则

2.2 的主离线包只包含 ClusterGuard 与数据库运行所需的小型签名依赖。PostgreSQL
官方源码编译依赖采用以下策略：

1. 明确指定 `--engine postgresql` 时，默认从构建节点已配置的软件源联网解析。
2. 编译依赖安装到一次性 DNF installroot，不修改生产宿主机 RPM 数据库。
3. 联网失败时明确停止，提示用户上传独立 PostgreSQL 依赖包。
4. 纯离线现场通过 `--postgresql-dependencies` 指定解压后的独立依赖仓库。
5. 主包与依赖包分别校验 SHA256、RPM 签名、架构、系统主版本和仓库元数据。

典型 2.2 附属包名称：

```text
clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64.tar.gz
```

该附属包不得重新塞回主离线包，以免所有 MySQL 用户承担 PostgreSQL 编译工具链
的体积。

## 6. 正式发版门禁

每个正式版本至少完成：

```bash
go test ./...
go vet ./...
find scripts -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
git diff --check
```

并验证：

1. Git 工作树干净，标签指向当前提交。
2. RPM 名称、版本和架构正确。
3. 离线包内外两层 SHA256 全部通过。
4. RPM 签名、依赖、systemd 单元和配置权限正确。
5. 安装器只读计划与真实安装均在干净三节点环境通过。
6. 对应数据库版本完成切换、故障、旧主恢复、重启、网络分区和并发测试。
7. 安全门禁、Leader 转发、Raft 多数、VIP 唯一性、审计和报告均通过。
8. Release Notes 明确支持范围、已知边界和升级方法。
9. 升级包同时包含目标与回退 RPM，签名、兼容合同、滚动顺序、断点续跑和自动回退测试通过。

任一门禁失败时只能生成内部候选包，不能创建正式标签或 GitHub Release。

`RELEASE-INFO` 中的 `release_channel` 由构建脚本（`scripts/build-clusterguard-offline-kit.sh`）
按**源码树是否干净**自动判定：干净写 `stable`，否则写 `candidate`。它**不代表**本节门禁已通过，
因此 `release_channel=stable` 的介质仍可能只是内部候选包——正式发布资格只以本节门禁是否全部通过为准，
`release_channel` 不能替代现场验收。

## 7. 发布步骤

正式发布按以下顺序执行：

1. 整理并提交本版本全部变更。
2. 执行全量测试和现场验收。
3. 从干净提交构建 RPM、主离线包、签名升级包和必要的独立依赖包（升级包只留本地，见 §3.1）。
4. 验证摘要、签名、包内容和安装流程。
5. 创建注释标签，例如 `v2.2.1`。
6. 推送提交和标签。
7. 创建 GitHub Release 并上传只读附件——**只上传完整安装介质、各自的 `.sha256`、`RELEASE-INFO`
   和 `verification.json`**；签名 `.cgupgrade` 与旧 `.cgpatch` 升级包**不得上传**（见 §3.1）。
   上传前后都跑 `node tools/verify-public-release-assets.cjs`：它既检查越权升级包，也检查是否
   存在**不带完整离线介质**的已发布 Release。缺介质的 Release 不得留在公开渠道。
8. 从 GitHub 重新下载附件，再执行一次摘要和安装冒烟验证。

发布完成后，只允许通过新版本修复问题。

现场升级、断点续跑和回退命令见[版本升级与回退手册](update-and-patch.md)。发布私钥不得进入仓库、RPM、离线包或客户服务器。
