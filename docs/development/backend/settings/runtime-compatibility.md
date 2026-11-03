# 持久策略与最低运行协议

[返回升级](updates.md) · [实例策略](instance-failover.md) · [本轮源码验证](../../policy-compatibility-floor-2026-11-03.md)

涉及变更、回退或构建前，仍必须先读[强制契约 v2](../../../zh-CN/upgrade-validation-chain.md)。本页解释兼容门槛，不能替代升级授权或现场验收。

## 何时锁定

首次保存实例策略时，策略、审计与 `runtime_compatibility_floor={"protocol":1,"instance_failover_policy":1}` 同一次 Raft 提交。这个最低协议是全控制面快照的持久状态，不是当前自动开关，也不是产品版本号。

关闭自动切换、重启、复制和退休集群不会降低门槛；旧快照恢复不能删去或降低它。历史快照已存在策略但没有显式门槛时，也推导为协议1，包含已关闭的策略；后续保存或退休时保留显式标记。没有保存过策略的合法旧状态仍是协议0。元数据损坏、不可读或未来未知协议不能当作协议0。带策略或兼容标记的当前快照损坏/未知时，启动明确拒绝，不自动读取可能早于首次保存的前一代来掩盖；保留原文件供恢复。

保存前逐一核对当前投票节点的身份、策略协议和运行门禁协议。节点只有确认本机 HA、Helper、升级器、job wrapper 四个程序都支持协议1，才报告运行门禁就绪；仅换 HA 不足以启用策略。全节点完成保护栈更新后再保存，不要求先改策略来绕过更新。

## 拒绝位置与真实目标

`API → Manager → 独立 root Helper → Runner → 节点工具` 各自检查。API/Manager 在建立新 job 前拒绝不兼容目标；Helper 重读本机可信配置及带摘要的状态，不信任调用者提供的较低门槛，并在启动及私有作业复制前独立验签、核对目标。Runner 在维护锁前、锁建立后及文件/RPM 替换前重查，自动回退同样受约束。新生成 apply/rollback 工具在建立备份或恢复文件前检查。

判断的是实际程序，不以文件名、产品版本大小或清单自报能力替代：

- 热修：重新验签清单及载荷摘要。正向检查声明的候选程序；回退按同包备份清单最早记录检查原备份，不能误选重试产生的较新备份。未更换的关键程序也必须兼容。
- 完整滚动包：正向检查签名清单的目标 RPM，回退检查源 RPM。先核对 RPM 摘要，再用系统 `rpm2cpio` 解码，私有目录只提取四个必需的常规可执行文件，不运行安装脚本。不接受关键程序的链接、重复路径、可写权限、缺失或未知能力。缺少解码器时明确拒绝，不能假装验证成功。
- 四个关键程序分别通过 `--version-json` 或脚本 `--runtime-compatibility-protocol` 报告运行门禁协议和实例策略协议；执行有超时及输出上限，检查前后摘要一致。

兼容的新旧目标可继续通过原状态机和安全检查；不认识策略或不执行兼容保护的历史程序均拒绝。首次部署这套保护宜使用包含全部四个关键程序的已签名热修：旧 Helper 不具备检查接口时不能凭产品版本猜测支持。完成后，兼容的滚动 RPM 更新也有明确放行路径。

不提供 force、清除门槛或把关闭自动当成清除策略的入口。这里保护产品支持的执行链，不能撤销系统 root 对任意旧脚本、文件或数据库的直接修改权限；历史已交付包字节不改。

## 错误、实现与证据

已知不兼容或无法证明兼容返回 HTTP409及`result.runtime_compatibility_rejected=true`的明确未受理标记；原 job、历史、策略和门槛保留，不启动 Helper 作业。控制台按当前中英文显示拒绝原因，API 原始诊断不改；只有明确未受理标记且committed不为true，才清除提交待确认状态。普通409、无标记或已提交仍核对原任务，不自动重发。只读 plan 不创建降级授权；实际执行必须重查。

- 状态：[model](../../../../pkg/model/runtime_compatibility.go)、[store](../../../../internal/store/runtime_compatibility.go)、[独立只读状态解析](../../../../internal/runtimecompat/metadata.go)。
- 边界：[API](../../../../internal/api/updates.go)、[Manager](../../../../internal/platformupdate/runtime_compatibility.go)、[Helper/热修](../../../../internal/platformupdate/hotfix_compatibility.go)、[RPM](../../../../internal/platformupdate/rolling_compatibility.go)、[Runner](../../../../scripts/clusterguard-upgrade.sh)。
- 回归：store/runtimecompat/platformupdate/api/runtime 对应测试、[Runner 真实 Bash 边界](../../../../tools/runtime-compatibility-runner.test.cjs)、SOURCE strict/self-test。

2026-11-03仅完成源码与隔离回归，包括 Linux root 私有目录实测；未生成、安装或验收包含此保护的新交付包。旧 HA3.1.7.7 的四项失败仍是历史事实，不能以新源码结果关闭现场验收。完整结果见本轮记录。
