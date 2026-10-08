# 热修补丁包版本、ID与交付目录

先读[门禁流程](validation-gate-workflow.md)；涉及构建、签名、上传或恢复时完整读[强制契约v2](upgrade-validation-chain.md)。本页不替代SOURCE/ART/FIELD门禁。

## 1. 新封板版本线

产品封板线从**3.1.1.1**开始。新规则要求四段版本同时绑定文件名、签名清单、运行二进制和包ID。

```json
{"id":"3.1.1.4","patch_version":"3.1.1.4","rpm_version":"2.2","rpm_release":"105"}
```

对应文件：`clusterguard-3.1.1.4.x86_64.cgpatch`。构建器注入`buildinfo.ProductVersion`，CLI与版本API的`product_version`显示运行产品身份。只改文件名或页面文案不构成版本迁移。

## 2. 四段含义与RPM基线

| 段 | 当前起点 | 含义 |
| --- | --- | --- |
| MAJOR | 3 | 生产稳定主版本线 |
| MINOR | 1 | 当前能力阶段 |
| PATCH | 1 | 当前封板功能发布 |
| BUGFIX | 1 | 首个Bug修订，后续递增 |

四段是产品运行与交付身份。RPM来源仍可为2.2-105；API的version/release和签名source描述兼容基线，不用产品显示版本替换来源准入。待升级或历史包不能决定当前运行版本。

旧3.1.1.1/3.1.1.2漏了运行版本绑定，原始字节冻结；3.1.1.3修复运行显示，3.1.1.4修复版本ID与旧失败占摘要。

## 3. 包ID必须等于版本号

新包规格`id`、签名`hotfix_id`、API`patch_id`、页面、确认输入、POST、Runner、维护锁和操作历史全部为**3.1.1.4**。字段名hotfix_id保留兼容，字段值使用产品版本。不得只把确认文案改成版本、却仍请求HF编号。

旧已交付HF包的ID和原日志保持原值，不改名伪造历史。同一版本不同摘要仍是冲突。第一次从旧HF身份迁移到版本身份使用新版本，并通过签名supersedes关联旧ID。

## 4. 重试、修订与不可变性

- 失败恢复使用原文件、原ID和原版本retry，不生成新身份。
- 已签名/交付文件不得覆盖重建。改字节递增BUGFIX并分配新版本ID。
- 签名revision及supersedes_artifact记录原文件与SHA；supersedes记录被替代操作ID。旧字节和失败历史保留。
- 目录移动不改包内字节，不重新签名；台账记录旧/新路径与同SHA，并同步回指。签名中的历史替代路径不改写。

## 5. 上传前核对

1. 签名source与现场实际RPM基线一致；文件名架构等于清单架构。
2. 新包ID=patch_version，版本为四段数字，BUGFIX大于0；签名、SHA侧车、私有台账指向同一包。
3. 实际CLI与API产品版本和清单一致；当前运行取二进制，目标/历史取各包自身已验证patch_version。
4. 历史缺字段只能从同包摘要与身份核对后的签名原件补读，不能猜文件名。
5. 只有验签且已部署后继的supersedes声明才能把旧失败标为已替代；旧失败仍留在历史，不能按时间或版本大小抹掉它。

## 6. 目录与渠道

```text
release/3.1.1.4/
├── clusterguard-3.1.1.4.x86_64.cgpatch
├── clusterguard-3.1.1.4.x86_64.cgpatch.sha256
├── 3.1.1.4.spec.json
├── 3.1.1.4-更新说明.md
└── 3.1.1.4-evidence/
```

新交付目录为release/<四段产品版本>/。旧*-hotfixes仅保留历史；来源RPM不决定新目录。目录门禁扫描两种目录，并按来源兼容线限制最多一个current，防止拆目录造成多个当前入口。

签名包、SHA与私有台账仅本地/签约交付，不能上传GitHub。cgupgrade的整包滚动升级格式独立，不套用热修规则。

## 7. 禁止的做法

禁止latest.cgpatch、hotfix.cgpatch、三段版本、BUGFIX=0、新包版本与ID不一致、同版本换字节、仅按目录排序选择当前包。发现签名/摘要/身份/替代链不一致时停止操作，保留证据。
