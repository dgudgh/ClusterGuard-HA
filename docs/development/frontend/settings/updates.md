# 设置：升级与热修交互

[前端页面](../README.md) → [设置功能](README.md) → 版本更新

修改本功能前必须读[强制契约](../../../zh-CN/upgrade-validation-chain.md)和[操作授权规则](../../rules/operation-safety.md)。

## 用户入口与请求链

`software-update-panel` → `loadSoftwareUpdates()` → 包列表/当前维护事实 → 选择动作对象 → 上传校验或确认框 → POST → 按 operation_id 查询结果。

实现集中在 [console.html](../../../../internal/api/console.html) 的 `loadSoftwareUpdates`、`softwareUpdateActionable`、动作确认和提交处理。接口及真实合法状态在[后端动作保护](../../backend/settings/update-actions.md)，不能以按钮 disabled 代替后端校验。

## 对象、状态与错误

- 先绑定用户点击项的 patch_id，再生成按钮与确认框；POST 的 ID 必须与二次确认输入一致。
- 成功热修不提供续跑；失败热修重新执行走 retry，不先 plan 覆盖失败；失败滚动升级走 resume。
- 部署状态与最新尝试分别展示；已安装后的失败计划不是卸载。无关较新成功不能按版本或时间自动覆盖旧失败。
- 受理响应不确定时仅查同一 operation_id；相同秒时间戳不能代替操作身份。不因断连、5xx 或超时自动再次提交。
- 用户换上下文、退出或失去授权时，旧确认不得继续执行；后台进度查询和提交授权是不同生命周期。

## 回归与条件跳转

[升级确认验收](../../../../tools/console-update-confirmation-acceptance.cjs)覆盖确认/请求/不确定结果；[热修恢复验收](../../../../tools/console-update-hotfix-recovery-acceptance.cjs)覆盖热修 retry、滚动 resume、跨记录执行/回退对象。两者是隔离 API 上的浏览器回归，不能代表现场包已验收。

后台状态也错误时读[部署与操作历史](../../backend/settings/update-history.md)；门禁无法推进时读[契约与验收门禁](../../backend/settings/update-validation.md)；实际构建或现场执行才进入[交付流程](../../delivery/README.md)。

## 必须保持与回归


- 升级与热修的动作对象必须用真实点击覆盖：成功的热修不提供「续跑」；失败的热修提供「重新执行」而非「续跑」；被拒绝的续跑在成功事件链上留下的失败状态按「本次尝试失败」解释；失败的滚动升级保留「续跑」；以及列表里「更新的成功记录在上、失败记录在下」时，执行与回退都只作用于失败的那一条，且请求里的 `patch_id` 与确认框要求输入的 id 一致。回归入口 `tools/console-update-hotfix-recovery-acceptance.cjs`，逐场景如实报告是否执行、是否通过。
