# 热修补丁包版本与文件名规则

> 本页规定新签名 `.cgpatch` 的版本字段、文件名和交付目录。先读[门禁执行与更新流程](validation-gate-workflow.md)；涉及构建、签名、上传、重试或回退时，再完整读取[升级与热修强制契约 v2](upgrade-validation-chain.md)。本页不能替代签名、SHA-256、PRE/ART/FIELD 门禁或现场验收。

## 1. 新包的唯一文件名

新热修规格必须填写 `patch_version`，构建器输出下面的固定格式：

```text
clusterguard-MAJOR.CAPABILITY.INTERNAL.BUGFIX.<架构>.cgpatch
```

当前规则的例子：

```json
{
  "id": "HF-2026-1008-01",
  "patch_version": "2.2.105.1",
  "rpm_version": "2.2",
  "rpm_release": "105"
}
```

构建结果为：

```text
clusterguard-2.2.105.1.x86_64.cgpatch
```

`patch_version` 会同时写入签名的 `HOTFIX-MANIFEST.json`。文件名和清单必须表达同一个版本；不能只改文件名而不改清单，也不能把目标版本临时拼到文件名里。

## 2. 四段版本的业务含义

| 段 | 示例 | 含义 | 约束 |
| --- | --- | --- | --- |
| `MAJOR` | `2` | 生产稳定主版本 | 表示生产稳定版本线 |
| `CAPABILITY` | `2.2` | 能力线：从 MySQL 扩展到 PostgreSQL、Oracle、SQL Server | 必须与 `rpm_version` 的 `2.2` 一致 |
| `INTERNAL` | `105` | 该能力线的内部功能发布 | 必须与 `rpm_release` 的 `105` 一致 |
| `BUGFIX` | `1` | 针对 `2.2.105` 的 Bug 修复序号 | 从 `1` 开始，不能写 `0` |

所以 `2.2.105.1` 不是新的 RPM release 写法：

- RPM/现场基线仍然是 `2.2-105`；
- `2.2.105` 表示内部功能发布；
- 最后的 `.1` 表示该内部发布的第一个 Bug 修复包；
- 目标版本、受影响文件、重启单元和操作身份仍以签名清单为准。

## 3. `HF-...` 身份与版本文件名的关系

`HF-YYYY-MMDD-NN` 仍然保留在规格和签名清单的 `hotfix_id` 中，供控制台、审计、重试和维护门禁关联使用，但新格式不再把它放进外层文件名：

| 位置 | 示例 | 用途 |
| --- | --- | --- |
| 外层文件名 | `clusterguard-2.2.105.1.x86_64.cgpatch` | 让操作手直接看懂产品版本和 Bug 修复序号 |
| `HOTFIX-MANIFEST.json` | `hotfix_id=HF-2026-1008-01` | 运行时操作、审计、同包 retry 和门禁归属 |
| `HOTFIX-MANIFEST.json` | `patch_version=2.2.105.1` | 让签名清单与外层文件名一致 |

确认框要求输入的包 ID仍是 `HF-2026-1008-01`；上传前还必须核对文件名、清单和 SHA-256 是同一份制品。

## 4. 重试、修订与不可变性

- 同一包失败且尚未确认安装结果时，使用原文件、原 `hotfix_id`、原 `patch_version` 做 `retry`。重试不生成新文件。
- 已签名文件不能原地重建或改名覆盖。文件实际身份由 `patch_version`、架构、清单中的 `hotfix_id`、`revision` 和 SHA-256 共同追溯。
- 新格式不在文件名中追加 `-r1`。如果已经签名的字节必须更正，使用下一个 Bug 修订版本，例如从 `2.2.105.1` 生成 `2.2.105.2`，并在新清单中记录 `revision` 与 `supersedes_artifact{file,sha256,reason}`。
- `.2` 代表新的可追溯 Bug 修订身份，不代表可以删除或覆盖 `.1`；旧文件和旧摘要继续留作证据。

历史规格没有 `patch_version` 的旧包保留原来的 `clusterguard-ha-hotfix-HF-...` 文件名和台账路径。它们是冻结历史身份，不能为了统一外观而重命名；从新增 `patch_version` 的规格开始使用本页的新格式。

## 5. 文件名、基线和目标的核对

新文件名的前三段必须与现场基线对应，但仍要以清单逐项确认：

```text
文件名：  clusterguard-2.2.105.1.x86_64.cgpatch
清单源：  source.version=2.2, source.release=105
清单版本：patch_version=2.2.105.1
清单目标：target.version=2.2, target.release=105+hf-2026-1008-01
```

上传前按以下顺序检查：

1. `2.2-105` 等于现场实际运行基线；
2. `patch_version` 的前两段等于 `rpm_version`，第三段等于 `rpm_release`，Bug 修订段大于 0；
3. 文件名架构等于清单 `target.rpm_architecture`；
4. `hotfix_id`、`patch_version`、`revision`、`source`、`target` 和受影响文件都来自同一签名清单；
5. `.sha256` 侧车摘要与文件实际字节一致，私有台账把该制品标为当前入口。

不要按目录时间、文件排序或 `latest` 标签选包。文件名清楚不等于签名和现场兼容性已经通过。

## 6. 目录与 `.cgupgrade` 边界

交付目录继续按**源基线**划分，而不是按 Bug 修订段划分：

```text
release/<源版本>-<源发行号>-hotfixes/
├── clusterguard-2.2.105.1.x86_64.cgpatch
├── clusterguard-2.2.105.1.x86_64.cgpatch.sha256
└── <版本>-更新说明.md
```

`2.2-105` 线使用 `release/2.2-105-hotfixes/`。签名 `.cgpatch`、`.sha256` 和私有台账只放本地/签约交付目录，不上传 GitHub。

`.cgupgrade` 仍使用自己的滚动升级文件名：

```text
clusterguard-ha-<源版本>-<源发行号>-to-<目标版本>-<目标发行号>-<架构>.cgupgrade
```

不要把 `.cgupgrade` 的源到目标格式套到热修包，也不要把 `HF-...` 或 `+hf-...` 拼入新热修文件名。

## 7. 禁止的名字

新规格不能使用以下名字：

```text
clusterguard-ha-hotfix-HF-2026-1008-01-2.2-105.x86_64.cgpatch  # 新规格仍用旧格式
clusterguard-2.2-105.1.x86_64.cgpatch                          # 缺少能力线/内部功能段
clusterguard-2.2.105.0.x86_64.cgpatch                          # Bug 修订从 0 开始
clusterguard-2.2.106.1.x86_64.cgpatch                          # 与 rpm_release=105 不一致
latest.cgpatch
hotfix.cgpatch
```

遇到文件名与清单不一致、同一版本出现不同摘要、缺少替代关系或无法确认当前台账入口时，停止上传并保留原始文件和日志，不能靠改名继续执行。
