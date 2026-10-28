# external_command 端点提供者（云/混合云写入端点）

日期：2026-10-28。分支：`codex/2.2-postgresql`。基线提交：`fc40d54`。

## 本次门禁状态

| 项 | 值 |
| --- | --- |
| 已读门禁 | [升级与热修门禁的阶段和更新流程](../zh-CN/validation-gate-workflow.md)（P0 必读） |
| 适用阶段 | `source`（`node tools/verify-upgrade-validation-chain.cjs --stage source --strict`） |
| 是否加载强制契约 v2 | 否。本次不涉及升级、热修、重试、续跑、回退、控制台更新流程、维护锁、构建、签名或发布，不产生新包身份，不触发契约 §20 的记录要求 |
| CG-UPGRADE-CONTRACT | 不适用 |
| 计划验证 | `go build ./...`、`go test ./...`（不含 homebrew 的 PATH）、变异验证、`--stage source --strict` 及 `--self-test`、`tools/verify-license-consistency.cjs` |

## 为什么需要它

ClusterGuard 此前只有一种 HA 端点提供者：`internal/agent/vip.go:28-39` 的 `ip addr add` + `arping -U`。它依赖同二层广播域内的交换机响应免费 ARP。Azure 等 SDN 网络不接受虚机发出的 ARP 通告，因此 VIP 漂移在云主机上不生效：产品认为切换完成，客户端仍被按云自己的转发表送到旧主。

全仓 `grep keepalived|cloud|ILB|loadbalancer` 零命中，也没有切换后触发外部动作的 hook，所以此前没有任何云友好路径。

本改动不删除也不修改 VIP 提供者（多个能力依赖它：受限 Agent 命令 `failover_safety.go:483`、一键恢复 `disaster_driver.go:114/122`、writer/VIP 联合授权 `ownership_keeper.go:247/286`、回放前校验 `protocol.go:444/471`）。二者共存，由每集群资源上的 `Provider` 字段决定用哪个。

## 旧实现对照

| 关注点 | 参考实现 `LinuxVIPProvider` | 本次新增 `ExternalCommandProvider` |
| --- | --- | --- |
| 选资源 | 恰好一个 active VIP 资源，且 `Provider` 必须是 `linux_vip` | 恰好一个 active `EndpointService` 资源，且 `Provider` 必须是 `external_command` |
| 观测手段 | 经 SSH Agent 向每个节点发 VIP 探针 | 调用钩子的 `observe`，解析其返回的 `owner_ids` / `complete` |
| 切换序列 | 观测 → 释放旧主 → 二次观测证明零持有 → 目标获取 → 三次观测 → 提交元数据 → 收敛复核 | 观测 → 迁移 → 二次观测 → 提交元数据 → 收敛复核（同为五步） |
| 失败处理 | 任一检查失败即返回错误 | 同，任一钩子失败、超时、输出超限、coverage 不完整均 fail closed |
| 承载配置 | `Interface` / `Prefix` | 复用既有预留字段 `HAEndpoint.ProviderRef`（K8s 提供者已在用），**无模型结构变更、无数据迁移** |

## 改动清单

| 文件 | 改动 |
| --- | --- |
| `pkg/model/model.go:251-262` | 新增 `EndpointProviderExternalCommand = "external_command"` 并加入 `Valid()` 白名单 |
| `internal/endpoint/external_command.go` | 新增提供者实现 |
| `internal/endpoint/external_command_test.go` | 新增 18 条测试 |
| `internal/config/config.go` | 新增 `ExternalEndpoint` 配置段与 `File.ExternalEndpoint` 字段，默认不启用 |
| `internal/runtime/runtime.go:1063-1071` | 在 `ExternalEndpoint.Enabled` 时注册提供者 |

## 安全决策（不是可选项）

1. **钩子以控制面权限运行**，因此路径必须：绝对路径、常规文件、非符号链接（用 `Lstat`）、不可 group/other 写、属主为 root 或当前运行 uid。任一条不满足即拒绝。
2. **不经 shell**。stage 作为唯一 argv 传入，所有上下文经 stdin 上的 JSON 传递。`OSProcessRunner` 已固定环境为 `PATH/LANG/LC_ALL`，进一步切断继承。
3. **绝不传递凭据**。`ResolvedOperation` 内含 `Credentials`（`adapter.go:65-69`），虽然已标记为 `json:"-"`，仍然构造显式白名单 payload，不复用结构体序列化。
4. **输出有上限**（默认 8 KiB，可配），超限直接失败而非静默截断。
5. **每次调用带超时**（默认 30s），deadline 进入进程而非事后检查。
6. **coverage 不完整即拒绝**。钩子若看不到完整拓扑就没证明任何人缺席，此时行动正是脑裂的来源。
7. **钩子的密级比集群低**：钩子只被告知往哪发流量，从不参与决定谁是主库 —— 决定权始终在 consensus 与 lease。

## 已完成的验证

- `go build ./...` 通过。
- `internal/endpoint` 包 18 条测试通过（PATH 为 `/usr/bin:/bin:/usr/sbin:/sbin`，不含 homebrew；此前曾因 PATH 含 homebrew 导致 `scripts` 包假红，已修正口径）。
- **变异验证 13 条全部被测试抓住**，源码按字节还原（SHA-256 前后一致）。其中首轮有 3 条漏网（符号链接、切换后归属验证、输出上限），已补精确断言后复测通过：
  - 符号链接原本被「非常规文件」间接兜住，现改为按错误原因逐条钉住。
  - 切换后归属验证原本被后续元数据复核掩盖，新增 `TestExternalCommandTransferRefusesWrongOwnerAfterMove` 单独钉住。
  - 输出上限原本靠其他路径碰巧变红，新增 `TestExternalCommandRunHookEnforcesOutputLimit` 直接断言。

## 本次没有做的事（不得据此宣称）

- **未做现场/真机验收**。没有在 Azure 或任何云环境执行过真实切换，没有 FIELD 阶段的任何条目。
- **未做数据库验收**。没有连接真实 MySQL/PG/Oracle/SQLServer 验证端到端。
- **未构建新包、未签名、未上传**。不产生新交付身份。
- **未做真实浏览器验收**（本改动不涉及控制台界面）。
- `gofmt` 报 `internal/agent/reconcile_protocol.go`、`internal/api/clusters_test.go`、`internal/recovery/controller.go` 三个文件不规范 —— **这三个是既有文件，本次未触碰也未顺手修改**，保持局部变更原则。

## 后续（不在本次范围）

按「先提供治理价值、再谈专有组件」的顺序，下一步是站点模型（`site/zone`），因为混合云的选址优先与跨站点仲裁规则目前全仓没有承载点 —— 这个缺口比数据面更靠前。
