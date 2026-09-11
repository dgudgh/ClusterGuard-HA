# Helper Linux 三节点测试

## 授权与基线

- 用户本轮明确要求到测试环境 `192.168.102.152–154` 执行，不再仅做本地交叉编译。
- 实际工作树 `.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，保留所有未提交修改。旧版对照延续 `bc0546a`，本轮再次读取旧 Linux `peerUID` 并核对 Helper 差异；详细修复前证据见 `helper-boundary-review-2026-09-10.md`。
- 三台 SSH 连接成功，核验既有主机密钥；凭据仅交给 SSH 交互输入，不落文件或命令参数。
- 现场核实三台均为 Linux 4.18 x86_64、安装版本 2.2-98。当前源码修复尚未安装，测试二进制独立上传，不覆盖现有程序。
- 证据目录 `.build/linux-helper-field-20260910.Yxylaq/`。

## 执行计划

1. [完成] 只读保存三台版本、服务 PID、数据库原生角色/复制、VIP、API 与维护状态。
2. [完成] 三台分别以 root / clusterguard 运行最新 Linux Helper 测试二进制；覆盖真实 SO_PEERCRED 与文件链接、特殊文件、终态回写。
3. [完成] 新开私有 socket 的 Helper 进程，使用无数据库副作用的 runner 夹具，验证真实 HTTP/UID/错误请求链路；不向现有 Helper 提交升级。
4. [完成] MySQL 三组隔离恢复、PG 启动脚本两个子场景及 PG 六个真实实例恢复场景通过；两种引擎各采集 50 个不同自然观测。HTTP 非 200 单列，不计为全部接口通过。
5. [完成] 前后核对业务容器、程序 SHA、PID、主从/VIP；本轮临时文件、容器、网络与 SSH 连接已清理，失败与未覆盖项见下文。

## 不变量与限制

- 不停现有数据库、不切主、不重启平台、不安装 RPM、不关闭 SELinux、不更换现场签名密钥或业务密码。
- 私有 Helper runner 只打印测试参数或返回约定错误，不是实际 RPM 升级；私有 socket 测试通过不能写成现场滚动升级通过。
- MySQL 临时测试只能创建随机命名独立网络/容器/数据卷；不发布端口、不挂业务目录、不注册平台，使用本机已有镜像并限制资源。
- 当前运行中的 PG 不为满足“停止态读取”测试而人为停库。该特定场景不适用时明确记录。
- Go Helper 目录安全已修复不代表 shell 整条并发目录协议已闭环。新测试工具是隔离验收夹具，不修改业务代码。

## 结果

### 现场与最新源码分开验收

| 层次 | 本轮实测 | 不能推出的结论 |
| --- | --- | --- |
| 已安装平台 2.2-98 | 三台 SSH 只读检查、两套活动集群各 50 个不同后台观测、三节点 API 最终读回 | 不代表最新源码已经安装 |
| 最新 Go Helper | 三台 Linux 原生执行，root / clusterguard 身份矩阵，以及独立 Helper 的真实 Unix socket / HTTP | 不代表 root shell 升级器或 RPM 滚动安装已完成验收 |
| 最新数据库恢复代码 | .153 独立 MySQL 8.0.44 容器，.154 独立 PostgreSQL 16.4 容器中的三实例恢复场景 | 不代表对现场业务集群执行了一键恢复、切主、VIP/lease 释放恢复 |

### Helper 与内核身份测试

- 三台分别以 root 和 clusterguard 运行 `platformupdate.test`，每次 72 个叶子用例通过，无跳过，覆盖文件/目录链接、硬链接、特殊文件、目录替换、失败回调、终态保留和严格输入。
- `peercred.test` 的 4 个 Linux 用例由两个实际身份合并覆盖：root 运行时按设计跳过“拒绝其他 UID”，非 root 运行时跳过“root 独立允许”；各自 3 个通过、1 个条件跳过，合并没有未覆盖用例。不把重复运行或父测试计成新增用例。
- 每台隔离验收脚本的 22 个检查通过，其中 4 个是上述测试进程退出码，另外 18 个覆盖真实 Helper 进程。root / 服务 UID 获准进入 HTTP；nobody 即使具有 socket 所属用户组、成功连接，也被 SO_PEERCRED 拒绝。
- plan/execute/resume/rollback 均到达无副作用的测试 runner，错误输入被拒绝；服务用户可以读取 0640 输出，0770 作业目录保留。故意返回 17 的 runner 产生失败终态。
- 默认现场目录逐级均非符号链接，原有 Helper 路径 `/usr/local/libexec/clusterguard-update-helper`；已核对 systemd 的 `User=root`、`Group=clusterguard`。自定义符号链接暂存路径仍会被拒绝，不宣称兼容任意部署。

首次 .152 夹具运行失败，保留 `152-helper.json`：测试目录权限 0711 缺少非 root 安全目录打开所需的读取权限，且私有进程未继承真实服务 GID，导致测试输出不可读。修正的是**本轮私有夹具**的目录权限与进程组身份，不是关闭安全检查或修改现场权限；复跑三台均通过，见 `152-helper-final.json`、`153-helper-final.json`、`154-helper-final.json`。首次失败结果未覆盖删除。

### MySQL / PostgreSQL 真实隔离恢复

| 测试 | 结果 |
| --- | --- |
| MySQL GuardedClone | 58.59 秒通过：实际物理克隆、业务写入隔离、接收端 UUID 保留、重启后 fencing 与 GTID 复制 |
| MySQL RelayDrainAndSelection | 53.31 秒通过：实际 relay 追平；持久化可写覆盖拒绝、重启保留写保护、独立提交拒绝选主、未决 XA 阻断四个子场景均通过 |
| MySQL ExecutorRebuild | 58.32 秒通过：真实执行器完成物理克隆并验证 GTID、身份和两笔业务提交保留 |
| PG 启动脚本 | .152 上 0.72 秒通过：已有从库保留运行期上游、提升后的主库不被 Bootstrap Env 改回从库 |
| PG WAL 权威选择 | 通过：三实例实际 WAL 字节证据，非单纯 max(LSN) |
| PG 独立分支 COMMIT | 通过：两条分支都有独立业务提交时拒绝自动选主 |
| PG 新时间线、旧分支无独立提交 | 通过：基于祖先 WAL 和唯一提交历史选择已提升分支 |
| PG WAL 缺失 / PREPARE 未决事务 | 两个场景均通过：证据不足时停止自动恢复 |
| PG GuardedStartAndRebuild | 通过：受控启动、保留数据重建、两条 streaming 复制 |

MySQL 镜像不带 mysqld supervisor，首组克隆日志保留 `ERROR 3707: Restart server failed`。测试随后显式重启**独立测试容器**并核对物理克隆、身份、写隔离及复制成功；不将该镜像自动重启能力写成通过。

PG 使用真实 initdb/pg_ctl/pg_waldump/数据库复制和重建流程，容器内 systemctl/runuser 为测试适配；没有验证实际宿主机 systemd 编排或现场 Raft Recovery Commit。所有镜像使用本机已有版本、禁止拉取、不发布端口、不挂业务目录，PG 原生测试以非 root postgres 身份运行。三份日志为 `mysql-isolated.log`、`pg-native-isolated.log`、`pg-entrypoint.log`，上述场景没有跳过或失败。

### 50 轮观测与响应耗时

2026-09-10 17:46:11 至 17:48:12（北京时间）采集约 118 秒，未强制触发 discovery。每套集群均有 50 个不同 `observed_at`，每轮拓扑为 healthy、三节点健康、唯一主库、两条健康且 lag=0 的复制链接：

- `swarm-mysql-8.0`：.153 为主，.152/.154 为从，VIP 192.168.102.156 在 .153。
- `swarm-pg16`：.154 为主，.152/.153 为从，VIP 192.168.102.157 在 .154。
- 最终三个 API 节点对两套集群返回相同观测时间戳和主从/两条链接；.152 为 Raft Leader，term=82，quorum=true，其他两台 ready 的 Follower 无写授权，符合角色边界。维护升级未激活。

**不能写成 500 次 HTTP 全通过。** 每轮读取五类接口：MySQL 有 3 轮、合计 5 次 health/candidates 返回 409 `topology observation changed`，另外 245 次返回 200；PG 250 次全部 200。对应拓扑自身仍完整健康，之后最终三台 API 复查均 200。保留原始请求 ID 和错误，不删除观测一致性保护；浏览器是否正确重取一致快照本轮未验收。

| 读取端点 | MySQL p50 / p95 | PG p50 / p95 |
| --- | --- | --- |
| topology | 131 / 206 ms | 15 / 140 ms |
| health | 112 / 180 ms | 13 / 35 ms |
| candidates | 22 / 118 ms | 11 / 25 ms |
| operation context | 13 / 33 ms | 13 / 24 ms |
| 日志第一页 | 15 / 162 ms | 25 / 62 ms |

这是从 .152 发起的现场 API 样本，包含非 200 的返回耗时；不是浏览器渲染性能或发现任务耗时，不足以否定之前报告的 PG 页面卡顿。日志只读第一页，未全量拉历史。

### 前后不变量与清理

- 三台版本仍为 2.2-98，平台/Agent/原有 Helper/runner 的 SHA 未变；平台 PID 1497/1534/1198、Helper PID 1394/1411/1041 未变，NRestarts 均 0。
- Swarm 六个服务仍为 1/1，原有容器 ID、服务定义、挂载/Bootstrap 配置、主从和 VIP 地址均未改变。
- 原有 Helper socket、暂存目录与 runner 的属主、模式、链接属性前后相同。SELinux .152/.153 仍 Enforcing，.154 原本 Disabled，未修改。
- 三台最新上传二进制以及 PG 脚本 SHA 均与本地对应产物一致，详见 `summary.json` 的 `uploaded_binaries`。
- 私有 Helper 已退出、测试子目录已删除；MySQL 测试容器/网络及 PG 夹具容器无残留。专属上传目录 `/var/tmp/cg-hb.KYH30f`、`/var/tmp/cg-hb.FD8jGh`、`/var/tmp/cg-hb.3ZQ3Zv` 已经白名单检查后删除。
- 三个 SSH ControlMaster 均已关闭，其本地临时 socket 目录已移除。只清理本轮明确创建的资源，不清理其他任务文件。
- 最后 10 分钟、每服务最多 60 条的日志样本未匹配到 failure/error/panic；这是有上限的样本检查，不代表全部历史日志审计通过。

## 仍未关闭的项

1. root shell 升级器仍使用服务账户可写的共享暂存目录，私有执行区/结果发布区协议仍是发布阻断项，见上一轮 Helper 边界报告。新增 Linux 实测只关闭原生 UID 与 Go 文件访问验证缺口。
2. 没有执行现场上传验签、实际滚动安装/回退、升级中 Leader 接管或现场业务集群灾难恢复；不得据此打包宣布整个升级链已验收。
3. `TestRecoveryPostgreSQLReadOnlyStoppedDockerEvidence` 需要明确指定的停止态现场夹具。本轮活动 Swarm PG 三台均运行中，未为测试而停库，该门槛未执行。
4. 旧宿主机 PG 5432 与 Swarm PG 55432 属于不同 system_identifier；前次记录的旧 policy 保留/退役问题未在本轮处理。不能把 Swarm healthy 写成所有宿主机 PG 都正常。
5. 未执行密码轮换、PXC/Galera、Oracle/SQL Server 原生恢复或浏览器交互验收；本轮不修改界面、业务代码、不提交、不打包、不部署。

**结论：已在指定三台测试机完成最新 Helper 的 Linux 身份/文件边界验证，以及 MySQL/PG 隔离恢复测试；现场两套活动集群通过 50 轮健康拓扑观察。整条发布/恢复验收仍为 partial，剩余安全项和未执行场景不能标成完成。**
