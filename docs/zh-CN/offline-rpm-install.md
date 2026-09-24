# ClusterGuard HA 离线安装与部署手册

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/offline-rpm-install.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->


适用产品：ClusterGuard HA 2.1-45（MySQL 封板版）及 2.2.x x86_64
适用系统：RHEL、Rocky Linux、AlmaLinux、Oracle Linux 8/9，systemd，x86_64
部署方式：一台运维机通过 SSH 远程安装多个控制节点和数据库节点

版本边界：`2.1-45` 只按 MySQL 正式线交付；PostgreSQL 安装与切换从
`2.2-1` 开始。不要使用 2.1 包执行本手册的 PostgreSQL 章节。2.1 封板信息见
[2.1-45 发布说明](release-2.1.45.md)，不可变规则见
[版本与发版规范](version-release-policy.md)。

本文只描述当前正式交付的多节点安装器 `install_clusterguard.sh`。它是推荐的生产安装方式：统一生成节点身份、证书、Raft 配置、数据库配置、复制拓扑、Agent 和 VIP 收敛配置。

不要把本手册与旧版 Orchestrator、单机 RPM 手工配置文档混用。

`clusterguard-configure` 是安装器在远端调用的节点级配置程序。初次部署不应由运维人员逐台手工调用它；只有平台节点生命周期、受控恢复流程或工程支持明确要求时才使用该程序。

## 1. 交付物与安装方式

正式交付目录是构建产物目录 `release/<bundle-version>/`（下例使用 2.1-45 介质版本）：

```text
release/2.1-45/
  RELEASE-INFO
  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
  clusterguard-ha-2.1-45.x86_64.rpm
  clusterguard-ha-2.1-45.x86_64.rpm.sha256
  SHA256SUMS
  docs/                                   # 随包中文手册
```

解包 `*.tar.gz` 得到的是**介质目录**，它与上面的交付目录同名但内容不同：RPM 与节点运行时
会重新出现在 `packages/` 下。

```text
clusterguard-ha-2.1-45-offline-linux-x86_64/
  install_clusterguard.sh                 # 生产安装入口
  packages/                               # ClusterGuard RPM 与节点运行时 tar.gz
  packages/database/                      # MySQL / PostgreSQL 介质与 README.txt
  dependencies/                           # 离线 RPM 仓库（含 repodata/）
  docs/                                   # 随包手册
  tools/                                  # jq、时钟网格、PostgreSQL 源码编译等工具
  examples/                               # fencing 与 docker-swarm 示例
  RELEASE-INFO
  SHA256SUMS
```

生产部署应使用完整离线包，不是单独手工安装 RPM：

```bash
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.1-45-offline-linux-x86_64
sha256sum -c SHA256SUMS
```

安装前可额外检查 RPM 的签名和内容，不要以解包后的同名文件替换正式 RPM：

```bash
rpm -K packages/clusterguard-ha-2.1-45.x86_64.rpm
rpm -qpl packages/clusterguard-ha-2.1-45.x86_64.rpm
```

离线包包含 ClusterGuard HA、安装器、静态 `jq`、MySQL 与控制面所需的基础运行依赖、示例配置和中文手册。体积较大的 PostgreSQL 源码编译依赖闭包单独交付；主离线包不包含 MySQL、UPSQL、PostgreSQL、Oracle 或 SQL Server 的厂商补丁与许可证。

## 2. 角色与部署拓扑

ClusterGuard HA 有三种节点角色：

| 角色 | 作用 | 数量规则 |
| --- | --- | --- |
| 控制节点 | 运行 API、Web Console、Raft、审计和工作流 | 必须为至少 3 个的奇数 |
| 数据节点 | 运行 MySQL 或 PostgreSQL 及受限 Agent | 不要求奇数 |
| 混合节点 | 同时是控制节点和数据节点 | 控制节点集合仍必须为奇数 |

常用部署模式：

