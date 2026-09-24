# 离线安装器数据库介质选择修复记录（2026-09-23）

## 修改前证据

- 源码工作树：`.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，基线提交 `467e533cd39a9c4224a6fdae70e1c561cf6d980b`。工作树已有其他未提交修改；本任务只涉及安装器介质选择、相应测试和本记录。
- 用户现场介质：`clusterguard-ha-2.2-103-offline-linux-x86_64`。本地原件位于主仓库 `release/2.2-103/`，只读 `tar -tf` 证实其中带有 MySQL 8.0.44 和 PostgreSQL 16.4 介质；现场两份 MySQL 包冲突的证据来自用户粘贴的 `--plan` 输出。未执行现场安装或远端验证。
- 历史基线：`scripts/install_clusterguard.sh` 在提交 `b33b2599` 首次加入；其 `resolve_database_package` 从一开始就同时扫描套件的 `packages/database` 和 `/opt`，再对全部候选按路径去重。父提交没有该安装器，故无可恢复的更早正确实现。
- 当前调用链：`main` → 参数解析 → `validate_inputs` → `resolve_database_package` → 介质格式/摘要检查 → `print_plan`。`--plan` 在选择阶段失败，没有开始远端安装。
- 现场复现：MySQL `8.0.44` 同时存在于套件内 `packages/database/mysql-8.0.44-linux-glibc2.17-x86_64-minimal.tar.xz` 和 `/opt/mysql-8.0.44-linux-glibc2.17-x86_64.tar.xz`；旧逻辑将两条路径当成两个候选并报“发现多个匹配的数据库介质”。

## 保持与修复范围

- 保持 `-r` 明确指定数据库包的最高优先级；`--database-package-dir` 只扫描指定目录；同一有效目录内若有多个同引擎、同版本候选，继续拒绝自动选择。
- 未指定目录时，先扫描当前套件的 `packages/database`。其中有唯一匹配时选用该包；有多个匹配时拒绝；没有匹配时才扫描 `/opt`，并保留其零候选或多候选的现有处理。
- 不根据文件名优先级、任意排序或仅凭文件是否存在跳过后续格式、摘要和介质内容校验。MySQL 与 PostgreSQL 都走相同的目录优先级。

## 回归方法与限制

- 使用隔离目录模拟套件介质和 `/opt`：修改前 `TestMultiNodeInstallerPrefersBundledDatabaseMediaBeforeOpt` 的 MySQL 和 PostgreSQL 两组均重现双目录歧义；修改后两组通过，且覆盖套件空缺、套件多候选、显式目录及 `-r`。
- `TestMultiNodeInstallerPlanPrefersBundledMySQLMedia` 使用真实可读取的测试 tar 包走完整 `--plan` 路径，验证两个目录均有同版本介质时输出只读计划。
- `go test ./scripts -run '^TestMultiNodeInstaller' -count=1 -timeout=120s`、`bash -n scripts/install_clusterguard.sh`、`gofmt -d scripts/scripts_test.go` 和 `git diff --check` 均通过。未运行全仓测试或现场安装。
- 真实 `2.2-103` 套件不会随工作树源码修复而变化；其安装手册已写入 `-r ./packages/database/mysql-8.0.44-linux-glibc2.17-x86_64-minimal.tar.xz` 的现场规避命令，但该修订命令尚未在现场验证通过。
