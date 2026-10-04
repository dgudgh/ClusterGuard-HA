# 打包前完整阻断清单

仅在准备新交付物或正式发布时读取；普通功能开发走对应模块测试。

- [ ] 已有修改前旧代码对照记录；记录基线与实际差异，不得倒填为已事前完成。
- [ ] 保持的旧行为及新增/修复行为均有对应回归证据。
- [ ] 最终源码、已测试 HTML 与包内嵌版本一致。
- [ ] 来源 RPM、目标 RPM、签名、现场信任公钥、回退载荷、SHA-256 和只读 inspect 校验一致。
- [ ] 已说明现场版本是否核实、哪些场景未测试、是否部署；用户手动升级的请求不得擅自改成直接安装 RPM。
- [ ] 不覆盖已交付版本；新修复用新的版本和补丁 ID。
- [ ] 交付记录可追溯：`node tools/verify-release-records.cjs` 报 `status=passed`，即 `release/` 下每个 `RELEASE-INFO` 的 `commit=` 都能被标签或远端引用到达（`local-only` 表示只被本地分支指着，必须推送到远端）。发布说明的提交必须回到主仓库分支，不得只留在 `.build/` 的一次性构建克隆里。
- [ ] 公开渠道只放完整安装介质，且每个 Release 都带完整介质：`node tools/verify-public-release-assets.cjs` 报 `status=passed`，即所有 GitHub Release（含草稿）附件中没有任何签名 `.cgupgrade`、旧 `.cgpatch` 或 `<来源>_to_<目标>` 形态的升级包，且没有任何已发布 Release 缺少完整离线介质。**升级包只对签约企业客户交付、只留本地，不得上传 GitHub 或任何公开渠道**（见 [发版规范 §3.1](../../zh-CN/version-release-policy.md)）。删除越权附件时必须连同其 `.sha256` 一起删；删除整个 Release 时保留 Git 标签。
- [ ] 升级与热修的规则都有落点：`node tools/verify-upgrade-validation-chain.cjs` 报 0 failed，且 [升级与热修统一校验链 v2 实现状态](../../zh-CN/upgrade-validation-chain-implementation-status.md) 的未决义务**如实报为 `OPEN`**（未实现的不得算作通过；加 `--strict` 时它们必须让门禁失败）。该契约是本仓库的强制执行文档：执行任何升级/热修动作前先读它。 门禁阶段与证据更新见[执行流程](../../zh-CN/validation-gate-workflow.md)：源码开发可用 `--stage source --strict`，不能据此宣称制品/现场通过；发布前仍需相应 ART/FIELD 真实证据与严格验收。
- [ ] 许可声明一致：`node tools/verify-license-consistency.cjs` 报 `status=passed`，即根 `LICENSE` 是未改动的 AGPL-3.0 官方原文、`packaging/rpm/nfpm.yaml` 的 `license` 为 `AGPL-3.0-only`、RPM 内容清单与两个构建脚本都会装入 `LICENSE`（连同 `THIRD-PARTY-NOTICES.md`、`MPL-2.0.txt`）、且中英 README 与许可页陈述一致。新增依赖必须已写入 [THIRD-PARTY-NOTICES.md](../../../THIRD-PARTY-NOTICES.md)。

任何未完成项必须报告，不得为了交付把它改成通过。发布细则见 [恢复与升级发布验收清单](../../zh-CN/release-recovery-acceptance-checklist.md)。

旧代码对比与流程违例的实例见 [日志集群联动回归记录](../../zh-CN/log-cluster-scope-regression.md)。

## 必须保持与回归

- 发布回归纳入 `tools/console-node-safety-audit.cjs`、`tools/console-bootstrap-audit.cjs`、`tools/console-engine-pages-audit.cjs`；后台辅助接口失败不得冒充会话失效，也不得让操作沿用旧授权。
