# 热修补丁包命名与交付目录规则

> 本页只规定已签名 `.cgpatch` 热修补丁的文件名、目录和识别方式。先读[门禁执行与更新流程](validation-gate-workflow.md)；涉及构建、签名、上传、重试或回退时，再完整读取[升级与热修强制契约 v2](upgrade-validation-chain.md)。本页不能替代包签名、SHA-256、PRE/ART/FIELD 门禁或现场验收。

## 1. 唯一文件名模板

构建器 `scripts/build-hotfix-patch.sh` 产生的热修包使用下面的固定模板：

```text
clusterguard-ha-hotfix-HF-YYYY-MMDD-NN[-rREV]-<源版本>-<源发行号>.<架构>.cgpatch
```

字段按顺序解释如下：

| 片段 | 规则 | 例子 |
| --- | --- | --- |
| `clusterguard-ha` | 产品名，固定不变 | `clusterguard-ha` |
| `hotfix` | 制品类型，固定不变；顶层目录也必须是 `clusterguard-hotfix/` | `hotfix` |
| `HF-YYYY-MMDD-NN` | 不可变热修编号：年份、月日、当天序号；`NN` 固定两位 | `HF-2026-1008-01` |
| `-rREV` | 制品修订号；只有修订已交付制品时出现。`REV` 从 `1` 开始，`r0` 不写 | `-r1` |
| `<源版本>-<源发行号>` | 补丁适用的已安装 RPM 基线，不是补丁目标版本 | `2.2-105` |
| `<架构>` | RPM 架构；由构建目标映射得到 | `x86_64`、`aarch64` |
| `.cgpatch` | 热修制品后缀，固定不变 | `.cgpatch` |

因此，当前这份文件应读作：

```text
clusterguard-ha-hotfix-HF-2026-1008-01-2.2-105.x86_64.cgpatch
└────────产品/类型────────┘ └─热修编号─┘ └源基线┘ └架构┘└类型┘
```

它表示“热修编号 `HF-2026-1008-01`，适用于 `2.2-105` 的 `x86_64` 热修包”。它**不**表示目标 RPM 发行号变成了另一个数字；目标值必须以签名清单为准，本例为 `2.2-105+hf-2026-1008-01`，RPM release 仍是 `105`。

## 2. 编号、修订和重试不能混用

### 热修编号 `HF-YYYY-MMDD-NN`

- `HF-2026-1008-01` 是一次现场热修处理的固定编号，写入 `HOTFIX-MANIFEST.json` 的 `hotfix_id`。
- 日期是编号分配/发布日，`NN` 是当天的序号；它不是 RPM release，也不是 Git 提交号。
- 已经上传、签名或交付的编号不能改名后覆盖，也不能用同编号重新构建不同字节。

### 制品修订 `-rREV`

- 首次制品的 `revision` 为 `0`，文件名省略 `-r0`。
- 已签名制品需要更正时，保留原文件，使用 `revision: 1`、`2`……生成新文件名，例如：

  ```text
  clusterguard-ha-hotfix-HF-2026-1008-01-r1-2.2-105.x86_64.cgpatch
  ```

- 修订清单必须带 `supersedes_artifact.file`、原 SHA-256 和更正原因；新制品必须重新验签并通过适用门禁。
- 文件实际身份按“热修编号 + revision + SHA-256”识别。`hotfix_id` 相同不代表字节可以相同或不同地覆盖。

### 同包失败重试

同一个制品失败且尚未确证安装成功时，按原文件、原 `hotfix_id` 和原 `revision` 使用 `retry`。重试不会生成新文件名，也不会递增 `NN` 或 `REV`。只有制品内容需要更正，才创建新的 `-rREV` 制品；不能用新文件名掩盖一次尚未查清的现场结果。

## 3. 文件名中的基线与清单中的目标

文件名只放**源基线**，这样操作手上传前就能判断它是否属于当前现场：

```text
文件名：  ...-2.2-105.x86_64.cgpatch
清单源：  source.version=2.2, source.release=105
清单目标：target.version=2.2, target.release=105+hf-2026-1008-01
```

