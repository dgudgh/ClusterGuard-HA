# Markdown 全仓同步记录（2026-10-03）

## 修改前核对

- 源码基线：`7b643f42ed11901c5d73f7703f3e3f176d053bb4`；唯一主线 `codex/2.2-postgresql`，本地与远端一致。
- 全量读取 Git 跟踪的 198 个 Markdown，检查本地链接、升级/热修模式、契约版本、门禁结果与分支描述。原有本地链接未发现缺失目标。
- 对照 `git show 7ba87b3:docs/zh-CN/update-and-patch.md` 与当前 Manager/Helper/Runner：旧手册只写滚动 execute/resume，未说明独立热修 retry 和操作身份；当前源码已在 `7b643f4` 实现 v2。
- 对照源码导览旧维护日期和分支整合历史：它们记录的是旧阶段，不能继续用作当前实现及分支状态说明。
- 拟改范围：中英文入口、当前运维/升级/发布/测试指引、实现状态；相关历史记录增加后续修复指向。仅改文档，不改业务代码或已交付制品。

## 保留规则

- 三份 v2 契约逐字保留；规则修改须按契约版本流程处理。
- 两份热修台账由签名制品生成，逐项描述仍代表旧交付包；不手改台账，也不为文档同步重建包。
- 日期化发布、事件、审查及设计材料保留原事实；相关历史结论与当前实现的差异通过补充说明标明。
- 无关未跟踪文档、预览、聊天记录及诊断证据不纳入本次提交；生产版本未重新核实。

## 验证与交付

本次修改 20 份原有文档，新增英文实现状态与本记录，共 22 份。纠正了早于受控升级基线的命令示例，并明确来源 RPM 使用既有交付字节。历史测试结果以原修复记录为准。

- 200 份跟踪/拟提交 Markdown 的本地链接检查：0 个缺失目标。
- 三份契约与下载原文逐字一致；生成台账未修改。
- `git diff --check`：通过。
- 许可门禁：exit 0，68 checks passed。
- 升级校验链：17 PASS / 1 OPEN / 0 failed；`--strict` exit 1，因完整 ART/FIELD 义务 OPEN 而阻断，符合当前验收状态。
- 本次只有文档改动，未重新运行业务 Go/浏览器验收；此前实际结果见修复记录。无新包、标签或现场部署。
- 所有本次文档归入唯一主线；未跟踪杂物未提交。

## 全量审阅清单

以下摘要针对修改前 `7b643f4` 的文件字节，用于区分历史版本与同步结果。所有文件均已读取并检查本地链接；未涉及本轮功能的文档不机械改写。

- 已检查，内容与本轮修改无冲突，保留：173 个。
- 更新当前指引或补充历史状态：20 个。
- 签名制品生成台账保留：2 个。
- 规范原文保留：3 个。

