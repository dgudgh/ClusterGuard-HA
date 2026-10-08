# 许可与依赖规则

修改依赖、许可或文档许可陈述前，以及打包或涉及许可的对外交付前，必须读取。普通文档修改不需展开全部场景，但仍须执行根规则要求的许可一致性校验。

**本项目采用 `AGPL-3.0-only`。** 权威全文是仓库根目录的 `LICENSE`（官方原文，**不得改动**，
sha256 `8486a10c4393cee1c25392769ddd3b2d6c242d6ec7928e1414efff7dfb2f07ef`）。

用户 2026-09-22 明确要求"用我的代码就必须也开源"。这是**刻意的永久决定**：

- **不得**替换 `LICENSE`、**不得**把任何位置的许可声明改成别的协议（尤其**不得**再出现
  `Proprietary`），除非用户在当次对话中明确要求换协议，且换协议必须按
  [许可与合规](../../zh-CN/licensing.md) §8 的表格**一次改全所有落点**。
- **不得**在未同步 `THIRD-PARTY-NOTICES.md` 的情况下新增任何第三方依赖。每个新依赖都要按
  现有格式补一行：模块、版本、SPDX 标识、版权人、upstream 地址。**许可是实读模块自带
  `LICENSE` 得出的，不得凭记忆或猜。**
- **依赖许可兼容性**：现有 4 个依赖是 MPL-2.0（`hashicorp/raft`、`raft-boltdb/v2`、
  `golang-lru`、`go-immutable-radix`）。因此 **`GPL-2.0-only` 永远不可选**——MPL-2.0 不允许与
  GPL-2.0-only 组合。引入新依赖前先判断其与 AGPL-3.0 的兼容性，不兼容就不引入。
- **交付物必须带许可**：RPM 必须把 `LICENSE`、`THIRD-PARTY-NOTICES.md`、`MPL-2.0.txt` 装到
  `/usr/share/doc/clusterguard-ha/`；离线介质必须把 `LICENSE` 放在介质根目录。
  `packaging/rpm/nfpm.yaml` 的 `license` 字段、`contents` 段与两个构建脚本不得被拆散。
- **不得**把本仓库的源码或二进制以附加限制条款的方式对外提供（AGPL 第 7 条禁止附加限制）。
  签NDA、卖升级包都属于正常商业行为，许可本身不禁止；但**不得**向客户声称"你不得再分发"。
- **门禁**：任何依赖、打包或文档改动后跑一次 `node tools/verify-license-consistency.cjs`，
  必须 `status=passed`。
  根目录 `.workbuddy/` 是不入库的本机记忆、参考和归档资料，全部排除产品许可声明扫描；
  该例外只适用于这个根目录。业务源码、文档和其中同名的嵌套目录仍须扫描，错误声明必须拒绝。
- 面向客户的各种场景义务表见 [许可与合规](../../zh-CN/licensing.md)；
  第三方逐项清单见 [THIRD-PARTY-NOTICES.md](../../../THIRD-PARTY-NOTICES.md)。
