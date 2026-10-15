# ClusterGuard HA 完整离线安装器参数说明

适用目标：ClusterGuard HA `3.1.3.1`，RPM `2.2-109`，当前仓库 `scripts/install_clusterguard.sh`。本文只解释当前源码行为；制品是否已构建、上传或现场验收，须以对应阶段证据为准。

关联手册：《ClusterGuard HA 完整离线安装与部署手册》。实际参数入口为离线介质根目录下的 `install_clusterguard.sh`。

## 1. 先理解执行边界

- 安装器默认是 `--plan`：解析参数、选择并检查介质、校验拓扑/目录/凭据映射、打印计划，不连接或修改远端节点。
- 只有显式加入 `--execute` 才进入真实安装。非交互执行还必须加入 `-y/--yes`；交互执行则要求输入集群名称确认。
- 本文示例均为参数片段或只读计划，不给出可直接复制到现场的一键执行命令。示例地址使用文档网段 `192.0.2.0/24`。
- 计划成功仅说明本地输入校验通过，不等于 RPM、数据库、Raft、复制、VIP 或现场高可用验收通过。
- 控制节点运行 API、控制台和 Raft。控制节点必须至少 3 个且为奇数；数据节点没有奇数要求。二者可以分离或重叠。

## 2. 快速索引

以下按功能列出当前解析器接受的全部选项。同一行中的短选项和长选项完全等价。

| 分类 | 参数 |
| --- | --- |
| 节点与介质 | `-l`、`-n`、`-g`、`-r`、`--database-package-dir`、`--engine`、`--data-on-arbitrators`、`--control-only` |
| SSH 与主机身份 | `-u`、`-P`、`-p`、`--ssh-credentials-file`、`--ssh-key`、`--ssh-port`、`--known-hosts`、`--accept-host-keys` |
| 路径与状态 | `-ld`、`-nd`、`--database-software-dir`、`--state-file`、`--secrets-file`、`--work-dir`、`--remote-stage` |
| 数据库 | `--database-version`、`--database-port`、MySQL 和 PostgreSQL 专用选项 |
| HA、安全与网络 | `--vip`、`--interface`、`--prefix`、`--fencer`、`--fencer-assets`、`--manual-failover-only`、`--no-vip-reconcile`、`--patch-trust-key` |
| 控制面与容量 | `--cluster-name`、`--timezone`、`--api-port`、`--raft-port`、两个最小空间参数 |
| 模式 | `--plan`、`--execute`、`-y`、`-h` |

“执行使用”列的含义：**计划+执行**表示两种模式都会解析和校验，真实远端效果只在 `--execute` 发生；**仅执行阶段**表示计划中保存输入，但只在执行阶段使用；**流程控制**表示直接决定是否执行。

## 3. 节点、角色与安装介质

| 参数及别名 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `-l LIST`、`--arbitrator-nodes LIST`、`--controller-nodes LIST` | 必填 | 逗号分隔的控制节点；去空白、去重。必须至少 3 个且为奇数。它们形成 Raft voter 集合，不能用数据节点数量替代多数派要求。 | `-l 192.0.2.11,192.0.2.12,192.0.2.13` | 计划+执行 |
| `-n LIST`、`--data-nodes LIST` | 非 control-only 数据库部署必填 | 数据库和 Agent 节点，去重后可为任意数量；少于 2 个时不能启用带 VIP 的自动故障切换。 | `-n 192.0.2.21,192.0.2.22` | 计划+执行 |
| `--data-on-arbitrators` | 默认关闭 | 把所有控制节点追加进数据节点集合；重复地址不重复添加，节点角色变为 mixed。 | `--data-on-arbitrators` | 计划+执行 |
| `-g FILE`、`--application-rpm FILE` | 可省略 | ClusterGuard RPM。省略时只接受介质 `packages/` 下唯一的 `clusterguard-ha-*.rpm`；0 个或多个都拒绝。存在 `.sha256` 时必须匹配。 | `-g ./packages/clusterguard-ha-2.2-109.x86_64.rpm` | 计划+执行 |
| `-r FILE`、`--database-package FILE`、`--mysql-package FILE`、`--postgresql-source-package FILE` | 数据库部署需要，可由自动发现满足 | 显式选择 MySQL/UPSQL 二进制 tar，或 PostgreSQL 二进制/官方源码 tar。别名只改变拼写，不强制引擎；介质须单一安全顶层目录并包含对应必需程序/源码文件。 | `-r /secure/media/mysql-8.0.44-linux-x86_64.tar.xz` | 计划+执行 |
| `--database-package-dir DIR` | 默认空；也可由 `CG_DATABASE_PACKAGE_DIR` 给出 | 仅在未给 `-r` 时使用，并把搜索范围限制到该目录。未设置时先查介质 `packages/database/`；只要这里有匹配便不再查 `/opt`，否则才查 `/opt`。多个匹配项拒绝猜选。 | `--database-package-dir /secure/database-media` | 计划+执行 |
| `--engine mysql\|postgresql\|none` | 有数据节点时默认 `mysql`；无数据节点或 control-only 时为 `none` | 选择数据库路径。`none` 禁用数据库和 VIP 收敛。MySQL/PG 都需要数据节点。 | `--engine postgresql` | 计划+执行 |
| `--control-only`、`--skip-mysql` | 默认关闭 | 只部署控制面；不能同时给数据节点或数据库介质，engine 只能空或 `none`。`--skip-mysql` 是旧兼容名，实际也跳过 PostgreSQL。 | `--control-only` | 计划+执行 |

