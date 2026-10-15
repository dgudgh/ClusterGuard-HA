# ClusterGuard HA 3.1.3.5 完整离线安装候选

## 版本与范围

产品3.1.3.5，RPM基线2.2-113，唯一目录release/3.1.3.5/。修复3.1.3.4在MySQL与控制面已完成后，首次启动更新Helper因缺少共享运行目录而退出226/NAMESPACE的问题。旧3.1.3.4制品保持不变，当前源码与文档使用唯一主线codex/2.2-postgresql。

这是供用户人工续装/新装的本地候选，未创建GitHub Release；生产签名ART与完整三节点FIELD仍OPEN。

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

在真实systemd239的独立命名单元和路径，用同一实际Helper程序复现旧226，并验证新单元首次启动、healthz、停止保留邻居文件、清空仅测试runtime后的再次启动、陌生UID拒绝、socket660/private700和主进程namespace/NoNewPrivs。持久私有目录缺失仍以226拒绝。真实Helper、数据库、站点配置未被代为修改。

源码、回归、制品结果及临时诊断清理记录见[开发任务](../development/update-helper-namespace-2026-10-15.md)。隔离Helper生命周期验证不代表已完成用户三节点安装、复制/VIP/恢复或生产签名验收。安装与全部命令参数见[安装手册](offline-rpm-install.md)及[命令参考](offline-installer-parameters.md)。
