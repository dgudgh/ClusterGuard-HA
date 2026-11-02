# MySQL Agent：systemd 与原厂 RPM 权限兼容

适用：Linux 主机上的 MySQL Agent，尤其是更换原厂 RPM、接入云数据库或把副本提升为主库之后。只在出现下述证据时处理；不是所有 MySQL 节点的安装必做项。

[English](../en-US/mysql-agent-systemd.md) · [API 运维参考](operations.md) · [后端节点入口](../development/backend/nodes.md)

## 先识别故障

页面切换任务可能已经 `succeeded`，之后 Agent 却在 `bootstrap_primary` 与 `release_and_read_only` 之间反复变化，主库双只读交替、网关授权到期并撤下入口。任务终态只证明当时完成了验证，持续可用性还须检查实际计时器与固定入口 SQL。

2026-11-02 的已复现条件是：阿里云 `mysql-server-8.0.46` RPM 的 `/usr/libexec/mysqld` 带 `cap_sys_nice=ep`，Agent 协调服务的 `CapabilityBoundingSet` 不含该能力。SSH 完整 root 上下文中的检查通过，相同命令在 systemd 沙箱下不能执行，报 `Operation not permitted` / `EPERM`，shell 退出码为 126。Agent 随后按原规则自隔离。

这是现场确认的一种原因。没有文件能力、错误并非执行权限或仍有其他拒绝时，继续排查实际日志，不照搬本页补权。

## 只读核对

先读取 `/etc/clusterguard/agent.json` 中目标集群的 `mysql_server_binary` 和 `mysql_server_defaults_file`，不要输出其客户端凭据文件。下面的 `MYSQLD` 必须换成实际服务二进制；不能拿 MySQL 客户端或另一个安装目录作对照。

```bash
MYSQLD=/usr/libexec/mysqld
getcap "$MYSQLD"
rpm -qf "$MYSQLD"
systemctl cat clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service \
  -p CapabilityBoundingSet -p AmbientCapabilities \
  -p ProtectSystem -p ProtectHome -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 50 --no-pager
```

结合原日志确认失败点是 `inspect writable MySQL restart state` / `inspect MySQL restart defaults`。在受控诊断单元中复用实际服务的用户、文件系统和能力边界，对照运行配置中的 `mysqld --defaults-file=... --verbose --help`；不要启动第二个数据库进程。普通 SSH 下成功不能代替这一步。诊断输出放 root 保护的现场目录，不上传完整配置或凭据。

文件有效能力超出进程能力上界时，Linux 的执行检查可能拒绝该程序；这是内核的权限规则，不是 MySQL 账号密码问题。参见 [Linux capabilities(7)](https://man7.org/linux/man-pages/man7/capabilities.7.html)。

## 仅为确认需要的服务增加一项能力

在授权维护窗口中，先保留 `systemctl cat/show` 和原日志。只在确认该厂商二进制确实声明 `cap_sys_nice=ep`、相同沙箱缺少它且补齐后检查成功时，由 root 管理以下 drop-in：

```ini
# /etc/systemd/system/clusterguard-agent-reconcile.service.d/30-mysql-vendor-capability.conf
[Service]
CapabilityBoundingSet=CAP_SYS_NICE
```

文件 root 所有、0644；已有同名文件时先检查和备份，不覆盖未知现场策略。这里依赖正向多行能力集合合并；不要先写空的 `CapabilityBoundingSet=` 清掉原来的能力，也不要使用 `~` 放开全部能力。以 `systemctl show` 的实际结果确认只增加这一项。`AmbientCapabilities`、`ProtectSystem`、`ProtectHome` 以及原厂二进制文件能力保持原值。配置语义见 [systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#CapabilityBoundingSet=)。

放置并复核后：

```bash
systemctl daemon-reload
systemctl show clusterguard-agent-reconcile.service \
  -p CapabilityBoundingSet -p AmbientCapabilities -p ProtectSystem -p ProtectHome
```

已有协调计时器会在后续调度采用新配置。只有确认当前多数派、签名策略和服务状态正确，且处于授权维护流程内，才主动执行一次已有协调服务：

```bash
systemctl start clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 20 --no-pager
```

Agent 是 oneshot：执行成功后可以是 `inactive`；结合计时器、`Result=success`、`ExecMainStatus=0` 和后续周期判断，不能只看协调服务的 `is-active`。初次接入尚未建立可信授权时，不据此启用计时器。不要关闭 SELinux/签名/fencing、删除租约或水位、取消只读检查，或手工强开主库写入。

## 验收持续状态

- 实际计时器经过多个周期不再反复自隔离；运行时主库双只读 OFF，重启只读保护仍保留，副本双只读 ON。
- 检查全部复制线程、延迟、错误及 GTID；固定入口 SQL 必须返回实际获授权主库的 `server_uuid`。
- 网关授权序号持续增长、目标与数据库角色一致，入口归属唯一。路由就绪还须和实际 SQL 联合验证；单网关不提供入口冗余。
- 切换完成后继续观察超过多个授权 TTL，记录时间窗口、采样次数和原始日志；一次 HTTP 200 或任务成功不够。

例如使用预先配置好的受保护只读客户端文件核验入口，不在命令行填写密码：

```bash
mysql --defaults-file=/secure/mysql-observer.cnf \
  --protocol=TCP --host='<固定入口地址>' --port=3306 --batch \
  --execute='SELECT @@server_uuid, @@global.read_only, @@global.super_read_only, @@global.gtid_executed'
```

按现场策略在该客户端文件中配置 TLS 验证，不能为连接成功而关闭证书校验。

## 已验证范围与保留事项

2026-11-02：HA 3.1.5.1、兼容 Agent 3.1.4.2、独立网关 0.1.0-preview.3 的内部测试现场，用户手动完成一次本地到云主库切换。补齐上述现场能力后，16 次、约 62 秒的真实固定入口 SQL 与授权续期检查通过；三个本地副本只读、IO/SQL 正常，四库 GTID 一致。此前发生过只读和入口抖动，原记录保留。

这是带条件的现场配置修复；现有 `ConfigureReconcileSandbox` 不会自动探测该原厂 MySQL 文件能力。drop-in 必须随现场配置保留，升级、更换 RPM 或重建主机后重新核对。本次没有修改 Agent 业务代码或重建已签名补丁，也没有完成回切、应用事务/连接池、故障注入或完整生产认证。完整历史见[网关与现场记录](../development/gateway-vip-frontend-2026-11-02.md)；私有证据不随公开文档分发。