| 模式 | `-l` 控制节点 | `-n` 数据节点 | 使用场景 |
| --- | --- | --- |
| 三节点混合 | 三台相同主机 | 三台相同主机 | 小型生产和测试环境 |
| 控制与数据库分离 | 三台仲裁主机 | 两台或更多数据库主机 | 推荐的生产隔离模式 |
| 仅控制面 | 三台仲裁主机 | 不传 | 先部署控制面，后续从控制台接入数据库 |

示例中的三台主机对应关系：

| IP | 主机名 | 固定节点名 |
| --- | --- | --- |
| `192.168.102.152` | `orch-mysql01` | `cg-node-0001` |
| `192.168.102.153` | `orch-mysql02` | `cg-node-0002` |
| `192.168.102.154` | `orch-mysql03` | `cg-node-0003` |

安装器会记录不可变 UUID。后续修改主机名、IP 或端口时，应从 Console 的元数据修正流程处理，不能删除本地部署状态后重新安装。

## 3. 部署前检查

### 3.1 运维机要求

运维机应能 SSH 到所有目标节点，并具备：

```text
bash, ssh, scp, ssh-keyscan, ssh-keygen, openssl, curl, tar
```

运维机需要保存部署状态、站点秘密和证书工作目录。它们是后续幂等执行、扩容、节点恢复和管理员接管所必需的资料，必须放在受保护且已备份的位置。

### 3.2 目标节点要求

- 使用同一 Linux 发行版主版本和 x86_64 架构。
- 每个节点使用唯一、稳定的主机名。
- 控制节点至少预留 2 GiB 可用空间；数据库数据目录至少预留 10 GiB，生产按容量规划增加。
- 所有节点之间时钟同步。安装器会检查 NTP 状态；生产环境必须先修复未同步状态。
- root 可通过 SSH 登录，或提供具备 sudo 权限的专用运维账户。
- 网络和安全策略允许如下连接。

| 端口 | 方向 | 用途 |
| --- | --- | --- |
| 22/TCP | 运维机到全部节点 | 安装、同步与恢复 |
| 3000/TCP | 管理员和控制节点之间 | Console、API、Leader 转发 |
| 10009/TCP | 控制节点互通 | Raft 共识 |
| 3306/TCP | MySQL 数据节点互通及控制面访问 | MySQL 复制和管理 |
| 5432/TCP | PostgreSQL 数据节点互通及控制面访问 | PostgreSQL 复制和管理 |

安装器会在启用且运行中的 firewalld 内自动配置它管理的端口。外部防火墙、安全组、ACL 和交换网络仍需由运维团队提前放通。安装器会调用受限离线 `dnf install` 或 `yum localinstall` 安装经校验的本地 RPM，运维人员不需要逐台手工执行 RPM 安装。

### 3.3 主机名与 VIP

先在目标节点确认主机名正确，例如：

```bash
hostnamectl set-hostname orch-mysql01
hostnamectl --static
```

VIP 必须是当前二层网络中未被其他设备使用的地址。不要预先手动将 VIP 配置到任一网卡；安装后由 ClusterGuard Agent 依据主库角色、Raft 多数和租约负责唯一绑定。

带 VIP 的 MySQL HA 部署默认启用数据库层自动故障切换，不依赖 VMware 虚拟机名称或 VMX 路径。控制面每秒探测 MySQL 端口和 SQL 健康；连续 4 次得到当前主库失败观测且时间跨度不少于 3 秒后，Raft Leader 才会评估候选主库过渡。旧主 Agent 只保留短期多数派授权；授权失效后会撤销 VIP、记录持久隔离意图并将 MySQL 保持为只读。控制面另行保留 15 秒授权失效隔离宽限，并在提升前再次确认 Raft 多数派、故障证据和精确过渡租约。被阻断的尝试使用独立的 30 秒重试退避。

这些时间控制承担不同职责，不能相加后当作应用 RTO 承诺。生产客户端必须设置有界连接超时和重试，并在现场从写入口测量实际 RTO。

