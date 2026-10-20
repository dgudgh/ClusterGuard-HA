# 2026-10-20 文档同步批次：现场写入记录、完整目录补齐与首页版本同步

## 修改前记录

开始时唯一主线为 `codex/2.2-postgresql`，HEAD 为 `0cc7619`，工作树干净，与远端同步（领先 0 / 落后 0）。上一批次为 2026-10-19 的完整介质验收路由纠正，台账已登记到该批。

修改前已读[门禁执行与更新流程](../zh-CN/validation-gate-workflow.md)。本批次第三项要改动根 README 对**当前发布介质**的表述，属于契约覆盖的 `release` 范围，因此已**完整**读取[强制契约 v2](../zh-CN/upgrade-validation-chain.md)（全文 1177 行，非节选）：

```text
CG-UPGRADE-CONTRACT
contract_version=2
contract_loaded=true
```

## 适用阶段

**纯文档改动。** 本批次不构建、不签名、不产生新制品、不上传 GitHub Release、不在现场安装任何包。因此执行适用的文档与许可校验；**不执行** `source` / `artifact` / `field` 阶段，也不因本批次改动重新声明它们通过。契约规定的证据绑定明确「普通 README 和历史文档的编辑不使源码验收失效」，本批次不触碰该绑定覆盖的文件。

契约 §18 是本批次的硬约束：**禁止把 `built/signed locally` 描述为 `released/pushed/installed`**。README 同步时严格按 3.1.3.6 已核验发布说明记录的实际状态书写——它是 `pushed` + `tagged` 的**预发布**，**不是** `installed_field`、也**不是** `verified_field`；并保留「完整现场安装、故障矩阵与目标 OS 生产签名信任未关闭」这一未完成范围，不把预发布包装成稳定验收。

## 一、现场测试数据写入器重建（含 17:00 事件记录）

### 起因与旧行为

承接 2026-10-12 起的「经 VIP 一直写测试数据」要求。原写入器在 `.153:/root/cg-traffic-writer.sh`，2026-10-12 16:10:35 之后停摆——环境重制时 VIP 消失，写入器空转到死；库里留下 `mesdb.cg_traffic_test` 939,000 行、round_no 1..5。

2026-10-14 16:59 用本机新脚本 `.build/field-access/cg-traffic-writer-local.py` 恢复写入（实测运维机可直连 VIP `:3306`，不需 SSH）。该实例是**会话绑定的后台任务**，且原脚本有三处不能跨切换长期运行的缺陷：

1. 启动时若读不到身份就 `return 1` 退出——切换期间 VIP 不可达会直接死亡。
2. 起始位置一律 `MAX(round_no)+1`——被中断的半轮会永久留下缺口。
3. 失败重试每批记一行——切换期间日志每 10 秒刷一行。

### 本次改动

重建为脱离会话的实例（PID 44927，`start_new_session=True` 起在新会话，父进程退出后为孤儿进程），并修掉上述三处：新增 `wait_for_writable_primary()`（写入口不可达或入口 `read_only=1` 时保持等待并降噪记录）；起始位置可续半轮（最新一轮 `MAX(seq) < 每轮行数` 时从 `MAX(seq)+1` 续写）；失败日志只在第 1 次与每 12 次记录。

脚本与日志位于 `.build/field-access/`，该目录不入 git，因此不在 Markdown 台账范围内。

### 17:00:32 事件与既有灾备记录的对应

写入恢复后约 1 分钟观察到一次集群变动：主库 `.152` 的 binlog 被 purge（`@@gtid_purged` 被设为 `…:1-716`，`@@gtid_executed` 已到 `:1-840`）；两台副本报 `Got fatal error 1236 … source purged required binary logs`；随后 `.152` 出现半同步等待超时；**三台一度全部 `read_only=1` + `super_read_only=1`，集群无可写主库**；约 3–4 分钟后 `.153`/`.154` 复制线程重连 `.152`，`.152` 恢复 `read_only=0` 且仍是主库，两条复制链 `SERVICE_STATE=ON`，VIP 恢复。

这与本仓已有的两份 2026-10-14 记录逐项吻合：[物理缺失 binlog 与过期恢复计划](disaster-binlog-recovery-2026-10-14.md) 记录「用户要求复现物理删除主库 binlog、两台从库报错而无法灾难恢复的场景」，真实复现中「两台均 IO=No、SQL=Yes、lag=NULL，错误正文为 source error1236」；[实际 binlog 可用性](disaster-binlog-availability-2026-10-14.md) 记录「仅在还原保留的原 binlog 后，同一任务重试才完成 Recovery Commit 并释放保护」。

