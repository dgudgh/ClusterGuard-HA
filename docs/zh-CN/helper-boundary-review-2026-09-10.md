# 升级 Helper 特权边界续修

后续实测补充：用户随后授权前往 152–154，已完成 Linux root/服务 UID 与隔离 MySQL/PG 测试，见 [Helper Linux 三节点测试](helper-linux-field-test-2026-09-10.md)。以下保留上一轮结束时的原始范围和结论；新增实测不关闭 shell 共享目录协议及真实升级发布门槛。

## 范围与修改前基线

- 用户本轮已明确：继续处理上一轮未修完的问题，不是处理现场告警。本轮不操作 152–154，不安装或触发升级。
- 工作树 `.worktrees/platform-auth-session`；分支 `codex/2.2-postgresql`，HEAD `19d02bc`。当前全部未提交修改保留，尤其前一轮严格 JSON、会话隔离及 UI 评审稿。
- 已读 AGENTS.md、恢复与升级发布验收清单、上一轮质量续修记录；按任务编排分步骤执行，遵循用户不使用其他模型的要求，由当前代理完成。
- `git log` 查到 Helper 相关基线 `bc0546a`（2.2-68）。`git show bc0546a:cmd/clusterguard-update-helper/main.go` 与 `git diff bc0546a -- internal/platformupdate/helper.go cmd/clusterguard-update-helper/` 确认：UID 规则未变，Helper 当前仅多了上轮 EOF 校验。
- 完整读取 Helper → CommandLauncher → update-job.sh → upgrade.sh 的本地文件操作与发布链，以及 RPM 目录权限、systemd 配置。服务账户拥有暂存根，作业目录 0770 是 Leader 接管/续跑约定，不直接收紧为只读。
- 证据目录 `.build/helper-boundary-20260910.Q4Zdny/`。

## 问题与安全不变量

### HB-01：Helper 路径检查与文件打开分离

旧版与当前均以 lexical `strictChild` 加最终补丁 `Lstat` 检查路径，随后按字符串 `Chmod`；父目录链接未阻断。CommandLauncher `os.OpenFile` 跟随日志链接，未拒绝硬链接或特殊文件；失败回写也重新按路径读写。需要实际反例确认，不把源码推测直接当测试结果。

拟改范围是 **Go Helper 的文件操作**：用目录描述符逐级 `openat(O_NOFOLLOW)`，文件必须 regular 且单链接；权限修改作用于已打开的描述符；失败状态用同一目录描述符创建随机临时文件并 renameat。拒绝目录/叶子链接、硬链接和特殊文件，保留正常追加日志、0640 读取权限、0770 作业目录、终态和四种模式。

Go 模块声明 1.22，复用现有 `golang.org/x/sys/unix`，不升级 Go 或引入 os.Root。Linux 与本地 Darwin 支持安全文件操作，其他平台 fail-closed；测试临时目录先规范化操作系统的 `/var` 等固定别名，再构造攻击链接。

### HB-02：peer credentials 授权缺少测试

当前 Accept 只允许内核报告的 root 或配置服务 UID，查询凭证失败即关闭连接，语义正确，不能改变为信任 HTTP 头/请求参数。补充实际 Unix socket 测试：非 Linux 应拒绝连接；Linux 读取实际 UID、允许当前受信 UID、拒绝其他 UID、关闭 listener 可退出等待。原生 Linux 分支没有运行环境时明确标注，不用 mock 通过冒充内核授权通过。

## 执行计划

1. [完成] 保存本轮基线，新增文件链接反例并在修改前执行。
2. [完成] 修复已复现的 Go 文件操作，补凭证与文件竞态回归。
3. [完成] 关联包 race、全仓测试、build/vet、Linux 交叉编译；核对既有升级模式与终态回归。原生 Linux 未执行，单独列为发布门槛。
4. [完成] 核对最终 diff、9 份源码哈希与最终结果，列出未覆盖的生产/脚本边界。

## 明确限制

