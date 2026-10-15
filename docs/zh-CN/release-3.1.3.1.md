# ClusterGuard HA 3.1.3.1 完整离线安装介质说明

## 版本与状态

产品版本 **3.1.3.1**，RPM 安装与兼容基线 **2.2-109**。新增生产模板与独立软件路径入口，按功能版本递增规则从 3.1.2 系列进入 3.1.3，修复段从 1 开始。

这是本地完整安装候选，唯一目录为 `release/3.1.3.1/`。源码、文档提交主线 `codex/2.2-postgresql`；构建提交与干净树状态写入随包 `RELEASE-INFO`。**本版尚未发布 GitHub Release，ART/FIELD 仍 OPEN。**

## 本版内容

- MySQL 8.0 安装使用用户批准的生产主从 cnf 原件，保留其他生产参数、注释、顺序和重复项，只按部署需要改变路径、端口、节点身份与已确认的只读启动保护。原件也随包提供。
- 软件默认 `/usr/local/mysql`，数据根默认 `/data`，MySQL 实例为 `/data/mysql/<端口>`。`--database-software-dir` 与 `-nd/--database-data` 可独立自定义；`--mysql-master-cnf`、`--mysql-slave-cnf` 可选现场生产模板。节点身份唯一性在执行前校验。
- 所有节点以只读保护启动；主库完成受控复制配置和 writer lease 授权后，`read_only`、`super_read_only` 才变为 `OFF`，副本保持只读。
- 两个副本仅丢失半同步 ACK 时，仍可保留健康现役主库的 VIP：只允许已有、未过期且同一 Owner 的 writer lease 续租。半同步仍标记降级；控制面多数派、Agent 观测多数派、主库可写证明、唯一 VIP Owner、维护门禁和隔离要求全部保留。失去这些证据仍撤销授权，不授予新主库。
- 累计包含已提交的灾难恢复日志缺失判断、实际日志历史验证、受控重建与错误保留，以及运行参数页面和双语提示修复。恢复中的克隆仍受授权、隔离和只读保护约束。
- 新增[完整离线安装命令参数](offline-installer-parameters.md)：逐项说明全部参数、别名、默认值、必填条件、效果、冲突和安全输入方式；仅覆盖安装命令，不扩展平台参数或 cnf 参数百科。介质 `docs/` 含中英文两份参考。

## 安装介质

- `clusterguard-ha-3.1.3.1-offline-linux-x86_64.tar.gz` 及同名 `.sha256`。
- `clusterguard-ha-2.2-109.x86_64.rpm` 及同名 `.sha256`。
- `RELEASE-INFO`、`SHA256SUMS`、完整介质结构校验 `verification.json` 和安装手册。

完整 tar 包含安装器、五个程序、systemd 单元、安装/配置/升级/恢复脚本、生产模板、静态 Linux jq、Rocky Linux 8 x86_64 基础依赖和仓库元数据、公钥及 AGPL-3.0-only/第三方许可。

**数据库厂商软件包默认不嵌入。** 现场自行准备匹配系统、架构和版本的原厂完整包，通过 `-r FILE` 明确指定，或按安装器规则从 `/opt` 发现；同时准备同名 `.sha256`。PostgreSQL 源码编译需要匹配的独立离线编译依赖，基础运行依赖不替代编译依赖。其他操作系统与数据库组合没有新增现场支持声明。

解包后先检查摘要、帮助与只读计划，安装流程见[离线安装与部署手册](offline-rpm-install.md)。完整 tar 是安装介质，不能作为 `.cgupgrade`/`.cgpatch` 上传控制台。

## 验证边界

SOURCE 结果、完整介质结构及摘要检查分别记录在开发任务与 `verification.json`。真实 Chrome 隔离 API 审计检查页面和交互，不连接现场数据库。

当前 RPM 构建链没有生产 RPM 签名，完整介质校验不替代强制契约的签名 ART 验收；本版不宣称 ART 全部通过。未安装三台服务器，未执行本版真实 systemd、复制、VIP 故障、切换或灾难恢复现场验收。用户自行更新前应在目标系统核对 RPM 信任、介质与生产配置，并安排对应验收。