因此安装命令默认不需要 `--fencer`。这不是把“3306 不通”直接当成旧主已断电，而是使用短期多数派授权和节点本地 fail-closed 机制防止失去多数派的节点继续持有写入口。若操作系统、Agent 定时器和管理网络也可能同时失效，生产环境仍建议增加 BMC、PDU、云 API 或虚拟化平台作为第二层外部隔离增强。显式添加 `--manual-failover-only` 可关闭自动恢复，仅保留人工受控切换。

#### 可选：VMware Workstation SSH 外部隔离增强

离线介质的 `examples/fencing/` 提供可选的 VMware Workstation SSH 隔离器。它不参与数据库节点身份、主从拓扑或默认切换判定，只在站点明确传入 `--fencer` 时作为更强的旧主断电证明。它通过独立管理网登录 Windows VMware 宿主机，使用 `vmrun list` 核对固定的 IP 到 VMX 白名单；只有 `fence` 动作会执行 `vmrun stop ... hard`，`status` 动作始终只读。

必须先满足以下条件：

1. VMware 宿主机启用 OpenSSH Server，并从三台控制节点的管理网可达。
2. 使用 SSH 密钥登录。禁止把 Windows 密码写进隔离脚本、环境变量或配置文件。
3. SSH 账号必须能看到并管理目标 VMware Workstation 虚拟机；通常应是启动这些虚拟机的同一 Windows 账号，或企业已配置的专用 VMware 管理账号。
4. `vmware_known_hosts` 必须预先固定宿主机 SSH 公钥；禁止 `StrictHostKeyChecking=no`。
5. 每个数据库实例 IP 必须在 `vmware-targets.tsv` 中恰好映射一个绝对 `.vmx` 路径。

在安全目录准备文件：

```bash
install -d -m 0700 /secure/clusterguard/fencing/site-config
cp examples/fencing/vmware-workstation.conf.example \
  /secure/clusterguard/fencing/site-config/vmware-workstation.conf
cp examples/fencing/vmware-targets.tsv.example \
  /secure/clusterguard/fencing/site-config/vmware-targets.tsv
install -m 0600 /secure/source/vmware_ed25519 \
  /secure/clusterguard/fencing/site-config/vmware_ed25519
ssh-keyscan -H 192.168.102.68 > \
  /secure/clusterguard/fencing/site-config/vmware_known_hosts
chmod 0600 /secure/clusterguard/fencing/site-config/*
```

编辑 `vmware-workstation.conf`，其中运行时路径必须保持为：

```text
identity_file=/etc/clusterguard/fencing/vmware_ed25519
known_hosts_file=/etc/clusterguard/fencing/vmware_known_hosts
targets_file=/etc/clusterguard/fencing/vmware-targets.tsv
```

然后在原安装命令中增加：

```bash
--fencer ./examples/fencing/vmware-workstation-ssh-fencer \
--fencer-assets /secure/clusterguard/fencing/site-config
```

安装器会把隔离器安装到 `/usr/local/libexec/clusterguard-fencer`，把站点文件以 `0640 root:clusterguard` 安装到 `/etc/clusterguard/fencing/`。上线前必须先以 `status` 契约验证每个映射，再在隔离测试环境验证一次真实关机；不能仅以 SSH 可连通作为验收依据。

## 4. 准备数据库介质与离线依赖

正式离线包可以在构建时通过可重复的 `--database-package` 直接嵌入审批的数据库介质。安装器在未传 `-r` 时，优先从当前离线包的 `packages/database/` 查找匹配的 tar 包；只有该目录没有匹配介质时，才到 `/opt` 查找。也可以通过 `--database-package-dir /secure/database-media` 仅扫描指定的企业统一介质目录。

安装器要求介质是可读取的 `tar`、`tar.gz`、`tgz`、`tar.xz`、`tar.bz2` 或 `tbz2`，并且压缩包只有一个安全的顶层目录。它使用 `--engine` 与 `--database-version` 匹配文件名；例如 MySQL 8.0.44 将匹配文件名中同时包含 `mysql` 或 `upsql` 和 `8.0.44` 的包。同一有效目录内找到多个候选时会拒绝执行，要求使用 `-r` 明确指定，绝不会随意选择一个版本。已发布的旧安装器若同时在套件和 `/opt` 找到候选，也需要显式指定 `-r`。