| 原有跟踪文档 | 基线 SHA-256 前缀 | 处理 |
| --- | --- | --- |
| `.superpowers/sdd/task-1-report.md` | `c216b8f1a26e` | 已检查，内容与本轮修改无冲突，保留 |
| `.superpowers/sdd/task-1-report.zh-CN.md` | `c0973568fe12` | 已检查，内容与本轮修改无冲突，保留 |
| `AGENTS.md` | `8f9ebe39e07b` | 已检查，内容与本轮修改无冲突，保留 |
| `README.md` | `ebd3ebb15f6d` | 更新当前指引或补充历史状态 |
| `README.zh-CN.md` | `b43c56287b4a` | 更新当前指引或补充历史状态 |
| `THIRD-PARTY-NOTICES.md` | `2dcec26c2267` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/README.md` | `36b0616beb16` | 更新当前指引或补充历史状态 |
| `docs/architecture.md` | `175b8988b515` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/README.md` | `78445e52e8ad` | 更新当前指引或补充历史状态 |
| `docs/en-US/database-preparation.md` | `50fec78beac9` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/docker-swarm-mysql-validation-plan.md` | `2f6981fa4e76` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/docker-swarm-mysql.md` | `04fd494f82e4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/kubernetes-mysql.md` | `c32bdc394f21` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/licensing.md` | `f1f3bf392570` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/mysql-former-primary-recovery-qualification-2026-08-09.md` | `43815643e109` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/offline-rpm-install.md` | `ab614f5fb645` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/operations-manual.md` | `aed7a54ae3f4` | 更新当前指引或补充历史状态 |
| `docs/en-US/orchestrator-migration.md` | `962c231372a9` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/postgresql-production-qualification-2026-08-23.md` | `b13a93f4f45f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/power-lifecycle-test-report.md` | `0e7ebbd29d65` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/product-tour.md` | `b8e5d7f976ca` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/production-chaos-test-report-2026-08-09.md` | `7003c05e85fa` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.1.45.md` | `eea8a21d96ae` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.39.md` | `56530a1af176` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.40.md` | `93f8703e52d7` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.41.md` | `e054f4ea7b7b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.42.md` | `652d88d2c6d2` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.43.md` | `713cb62797f6` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.44.md` | `d57a9c3d1283` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.45.md` | `e249c48acb31` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.46.md` | `5522b4af0648` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/release-2.2.47.md` | `b59f6d062d71` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/update-and-patch.md` | `7892c4d65dfb` | 更新当前指引或补充历史状态 |
| `docs/en-US/update-operation-identity-incident-2026-09-30.md` | `0dc2aa8fae2d` | 更新当前指引或补充历史状态 |
| `docs/en-US/update-signature-incident-2026-08-31.md` | `6b3fc8c7998f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/en-US/version-release-policy.md` | `68cf64f64cf2` | 更新当前指引或补充历史状态 |
| `docs/hotfix-patches.md` | `5cb077a0f478` | 签名制品生成台账保留 |
| `docs/mysql-feature-parity-acceptance.md` | `d3e85dcd6d7b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/mysql-production-qualification-2026-07-28.md` | `e2d55be2e422` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/offline-install.md` | `085bc930b982` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/operations.md` | `5a0771430af2` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/postgresql-ha.md` | `d281872aefce` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/proven-mysql-ha-methods.md` | `b8ff8ab0547e` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/README.md` | `68aad633b846` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-10-clusterguard-ha-mysql-topology-candidate-intelligence.md` | `d629e3c39bb6` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-10-clusterguard-ha-phase1.md` | `f8f9179c6d8a` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-12-clusterguard-ha-guarded-mysql-switchover.md` | `d43694ff52df` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-13-mysql-feature-parity.md` | `6847df8bdb56` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-15-console-benchmark-and-superiority.md` | `3593f51b3312` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-16-clusterguard-one-time-approval.md` | `cd4ad66eb3e7` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-16-platform-authenticated-operations.md` | `bafd9127c672` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-17-console-cluster-management.md` | `4f15ee634513` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-20-control-plane-operability.md` | `5d43ab956066` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-20-postgresql-enterprise-ha.md` | `314960465df2` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/plans/2026-07-20-postgresql-readonly-compatibility.md` | `40dc193081f6` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-10-clusterguard-ha-mysql-capability-migration-design.md` | `e0cc7d394f6d` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-10-clusterguard-ha-phase1-design.md` | `5f32aedd8f52` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-12-clusterguard-ha-guarded-mysql-switchover-design.md` | `49cfc4287fac` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-13-mysql-feature-parity-design.md` | `6c46baeca0d0` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-15-console-benchmark-and-superiority-design.md` | `9a2c2d6952ef` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-16-clusterguard-one-time-approval-design.md` | `ad1adca15cbb` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-16-platform-authenticated-operations-design.md` | `21db1effa405` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-17-console-cluster-management-design.md` | `1275883d0125` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-20-control-plane-operability-design.md` | `e673d0f8b340` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-20-postgresql-enterprise-ha-design.md` | `76e33771f04e` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/specs/2026-07-20-postgresql-readonly-compatibility-design.md` | `5ea415c62751` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-10-clusterguard-ha-mysql-topology-candidate-intelligence.md` | `4a6319c9db88` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-10-clusterguard-ha-phase1.md` | `200451ec3af7` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-12-clusterguard-ha-guarded-mysql-switchover.md` | `40e3f04fd53b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-13-mysql-feature-parity.md` | `9245472051ab` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-15-console-benchmark-and-superiority.md` | `0bd091f9b4fd` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-16-clusterguard-one-time-approval.md` | `0c6f106ab9b4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-16-platform-authenticated-operations.md` | `da2c4eb0d5c8` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-17-console-cluster-management.md` | `09d073ab370e` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-20-control-plane-operability.md` | `b9657f3659f1` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-20-postgresql-enterprise-ha.md` | `36bca767bc95` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/plans/2026-07-20-postgresql-readonly-compatibility.md` | `ace05a6c5c63` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-10-clusterguard-ha-mysql-capability-migration-design.md` | `4cdd6d2ca405` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-10-clusterguard-ha-phase1-design.md` | `58cfac40c3a4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-12-clusterguard-ha-guarded-mysql-switchover-design.md` | `c846fafc18a8` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-13-mysql-feature-parity-design.md` | `b97e45c61911` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-15-console-benchmark-and-superiority-design.md` | `4fd8fcb57cb8` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-16-clusterguard-one-time-approval-design.md` | `3ab58df03744` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-16-platform-authenticated-operations-design.md` | `a90b8ab9ecb1` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-17-console-cluster-management-design.md` | `7b369daea7cd` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-20-control-plane-operability-design.md` | `3e2031a83c8f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-20-postgresql-enterprise-ha-design.md` | `df59a9de3982` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/superpowers/zh-CN/specs/2026-07-20-postgresql-readonly-compatibility-design.md` | `af2725c15646` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/upgrade-validation-chain.md` | `4a8bfd9c8067` | 规范原文保留 |
| `docs/zh-CN/README.md` | `312eee335bb9` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/architecture.md` | `edfc55215d37` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/bootstrap-admin-installer-output-2026-09-23.md` | `2e72c1c32870` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/cluster-policy-audit-baseline-2026-09-24.md` | `f0a748fb758a` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/console-design-preview.md` | `501217671f49` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/database-preparation.md` | `166f5c532082` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/dead-code-cleanup-2026-09-11.md` | `fccaf23d9fa0` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/docker-swarm-mysql-validation-plan.md` | `2550b04f59ad` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/docker-swarm-mysql.md` | `6c4545f05f73` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/domain-model-review-2026-09-11.md` | `b897af62fe01` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/end-to-end-audit-2026-09-10.md` | `88527d5a838a` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/field-audit-2026-09-10.md` | `dee851a56c0b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/field-repair-2026-09-10.md` | `690d2002005f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/functional-review-2026-09-10.md` | `f52d33e6b424` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/helper-boundary-review-2026-09-10.md` | `c4e6193479eb` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/helper-linux-field-test-2026-09-10.md` | `1f72c67c1273` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/hf05-production-acceptance-2026-09-30.md` | `c8e4b509aac2` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/hotfix-history-mode-change-record.md` | `461282cdfbab` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/hotfix-patches.md` | `a8f370551218` | 签名制品生成台账保留 |
| `docs/zh-CN/kubernetes-mysql.md` | `921c9b376a29` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/licensing.md` | `8e2549e51c5b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/log-cluster-scope-regression.md` | `2e2bcfc56a8f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/mainline-hotfix-integration-2026-10-02.md` | `a14b4b340219` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/mysql-feature-parity-acceptance.md` | `84fa60645249` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/mysql-former-primary-recovery-qualification-2026-08-09.md` | `6e0326715cec` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/mysql-postgresql-disaster-recovery-progress-2026-09-07.md` | `3c415d0dc422` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/mysql-production-qualification-2026-07-28.md` | `3ec8ea72dfe6` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/offline-install.md` | `9e55dabab2bf` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/offline-kit-99-build-audit.md` | `c5dba5b36b4d` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/offline-media-selection-2026-09-23.md` | `5a7c16577583` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/offline-rpm-install.md` | `0b58ccf6d3a4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/operation-lock-audit-98.md` | `b2f2c58bc632` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/operation-lock-execution-97.md` | `60792cee0920` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/operation-lock-regression.md` | `c5ea40b25fd8` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/operations-manual.md` | `84a88645754c` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/operations.md` | `eb130130e5fb` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/orchestrator-migration.md` | `026feabb614f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/postgresql-ha.md` | `cc407f6db556` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/postgresql-p0-card01-diagnosis-2026-09-07.md` | `7934e372bbbc` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/postgresql-production-qualification-2026-08-23.md` | `9cbc7c3348ef` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/power-lifecycle-test-report.md` | `6ea622d93944` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/product-tour.md` | `aff2c0ce0218` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/production-chaos-test-report-2026-08-09.md` | `36ae993b88b7` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/proven-mysql-ha-methods.md` | `4f5eaf92ef1c` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/quality-followup-2026-09-10.md` | `2d9ca354830f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/quality-review-2026-09-10.md` | `5df97fd39a72` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/recovery-scenarios-pxc-reference-2026-09-08.md` | `e87c5a159835` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.1.45.md` | `471cf64de198` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.100.md` | `f5bdf5bf06ba` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.101.md` | `11dce4c023a3` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.102.md` | `bc5251e60583` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.103.md` | `2e0b232417c4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.104.md` | `fc69b91df5b5` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.105.md` | `6f45f7031317` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.39.md` | `8639e5c3126d` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.40.md` | `b2bbc4a4113f` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.41.md` | `962186abf8eb` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.42.md` | `87024435d5d5` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.43.md` | `f7fa62fc3507` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.44.md` | `8b8d9952ccd9` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.45.md` | `609d38351749` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.46.md` | `5de1991f53ff` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.47.md` | `8ebbe8bcabec` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.69.md` | `b840e5c84702` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.70.md` | `2fb86ae1d31b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.71.md` | `57cedd18edb9` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.72.md` | `d6a060714298` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.73.md` | `ac63a822a586` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.86.md` | `f4d01ee7beb0` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.88.md` | `2351abb585bd` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.89.md` | `59ce5311f614` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.90.md` | `abab0f37ace3` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.91.md` | `980a7100350b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.92.md` | `4fe4a16619e0` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.93.md` | `30c71b93414e` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.94.md` | `96ca429b032b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.95.md` | `75183b2548ab` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.96.md` | `49ea3412dd06` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.97.md` | `5ee3e98a1c95` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.98.md` | `da3bb1a0d289` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-2.2.99.md` | `091b8c3134a6` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/release-recovery-acceptance-checklist.md` | `374fc88c1efd` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/source-layout-and-testing.md` | `a2c02fe9a26c` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/ui-ux-review-2026-09-10.md` | `213c353b6f2a` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/update-and-patch.md` | `a80ce077fb7d` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/update-maintenance-gate-incident-2026-09-07.md` | `13cc98a592e4` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/update-operation-identity-incident-2026-09-30.md` | `371af319d422` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/update-signature-incident-2026-08-31.md` | `d5903b377c6a` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/updater-private-workspace-2026-09-11.md` | `89eb8f17c567` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/upgrade-hotfix-v2-audit-2026-10-03.md` | `0ba02297ab15` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/upgrade-hotfix-v2-repair-2026-10-03.md` | `fd73d443b7eb` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/upgrade-validation-chain-implementation-status.md` | `0d2b699aa9d3` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/upgrade-validation-chain.md` | `4a8bfd9c8067` | 规范原文保留 |
| `docs/zh-CN/version-release-policy.md` | `a11518f21f88` | 更新当前指引或补充历史状态 |
| `docs/zh-CN/wechat-01-why-clusterguard-ha.md` | `ef0112607ee8` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-02-mha-comparison.md` | `33a704ebf7ea` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-03-orchestrator-comparison.md` | `dcbf86343d41` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-05-offline-install.md` | `dd9452a46ff3` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-06-operations-tutorial.md` | `c360f274dc18` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-07-production-readiness.md` | `3140bbcd8a94` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-08-postgresql-ha.md` | `a3ec5be52613` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-clusterguard-ha-introduction.md` | `3d6b8aca2c33` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-hook-01-hostname-duplicate.md` | `0c9e91f6a047` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-hook-02-vip-failover.md` | `874bd8fd01ff` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-hook-03-former-primary-rejoin.md` | `2fb3534e0ae5` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-hook-04-dual-controller-migration.md` | `248de9880635` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-hooks.md` | `cf96c4fe0e7b` | 已检查，内容与本轮修改无冲突，保留 |
| `docs/zh-CN/wechat-series.md` | `62bea7101735` | 已检查，内容与本轮修改无冲突，保留 |
| `internal/updatecontract/contract.md` | `4a8bfd9c8067` | 规范原文保留 |
