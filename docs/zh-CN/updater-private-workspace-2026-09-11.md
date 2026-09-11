# 升级器私有执行区安全修复

## 当前状态

截至 2026-09-11，代码修复已纳入 `f76b2f4`；包含该修复的 2.2-101 安装包从干净提交 `78dbdbffc9645dd22c9867888cd11b4f2dc2bd89` 构建并发布至 GitHub。下载、摘要和验证边界统一见 [2.2-101 发布说明](release-2.2.101.md)。代码与安装包校验已完成；生产签名升级包及最终版现场升级验收仍未完成。

## 修改前证据

- 实际工作树：`.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD `19d02bcf076b6e294af0e1f30e2c34f0727e6ae4`。保留已有未提交修改，不提交无关图片删除。
- 本轮用户明确要求先修复已知升级器安全项，暂停安装包与 GitHub 发布，不以候选包绕过阻断项。
- 已读取 AGENTS.md、发布验收清单及 Helper 边界报告。对照 `git show bc0546a:scripts/clusterguard-update-job.sh`、`git diff bc0546a c8795a3 -- scripts/clusterguard-update-job.sh scripts/clusterguard-upgrade.sh`。
- 旧版 2.2-68 已使用 0770 公共目录运行 root 脚本及固定 status.json.tmp；2.2-99 增加了维护失败状态保留、执行所有权及恢复判断，但没有隔离执行目录。当前 shell 与该版本同源，此风险不是 Go 描述符修复已经覆盖的部分。
- 已沿 Helper -> CommandLauncher -> job runner -> 签名 bootstrap -> 本地 journal -> SSH 分发/远端状态 -> 维护接管读取检查。Go 打开的描述符不能保护子脚本重新打开的字符串路径。
- 具体入口：job runner 的 chmod/chown、固定状态临时文件；升级器在 PWD 写 journal/status，root SCP 到公共目录；维护接管读取公共 status/events；旧 remote_stage 位于服务可写父目录之下。

## 保持的不变量

1. 公共上传区仍供服务账号上传与读取结果，不用收紧 0770 来破坏正常功能。
2. 特权输入必须复制到 root 私有目录，再验签、核对请求 ID、校验 RPM 与 bootstrap 摘要。公共元数据不成为授权依据。
3. 私有目录的全部祖先必须可信，不接受服务账号持有的父目录、链接或 group/world-writable 目录。不能用单次叶子 Lstat 冒充抗竞态。
4. root 不在公共目录按字符串写入、chmod、chown 或发布临时文件。结果发布必须降低权限或通过固定目录描述符。
5. 维护接管只读取私有权威记录；旧公共失败记录不能自动获得接管权，缺少可信记录必须保留门禁并明确阻断。
6. 保留四种模式、自动回退、节点合同/多数派、维护门禁、失败闭锁、服务可读进度与历史；不改变数据库。

## 执行与验收

- [完成] 修改前对照与威胁边界记录。
- [完成] 私有工作区、输入快照、状态发布与远端可信恢复记录。
- [完成] root 入口校验：升级包必须是 root 拥有的普通文件、不可由组/其他用户写入、不可硬链接；输入目录及运行目录的所有祖先必须是 root 拥有且不可写；引导阶段只接受 root 创建的 0700 `/tmp/clusterguard-upgrade.*` 快照目录。
- [完成] 正常/攻击路径与关联回归：`go test ./...`、`go test -race ./internal/platformupdate ./cmd/clusterguard-update-helper`、`go vet ./...`、shell 语法检查、`git diff --check` 均通过；脚本重点集成回归通过。
- [完成] 152–154 Linux 原生 workspace 权限检查：可信 root 目录和文件检查、快照均通过；组/其他可写目录、目录符号链接、文件符号链接均被拒绝。测试未修改数据库服务或数据目录。
- [未采集] 最终版安装后的 Helper Unix socket root/服务 UID 隔离测试。9 月 11 日检查记录称账号/unit/目标路径不可用，与 [9 月 10 日测试记录](helper-linux-field-test-2026-09-10.md) 的已安装状态不一致；本次文档更新未重查主机，现场状态需重新确认，不能将历史 socket 通过或本次 workspace 通过写成最终版安装验收。
- [完成] 构建并校验 2.2-101：一份同时包含 MySQL 8.0.44 与 PostgreSQL 16.4 的安装介质及单独 RPM，`verification.json` 为 `passed`；最终源码分支和 GitHub 预发布已上传，六个附件齐全。
- [未完成] 生产签名 `.cgupgrade`：尚未确认匹配现场信任公钥的生产签名私钥，没有发布该产物。
- [未采集] 最终版真实安装、上传验签、滚动升级、回退、Leader 接管和业务集群灾难恢复；发布报告保留 `deployed=false`、`field_acceptance=false`。

本记录先于本轮业务代码编辑建立；上面的现场限制仍然有效。测试通过表示代码和隔离 fixture 达到对应断言，不等于 152–154 已完成 ClusterGuard 安装、升级、回退或灾难恢复验收。