MySQL 8.0 示例：

```bash
sha256sum /opt/mysql-8.0.44-linux-glibc2.17-x86_64.tar.xz
```

MySQL 8.4 仅需替换文件名、`--database-version` 和集群名。UPSQL 使用其企业审批的兼容 MySQL tar 包，并在 `--engine mysql` 下部署。

安装器会在每个 MySQL 数据节点读取物理内存并自动生成该节点的参数。`innodb_buffer_pool_size` 默认取物理内存的 70% 并向上取整到 4 GiB 的整数倍；若取整后超过物理内存的 80%，则取不超过 80% 的最大 4 GiB 倍数，不设置固定容量上限。例如 256 GiB 内存会配置为 180 GiB。`max_connections` 默认固定为 1000。缓存、临时表和 redo 仍按内存分档。单连接缓冲保持受控值，避免 1000 个连接并发时产生不可控的额外内存放大。

MySQL 数据节点至少需要 5 GiB 物理内存；低于此容量时无法同时满足 4 GiB 对齐和 80% 上限，安装器会在初始化数据目录前阻断。

MySQL 默认监听数据库端口的所有 IPv4 地址，ClusterGuard 管理账号可直接通过 TCP 连接。为兼容传统驱动，MySQL 8.0 使用 `default_authentication_plugin=mysql_native_password`；MySQL 8.4 使用其支持的 `mysql_native_password=ON` 并显式设置平台账号插件。默认不会创建或修改任何远程 `root` 账户，远程控制使用独立的发现、执行和复制账号。

现场确实需要远程 root 时，必须显式加入参数。`%` 表示允许任意来源，生产环境更建议限制到管理网段：

```bash
# 明确允许 root 从任意来源登录
--mysql-root-remote-host '%'

# 更收敛的管理网段示例
--mysql-root-remote-host '192.168.102.%'
```

安装计划会明确显示 `MySQL 远程 root : 关闭（默认）` 或 `root@...（显式启用）`。该策略会写入部署状态和控制节点生命周期配置，后续从 Console 添加或重建 MySQL 节点时保持一致。省略参数不会删除现场原有的远程 root，也不会重置其密码或权限。

如果 MySQL 二进制依赖目标操作系统没有提供的库，必须把同发行版、同主版本、同架构、已签名的 RPM 放进 `dependencies/`。可在联网的同版本镜像机上使用离线包内工具收集：

```bash
tools/收集RHEL离线依赖.sh --help
```

不要对联网仓库或未经逐包验签的 RPM 使用 `--nogpgcheck`。只有在已经对 `dependencies/` 逐个执行 `rpm --checksig` 并核对 `SHA256SUMS` 之后，才允许对**指向本地介质目录**的仓库关闭 `gpgcheck`——产品自身的安装器与 PostgreSQL 源码编译器正是这样做的（先逐包验签，再在隔离构建根内关闭校验）。这里的 `--dependencies` 只用于 ClusterGuard 和 MySQL 的基础运行依赖，不承载 PostgreSQL 源码编译工具链，且至少必须包含 `libaio`、`ncurses-compat-libs`、`numactl-libs` 三个 RPM；实际闭包大小取决于收集方式，以介质内 `PACKAGE-MANIFEST.txt` / `COLLECTION-INFO` 为准。

### PostgreSQL 官方源码安装

PostgreSQL 可以直接使用官方 release 源码包，不要求用户预先制作二进制包。例如：

```text
/opt/postgresql-16.4.tar.bz2
```

源码模式不是让三台服务器分别编译。ClusterGuard 会执行以下受控流程：

1. 在连接远端前校验压缩包路径安全、单一顶层目录和官方源码结构。
2. 默认选择第一个数据节点作为构建节点，也可用 `--postgresql-build-node` 指定当前数据节点清单中的另一台。
3. 只在构建节点编译一次，安装前缀固定为 `/opt/clusterguard/postgresql/<PORT>/software`。
4. 构建 `contrib`，校验 `initdb`、`postgres`、`psql`、`pg_basebackup`、`pg_rewind`、`pg_controldata` 和 `pg_config`。
5. 生成 `BUILD-MANIFEST.json`、统一二进制 tar 包和 SHA256 摘要。
6. 把完全相同的制品分发给全部控制节点和数据节点，再初始化及同步流复制。

