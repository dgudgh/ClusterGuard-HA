# PostgreSQL Agent：配置、systemd 与能力边界

[English](../en-US/postgresql-agent-systemd.md) · [PostgreSQL HA 手册](postgresql-ha.md) · [后端节点入口](../development/backend/nodes.md)

本页用于 Linux PostgreSQL 原生主备的 Agent 部署与排障。2026-11-02 按当前源码核对；先前文档核对不包含PG现场；随后用户授权部署和网关测试，阶段进展以[本轮记录](../development/postgresql-hybrid-field-2026-11-02.md)为准，不能把MySQL结果计为PG验收。

## 先确认支持路径

| 范围 | 当前实现与边界 |
| --- | --- |
| 原生 PG HA | 有发现、候选、受控切换、故障接管、旧主恢复和节点生命周期实现；按匹配版本与现场矩阵验收后启用。 |
| Linux Agent | 使用 PG 专属服务、OS 用户、PGDATA、工具目录、passfile 和节点身份；与主机 VIP、签名和多数派租约配合。 |
| 企业网关自动跟随 PG 切主 | 本轮源码增加Linux PG适配：多数派租约、严格撤权、原生停止/提升/重挂及持续可写证据。必须同时部署匹配HA/Agent和受信gateway配置；现役旧版本不会自动获得能力，制品与现场结果见本轮记录。 |
| TCP转发 | 独立组件可转发TCP字节，但这不证明PG授权发布、隔离、续期或真实切主闭环；没有PG连接池、读写分离或事务重放承诺。 |

对应源码为 `internal/endpoint/gateway.go` 的 `Precheck` 与原生可写证据、`internal/agent/config.go` 的Linux/无本机VIP策略，以及 `gateway_reconcile.go` 的PG协调分支。其他引擎和PG非Linux运行时仍拒绝；不能手工授予网关路由或跳过检查。

## 固定本机身份与配置

