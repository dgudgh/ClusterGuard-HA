# ClusterGuard HA 3.1.3.5 完整离线安装候选

## 版本与范围

产品3.1.3.5，RPM基线2.2-113，唯一目录release/3.1.3.5/。修复3.1.3.4在MySQL与控制面已完成后，首次启动更新Helper因缺少共享运行目录而退出226/NAMESPACE的问题。旧3.1.3.4制品保持不变，当前源码与文档使用唯一主线codex/2.2-postgresql。

这是本地安装测试候选，未创建GitHub Release。用户随后授权接管三台，完整续装已退出0，服务器侧真实验收与登录后页面核对通过；生产签名ART与升级完整FIELD契约仍有未执行项。

## 修复

- systemd在ExecStartPre之前处理ReadWritePaths。仅易失的/run/clusterguard允许暂时不存在，再由普通预命令创建为root:clusterguard、0755；持久的更新/私有工作区、配置、状态和日志目录仍须存在。
- 安装器在启动Helper前显式准备共享父目录。Helper启动失败时输出systemd状态及三个相关目录的存在/权限诊断，保留原错误。
- 主进程仍保留PrivateTmp、ProtectHome、NoNewPrivileges与Unix凭据检查。未将共享目录设为RuntimeDirectory，停止Helper不删除其他组件的文件；没有给Helper主进程增加特权前缀或关闭SELinux。
- 累计保留3.1.3.4的MySQL失败阶段证明、完整残留保留、并发锁与挂载保护，以及此前生产cnf与/data路径规则。

## 当前人工安装如何继续

用户日志已完成三台MySQL安装、两个副本同步和三个控制面启动，失败在Helper配置阶段；当前.152主库active、read_only/super_read_only均0，站点phase为registered。**不要移动数据库目录、清空状态或重新生成密钥。**

使用新介质解包目录，复用原安装命令和原--state-file、--secrets-file、--work-dir。安装器会核对全部数据节点受管标记；registered且标记一致时不进入数据库初始化或首次副本同步。标记不一致会拒绝整集群覆盖，不应修改状态来绕过。包目录附MANUAL-TEST.md的当前完整续装命令。

默认数据库软件/usr/local/mysql、数据根/data；保留生产模板普通参数。数据库原厂完整包及摘要仍由现场提供，通过-r明确指定或/opt发现。完整tar不能上传控制台作为cgpatch。

## 验证与边界

构建前在真实systemd239的独立命名单元和路径，用同一实际Helper程序复现旧226，并验证新单元首次启动、healthz、停止保留邻居文件、清空仅测试runtime后的再次启动、陌生UID拒绝、socket660/private700和主进程namespace/NoNewPrivs。持久私有目录缺失仍以226拒绝；该阶段未修改真实Helper、数据库或站点配置。

源码、回归、制品结果及临时诊断清理记录见[开发任务](../development/update-helper-namespace-2026-10-15.md)。隔离Helper生命周期验证不代表已完成用户三节点安装、复制/VIP/恢复或生产签名验收。安装与全部命令参数见[安装手册](offline-rpm-install.md)及[命令参考](offline-installer-parameters.md)。

随后按用户明确授权执行完整三节点续装，2026-10-15 14:57:41安装验证通过、exit0。三台RPM/运行版本、五程序摘要、实际Helper启动与重启、Raft、Agent及定时器通过；真实写入在两副本GTID追平，VIP绑定唯一可写主库，生产my.cnf与受管标记字节保留。详细范围、原始证据与未执行项见[现场验收](../development/installation-field-3.1.3.5-2026-10-15.md)。本次不是空盘重装，也未测试故障切换、灾难注入或签名升级回退。

## 构建回执

构建提交2024567，干净树、未跟踪0，已推GitHub主线。完整tar为33,850,253字节，SHA256为809abbf457f851134471de8ad3689d9f66da66c91154c4bb12fc06da29dd4c85。结构15项及安装器/运行包unit/RPM unit三处实际载荷字节比对通过，3.1.3.4原tar摘要不变。SOURCE42项、114变异、Go40包及Chrome109项通过。包目录附完整续装命令与人工验收表；构建时签名ART/FIELD为OPEN，后续真实安装验收按上节单独记录。回执及现场记录均在构建后追加，未覆盖重建已封板介质。