源码制品会动态链接目标系统库，因此所有控制/数据节点必须使用相同 CPU 架构、发行版版本和 glibc。安装器会在编译前检查，不一致时直接阻断。

构建节点需要至少 6 GiB 临时空间。安装中断后重跑时，只有源码摘要、构建器摘要和制品 SHA256 都一致才会复用缓存；任何一项不一致都会重新编译，避免把旧制品误发到新集群。

主离线包不再内置体积较大的 PostgreSQL 源码编译依赖闭包。明确指定
`--engine postgresql` 且使用官方源码包时，安装器默认在选定的构建节点上使用
该节点已配置的软件源联网解析依赖。所有编译依赖只安装到一次性 DNF installroot，
不会写入生产宿主机的软件包数据库；仓库签名校验保持启用。

安装计划会显示 `PG 编译依赖 : 默认联网安装到隔离构建根`。如果现场仓库、DNS
或网络不可用，安装会停止并明确提示上传独立的 PostgreSQL 依赖包。可在与生产节点
同发行版、同主版本、同架构的联网镜像机上制作该附属包：

```bash
tools/收集RHEL离线依赖.sh \
  --output /tmp/clusterguard-pg-source-deps \
  --postgresql-source-build

tools/构建PostgreSQL依赖包.sh \
  --input /tmp/clusterguard-pg-source-deps \
  --output /secure/release \
  --version 2.2 \
  --release 1 \
  --platform rocky-8 \
  --arch x86_64
```

生成的文件名类似
`clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64.tar.gz`。
上传并校验旁边的 `.sha256` 文件，解压后在安装命令中增加：

```bash
--postgresql-dependencies ./clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64/dependencies
```

安装器会校验附属包的 SHA256、RPM 签名、CPU 架构、操作系统主版本和仓库元数据，
随后只启用这个本地仓库完成隔离构建。缺少签名、摘要、仓库元数据或依赖时会明确
报错并停止。

三节点 PostgreSQL 16.4 计划示例：

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -P 'SSH密码' \
  -r /opt/postgresql-16.4.tar.bz2 \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine postgresql \
  --database-version 16.4 \
  --cluster-name pg16-production \
  --postgresql-allowed-cidr 192.168.102.0/24 \
  --accept-host-keys \
  --plan
```

确认计划中显示“PostgreSQL 官方源码”“仅编译一次”、正确的构建节点，以及“默认联网安装到隔离构建根”后，将最后一行改为 `--execute` 执行。纯离线现场则追加上面的 `--postgresql-dependencies` 参数。

## 5. SSH 主机密钥与密码处理

推荐提前审核目标主机 SSH 指纹并准备 `known_hosts` 文件：

```bash
ssh-keyscan -H 192.168.102.152 192.168.102.153 192.168.102.154 > site/known_hosts
# 必须通过独立可信渠道核对每台主机的 SSH 指纹后，才可使用该文件。
chmod 0600 site/known_hosts
```

首次实验环境可使用 `--accept-host-keys` 让安装器采集当前主机密钥。生产环境优先使用 `--known-hosts site/known_hosts`。

真实安装时，如果没有传 `--ssh-key`、`-P` 或 `CG_SSH_PASSWORD`，安装器会在开始阶段只提示一次隐藏密码，随后自动复用到所有 SSH 和 SCP 操作。最简单的交互方式是直接执行安装命令，不需要额外处理密码。

不要把 SSH 密码写进命令行、Shell 历史或变更单。需要无交互执行时，可在当前 Shell 中输入并导出环境变量：

```bash
read -r -s -p 'SSH 密码: ' CG_SSH_PASSWORD
printf '\n'
export CG_SSH_PASSWORD
```

也可使用 `--ssh-key /secure/keys/clusterguard-deploy_ed25519`。`-P` / `--ssh-password` 仅为兼容已有自动化保留，不建议在共享终端、Shell 历史或进程列表可见的环境中使用。

### 多台服务器密码不同

可直接按节点顺序传入密码。例如 `-l/-n` 去重后顺序为 `152,153,154`，则：

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -p '152的密码,153的密码,154的密码'
```

