# 完整离线安装介质：产品版本与公开发布

只在构建/校验/公开发布完整介质时读本页。前置：[门禁流程](../../zh-CN/validation-gate-workflow.md)、完整[强制契约](../../zh-CN/upgrade-validation-chain.md)、[发布清单](../rules/release-checklist.md)及[公开渠道规则](../rules/public-release.md)。

## 身份与构建入口

四段产品版本用于运行展示、介质目录和 GitHub 标签；RPM Version/Release 只用于安装和兼容。例如产品 `3.1.2.8` 对应新 RPM `2.2-106`。不得仅给文件改名却不注入运行产品版本。

- `scripts/build-clusterguard-offline-kit.sh`：校验四段介质身份，向运行包和 RPM 两个入口传递 `--product-version`，在 `RELEASE-INFO` 记录产品身份。
- `scripts/build-clusterguard-bundle.sh`：注入所有运行二进制；四段 `--version` 自动绑定同一产品版本，显式不一致拒绝构建。注入只有被引用才留在制品里：每个负载命令都必须报告该身份（`buildinfo.VersionLine`），否则链接器会丢掉未引用的变量，制品悄悄退回 RPM 基线。契约 `ART-005` 从两个构建脚本推导负载命令集，逐个要求报告身份。
- `scripts/build-clusterguard-rpm.sh`：注入全部五个 Go 目标，在 RPM `BUILD-INFO` 保留产品版本。
- `tools/bundle-version-acceptance.cjs`：实际执行本机二进制版本接口，保留旧基线行为并验证新产品版本及拒绝场景。
- `tools/verify-offline-kit.cjs`：解包介质与 RPM，验证各级摘要、来源、产品身份、运行包、嵌入页面、许可和敏感文件；产品身份按字节检查每个负载二进制；不部署节点。

## 构建示例

从已提交、可远端追溯且无未跟踪文件的干净源码构建；使用主仓库 `release/<产品版本>/` 作为唯一最终位置。新包及 sidecar 已存在时拒绝覆盖，重新构建必须分配新身份。

```bash
bash scripts/build-clusterguard-offline-kit.sh \
  --output /path/to/main-repository/release/3.1.2.8 \
  --version 2.2 --release 106 --bundle-version 3.1.2.8 \
  --product-version 3.1.2.8 --release-channel candidate \
  --goarch amd64 --nfpm-binary /trusted/nfpm \
  --jq-binary /trusted/jq-linux-amd64 \
  --patch-trust-key /trusted/public.pem \
  --database-package /approved/mysql.tar.xz \
  --database-package /approved/postgresql.tar.bz2
node tools/verify-offline-kit.cjs \
  /path/to/main-repository/release/3.1.2.8/clusterguard-ha-3.1.2.8-offline-linux-x86_64.tar.gz \
  /path/to/clean-source /path/to/main-repository/release/3.1.2.8/verification.json \
  2.2 106 2 3.1.2.8
```

PostgreSQL 源码编译依赖需对应发行版的独立依赖包或可信构建源，不能把包含源码误写成已完成数据库安装。

## 发布与验收

完成本次 SOURCE 与实际 ART 证据后，将完整介质、RPM、各自摘要、`RELEASE-INFO`、公开 `verification.json` 及双语发布说明列为显式附件白名单。禁止通配上传整个目录；签名包及私有证据不得公开。

先创建新标签对应的草稿，上传后核对附件类型、大小、摘要及下载回读，再发布。FIELD 未完成时只发布预发布，说明具体未执行范围；不得沿用旧验收报告关闭本版 FIELD。新发布说明按双语页登记 Markdown 台账并重建 HTML；历史发布保留其实际记录。