## 4. SSH、密码与主机密钥

密码优先级按所选模式确定。逐节点列表排斥统一密码、逐节点文件和私钥；逐节点文件排斥逐节点列表和私钥。源码允许统一密码与逐节点文件同时出现，此时逐节点文件覆盖实际节点取值；也允许统一密码与私钥同时出现，此时进入密码传输模式并仍把私钥传给 SSH。不要依赖这种混合输入，生产命令应只选择一种明确认证方式。密码不写入部署状态，也不在计划中显示。命令行参数可能进入 shell 历史和进程列表，生产环境优先使用私钥、`0600` 凭据文件或交互隐藏输入。

| 参数及别名 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `-u USER`、`--ssh-user USER` | `root` | 远端 SSH 用户；只接受常规 Linux 用户名。当前安装器不会调用 `sudo`，真实安装中的软件安装、系统目录写入和 systemd 操作要求远端会话为 UID 0，因此应使用 `-u root`；普通非 root 用户即使配置 NOPASSWD sudo 也不受当前入口支持。 | `-u root` | 计划+执行 |
| `-P PASS`、`--ssh-password PASS` | 默认取 `CG_SSH_PASSWORD`；仍空时执行前隐藏输入 | 所有节点共用密码。逐节点文件同时存在时实际按文件取值；与私钥同时存在时仍进入密码传输模式。不要把真实密码写进手册、任务记录或公开命令。 | `--ssh-password '<从安全输入获得>'` | 仅执行阶段 |
| `-p LIST`、`--ssh-passwords LIST` | 默认空 | 按**去重后的 `all_nodes` 顺序**映射密码：先控制节点原顺序，再追加尚未出现的数据节点。数量必须完全相等且不得为空；不能与 `-P`、凭据文件或私钥并用。密码含逗号时不能用此方式。 | `-p '<node1-pass>,<node2-pass>,<node3-pass>'` | 计划校验映射，执行使用 |
| `--ssh-credentials-file FILE` | 默认空 | 每行 `节点地址=密码`，空行和 `#` 注释忽略。文件权限必须恰为 `0600`，必须覆盖所有去重节点且不能重复 key；值可包含 `=`。不能与逐节点列表或私钥并用。 | `--ssh-credentials-file /secure/site/ssh-passwords.env` | 计划校验，执行使用 |
| `--ssh-key FILE` | 默认空 | SSH 私钥路径。传入的是私钥，仅用于 SSH；它与补丁签名公钥用途不同。与逐节点密码列表/文件互斥。 | `--ssh-key /secure/site/installer_ed25519` | 计划+执行 |
| `--ssh-port NUMBER` | `22` | 安装 SSH 端口，范围 1..65535。当前有数据库节点时 Agent 清单固定使用 22，因此数据库部署拒绝非 22；control-only 可改。 | `--ssh-port 22` | 计划+执行 |
| `--known-hosts FILE` | 默认空 | 使用已审核的 known_hosts，不复制成另一份；文件必须存在。所有 SSH/SCP 都保持严格主机密钥校验。 | `--known-hosts /secure/site/known_hosts` | 仅执行阶段 |
| `--accept-host-keys` | 默认关闭 | 首次执行时通过 `ssh-keyscan` 采集到工作目录并显示指纹。它表示显式接受当时看到的主机密钥，不关闭后续严格校验；已有工作目录文件会复用。 | `--accept-host-keys` | 仅执行阶段 |