小写 `-p` 表示“节点密码列表”；大写 `-P` 表示“所有节点使用同一个密码”。密码数量必须与去重后的节点数量完全一致，安装器会在计划阶段显示节点顺序但不会显示密码。密码中包含英文逗号时请使用下方凭据文件方式。

为每个节点创建一个仅 root 可读的凭据文件，例如 `/root/clusterguard-node-ssh.credentials`：

```text
# 节点地址=该节点的 SSH 密码
192.168.102.152=first-node-password
192.168.102.153=second-node-password
192.168.102.154=third-node-password
```

```bash
chmod 600 /root/clusterguard-node-ssh.credentials
```

随后在安装命令中加入：

```bash
--ssh-credentials-file /root/clusterguard-node-ssh.credentials
```

该文件必须包含本次 `-l` 和 `-n` 涉及的每台服务器；安装器会在开始前检查完整性和 `0600` 权限。密码不会显示在部署计划、日志、状态文件或命令行中。节点地址中不能包含空格；密码可以包含 `=`，但不能换行。

## 6. 三节点 MySQL 混合部署

这是最常用的完整安装流程。三台机器既运行 ClusterGuard 控制面，也运行 MySQL 与 Agent。

先创建持久化站点目录：

```bash
install -d -m 0700 /secure/clusterguard/mysql-ha-3306
```

### 6.1 只读计划

以下命令不会修改远端主机：

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --database-port 3306 \
  --cluster-name mysql-ha-3306 \
  --vip 192.168.102.155 \
  --interface ens160 \
  --prefix 24 \
  --dependencies dependencies \
  --known-hosts /secure/clusterguard/mysql-ha-3306/known_hosts \
  --state-file /secure/clusterguard/mysql-ha-3306/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-ha-3306/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-ha-3306/site \
  --plan
```

核对计划中的控制节点、数据节点、数据库版本、端口、网卡、VIP 和数据目录。若有任何错误，修改参数后重新运行只读计划。

### 6.2 真实安装

将同一命令最后的 `--plan` 改为 `--execute`。交互执行时，安装器会要求输入集群名称确认：

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --database-port 3306 \
  --cluster-name mysql-ha-3306 \
  --vip 192.168.102.155 \
  --interface ens160 \
  --prefix 24 \
  --dependencies dependencies \
  --known-hosts /secure/clusterguard/mysql-ha-3306/known_hosts \
  --state-file /secure/clusterguard/mysql-ha-3306/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-ha-3306/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-ha-3306/site \
  --execute
```

非交互自动化可额外增加 `-y`，但只应在已审阅的变更作业中使用。

安装完成后，控制台地址为：

```text
https://192.168.102.152:3000/
```

初始账号为 `admin`。新安装的首次密码由控制面生成到 `/var/lib/clusterguard/bootstrap-admin-password`（权限 0600，与 `metadata.json` 同目录）。交互式安装完成后，安装器从当前 Raft Leader 安全读取并直接显示；非交互标准输出不显示明文，需在生成口令的控制节点上以 root 读取该文件。交互终端显示的口令可能被终端录屏或会话记录保存，应保护安装会话并在首次登录后立即改密。若文件不存在，可能是管理员已经存在或已改密，不得使用旧版固定口令，也不得删除集群状态重新初始化；应使用管理员恢复流程。改密前控制台和 API 仅允许认证与改密，不能查看或操作集群数据。

### 6.3 MySQL 8.4 部署

将以上命令的两项替换为实际值即可。若 `/opt` 同时有多个 MySQL 8.4 包，可显式增加 `-r /opt/EXACT_FILENAME`：

```text
--database-version 8.4.10
--cluster-name mysql-ha-3306-84
```

