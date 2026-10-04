# 升级契约与验收门禁

[后端](../README.md) → [设置接口](README.md) → [升级与热修](updates.md) → 门禁

必须先读[强制契约](../../../zh-CN/upgrade-validation-chain.md)。本页说明实现落点；具体命令和报告格式在[分阶段门禁流程](../../../zh-CN/validation-gate-workflow.md)，需要执行对应阶段时再读。

## 先判断是哪类问题

| 问题 | 定位入口 | 通过的含义 |
| --- | --- | --- |
| 契约缺失、未知字段/版本、三份不一致 | [updatecontract](../../../../internal/updatecontract)、[源码校验脚本](../../../../tools/verify-upgrade-validation-chain.cjs) | 消费者支持当前强制契约 |
| 新功能破坏已有源码规则 | 校验脚本的 CHECKS / MUTATIONS、具体功能测试 | 已有断言成立；新增功能还须补自己的测试 |
| ART/FIELD 一直 OPEN 或旧证据复用 | [证据验证器](../../../../tools/upgrade-acceptance-evidence.cjs) | 当前版本证据清单完整，不自动证明现场健康 |
| 旧 Helper 不支持 contract | Helper 的 contract 子命令、Runner 前置检查 | 匹配运行组件可读取受支持契约；不可绕过检查 |

## 生命周期

源码更新走 `source --strict`，不宣称新包或现场完成；构建入口的 `--contract-only` 只做契约预检查。新包走 artifact 阶段，最终现场走 field 阶段。默认仍是 field，缺真实证据必须 OPEN，strict 失败。

证据报告绑定当前契约、跟踪源码、制品和证据文件摘要。换了功能、包或契约后不能沿用旧摘要；人工填 passed 必须来自实际验收记录。工具校验证据清单，不代替验签、浏览器或原生数据库测试。

强制规则未变时无需升契约版本；规则/机器接口变化时按契约版本流程同步三份规范、schema、Go/Node 支持版本及回归。不能单改 MD 或放宽未知字段来获得绿色结果。

## 验证落点

- 证据完整性、过期与 CLI 阶段：[证据测试](../../../../tools/upgrade-acceptance-evidence.test.cjs)。
- 源码断言失效：校验脚本 `--self-test`，要求变异被捕获且无关修改对照通过。
- 构建前加载：[热修构建器](../../../../scripts/build-hotfix-patch.sh)、[滚动升级包构建器](../../../../scripts/build-clusterguard-patch.sh)及 RPM/离线构建入口。
- 实际 ART/FIELD 完成情况：[实现与验收状态](../../../zh-CN/upgrade-validation-chain-implementation-status.md)。未执行项不能因选择 source 阶段而关闭。

仅在产出、签名或发布新制品时再进入[交付流程](../../delivery/README.md)。