逐节点文件格式示意（仅占位）：

```text
192.0.2.11=<password-for-11>
192.0.2.12=<password-for-12>
192.0.2.13=<password-for-13>
```

## 5. 数据、软件、状态与工作目录

| 参数及别名 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `-ld DIR`、`--controller-data DIR` | `/var/lib/clusterguard` | 控制面 metadata、Raft 和首次管理员口令根目录。必须是安全绝对路径且不能为 `/`。它不是数据库数据根。 | `-ld /data/clusterguard` | 计划+执行 |
| `-nd DIR`、`--database-data DIR`、`--mysql-data DIR` | `/data` | 数据库数据根。MySQL 实例为 `<DIR>/mysql/<port>`，PG 为 `<DIR>/postgresql/<port>/data`。`--mysql-data` 也会影响 PostgreSQL，是兼容名。 | `-nd /data` | 计划+执行 |
| `--database-software-dir DIR`、`--mysql-software-dir DIR` | MySQL 默认 `/usr/local/mysql` | MySQL **完整软件目录**，独立于 `-nd`。须为安全绝对路径，不能包含/重叠实例状态目录；仅 MySQL 接受。它不是 tar 包路径。 | `--database-software-dir /apps/mysql8` | 计划+执行 |
| `--state-file FILE` | `$PWD/clusterguard-deployment-state.json` | 保存固定 cluster/node/instance UUID、phase 和身份绑定。恢复或重跑必须复用原文件；现有文件与集群名、引擎、端口或远程 root 策略不一致时拒绝。首次执行创建为 `0600`。 | `--state-file /secure/site/deployment-state.json` | 计划显示，执行读写 |
| `--secrets-file FILE` | `$PWD/clusterguard-deployment-secrets.env` | 站点秘密和受管数据库凭据。首次执行生成并设为 `0600`；重跑复用并要求 13 个键齐全。丢失后不能靠新文件安全“续跑”既有身份。 | `--secrets-file /secure/site/deployment-secrets.env` | 执行读写 |
| `--work-dir DIR` | `$PWD/clusterguard-site` | 本地证书源、SSH key、节点配置、临时 API 文件和 known_hosts 工作区，执行时 `umask 077`。恢复重跑应与 state/secrets 一起保留和备份。 | `--work-dir /secure/site/work` | 执行读写 |
| `--remote-stage DIR` | `/var/lib/clusterguard/bootstrap` | 每个远端节点的安装暂存目录；必须绝对路径且不能含换行。 | `--remote-stage /var/lib/clusterguard/bootstrap` | 仅执行阶段 |

`state-file` 负责身份和阶段，`secrets-file` 负责秘密，`work-dir` 负责证书与生成配置。三者不是可互换的缓存；重跑时更换任一项都可能造成身份、证书或凭据不一致。

## 6. 数据库通用参数

| 参数及别名 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `--database-version VER`、`--postgresql-version VER` | 初始为空；PG 选定介质后才补默认 `16` | 显式值参与数据库介质文件名匹配，并传给 PG 构建/安装；PG 只接受点分数字。省略时，自动发现阶段不会用 `16` 过滤，介质选定后才把 PG 版本补为 `16`。目录内有多个版本时必须显式指定；生产命令建议始终明确版本。 | `--database-version 16.4` | 计划+执行 |
| `--database-port N`、`--mysql-port N`、`--postgresql-port N` | MySQL `3306`；PG `5432`；none `0` | 数据库端口 1..65535。三个拼写完全等价，不会因别名自动切换引擎。端口参与路径、服务名、身份和 MySQL `server_id`。 | `--database-port 3306` | 计划+执行 |
| `--dependencies DIR` | 若介质 `dependencies/` 有 RPM 则自动使用 | 基础运行依赖的本地 RPM 仓库。PG 源码构建只有当其 `COLLECTION-INFO` 明示 `postgresql_source_build=true` 时才兼容借用；通常应与 PG 构建依赖分开。 | `--dependencies ./dependencies` | 计划+执行 |
| `--controller-min-free-gb N` | `2` | 每个控制节点对 `-ld` 所在文件系统的最小可用 GiB；必须正整数。 | `--controller-min-free-gb 4` | 计划+执行预检 |
| `--database-min-free-gb N` | `10` | 每个数据节点对 `-nd` 所在文件系统的最小可用 GiB；必须正整数。 | `--database-min-free-gb 100` | 计划+执行预检 |