**状态标注：这是吻合，不是确认。** 现场证据全部来自数据面（MySQL 错误日志与复制表），本次**没有**控制面证据（控制面 API 需 `Bearer` token，token 在节点 `clusterguard.env`，本次无 SSH 通道取不到）。因此不宣称已定位触发者，也不把它记成新的源码缺陷。

### 数据现状与一个必须保留的坑

本次续写后 round 6 = 100,000 行（seq 1..100000，干净）、round 7 持续写入，速率约 650 行/秒。**round 1..5 存在同一 `(round_no,seq)` 重复行**（如 round 5 有 161,000 行但 `MAX(seq)=81000`）：`idx_round_seq` 是普通索引不是唯一键，拦不住；成因是历史上曾同时存在多个写入器实例。按轮统计行数时不能当作数据量使用。

## 二、`docs/catalogue.md` 补齐

对照 `docs/zh-CN/` 与 `docs/en-US/` 实际文件，目录存在两处缺口：

1. **历史发布表格最新只到 2.2.47**，而 `release-3.1.3.1` ~ `release-3.1.3.5` 五份发布说明早已双语齐全却未收录（仅 3.1.3.6 出现在顶部「当前完整介质」）。本次按现有行格式补入，中英两列各自指向对应语言目录。
2. **10-02 ~ 10-05 的文档与门禁演进记录未收录**：`documentation-sync-2026-10-03`、`documentation-navigation-2026-10-04`、`documentation-navigation-review-2026-10-04`、`documentation-reading-priority-2026-10-05`、`documentation-online-verification-2026-10-05`、`validation-gate-evolution-2026-10-04`、`validation-gate-reading-priority-2026-10-04`、`upgrade-hotfix-v2-audit-2026-10-03`、`upgrade-hotfix-v2-repair-2026-10-03`、`mainline-hotfix-integration-2026-10-02`。这十份**只有中文**，故按目录既有惯例放入「内部工程记录」段的纯中文链接风格，不伪造英文列。

顶部「当前完整介质」段落不动：它已在 10-19 批次更新到 3.1.3.6，本次不重复修改。

## 三、根 README 首页版本同步

旧表述（两处，2026-10-15 及更早遗留）：

```text
### 当前完整安装介质：3.1.3.5（本地候选）
… 产品版本 3.1.3.5，RPM 兼容基线 2.2-113 … 源码与文档提交 GitHub；本版尚未发布 GitHub Release，ART/FIELD 仍 OPEN。
```

与当前事实不符的有三处：版本落后一整版（现为 3.1.3.6 / 2.2-114）；「尚未发布 GitHub Release」已不成立（[GitHub 预发布 v3.1.3.6](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.3.6) 已公布，8 个公开附件全部下载回读、大小与 SHA256 核对通过，未设为 Latest）；链接指向 `docs/zh-CN/release-3.1.3.5.md` 而非 3.1.3.6。

新表述按已核验的 [3.1.3.6 发布说明](../zh-CN/release-3.1.3.6.md) 事实书写：产品 3.1.3.6、RPM 基线 2.2-114、构建提交 `9339a6a`、唯一目录 `release/3.1.3.6/`、预发布标签 v3.1.3.6 已公布且附件回读通过；同时保留未完成范围——完整现场安装、故障矩阵与目标 OS 生产签名信任未关闭，不宣称正式稳定验收。`README.zh-CN.md` 是 5 行的迁移跳转页，指向 `README.md`，无需改动。

## 验证计划

1. 登记 [Markdown 变更记录](markdown-change-record.md)：本文件为 `A`，`docs/catalogue.md`、`README.md`、`README.en.md` 为 `M`。
2. `node tools/verify-markdown-change-record.cjs` 与 `--self-test` 均通过。
3. `node tools/verify-license-consistency.cjs` 通过（无依赖或打包改动，用于确认本批次未引入偏差）。
4. 新增链接逐个核对实际文件路径存在（本仓已无独立链接巡检脚本，改为逐条 `ls` 验证）。
5. `cd docs && node build-html-docs.mjs` 重建生成页，`git status --porcelain docs/html` 确认改动仅来自本批次源变更、无意外漂移。
6. 生成页不逐页登记，按台账规则写进本批次说明。

未执行即不计通过：本批次不跑升级链的 source/artifact/field 阶段，也不因文档同步宣称任何现场验收状态发生变化。
