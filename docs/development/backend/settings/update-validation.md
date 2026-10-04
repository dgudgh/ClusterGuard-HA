# 升级：契约、门禁与证据演进

[返回模块](README.md)

## 什么时候读

仅当任务涉及本功能的实现、故障或回归时读取；相关功能通过末尾链接继续进入。

## 路径

先读契约，再按 source/artifact/field 执行；功能测试与源码检查通过后才推进新包和现场证据。缺 contract 能力的 Helper 属于运行兼容边界，不能靠选择 source 阶段绕过。

## 源码与验证入口

| 职责 | 入口 |
| --- | --- |
| 实现 | [updatecontract](../../../../internal/updatecontract)、[verify-upgrade-validation-chain.cjs](../../../../tools/verify-upgrade-validation-chain.cjs)、[upgrade-acceptance-evidence.cjs](../../../../tools/upgrade-acceptance-evidence.cjs)、[build-hotfix-patch.sh](../../../../scripts/build-hotfix-patch.sh)、[build-clusterguard-patch.sh](../../../../scripts/build-clusterguard-patch.sh) |
| 回归 | [upgrade-acceptance-evidence.test.cjs](../../../../tools/upgrade-acceptance-evidence.test.cjs) |

## 需要时再读

- [upgrade-validation-chain](../../../zh-CN/upgrade-validation-chain.md)
- [validation-gate-workflow](../../../zh-CN/validation-gate-workflow.md)
- [upgrade-validation-chain-implementation-status](../../../zh-CN/upgrade-validation-chain-implementation-status.md)