按[PG HA手册的Agent策略](postgresql-ha.md#受限-agent-策略)配置，不复制 MySQL 的角色参数。至少核对：

- `cluster_id`、平台 `instance_id`、独立的 `postgresql_node_id` 与集群 `system_identifier`；平台UUID和数据库本地UUID用途不同。
- `engine: "postgresql"`、实际 `postgresql_service`、`postgresql_user`、`postgresql_port` 和 `postgresql_database`。
- `postgresql_data_directory`、`postgresql_binary_directory`、`postgresql_passfile` 和 `postgresql_peers` 中允许的源身份/坐标。
- 当前原生VIP部署的 `vip`、`interface`、`prefix`，以及可信控制器URL、CA和签名共享秘密引用；原生VIP模式与gateway模式分开：新PG gateway策略显式配置 `endpoint_provider: "gateway"`，VIP/interface留空、prefix=0，继续填写完整PG身份/路径/peers。

当前默认本地执行器使用 `/usr/bin/setpriv --reuid ... --regid ... --init-groups` 切换到配置的PG用户，再调用工具，并把HOME固定到受保护passfile的父目录，避免继承root的libpq客户端证书路径；既有部分回归夹具使用runuser。确认实际 `setpriv`、用户/组、工具及目录权限，不把测试夹具的命令当成部署实现。

passfile须由配置的PG OS用户拥有、0600，父目录允许该用户穿越，内容匹配本地管理和允许的复制连接；不在浏览器、命令行或日志中填写密码。`PGPASSFILE` 和文件权限规则参见[PG16官方说明](https://www.postgresql.org/docs/16/libpq-pgpass.html)。

## 生成窄范围 systemd 沙箱

在授权部署流程中，以实际 `/etc/clusterguard/agent.json` 为输入，由root执行：

```bash
/usr/local/bin/clusterguard-agent \
  --config /etc/clusterguard/agent.json --configure-systemd-sandbox
systemctl daemon-reload
systemctl cat clusterguard-agent-reconcile.service
systemctl show clusterguard-agent-reconcile.service \
  -p ReadWritePaths -p CapabilityBoundingSet -p AmbientCapabilities \
  -p ProtectSystem -p ProtectHome
```

生成文件为 `/etc/systemd/system/clusterguard-agent-reconcile.service.d/20-postgresql-recovery.conf`。仅按已校验PGDATA加入可写路径和其相邻的 `<PGDATA>.clusterguard-recovery` 私有回执目录，并追加所需DAC能力；不清空原能力集合或关闭文件系统保护。命令会写部署文件/回执目录，不启停数据库、不授予writer授权，也不需要读取数据库密码。

路径须是规范、足够具体的绝对目录，不含systemd占位符、控制字符、路径遍历或已有符号链接祖先。例如 `/data/postgresql/5432/data` 只是示例，须与实际PGDATA一致；不能用 `/data`、`/var/lib` 或整个文件系统代替。回执目录要求执行Agent的用户拥有、0700。生成器拒绝输入时修正真实配置，不改宽泛 `ReadWritePaths=/`。

RPM postinstall与配置工具已有该生成调用；手改PGDATA、更换布局或升级后仍须核对实际合并单元。生成成功不等于实际协调器已经通过权限、SQL或隔离验证。

## 在实际执行上下文诊断

```bash
systemctl status clusterguard-agent-reconcile.timer --no-pager
systemctl show clusterguard-agent-reconcile.service -p Result -p ExecMainStatus
journalctl -u clusterguard-agent-reconcile.service -n 50 --no-pager
```

协调器是oneshot，成功后 `inactive` 可以正常；结合计时器、退出状态及后续周期判断。区分服务运行/恢复模式、凭据和目录不可读、身份不匹配、控制链路/证书/时钟、有效多数派授权等原因。普通root SSH探针通过不能替代实际systemd沙箱检查。

本次 MySQL 原厂RPM的 `CAP_SYS_NICE` 故障不构成PG需要该能力的证据。不要照搬该drop-in，也不要关闭SELinux、签名、lease或fencing来消除告警。

以下变量须取自实际Agent策略。示例仅重现本机PG身份下的只读查询，不执行提升或写参数：

```bash
PG_OS_USER=postgres
PG_BIN=/usr/pgsql-16/bin
PG_PASSFILE=/etc/clusterguard/postgresql/5432.pass
PG_PORT=5432
PG_DATABASE=postgres
/usr/bin/setpriv --reuid "$PG_OS_USER" --regid "$PG_OS_USER" --init-groups -- \
  /usr/bin/env HOME="$(dirname "$PG_PASSFILE")" PGPASSFILE="$PG_PASSFILE" PGCONNECT_TIMEOUT=5 \
  "$PG_BIN/psql" --no-password --no-psqlrc --set=ON_ERROR_STOP=1 \
  --host=127.0.0.1 --port="$PG_PORT" --username="$PG_OS_USER" \
  --dbname="$PG_DATABASE" --tuples-only --no-align \
  --command="SELECT pg_is_in_recovery(), current_setting('transaction_read_only'), current_setting('default_transaction_read_only');"
```

恢复状态函数见[PG16官方说明](https://www.postgresql.org/docs/16/functions-admin.html#FUNCTIONS-RECOVERY-CONTROL)。单个会话的只读设置不能单独证明旧主已经隔离；还须具备产品认可的停服/恢复模式和多数派/租约证据。需要写配置、启动、停止、promote、rewind或basebackup时走原受控流程，不在排障中直接执行。

## 持续验收与尚未完成项

核对原生节点UUID、`system_identifier`、时间线、实际上下游身份、WAL接收/重放/延迟、服务状态、唯一writer和唯一入口；主库恢复模式为false、备用为true只是其中部分证据。通过实际业务入口复核角色、TLS、连接池重连及结果不明事务，再观察多个Agent授权周期。历史PG16.4实验室结果须按其原日期和场景阅读，不代表新站点自动通过。

四节点原生复制已部署并做初步真实数据/TLS核验，网关适配已进入源码回归；这不代表云往返、故障注入和应用恢复通过。PG稳定授权要求本地运行且非恢复、原生SQL可写；授权失去或证据错误时停止主库，流式备用保留运行。过渡不由Agent提升或修改写角色，PG gateway灾难重建仍未支持。制品、页面、往返、断链/过期/回退与应用实际结果仅见本轮记录，不以源码绿灯代替现场。
