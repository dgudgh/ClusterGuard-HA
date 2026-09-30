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
