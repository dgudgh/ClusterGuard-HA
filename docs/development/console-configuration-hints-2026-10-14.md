# 控制台「运行参数」两条顶部提示的下线与参数级说明

日期：2026-10-14

## 门禁与适用阶段

- 已读：[门禁执行与更新流程](../../zh-CN/validation-gate-workflow.md)、[修改与证据规则](../rules/change-policy.md)、[前端通用交互规则](../rules/frontend-common.md)、[设置功能](frontend/settings/configuration.md)。
- 适用阶段：**源码**（`node tools/verify-upgrade-validation-chain.cjs --stage source --strict`）。
- 契约判断：本任务只改控制台「运行参数」页两条提示的来源与一处参数说明，**不涉及**升级、热修、重试、续跑、回退、控制台更新流程、操作管理、维护锁、构建、签名或发布，因此**未加载强制契约 v2**（`contract_loaded=false`）。若后续动作超出此边界，须先补读 v2 再继续。
- 本任务只改源码与隔离测试；现场重启、参数生效核验、真实浏览器实机验收均**不执行**。

## 基线与旧行为对照

- 工作树基线 `754a824`（`codex/2.2-postgresql`），开始前无未提交改动。
- 旧行为（基线）：控制台「运行参数」面板上方常驻两条橙底提示：

  1. **「重新读取文件」说明** —— 来自 `internal/runtime/configuration_view.go:28-30` 常量 `configurationReloadNote`，在 `ReloadSupported=false` 时无条件下发（`:85-86`）。前端渲染在 `internal/api/console.html:1489`（`id="configuration-reload-note"`），赋值在 `:3499`（重置态）与 `:3505`（渲染态）。
  2. **`CG_APPROVAL_TOKEN` 弃用告警** —— 来自 `internal/config/config.go:279-284`（`approval_token_env` 非空即追加 `DeprecationWarnings`），经 `configuration_view.go:87` 进入 `view.Warnings`，前端渲染在 `console.html:1490`（`id="configuration-warnings"`）。

- **触发面（关键）**：`scripts/install_clusterguard.sh:1910` 生成控制器配置时默认写 `approval_token_env:"CG_APPROVAL_TOKEN"`。因此凡按官方安装器部署的节点，该告警**必然**常驻，等于产品抱怨自己的默认配置；现场截图出现该条即为佐证。
- 旧版对照命令：`git show 754a824:internal/config/config.go`、`git show 754a824:internal/runtime/configuration_view.go`、`git show 754a824:internal/api/console.html`。

## 必须保持的不变量

- `internal/api` 的 `ConfigurationView.ReloadNote` 字段与 `configurationReloadNote` 常量**保留**（`internal/api/configuration_test.go:65`、`internal/runtime/configuration_view_test.go:159` 依赖它们），接口形状不变。
- `config.go` 的 `DeprecationWarnings` 机制**保留**：该条仍进入启动日志（`cmd/clusterguard/main.go:141` 的 `logConfigurationWarnings`），只是不再进入控制台 `view.Warnings`。
- `approval_token_env` 仍解析为 `configuration.ApprovalToken`（`internal/config/config.go:279-284`）并用于 legacy 节点生命周期审批（`internal/runtime/runtime.go:1251` → `internal/lifecycle/gates.go:99-107`，fail-closed）；该行为不变。
- 参数下发、授权、预检、维护门禁与既有语言映射机制不变。

## 源码改动

- `internal/config/config.go`：告警文案提为导出常量 `ApprovalTokenDeprecation`，`DeprecationWarnings` 仍追加它（供启动日志）。
- `internal/runtime/configuration_view.go`：新增过滤，把该条从控制台 `view.Warnings` 移出（其他告警照常保留）；`approval_token_env` 的 `Note` 改写为弃用说明——数据库操作与自动恢复不再使用、仅用于旧节点生命周期审批、且引用环境变量为空时节点生命周期操作会被拒绝。
- `internal/api/console.html`：删除 `configuration-reload-note` 元素（`:1489`）与两处赋值（`:3499`、`:3505`）。UI 语言映射条目保留，避免触碰语言门禁的目录计数边界。
- `internal/api/console_test.go`：去掉 `configuration-reload-note` 的元素与渲染契约断言。
- `internal/config/config_test.go` / `internal/runtime/configuration_view_test.go`：按新行为补充/调整断言。

## 验证计划

1. `go test ./internal/config ./internal/runtime ./internal/api ./cmd/clusterguard`
2. `node tools/console-language.test.cjs`
3. 受影响的控制台验收工具：`console-configuration-collapse-acceptance.cjs`、`console-configuration-distribution-acceptance.cjs`、`console-configuration-groups-acceptance.cjs`、`console-cluster-policy-audit.cjs`
4. `node tools/verify-upgrade-validation-chain.cjs --stage source --strict`
5. 登记 `../markdown-change-record.md`，执行 `node tools/verify-markdown-change-record.cjs`，并 `cd docs && node build-html-docs.mjs`