上传前必须同时核对：

1. 文件名中的 `<源版本>-<源发行号>` 等于现场实际运行基线；
2. `HOTFIX-MANIFEST.json` 的 `hotfix_id`、`revision`、`source`、`target`、`architecture` 与交付说明一致；
3. 同名 `.sha256` 侧车文件指向该文件的实际字节；
4. 当前私有台账/制品目录把该制品标为 `current`，而不是 `frozen`、`superseded` 或历史证据。

不要按文件修改时间、目录排序或“看起来最新”的编号选择包。控制台确认框中的包 ID只能确认 `hotfix_id`；文件名、清单和 SHA-256 还要作为同一制品一起核对。

## 4. 目录和侧车文件

热修制品保存在对应源基线目录：

```text
release/<源版本>-<源发行号>-hotfixes/
├── clusterguard-ha-hotfix-HF-...-<源版本>-<源发行号>.<架构>.cgpatch
├── clusterguard-ha-hotfix-HF-...-<源版本>-<源发行号>.<架构>.cgpatch.sha256
└── <HF编号>-更新说明.md
```

例如 `2.2-105` 线使用 `release/2.2-105-hotfixes/`。同一目录可以保留 revision 0、revision 1 和被取代的历史文件；它们都不能原地覆盖。真正上传的文件由私有台账的当前制品条目指定。

签名 `.cgpatch`、对应 `.sha256` 和现场台账属于签约交付物，只留在本地 `release/` 或约定的交付目录，不放入 GitHub 或其他公开渠道。公开仓库只保留本规则、构建器和不含私有摘要的操作说明。

## 5. 与 `.cgupgrade` 的边界

`.cgpatch` 与 `.cgupgrade` 都可能出现在“版本更新”入口，但命名规则不同：

| 制品 | 文件名识别 | 版本语义 |
| --- | --- | --- |
| 热修 | `clusterguard-ha-hotfix-HF-...-<源基线>.<架构>.cgpatch` | 替换清单声明的文件，不改变 RPM release |
| 滚动升级 | `clusterguard-ha-<源版本>-<源发行号>-to-<目标版本>-<目标发行号>-<架构>.cgupgrade` | 携带目标与回退 RPM，按源到目标滚动升级 |

不要把热修编号拼进 `.cgupgrade` 文件名，也不要把目标 `+hf-...` 写成热修文件名的基线。两种制品都必须以各自签名清单和构建器输出为准。

## 6. 禁止的名字和操作

下面这些名字不能作为正式交付文件名：

```text
hotfix.cgpatch
latest.cgpatch
clusterguard-ha-hotfix-2.2-105+hf-2026-1008-01.x86_64.cgpatch
clusterguard-ha-hotfix-HF-2026-1008-01-r0-2.2-105.x86_64.cgpatch
clusterguard-ha-hotfix-HF-2026-1008-01-2.2-106.x86_64.cgpatch  # 源基线写错
```

也禁止：

- 手工把正式包改名为“最新”或其他易混淆名称后上传；
- 用同一个文件名替换已上传制品；
- 只看文件名、不验签名清单和 SHA-256；
- 现场失败后直接递增编号，绕过同包 `retry` 和原始结果核查。

## 7. 操作手快速核对

拿到一个热修包时按这个顺序判断：

1. 文件名是否匹配本页模板，且源基线与现场一致；
2. `.sha256` 是否与文件同名、摘要是否重新计算一致；
3. 清单中的 `hotfix_id`、`revision`、`source`、`target`、架构是否与文件名和交付说明一致；
4. 私有台账是否把该 `(hotfix_id, revision, SHA-256)` 标为当前制品；
5. 现场失败时先查同一 `operation_id` 和安装结果，再决定对同一文件 `retry` 或走受控回退。

遇到文件名与清单不一致、同 ID 出现不同摘要、缺少修订替代关系或无法确认当前制品时，停止上传并保留原文件与日志，不能靠改名继续执行。