同一个 IP:端口只属于一个 ClusterGuard 集群。若 8.0 和 8.4 共存，必须使用不同端口、独立数据目录、独立 VIP、独立 `--state-file`、`--secrets-file` 和 `--work-dir`。

## 7. 控制节点与数据节点分离部署

以下示例使用三台仲裁控制节点和三台独立 MySQL 数据节点：

```bash
./install_clusterguard.sh \
  -l 192.168.40.81,192.168.40.82,192.168.40.83 \
  -n 192.168.40.91,192.168.40.92,192.168.40.93 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.4.10 \
  --database-port 3306 \
  --cluster-name mysql-production \
  --vip 192.168.40.100 \
  --interface ens160 \
  --prefix 24 \
  --known-hosts /secure/clusterguard/mysql-production/known_hosts \
  --state-file /secure/clusterguard/mysql-production/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-production/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-production/site \
  --plan
```

先执行 `--plan`，确认无误后改成 `--execute`。控制节点数量必须保持奇数；数据节点数量可为 1、2、3 或更多。

## 8. 仅部署三节点控制面

不部署数据库和 Agent 时：

```bash
./install_clusterguard.sh \
  -l 192.168.40.81,192.168.40.82,192.168.40.83 \
  -u root \
  -ld /var/lib/clusterguard \
  --engine none \
  --control-only \
  --known-hosts /secure/clusterguard/control-plane/known_hosts \
  --state-file /secure/clusterguard/control-plane/deployment-state.json \
  --secrets-file /secure/clusterguard/control-plane/deployment-secrets.env \
  --work-dir /secure/clusterguard/control-plane/site \
  --plan
```

确认后追加 `--execute`。后续通过 Console 的“集群管理”和“节点”页面接入 MySQL、PostgreSQL、Oracle Data Guard 或 SQL Server Always On，不要重新创建控制面状态文件。

## 9. 安装后的验收

### 9.1 服务和 Raft

每台控制节点执行：

```bash
systemctl is-enabled clusterguard-ha.service
systemctl is-active clusterguard-ha.service
systemctl status clusterguard-ha.service --no-pager
```

每台数据节点执行：

```bash
systemctl is-enabled clusterguard-agent.service
systemctl is-active clusterguard-agent.service
systemctl status clusterguard-agent.service --no-pager
```

MySQL 混合节点还应验证：

```bash
systemctl is-enabled clusterguard-mysql-3306.service
systemctl is-active clusterguard-mysql-3306.service
ss -lnt | grep ':3306'
mysql -uroot -p -e 'SELECT @@hostname, @@port, @@read_only, @@super_read_only;'
readlink /tmp/mysql.sock
```

ClusterGuard 管理的服务端 socket 位于 `/run/clusterguard/mysql/3306/mysql.sock`，不放在数据目录中。安装器会创建不含密码的 `/etc/clusterguard/mysql/default-client.cnf`，并从 `/etc/my.cnf` 和 `/root/.my.cnf` 的末尾包含该文件，因此已有配置中的旧 socket 不会继续覆盖当前实例。标准 3306 端口还会通过 `/etc/tmpfiles.d/clusterguard-mysql-3306.conf` 维护 `/tmp/mysql.sock` 兼容链接，重启后自动恢复。受管 root 密码仍只保存在权限为 `0600` 的 `/etc/clusterguard/mysql/3306-client.cnf` 和站点秘密文件中。

控制台中应看到：三名控制节点、一个 Raft Leader、两个 Follower、一个主库、其余副本，以及唯一的 VIP Owner。

### 9.2 VIP 唯一性

在三台数据节点分别执行：

```bash
ip -o -4 addr show dev ens160 | grep '192.168.102.155/24' || true
```

必须只在当前主库节点返回一次。不要手工 `ip addr add` 绑定 VIP，也不要通过 keepalived、Pacemaker 或其他工具同时管理同一 VIP。

### 9.3 重启恢复

