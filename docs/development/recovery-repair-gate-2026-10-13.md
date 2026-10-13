# 复制修复入口与灾难恢复门禁修复记录

日期：2026-10-13

```text
CG-UPGRADE-CONTRACT
contract_version=2
contract_loaded=true
```

## 基线与复现

- 工作树基线为 `d24ba32`（`codex/2.2-postgresql`），开始前无未提交改动。现场 `.152`、`.153`、`.154` 的只读命令 `clusterguard --version` 均返回 `3.1.2.10`、构建 `e08506c7bd8db0eb9c99e57816bffbbe4a5822dd`；`git diff e08506c..HEAD` 显示本轮涉及的控制台、灾难门禁、MySQL 修复执行器与候选评估文件在现场构建和源码基线间无差异。现场操作状态尚未通过 API 核实，`.152` 当日日志过滤未找到可归属此次修复点击的报错。
- `git show 14defea:internal/api/console.html`：初始控制台的复制修复按钮直接调用 `executeOperation('replication_repair', ...)`；`git show 14defea:adapters/mysql/repair.go`：同一版本的执行器已经要求 `parameters.action` 属于低风险白名单。当前控制台 `executeOperation` 请求仍未传 `parameters`，所以即使按钮可点，MySQL 和 PostgreSQL 修复预检均被动作白名单拦截。截图中按钮变灰时页面正在执行旧主回挂，不能据此单独认定该状态是缺陷。
- `git show c8795a3:internal/store/disaster.go`：最初的恢复门禁就把同集群 `running` 与所有 `indeterminate` 操作一并阻断。当前 `ReviewIndeterminateOperation` 保留原始 `indeterminate` 状态、仅写入不可变 `Review`；`OperationRecord.RequiresReview()` 才定义未复核的不确定操作。历史已复核记录仍阻断恢复，和人工复核出口矛盾。

## 本轮契约与边界

- 复制修复必须显式选择受引擎支持的低风险动作，并由后端继续执行白名单、身份、复制配置、锁、预检和后验核查。刷新、登出、换集群或换目标时，旧意图不得提交。
- 同集群运行中或未复核的不确定操作仍阻断灾难恢复；已有不可变人工复核的历史不确定操作不再永久阻断。复核不等于操作成功，也不跳过 fencing、GTID/WAL 候选比较、重建或后验核查。
- 控制台显示可区分无主库与多主库。若增加验证入口，验证失败不得自动复核或清除不确定结果。
- 本任务只改源码和隔离测试；现场数据库提升、复制修复、克隆、流量写入器和安装发布均不执行。现场验收另行记录。

## 源码修复

- 控制台依据引擎提供明确的修复动作选择，提交 `parameters.action`；换动作重新锁定。MySQL 限制为启动 IO/SQL 线程，PostgreSQL 限制为恢复 WAL 回放或重新加载配置，最终白名单与状态仍由适配器检查。
- `BeginRecovery` 继续拦运行中及未复核的不确定操作，已复核的历史不确定结果可进入后续隔离与证据检查；原始状态不被改写。
- 候选评估把零主库与多主库返回为不同原因，控制台分别提示灾难恢复与主库冲突。

## 验证计划

- Go 定向测试覆盖复制修复白名单及灾难门禁的运行中、未复核、已复核与跨集群情形。
- 浏览器隔离夹具核对动作参数、锁与陈旧意图；控制台与 API 测试核对文案和验证入口（若实施）。
- 执行源码严格门禁及自测、适用变异、Markdown 台账与 HTML 重建、许可一致性检查。源码通过不代表制品或现场通过。

## 本轮验证结果

- `go test ./internal/store ./internal/api ./adapters/mysql ./adapters/postgresql -count=1`、`go test ./internal/disaster ./internal/workflow -count=1` 通过；候选零主库/多主库原因的 API 定向测试通过。
- 隔离浏览器的 `console-operation-lock-acceptance.cjs` 与 `console-operation-lifecycle-audit.cjs` 通过，涵盖两引擎动作参数、换动作重新锁定、操作生命周期与拒绝后锁状态；没有连接现场数据库。
- 严格 SOURCE 门禁 33 项通过，自测 60 个变异全部检出；Markdown 记录常规门禁 11 项通过，自测 10 个变异检出。自测中两条旧夹具错选了已有重复台账行，现已改为唯一的基线文件与未变更文件。许可一致性 68 项通过；`docs/html` 已重新生成且没有净变更。
- 依赖历史 `.build/end-to-end-audit-20260910/field/152-api.json` 的现场响应重放未能运行，因为该本地输入文件不存在。ART 与 FIELD 均未执行，现场运行版本仍是旧构建。

## 3.1.2.11 热修构建计划

- 现场 3.1.2.10 的确切构建提交是 `e08506c7bd8db0eb9c99e57816bffbbe4a5822dd`，RPM 兼容基线由随包 `RELEASE-INFO` 确认为 `2.2-108`，架构为 `x86_64`。本次只修现有能力，按热修版本规则将产品版本递增为 `3.1.2.11`，包名为 `clusterguard-3.1.2.11.x86_64.cgpatch`。
- 从该现场提交到新的修复提交之间，已有 `03eb74c`（中断安装的 MySQL 单元处理）与 `d24ba32`（合法空 GTID 切换），均涉及生产路径。热修声明必须逐项列入这两个提交与本轮提交；构建器按导入图和打包清单决定二进制、运行脚本及安装器载荷，不得仅把本轮改动打成旧二进制。
- 签名前用现场受信公钥校验发布私钥的公钥指纹。缺少现有离线私钥时，只能形成规格与不适用于现场的构建预演，不能声称产生可安装的签名补丁。正式包须通过签名、摘要、包内路径、运行版本、热修目录台账与 ART 门禁；FIELD 留待获准上线后执行。
