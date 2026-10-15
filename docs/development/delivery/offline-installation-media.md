# 完整离线安装介质：产品版本与公开发布

只在构建/校验/公开发布完整介质时读本页。前置：[门禁流程](../../zh-CN/validation-gate-workflow.md)、完整[强制契约](../../zh-CN/upgrade-validation-chain.md)、[发布清单](../rules/release-checklist.md)及[公开渠道规则](../rules/public-release.md)。

## 身份与构建入口

四段产品版本用于运行展示、介质目录和 GitHub 标签；RPM Version/Release 只用于安装和兼容。例如产品 `3.1.3.4` 对应新 RPM `2.2-112`。不得仅给文件改名却不注入运行产品版本。

- `scripts/build-clusterguard-offline-kit.sh`：校验四段介质身份，向运行包和 RPM 两个入口传递 `--product-version`，在 `RELEASE-INFO` 记录产品身份。
- `scripts/build-clusterguard-bundle.sh`：注入所有运行二进制；四段 `--version` 自动绑定同一产品版本，显式不一致拒绝构建。注入只有被引用才留在制品里：每个负载命令都必须报告该身份（`buildinfo.VersionLine`），否则链接器会丢掉未引用的变量，制品悄悄退回 RPM 基线。契约 `ART-005` 从两个构建脚本推导负载命令集，逐个要求报告身份。
- `scripts/build-clusterguard-rpm.sh`：注入全部五个 Go 目标，在 RPM `BUILD-INFO` 保留产品版本。
- `tools/bundle-version-acceptance.cjs`：实际执行本机二进制版本接口，保留旧基线行为并验证新产品版本及拒绝场景。
- `tools/verify-offline-kit.cjs`：解包介质与 RPM，验证各级摘要、来源、产品身份、运行包、嵌入页面、许可和敏感文件；产品身份按字节检查每个负载二进制；不部署节点。
- `scripts/install_clusterguard.sh`：随介质出货的安装器。契约禁止它出现反引号——双引号远端载荷里的反引号由**运维机本地**解释；并要求原本为 `Type=simple` 的服务在 `enable --now` 之后、就绪断言之前做有界重试，避免刚 fork 就去断言 socket 的竞态。

## 数据库介质：默认不嵌入

完整介质**默认不携带数据库安装包**：`packages/database/` 建成空目录，只放该目录自带的 `README.txt`。构建脚本对数据库包**没有任何自动发现**——只有显式传 `--database-package FILE`（可重复）才会嵌入；全脚本唯一的自动发现是 `--dependencies`（内置 Rocky 8 运行依赖）。

数据库介质由现场提供，安装器的查找优先级是：

1. `-r`/`--database-package FILE`——显式指定，命中即用，不再查找其它位置。
2. `--database-package-dir DIR`——只在该目录里找。
3. 两者都不传——按 `${script_dir}/packages/database` → `/opt` 顺序查找；**在介质目录命中候选就停止**，不再看 `/opt`。`/opt` 是「介质里没有所需版本时现场补一个」的兜底位，不是首选。找到多个不同候选时拒绝自动选择，要求用 `-r` 指明。

现场放入的包必须与目标发行版、架构、数据库引擎和版本匹配，并**必须同时放一份同名 `.sha256`**：安装器只在侧车存在时才校验，缺侧车会**静默跳过**校验。嵌入数据库包的一方即成为该数据库二进制再分发的责任方（MySQL Community 为 GPL-2.0）——义务归属见根目录 `THIRD-PARTY-NOTICES.md`；由客户自行提供包的，该义务由客户承担。

`packages/database/README.txt` 是给现场的说明，内容随是否嵌入而变化：它始终说明可以「现场放入已批准的软件包」，嵌入时额外追加本介质已嵌入的文件名清单。

## 构建示例

从已提交、可远端追溯且无未跟踪文件的干净源码构建；使用主仓库 `release/<产品版本>/` 作为唯一最终位置。新包及 sidecar 已存在时拒绝覆盖，重新构建必须分配新身份。

```bash
bash scripts/build-clusterguard-offline-kit.sh \
  --output /path/to/main-repository/release/3.1.3.4 \
  --version 2.2 --release 112 --bundle-version 3.1.3.4 \
  --product-version 3.1.3.4 --release-channel candidate \
  --goarch amd64 --nfpm-binary /trusted/nfpm \
  --jq-binary /trusted/jq-linux-amd64 \
  --patch-trust-key /trusted/public.pem
node tools/verify-offline-kit.cjs \
  /path/to/main-repository/release/3.1.3.4/clusterguard-ha-3.1.3.4-offline-linux-x86_64.tar.gz \
  /path/to/clean-source /path/to/main-repository/release/3.1.3.4/verification.json \
  2.2 112 0 3.1.3.4
```

`verify-offline-kit.cjs` 的第六个参数是期望的数据库包数量，默认口径为 `0`；该数字必须与 `RELEASE-INFO` 的 `database_package_count` 一致。确需把已批准的数据库包封进介质时，加 `--database-package FILE`（可重复）并把该参数改成实际数量——这会改变介质内容，因而必须分配新的产品版本与 RPM 基线。

PostgreSQL 源码编译依赖需对应发行版的独立依赖包或可信构建源，不能把包含源码误写成已完成数据库安装。

## 随包参数说明

安装器的完整命令参考分别映射为介质 `docs/offline-installer-parameters.md`（中文）和 `docs/offline-installer-parameters.en.md`（英文），由完整介质校验器与源码逐字节比较。上层安装手册只保留导航；参数详情见对应文件。源码门禁从当前 `parse_args` 提取选项拼写，拒绝两种语言漏项。

## 发布与验收

完成本次 SOURCE 与实际 ART 证据后，将完整介质、RPM、各自摘要、`RELEASE-INFO`、公开 `verification.json` 及双语发布说明列为显式附件白名单。禁止通配上传整个目录；签名包及私有证据不得公开。

先创建新标签对应的草稿，上传后核对附件类型、大小、摘要及下载回读，再发布。FIELD 未完成时只发布预发布，说明具体未执行范围；不得沿用旧验收报告关闭本版 FIELD。新发布说明按双语页登记 Markdown 台账并重建 HTML；历史发布保留其实际记录。