所有数据库 tar 都要求可读取、无绝对路径/`..` 越界项且只有一个顶层目录。MySQL 包必须含 `mysqld`、`mysql`、`mysqldump`；PG 二进制包必须含 `initdb`、`postgres`、`psql`、`pg_basebackup`、`pg_rewind`、`pg_controldata`，否则按官方源码结构继续识别。存在同名 `.sha256` 时会校验；没有 sidecar 不代表已验证供应方签名。

## 7. MySQL 专用参数和配置保真

| 参数 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `--mysql-master-cnf FILE` | 默认介质 `templates/mysql/my-master.cnf`，开发树回退 `configs/mysql/` | 第一台数据节点的生产模板。必须是非符号链接普通文件。自定义模板只替换模板来源；安装器仍只改路径、端口、`server_id` 和只读启动保护，保留其余参数、注释、顺序及重复项。 | `--mysql-master-cnf /secure/config/my-master.cnf` | 计划+执行 |
| `--mysql-slave-cnf FILE` | 默认介质 `templates/mysql/my-slave.cnf`，开发树回退 `configs/mysql/` | 第二台及后续数据节点模板；规则同上。参数名是历史叫法，运行角色仍由受控拓扑和 writer lease 决定。 | `--mysql-slave-cnf /secure/config/my-slave.cnf` | 计划+执行 |
| `--mysql-root-password PASS` | 默认取 `CG_NODE_MYSQL_ROOT_PASSWORD`；仍空则首次执行随机生成 | 本机 MySQL root 密码，8..128 字符，仅允许字母、数字和 `@%+=:,._-`。命令行会泄露历史/进程信息，优先环境安全注入或让安装器生成。 | `--mysql-root-password '<generated-secret>'` | 计划校验，执行写入秘密文件 |
| `--mysql-root-remote-host HOST` | 默认取 `CG_NODE_MYSQL_ROOT_REMOTE_HOST`；空表示不创建/修改远程 root | 显式创建远程 `root@HOST`；只适用于 MySQL。`%` 范围最大，应由现场安全策略决定。值绑定进状态文件，重跑必须一致。 | `--mysql-root-remote-host '192.0.2.%'` | 计划+执行 |

MySQL 软件默认 `/usr/local/mysql`，实例默认 `/data/mysql/<port>`，二者独立。自定义主从模板只改变模板来源；安装器保留其他生产参数，并按部署路径、端口、节点身份和只读启动保护生成实例配置。所有节点以 `read_only=ON`、`super_read_only=ON` 安全启动；主库完成受控复制配置和角色授权后才切换为 `OFF`，从库继续保持只读。生成的节点身份重复会在执行前拒绝。

## 8. PostgreSQL 专用参数和独立构建依赖

| 参数 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `--postgresql-password PASS` | 默认取 `CG_POSTGRESQL_ADMIN_PASSWORD`；仍空则首次执行随机生成 | `postgres` 管理密码，字符规则与 MySQL root 相同。 | `--postgresql-password '<generated-secret>'` | 计划校验，执行使用 |
| `--postgresql-allowed-cidr CIDR` | 同一 IPv4 `/24` 数据网时自动推导；DNS 或跨网段时必填 | PG 管理与复制允许网段，只接受 IPv4 CIDR。 | `--postgresql-allowed-cidr 192.0.2.0/24` | 计划+执行 |
| `--postgresql-build-node HOST` | PG 官方源码包时默认第一台数据节点 | 只在该数据节点编译一次，再按 SHA256 分发统一二进制；必须属于数据节点。对 PG 二进制包或其他引擎非法。 | `--postgresql-build-node 192.0.2.21` | 计划+执行 |
| `--postgresql-build-jobs N` | 默认取 `CG_POSTGRESQL_BUILD_JOBS`；空时构建 Helper 自动检测，最多 16 | 显式值允许 1..128，只适用于 PG 官方源码包。显式上限与自动上限不同。 | `--postgresql-build-jobs 8` | 计划+执行 |
| `--postgresql-dependencies DIR` | 默认空；源码构建为空时使用构建节点联网源 | **单独的 PG 源码编译依赖仓库**，执行时必须含 `repodata/repomd.xml`。不要把它等同于基础运行 `--dependencies`。 | `--postgresql-dependencies /secure/pg-build-dependencies` | 计划显示，执行打包传输 |

