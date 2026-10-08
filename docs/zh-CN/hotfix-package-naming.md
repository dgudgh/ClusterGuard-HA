# 热修补丁包版本与文件名规则

> 本页规定新签名 `.cgpatch` 的版本字段、文件名和交付目录。先读[门禁执行与更新流程](validation-gate-workflow.md)；涉及构建、签名、上传、重试或回退时，再完整读取[升级与热修强制契约 v2](upgrade-validation-chain.md)。本页不能替代签名、SHA-256、PRE/ART/FIELD 门禁或现场验收。

## 1. 新封板版本线

旧的 2.x 版本和 `HF-...` 文件名全部视为历史版本，文件字节和摘要保持不变，不为统一外观而改名。新的封板版本从 **`3.1.1.1`** 开始，使用四段版本字段：

```text
clusterguard-MAJOR.MINOR.PATCH.BUGFIX.<架构>.cgpatch
```

新规格示例：

```json
{
  "id": "HF-2026-1008-02",
  "patch_version": "3.1.1.1",
  "rpm_version": "2.2",
  "rpm_release": "105"
}
```

构建结果：

```text
clusterguard-3.1.1.1.x86_64.cgpatch
```

`patch_version` 同时绑定文件名、签名 `HOTFIX-MANIFEST.json` 和实际运行二进制的 `buildinfo.ProductVersion`。平台 API 通过 `product_version` 返回当前程序的四段版本，控制台优先显示它；不能只改文件名或清单。

旧的3.1.1.1、3.1.1.2制品未绑定运行产品版本，原始字节保持冻结；需通过新修订3.1.1.3补齐链路，不能原地重建旧包。

## 2. 四段版本的含义

| 段 | 当前起点 | 含义 |
| --- | --- | --- |
| `MAJOR` | `3` | 新生产稳定主版本线 |
| `MINOR` | `1` | 当前能力阶段 |
| `PATCH` | `1` | 当前封板功能发布 |
| `BUGFIX` | `1` | 该封板发布的第一个 Bug 修订 |

这些四段是**产品运行与交付版本身份**，不要求与热修来源 RPM 的 `rpm_version`、`rpm_release` 逐段相等。包仍然按已安装的基线构建，例如 `2.2-105`，于是签名清单保留 `source.version=2.2`、`source.release=105`；它们描述可应用基线，`patch_version=3.1.1.1` 描述新的封板身份。

Bug 修订段必须从 `1` 开始。已签名字节需要修正时使用新的版本身份，例如 `3.1.1.2`，并在签名清单记录 `revision` 与 `supersedes_artifact{file,sha256,reason}`；不能覆盖 `3.1.1.1`。

## 3. `HF-...` 身份与版本文件名

`HF-YYYY-MMDD-NN` 继续保留在规格和签名清单的 `hotfix_id` 中，供控制台、审计、重试和维护门禁关联，但不再放进新文件名：

| 位置 | 示例 | 用途 |
| --- | --- | --- |
| 外层文件名 | `clusterguard-3.1.1.1.x86_64.cgpatch` | 交付版本身份 |
| `HOTFIX-MANIFEST.json` | `hotfix_id=HF-2026-1008-02` | 运行时操作、审计和同包 retry |
| `HOTFIX-MANIFEST.json` | `patch_version=3.1.1.1` | 绑定文件名与签名 |

确认框要求输入的包 ID 仍是 `HF-2026-1008-02`。上传前必须核对文件名、签名清单和 `.sha256` 是同一份制品。

首次从旧命名迁移到新封板身份时，必须分配新的 `hotfix_id`。例如旧包 `HF-2026-1008-01` 迁移为 `HF-2026-1008-02`，清单通过 `supersedes` 记录关联。控制台按 `hotfix_id` 和摘要识别包；只换版本名仍沿用旧 ID、但摘要不同，会触发包冲突，不能作为交付入口。

## 4. 重试、修订与不可变性

- 同一包失败且尚未确认安装结果时，用原文件、原 `hotfix_id`、原 `patch_version` 做 `retry`；重试不生成新文件。
- 已签名文件不能原地重建、覆盖或仅靠改名伪造新版本。
- 新版本需要更正时递增 Bug 修订段，例如从 `3.1.1.1` 生成 `3.1.1.2`，并保留被替代文件和摘要。
- 历史 2.x/HF 文件名是冻结历史身份，只能作为证据，不能重新发布为新的当前入口。

## 5. 上传前核对

```text
文件名：  clusterguard-3.1.1.1.x86_64.cgpatch
清单源：  source.version=2.2, source.release=105
清单版本：patch_version=3.1.1.1
清单目标：target.version=2.2, target.release=105+hf-2026-1008-02
```

按以下顺序检查：

1. 源基线与现场运行版本一致；
2. `patch_version` 是四段数字且 Bug 修订段大于 `0`；
3. 文件名架构等于清单 `target.rpm_architecture`；
4. `hotfix_id`、`patch_version`、`revision`、`source`、`target` 和受影响文件来自同一签名清单；
5. `.sha256` 与文件实际字节一致，私有台账把该身份标为当前入口；
6. 实际构建的 `clusterguard version`、`--version-json` 中的 `product_version` 与签名版本一致；更新后核对每个节点的运行版本和 API。API 的 `version/release` 仍为 RPM 兼容基线，不能拿四段版本替换升级来源校验。

历史/待升级目标显示该包自身经验签得到的 `patch_version`。旧历史缺字段时，仅从同一包摘要与身份核对后的签名原件补读；没有该字段的旧包保留原目标，不猜文件名、不以任意上传包冒充当前运行版本。

不要按目录时间、文件排序或 `latest` 标签选包。文件名正确不等于签名、兼容性或现场验收已经通过。

## 6. 目录与 `.cgupgrade` 边界

交付目录按**源基线**划分，而不是按四段 Bug 修订划分：

```text
release/<源版本>-<源发行号>-hotfixes/
├── clusterguard-3.1.1.1.x86_64.cgpatch
├── clusterguard-3.1.1.1.x86_64.cgpatch.sha256
└── <版本>-更新说明.md
```

包的来源目录仍是 `release/2.2-105-hotfixes/`，只要它的可应用基线仍为 `2.2-105`。签名 `.cgpatch`、`.sha256` 和私有台账只放本地/签约交付目录，不上传 GitHub。

`.cgupgrade` 仍使用自己的滚动升级文件名：

```text
clusterguard-ha-<源版本>-<源发行号>-to-<目标版本>-<目标发行号>-<架构>.cgupgrade
```

不要把 `.cgupgrade` 的源到目标格式套到热修包，也不要把 `HF-...` 或 `+hf-...` 拼入新热修文件名。

## 7. 禁止的名字

新规格不能使用以下名字：

```text
clusterguard-ha-hotfix-HF-2026-1008-02-2.2-105.x86_64.cgpatch  # 新规格仍按旧 HF 写法命名
clusterguard-3.1.1.x86_64.cgpatch                              # 只写了三段
clusterguard-3.1.1.0.x86_64.cgpatch                            # Bug 修订段从 0 开始
latest.cgpatch
hotfix.cgpatch
```

遇到文件名与清单不一致、同一版本出现不同摘要、缺少替代关系或无法确认当前台账入口时，停止上传并保留原始文件和日志，不能靠改名继续执行。
