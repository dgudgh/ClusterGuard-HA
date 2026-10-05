# 打包前完整阻断清单

准备构建新交付物或正式发布前必须读取；普通功能开发完成适用源码验证，不宣称制品或现场阶段通过。

## 先确定适用阶段

| 本次工作 | 必须完成的门禁 |
| --- | --- |
| 源码修复 | `node tools/verify-upgrade-validation-chain.cjs --stage source --strict` 及相应功能回归 |
| 新包交付/发布 | `node tools/verify-upgrade-validation-chain.cjs --stage artifact --strict --acceptance-report FILE`；报告绑定本次源码和实际制品 |
| 现场升级/恢复验收 | `node tools/verify-upgrade-validation-chain.cjs --stage field --strict --acceptance-report FILE`；包含本次真实 ART/FIELD 证据 |

完整流程见[门禁执行与更新流程](../../zh-CN/validation-gate-workflow.md)。仅有“0 failed”、源码阶段通过或旧状态页的通过记录都不构成制品/现场通过；适用严格阶段必须退出 0，未完成义务仍如实保留。下面约束与对应阶段证据一并核对。

## 完整阻断项

- [ ] 已有修改前旧代码对照记录；记录基线与实际差异，不得倒填为已事前完成。
- [ ] 保持的旧行为及新增/修复行为均有对应回归证据。
- [ ] 最终源码、已测试 HTML 与包内嵌版本一致。
- [ ] 来源 RPM、目标 RPM、签名、现场信任公钥、回退载荷、SHA-256 和只读 inspect 校验一致。
- [ ] 已说明现场版本是否核实、哪些场景未测试、是否部署；用户手动升级的请求不得擅自改成直接安装 RPM。
- [ ] 不覆盖已交付版本；新修复用新的版本和补丁 ID。
- [ ] 交付记录可追溯：`node tools/verify-release-records.cjs` 报 `status=passed`，即 `release/` 下每个 `RELEASE-INFO` 的 `commit=` 都能被标签或远端引用到达（`local-only` 表示只被本地分支指着，必须推送到远端）。发布说明的提交必须回到主仓库分支，不得只留在 `.build/` 的一次性构建克隆里。
- [ ] 公开渠道只放完整安装介质，且每个 Release 都带完整介质：`node tools/verify-public-release-assets.cjs` 报 `status=passed`，即所有 GitHub Release（含草稿）附件中没有任何签名 `.cgupgrade`、旧 `.cgpatch` 或 `<来源>_to_<目标>` 形态的升级包，且没有任何已发布 Release 缺少完整离线介质。**升级包只对签约企业客户交付、只留本地，不得上传 GitHub 或任何公开渠道**（见 [发版规范 §3.1](../../zh-CN/version-release-policy.md)）。删除越权附件时必须连同其 `.sha256` 一起删；删除整个 Release 时保留 Git 标签。
- [ ] 升级与热修的规则都有落点：按上表和[执行流程](../../zh-CN/validation-gate-workflow.md)，使用本次绑定与真实证据通过适用严格阶段，且为 0 failed。本次制品/现场未完成义务**如实报为 `OPEN`**，不得算作通过；源码阶段明确 DEFERRED 的 ART/FIELD 不因此关闭。修改/执行升级热修前必须读[强制契约](../../zh-CN/upgrade-validation-chain.md)。[历史实现快照](../../zh-CN/upgrade-validation-chain-implementation-status.md)只用于追溯当时未决项，不作为本次门禁输入；发布前仍需相应 ART/FIELD 真实证据与严格验收。
- [ ] 许可声明一致：`node tools/verify-license-consistency.cjs` 报 `status=passed`，即根 `LICENSE` 是未改动的 AGPL-3.0 官方原文、`packaging/rpm/nfpm.yaml` 的 `license` 为 `AGPL-3.0-only`、RPM 内容清单与两个构建脚本都会装入 `LICENSE`（连同 `THIRD-PARTY-NOTICES.md`、`MPL-2.0.txt`）、且中英 README 与许可页陈述一致。新增依赖必须已写入 [THIRD-PARTY-NOTICES.md](../../../THIRD-PARTY-NOTICES.md)。

任何未完成项必须报告，不得为了交付把它改成通过。发布细则见 [恢复与升级发布验收清单](../../zh-CN/release-recovery-acceptance-checklist.md)。

旧代码对比与流程违例的实例见 [日志集群联动回归记录](../../zh-CN/log-cluster-scope-regression.md)。

## 必须保持与回归

- 发布回归纳入 `tools/console-node-safety-audit.cjs`、`tools/console-bootstrap-audit.cjs`、`tools/console-engine-pages-audit.cjs`；后台辅助接口失败不得冒充会话失效，也不得让操作沿用旧授权。