源码构建节点还要求至少 6 GiB 临时空间。`--postgresql-dependencies` 对 PG 二进制包不会触发源码构建；`--postgresql-build-node/jobs` 在非源码包时直接拒绝。

## 9. 控制面、网络、时区与 HA 安全关系

| 参数 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `--cluster-name NAME` | `clusterguard-control-plane`，或 `<engine>-ha-<port>` | 3..63 个允许字符，以字母/数字开头。它参与 state 绑定和真实执行确认。 | `--cluster-name mysql-ha-3306` | 计划+执行 |
| `--vip ADDRESS` | 默认空 | 可选 IPv4 写入口。MySQL/PG + VIP 且未设 manual-only 时，源码会同时启用数据库自动故障切换和 Agent 多数派隔离；至少 2 个数据节点。VIP 本身不是安全授权。 | `--vip 192.0.2.100` | 计划+执行 |
| `--interface NAME` | `ens160` | VIP 网卡名，允许字母数字及 `_.:-`。 | `--interface ens160` | 计划+执行 |
| `--prefix N` | `24` | VIP CIDR 前缀，1..32。未配置 VIP 时仍会校验但不绑定地址。 | `--prefix 24` | 计划+执行 |
| `--no-vip-reconcile` | 默认不设置，即安装后启用收敛 | 关闭安装结束时的 Agent reconcile timer 启用和初始 VIP lease 等待。它**不关闭数据库自动故障切换配置**；如需禁止自动切换，使用 `--manual-failover-only`。 | `--no-vip-reconcile` | 执行阶段 |
| `--manual-failover-only` | 默认关闭 | 对带 VIP 的 MySQL/PG 明确关闭自动故障切换，仅保留人工受控切换；同时不能提供 fencer 或 fencer assets。无 VIP 时不改变其他默认。 | `--manual-failover-only` | 计划+执行 |
| `--fencer FILE` | 默认空 | 可选外部隔离增强程序，须是本地可执行、非符号链接普通文件；只允许带 VIP 的 MySQL/PG，安装到 `/usr/local/libexec/clusterguard-fencer`。它增强隔离，不替代 Raft 多数派、Agent quorum fencing、writer lease 或维护门禁。 | `--fencer /secure/fencing/site-fencer` | 计划+执行 |
| `--fencer-assets DIR` | 默认空；使用时 `--fencer` 必填 | 站点配置目录；不能是符号链接，不能含符号链接或子目录，顶层普通文件名受限，安装到 `/etc/clusterguard/fencing/`。不要放未受保护的私钥样例。 | `--fencer-assets /secure/fencing/site-config` | 计划+执行 |
| `--api-port N` | `3000` | 控制台/API 端口，1..65535；用于控制节点访问及 Leader 定向。 | `--api-port 3000` | 计划+执行 |
| `--raft-port N` | `10009` | 控制节点 Raft 端口，1..65535。改变端口不改变至少 3 个奇数 voter 和多数派要求。 | `--raft-port 10009` | 计划+执行 |
| `--timezone ZONE` | 空时先读执行机 `timedatectl`，再尝试 `/etc/localtime` 对应区域 | 找到有效 zoneinfo 时固定所有节点显示时区。显式值无效或默认值无法识别时，源码只告警并不下发时区，各节点保留自身显示时区。它不代替 NTP/时钟偏差检查。 | `--timezone Asia/Shanghai` | 计划+执行 |
| `--patch-trust-key FILE` | 默认取 `CG_PATCH_TRUST_KEY`；否则自动查介质公钥；仍空则更新入口只读不可用 | 必须是 OpenSSL 可解析的**公钥**普通文件，不是签名私钥。执行时部署可信链并启用受控更新 Helper；不会绕过签名、计划、维护锁或滚动门禁。 | `--patch-trust-key ./trust/patch-signing-public.pem` | 计划+执行 |

