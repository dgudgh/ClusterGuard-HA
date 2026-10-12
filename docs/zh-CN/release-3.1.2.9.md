# ClusterGuard HA 3.1.2.9 发布说明

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/release-3.1.2.9.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

## 版本与下载

产品版本 **3.1.2.9**，Linux x86_64；RPM 安装与升级兼容基线 **2.2-107**。产品运行版本、介质目录和 GitHub 标签采用四段版本。RPM 的 Version/Release 保留兼容身份，不代表控制台仍运行旧产品。

本版以 [GitHub v3.1.2.9 预发布](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.2.9) 交付完整安装介质。已交付的 3.1.2.8 及旧版文件保持原字节；本版不覆盖原包或标签。源码在唯一主线 `codex/2.2-postgresql`，确切构建提交记录于随包 `RELEASE-INFO` 与标签。

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.9/clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.9/clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.2.9-offline-linux-x86_64
bash install_clusterguard.sh --help
```

安装前按[离线安装手册](offline-rpm-install.md)生成并核对计划，再执行授权的安装。已有集群按[升级与回退手册](update-and-patch.md)走签名升级流程；完整安装介质不是可直接上传的签名升级包。

## 本版修复

- **安装器不再在运维机上执行远端载荷里的命令替换。** 配置图形化滚动升级 Helper 的远端载荷是一段双引号字符串，其中一行注释含反引号 `` `workspace check-file` ``；双引号内的反引号由**运维机本地**解释，安装时打印 `workspace: 未找到命令`，并使该注释行被静默替换为空。反引号已移除。契约新增一条检查：`scripts/install_clusterguard.sh` 不得出现反引号，并配一条变异守住。
- **刚启动的 `Type=simple` 单元改为有界就绪等待。** `clusterguard-update-helper.service` 是 `Type=simple`，`systemctl enable --now` 返回时 systemd 已宣告 active，但受管进程可能尚未绑定 `/run/clusterguard/update-helper.sock`；紧随其后的 `systemctl is-active --quiet` 与 `test -S` 断言因此在就绪竞态中失败，整步以 `exit=1` 中止，**即使 helper 实际已正常启动**。现改为对两项条件做有界重试，两条硬断言保留在等待之后，确实未就绪的 helper 仍会失败。契约新增一条检查：`enable --now` 之后的就绪断言前必须有重试，并配一条变异守住。

两处修复共同保证**全新安装可一次跑完全部步骤**；此前它们会让安装在“配置软件更新可信链与清单”步骤中止。

## 累计功能

- 完整安装介质和 RPM 的全部 Go 构建目标注入四段产品版本，五个负载命令（`clusterguard`、`cgctl`、`clusterguard-agent`、`clusterguard-k8s-fence-guard`、`clusterguard-update-helper`）都以 `--version` 报告该身份；控制台和版本接口使用同一产品身份。包内 `BUILD-INFO`、`RELEASE-INFO` 记录产品版本及 RPM 基线。
- 完整介质校验器按实际四段介质版本定位运行包，核对产品身份、构建源码、两份控制台嵌入内容与全部摘要。构建入口拒绝覆盖已存在的交付物。
- 完整包累计包含版本/历史投影、重试与续跑分流、升级进度保持、中英文偏好及历史结果本地化等主线修复。
- 控制节点参数下发：页面编辑允许的 25 项整数参数，预检与二次确认后下发，逐节点受控重启并读取实际生效值；集群级参数必须选择全部投票节点。失败保留维护门禁，通过重试或回退恢复。
- 运行参数界面采用节点卡片、默认折叠分组及固定操作栏。可在线下发的参数放在上方，修改值位于末列；身份、网络、凭据、路径和隔离等需专用流程的参数在下方，不显示编辑框。集群策略保留独立保存路径。

“在线下发”指控制台提交受控任务，普通进程启动参数仍通过滚动重启生效；重新读取文件只刷新展示，不进行热重载。详见[参数下发实现与边界](../development/backend/settings/configuration.md)和[页面交互](../development/frontend/settings/configuration.md)。

## 完整介质内容与支持边界

包含控制面、cgctl、Agent、Kubernetes fencing guard、受限 Update Helper、systemd 单元、安装/配置/升级/恢复脚本、静态 Linux jq、Rocky Linux 8 x86_64 基础运行依赖及仓库元数据、配置示例、签名公钥、安装运维手册与 AGPL-3.0-only / 第三方许可。

同一介质嵌入 MySQL **8.0.44** 原厂最小二进制包和 PostgreSQL **16.4** 官方源码包，各有摘要。PostgreSQL 源码的编译依赖采用独立依赖包或构建节点可信软件源，不包含在基础运行仓库内；全隔离环境须事先按[数据库接入手册](database-preparation.md)准备匹配的编译依赖。Oracle / SQL Server 未增加生产支持声明。

公开附件为完整 `.tar.gz`、RPM、对应 SHA-256、`RELEASE-INFO`、`verification.json` 及发布说明；签名升级/热修制品只留本地和签约交付渠道。

## 验证与现场状态

本版验证范围：Go 回归、适用 race/vet、源码契约及变异（含本版新增的反引号与就绪等待两条规则）、许可/Markdown 门禁、完整介质解包/摘要/源码绑定/嵌入页面/可执行版本、私有升级配套件签名与只读检查。每项结果以本次验证记录及公开 `verification.json` 为准；隔离 API 浏览器不等于现场验证。

**未在目标 Linux 执行客户生产现场验收，未进行本版真实数据库、systemctl 逐节点重启、多数派/VIP/恢复与滚动升级的现场验收。FIELD 仍 OPEN，因此为预发布，不宣称生产稳定验收。** 历史验收报告只说明其对应版本和范围。部署前核对目标系统 RPM 信任链、配置与适用门禁。
