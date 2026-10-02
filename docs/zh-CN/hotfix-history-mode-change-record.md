# HF-04 历史记录动作名称修复前对照（2026-09-30）

- 源码：`codex/hotfix-history-kind`，基线 `d6f8ff2`；修改前工作树干净。目标现场为 2.2-105 的 `HF-2026-0929-04` 历史作业，当前现场状态未重新读取。
- 旧行为：`81fe3c8^` 的 `internal/api/console.html` 只处理滚动升级；`softwareUpdateModeText('execute')` 显示“滚动升级”，当时与包类型一致。`81fe3c8` 加入 `.cgpatch` 热修通道和包类型展示，但 `renderSoftwareUpdateHistory` 仍只传 `job.mode`，因此热修的 `execute` 行也写成“滚动升级”。`5fb048f` 改进热修控制台后，这一历史表调用仍未改变。
- 实际调用链：服务端 `internal/platformupdate/manager.go` 将签名包的 `kind=hotfix` 写入持久化 package 记录，列表 API 把 package 与 job 合并；页面加载 `/api/v1/platform/updates` 后 `renderSoftwareUpdates` 调用 `renderSoftwareUpdateHistory`。历史行已有 `record.kind`，但动作列只用 `job.mode`。HF-04 失败记录的 `mode=execute` 被展示成“滚动升级”。刷新或重登仍会从同一记录复现。
- 保持的不变量：`job.status=failed`、原错误信息、时间、进度和维护门禁都由原始作业记录决定，不因文案修复改写；普通 `.cgupgrade` 的 `execute` 仍显示“滚动升级”；没有 job 的行仍显示“-”。
- 拟改范围：仅按 `record.kind` 解释历史行与当前包详情中的动作名称；不改包验签、执行、续跑、回退或现场记录。修改前的源码复现可得，现场浏览器与真实 API 此时未连接，不能宣称已做现场验收。
- 现场处置参考：`docs/zh-CN/release-2.2.105.md` 的“作业记录的处置”要求保留 HF-04 原始失败事实，待核对后追加恢复结果。

## 修改后验证

- 使用本仓库实际 `console.html` 在本机 Chrome 中渲染三条历史记录：HF-04 `kind=hotfix, mode=execute, status=failed` 显示“热修应用”，且保留“升级失败”和原始回滚错误；普通升级的 `execute` 仍显示“滚动升级”；仅上传未执行的热修动作列仍为“-”。测试数据只在浏览器内存中注入，没有请求现场 API。
- `go test ./internal/api -run 'TestConsoleProvidesAdminOnlySignedSoftwareUpdateWorkflow|TestSoftwareUpdateDialogPresentsWarningUploadMetadataAndActionInOrder|TestSoftwareUpdateDialogEnablesRollingUpgradeOnlyAfterValidation' -count=1` 通过。
- `git diff --check` 通过。本次未生成发布包，未做现场上传与集群验收。

## HF-05 成功后的旧失败摘要：第二次修改前对照

- 2026-09-30 09:30:59，现场 HF-05 已成功，三台载荷摘要与签名清单一致、维护门禁已释放。版本更新历史表新增成功的 HF-05 行，HF-04 失败行仍应保留；但摘要卡仍显示“待升级目标版本 HF-04 · 升级失败”，当前动作入口也仍选择 HF-04。
- `39ef673`、`d0f63e6` 确立了“选择最新且仍可执行的包”并排除异发布线包的旧行为；这些修复避免未来时间戳与错误基线抢占动作入口。`5fb048f` 加入热修后，`softwareUpdateActionable` 仍把任意 `failed` 记录判为可执行，即使同一发布线已有更新的成功热修且实时维护门禁已释放。`pendingSoftwareUpdate` 再把这条旧失败记录放回摘要卡。
- 新不变量：仅当控制面明确报告维护门禁已释放，且同类型、同来源发布线的**后续执行任务**已成功时，较早的失败作业从动作入口退为历史记录；原 `status/message/progress` 不修改。若门禁仍活动或状态未知，不能用这个规则隐藏失败作业。新上传的未执行包仍可成为待执行目标。
- 拟改范围：`softwareUpdateActionable` 的筛选条件；保留后端原始审计记录和所有执行接口。服务器侧针对旧包的直接操作授权尚未改变，本次前端修复不作为安全边界。

### 第二次修改后验证

- 本机 Chrome 用实际 `console.html` 渲染 HF-04 失败、HF-05 成功两条记录：门禁明确关闭时，摘要显示“最近完成版本 2.2-105+hf-2026-0929-05 · 升级成功”，历史表仍显示 HF-04 原始失败和错误。门禁活动或没有后续成功包时，HF-04 仍可作为待处置项；再上传新包时，新包成为待执行目标。
- `go test ./internal/api -run 'TestConsoleProvidesAdminOnlySignedSoftwareUpdateWorkflow|TestSoftwareUpdateDialogPresentsWarningUploadMetadataAndActionInOrder|TestSoftwareUpdateDialogEnablesRollingUpgradeOnlyAfterValidation|TestSoftwareUpdateRollingActionTargetsTheNewestActionablePackage' -count=1` 与 `git diff --check` 通过。
- 本地前端修改**未部署现场**，现场控制台仍显示原文案。上线须另出新身份的签名交付物，不能改写已发布的 HF-05。