安全关系固定为：Raft 多数派确认控制面授权；Agent quorum fencing 使失去多数派授权的旧节点本地撤销 VIP 并保持只读；writer lease 控制写角色；可选外部 fencer 提供额外现场隔离证明。`--no-vip-reconcile` 仅控制 VIP 定时收敛激活，不能当作关闭自动故障切换的替代参数。

## 10. 执行模式与帮助

| 参数及别名 | 默认/必填条件 | 含义、校验、影响与限制 | 安全示例 | 执行使用 |
| --- | --- | --- | --- | --- |
| `--plan` | 默认 | 把 `execute` 设为 false；若与 `--execute` 同时出现，最后出现者生效。仍会读取本地 tar 列表、摘要、cnf 和凭据映射。 | `./install_clusterguard.sh ... --plan` | 流程控制 |
| `--execute` | 必须显式提供才真实安装 | 完成本地验证并打印计划后，才检查执行工具、确认、连接节点和变更远端。 | 先审查同参数 `--plan`，再由授权人员单独追加 | 流程控制 |
| `-y`、`--yes` | 默认关闭 | 跳过输入集群名称的交互确认；非交互 `--execute` 必填。它不跳过任何安全/介质/拓扑校验。 | `--execute --yes`（仅经授权的自动化） | 流程控制 |
| `-h`、`--help` | 可选 | 打印内置帮助并以 0 退出。内置帮助未列出全部兼容别名，完整集合以本文和解析器为准。 | `./install_clusterguard.sh --help` | 不执行安装 |

## 11. 公开环境变量

命令行同类参数通常覆盖初始化时读取的环境值。秘密不得写入公开日志。下表中的等待/重试变量是当前脚本接受的运维调节面，但不会放宽健康条件。

| 环境变量 | 默认 | 作用与限制 | 对应 CLI/阶段 |
| --- | --- | --- | --- |
| `CG_DATABASE_PACKAGE_DIR` | 空 | 数据库自动搜索的唯一目录。 | `--database-package-dir` 可覆盖 |
| `CG_POSTGRESQL_BUILD_JOBS` | 空 | PG 源码并行任务数，显式值 1..128。 | `--postgresql-build-jobs` 可覆盖 |
| `CG_SSH_PASSWORD` | 空 | 全节点统一 SSH 密码；真实执行无其他认证时才需要。 | `-P` 可覆盖 |
| `CG_NODE_MYSQL_ROOT_PASSWORD` | 空 | 首次 secrets 创建时的 MySQL root 初值；重跑以 secrets 文件为准。 | `--mysql-root-password` 可覆盖初值 |
| `CG_NODE_MYSQL_ROOT_REMOTE_HOST` | 空 | 显式远程 root Host 策略。 | `--mysql-root-remote-host` 可覆盖 |
| `CG_POSTGRESQL_ADMIN_PASSWORD` | 空 | 首次 secrets 创建时的 PG 管理密码初值；重跑以 secrets 为准。 | `--postgresql-password` 可覆盖初值 |
| `CG_POSTGRESQL_ALLOWED_CIDR` | 空 | PG 管理/复制 IPv4 CIDR。 | `--postgresql-allowed-cidr` 可覆盖 |
| `CG_PATCH_TRUST_KEY` | 空 | 补丁签名公钥路径。 | `--patch-trust-key` 可覆盖，随后还有介质自动发现 |
| `CG_JQ_BINARY` | PATH 中的 jq，随后回退介质静态 jq | 指定可执行 jq；无效路径会拒绝。 | 无 CLI |
| `CG_INSTALL_API_RETRY_ATTEMPTS` | `4` | API Leader 定向/GET 传输重试次数，1..10。非 GET 传输失败不会盲重试突变。 | 执行阶段 |
| `CG_INSTALL_VIP_LEASE_TIMEOUT_SECONDS` | `300` | 初始 VIP lease 等待，30..600 秒。 | 执行验收 |
| `CG_INSTALL_VERIFY_TIMEOUT_SECONDS` | `180` | 数据库拓扑稳定健康总等待，30..900 秒。 | 执行验收 |
| `CG_INSTALL_VERIFY_POLL_SECONDS` | `2` | 拓扑轮询间隔，1..30 秒。 | 执行验收 |
| `CG_INSTALL_VERIFY_STABLE_OBSERVATIONS` | `3` | 连续健康观测次数，2..10。 | 执行验收 |