- 本轮暂不改变 shell 升级器的运行目录、远程分发、状态发布协议；在服务账户可并发修改暂存区的模型下，Go 打开文件安全不等于 shell 的所有路径访问都已抗竞态。完整隔离运行目录需要独立设计与发布验收，不用追加 Lstat 掩盖此限制。
- 未部署、未现场升级、未跑真实 MySQL 容器恢复；不把这些记录为已通过。

## 结果

### 反例与局部修复

- `before-tests.jsonl`：直接在本轮修改前的 Helper 上执行 9 个叶子用例，8 个失败、1 个通过。原有代码已经拒绝补丁叶子的 symlink，但未拒绝作业目录/暂存根/祖先目录链接、补丁 hardlink、日志 symlink/hardlink/父目录链接，以及失败回调前被替换的目录。
- 失败证据包含实际外部测试文件权限由 0600 变为 0640、日志追加 `should-not-run`、外部状态被回写；不是只依据源码推测，也不是在生产目录做攻击实验。
- `helper_files_unix.go` 按组件打开并固定目录，用 `O_NOFOLLOW` 阻断链接，使用 `fstat` 拒绝非普通文件和多链接文件，用 `O_NONBLOCK` 避免 FIFO 阻塞；所有 chmod 针对已打开的描述符。未使用 Go 1.22 以后的标准库 API。
- Handler 保持原目录描述符至完成回调，失败回写通过该目录创建随机独占临时文件并 `renameat`；目录即使被改名并替换为链接，回写仍落在原目录，不进入替换目录。
- 保留 0770 作业目录、0640 补丁/日志/状态，以及 planned/succeeded/failed/rolled_back 四个权威终态；`sync.Once` 保证完成回调只释放一次目录和执行占用。
- 非 Linux/Darwin 的文件操作 fail-closed；现有 Unix socket 的 UID 授权仍只支持 Linux，没有因为本地测试而放宽。
- 部署约束：Helper 暂存根必须是绝对路径，路径每一级都必须是真实目录。默认 `/var/lib/clusterguard/updates` 不变，但使用符号链接转移暂存区的自定义部署将被拒绝；发布前必须核对实际目录布局，不能跳过这一兼容性检查。本轮未改现场配置。

### 当前测试

| 检查 | 结果 |
| --- | --- |
| Helper 两包定向测试 | 最终 73 个叶子用例通过，`final-targeted-tests.jsonl` |
| 两包 race | 最终 73 个叶子用例通过，无跳过，`final-race-tests.jsonl` |
| 文件替换与特殊文件 | 通过：校验后启动前目录替换、回调前目录替换、打开文件后父目录替换、状态链接、FIFO/目录/Unix socket；随机临时文件无残留 |
| Darwin 实际 Unix socket | 获取平台不支持的凭证时关闭连接，Accept 在 listener 关闭后退出；通过 |
| Linux amd64 两包测试二进制 | 交叉编译通过，未运行；不等于 SO_PEERCRED 验收通过 |
| 全仓测试 | 最终 38 个有测试包通过、2 个无测试包；2089 个叶子用例通过、5 个环境用例跳过，无失败，`final-full-tests.jsonl` |
| 最终 build / vet | 通过，`final-build.log` / `final-vet.log` |
| PostgreSQL 原生回归 | 最终版本通过本地 PostgreSQL 16 三实例的 50 个后台发现周期、WAL 分支及恢复相关测试；发现用例使用内存仓库，不代表现场 Raft/VIP 全链路验收 |
| 格式 / diff | `gofmt -l` 无输出、`git diff --check` 通过 |
| 最终源码一致性 | `verified-source.json` 中 9 份代码/测试 SHA-256 在最终测试后复核一致；最终汇总 `summary.json` |
| 现场 / 发布 | 未 SSH、未部署、未执行现场升级或恢复、未打包、未提交或推送 |

测试构造 Unix socket 最初使用 `mknod`，本地 Darwin 返回不允许操作；已改用实际 `net.ListenUnix` 和短临时路径后执行通过，不跳过此用例。