计划维护或内核升级后，依次重启一个 Follower、另一个 Follower、最后再重启 Leader。每次恢复后确认控制台有 quorum、数据库复制正常、VIP 仍只在主库。禁止在失去多数控制节点后进行主库切换。

## 10. 必须保存的站点资料

以下文件包含集群身份、证书、控制令牌和数据库受管密码，必须 0600 权限并纳入加密备份：

```text
/secure/clusterguard/<集群名>/deployment-state.json
/secure/clusterguard/<集群名>/deployment-secrets.env
/secure/clusterguard/<集群名>/site/
/secure/clusterguard/<集群名>/known_hosts
```

再次执行同一集群安装器、进行受控修复或扩容时，必须复用同一组文件。不要删除、复制到其他集群，或以空状态文件覆盖已运行集群，否则会破坏资源 UUID、证书和受管凭据的一致性。

## 11. 后续操作边界

安装完成后，以下操作通过 Console 执行：

- 受控主库与 VIP 同步切换。
- 旧主恢复并回挂为从库。
- 添加数据节点，自动安装数据库、同步数据、验证复制并提交元数据。
- 添加控制节点，控制节点总数必须维持奇数。
- 修改主机名、IP、端口、endpoint alias 和其他元数据。
- 计划关机、整机恢复、审计、报告和操作日志查看。

数据库节点故障、磁盘替换或系统重装后，应从 Console 的节点恢复流程发起，不要手工复制数据目录或自行修改 ClusterGuard 元数据库。

## 12. 常见问题

### 12.1 安装器只输出计划

这是正常行为。安装器默认只读，只有追加 `--execute` 才会改动远端主机。

### 12.2 报错“首次执行必须提供 --known-hosts 或显式使用 --accept-host-keys”

生产环境应提供经过指纹审核的 `--known-hosts` 文件。实验环境可追加 `--accept-host-keys`。

### 12.3 报错“状态文件与本次集群名称、引擎或端口不一致”

说明复用了不属于当前集群的 `--state-file`。停止执行，恢复正确的站点目录；不要删除原状态文件规避检查。

### 12.4 数据库包依赖缺失

MySQL 或控制面运行库缺失时，在同发行版、同架构的镜像机收集签名 RPM，并通过 `--dependencies dependencies` 重试。PostgreSQL 源码编译依赖联网解析失败时，制作并上传独立附属包，再通过 `--postgresql-dependencies <EXTRACTED_DIRECTORY>/dependencies` 重试。不要混用两个参数，也不要跳过校验。

### 12.5 Console 无法登录

检查控制面服务、Raft quorum 和浏览器访问的节点地址：

```bash
systemctl status clusterguard-ha.service --no-pager
journalctl -u clusterguard-ha.service -n 200 --no-pager
```

新建集群的初始账号为 `admin`，初始口令来源见 6.2 节；首次登录后必须立即更改密码。若已改过密码，应使用新密码，服务重启不会恢复初始口令，且平台会删除该引导口令文件。

### 12.6 VIP 未绑定或出现多个 Owner

先停止任何外部 VIP 管理工具，再检查控制节点多数、当前主库角色、Agent 服务、网卡名和 CIDR。VIP 只允许 ClusterGuard Agent 管理；没有 quorum 时高风险操作必须被拒绝。

## 13. 卸载与回滚

单独卸载 RPM 不会删除 `/etc/clusterguard/`、`/var/lib/clusterguard/` 和审计资料：

```bash
dnf remove clusterguard-ha
```

不要在仍运行的 Raft 集群中通过删除状态目录回滚。升级、回滚、控制节点退役和灾难恢复均应在 Console 的受控工作流中完成，并先导出审计和报告。

## 14. 安全检查清单

- 控制节点始终为至少 3 个的奇数。
- 所有节点时钟已同步。
- SSH 主机密钥已独立核对。
- VIP 未被外部 HA 工具管理。
- 数据库软件及离线依赖已经过企业审批和 SHA256 校验。
- 站点状态、秘密和证书已加密备份。
- 首次管理员密码已修改。
- 已完成一次受控切换、旧主回挂、节点重启恢复和 VIP 唯一性验证。