### 复核中修正的问题

本轮将状态读取改为单次描述符读取时，最初忽略 `json.Unmarshal` 的类型/日期错误，可能把部分解码出来的终态字段当作有效终态。新增 2 个反例复现该问题（`review-before-tests.jsonl`），随后改为解码失败时清空部分结果，再生成合法失败状态。四种格式有效的终态仍保持不变。这是修改过程中发现并修正的问题，不重复算作旧版已有缺陷，也不把当前代理复核称作独立代理审计。

最终全仓测试在 2026-09-10 17:31:24–17:37:29（Asia/Shanghai）执行，使用本地 Darwin arm64 / Go 1.26.5；模块仍声明 Go 1.22。Linux amd64 Helper 和两份测试二进制仅交叉编译通过，不宣称已经在 Go 1.22 工具链或 Linux 内核上运行。

5 个跳过用例及条件：

- `TestEntrypointPreservesDynamicPostgreSQLRole`：需要 `CG_DOCKER_INTEGRATION_TESTS` 与 Docker。
- `TestRecoveryMySQLActualGuardedClone`、`TestRecoveryMySQLActualThreeNodeRelayDrainAndSelection`、`TestRecoveryMySQLActualExecutorRebuild`：需要 `CG_MYSQL_RECOVERY_TEST_IMAGE` 与 Docker。
- `TestRecoveryPostgreSQLReadOnlyStoppedDockerEvidence`：需要显式只读 Agent 配置和集群。

本轮变更限定为 `internal/platformupdate/helper.go`、新增的 `helper_files_{unix,other}.go` 和 `helper_files_test.go`、现有 Helper 测试的临时目录规范化、`cmd/clusterguard-update-helper/peercred*_test.go` 与本文。未改控制台、数据库适配器、现场状态、签名协议或 UID 判定；未撤回其他未提交修改。

## 下一张安全任务卡与发布门槛

HB-01 本轮仅关闭 **Go Helper 已复现的文件操作缺陷**，不关闭整个特权升级链路：

1. `scripts/clusterguard-update-job.sh:35` 的公开暂存目录仍同时作为 root 运行目录；第 38–48 行的路径检查与 chmod/chown 分离，第 57–71 行使用固定 `status.json.tmp` 路径，第 77–82 行按路径改权限。Go 侧描述符不能保护另一个进程重新打开这些路径。
2. `scripts/clusterguard-upgrade.sh:1375` 开始的 journal/event 临时文件、第 1407 行的状态输出和第 1346 行开始的远端结果发布，仍要和根目录隔离一起设计。只给这些路径补 `test ! -L` 不算抗竞态修复。
3. 后续应先定义 root 私有执行区与服务组可读结果区，明确签名包复制/复验、日志与状态发布、重启及 Leader 接管续跑协议，再实施，不能直接取消 0770 而破坏现有续跑。
4. Linux 原生验证须分别在 root 和非 root 服务 UID 下执行 peer credential 用例，覆盖允许服务 UID、允许 root、拒绝其他 UID、错误凭证关闭连接；目前只有 Linux 交叉编译和 Darwin 拒绝连接通过。
5. 发布前验证默认与自定义暂存路径、升级签名/可信公钥、升级失败回退、维护门禁、Leader 接管、安装包仅保留最近三版但不截断操作历史，并完成现场 MySQL/PG 验收。此前列出的 secret 轮换和其他引擎原生恢复仍未完成，不因本轮修改而自动关闭。

以上 1–3 是源码审查确认的剩余边界，尚未用本轮 root shell 攻击实验复现，不能冒充已修复或已实测。公开暂存区与私有执行区之间的完整并发协议也是发布阻断项，而不是普通界面优化项。

**结论：Go Helper 的本轮文件操作缺陷已复现、修复并通过上述本地验证；整条特权升级链路和现场验收仍为 partial，不把本轮结果当作可发布升级包或所有 P0 已关闭的证明。**
