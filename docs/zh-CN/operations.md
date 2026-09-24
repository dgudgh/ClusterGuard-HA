# ClusterGuard HA 操作

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../operations.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

版本边界：`v2.1.45` 是已封板的 MySQL 发布版本。PostgreSQL 操作属于 2.2 系列。Oracle 和 SQL Server 流程在它们各自的生产认证完成之前仍分别受限制。

> **文档定位：** 本文是控制面与 API 参考。全文示例使用**源码配置的默认值**（`http://127.0.0.1:8088`，明文回环）。
> 生产安装器 `scripts/install_clusterguard.sh` 生成的部署监听 `https://<host>:3000` 并强制 TLS
> （证书 `/etc/clusterguard/tls/server.crt`，CA `/etc/clusterguard/tls/ca.crt`，API 端口默认 3000）。
> 因此在现场请把示例中的地址换成 `https://<host>:3000`，curl 命令补 `--cacert /etc/clusterguard/tls/ca.crt`，
> 并把每条 `cgctl <子命令>` 改写成
> `cgctl --server https://<host>:3000 --ca-file /etc/clusterguard/tls/ca.crt <子命令>`
> （全局 flag 必须写在子命令之前）。日常值守流程以[运维操作手册](operations-manual.md)为准。

## 1. 配置控制平面

使用 `configs/clusterguard.example.json` 作为配置形状。加载器拒绝未知的键和多个 JSON 值。

实现的键：

| 键 | 必需 | 含义 |
| --- | --- | --- |
| `http_address` | 否 | HTTP 监听地址；留空默认为 `127.0.0.1:8088`。 |
| `tls_cert_file`, `tls_key_file` | 非回环 API 访问 | 服务器证书和密钥。除非明确启用不安全实验室覆盖，否则拒绝非回环明文。 |
| `tls_ca_file` | HTTPS 控制器 RPC | 用于在变更操作转发期间验证配置的 Leader API 地址的 CA。 |
| `allow_insecure_http` | 实验室专用 | 明确允许非回环明文 HTTP 并发出启动警告。在凭证或会话跨越不受信任网络时，永远不要启用。 |
| `metadata_path` | 是 | 可靠元数据快照路径。 |
| `control_token_env` | 否 | 包含控制 API `POST` 请求的 Bearer 令牌的环境变量。没有它，所有控制 `POST` 路由都会以 `503` 关闭。 |
| `approval_token_env` | 已弃用 | 仅用于旧版节点生命周期审批。数据库操作和自动恢复会忽略它；新数据库执行使用一次性授权。 |
| `mysql.enabled` | 否 | 启用服务器端 MySQL 发现和操作凭证。 |
| `mysql.discovery` | 启用时 | 专用只读发现用户名和密码环境引用。 |
| `mysql.operation` | 启用时 | 专用管理操作用户名和密码环境引用。 |
| `mysql.replication` | 启用时 | 专用复制用户名和密码环境引用。 |
| `mysql.automatic_failover_enabled` | 否 | 启用仅 Leader 的自动故障转移。原始配置默认为 `false`；支持多节点安装程序在 VIP 支持的 MySQL HA 中启用它，并使用 Agent 仲裁隔离。 |
| `mysql.automatic_failover_interval_seconds` | 否 | 恢复控制器轮询间隔；默认为 1 秒。 |
| `mysql.automatic_failover_retry_seconds` | 否 | 阻塞或失败事件尝试后的退避时间；默认为 30 秒。 |
| `postgresql.enabled` | 否 | 启用原生 PostgreSQL 发现和配置的 HA 功能；默认为 `false`。 |
| `postgresql.discovery_interval_seconds` | 否 | PostgreSQL 调度器间隔；默认为 1 秒，与 MySQL 独立。 |
| `postgresql.discovery_timeout_seconds` | 否 | 每个端点的 PostgreSQL 探针超时；默认为 1 秒。 |
| `postgresql.automatic_failover_enabled` | 否 | 启用仅 Leader 的 PostgreSQL 自动故障转移；默认为 `false`。 |
| `postgresql.automatic_failover_interval_seconds` | 否 | PostgreSQL 恢复控制器轮询间隔；默认为 1 秒。 |
| `postgresql.automatic_failover_retry_seconds` | 否 | 阻塞或失败的 PostgreSQL 事件尝试后的退避时间；默认为 30 秒。 |
| `postgresql.discovery` | 启用时 | 专用监控用户名、数据库和密码环境引用。 |
| `postgresql.operation` | PostgreSQL 变更操作 | 专用操作用户名、数据库和密码环境引用。必须与 `postgresql.replication` 一起配置。 |
| `postgresql.replication` | PostgreSQL 变更操作和节点同步 | 专用复制用户名、数据库和密码环境引用。必须与 `postgresql.operation` 一起配置。 |
| `consensus` | 实现真正的 HA 变更操作 | 奇数 Raft 控制器成员资格、持久状态和多数权威。 |
| `consensus.snapshot_cas_enabled` | 实现复制变更操作 | 在所有升级的控制器集上显式激活快照内容比较和交换。缺少或 `false` 会使元数据变更操作关闭。 |
| `consensus.replicated_log_compression_enabled` | 滚动升级后推荐 | 在继续读取旧版未压缩条目时压缩完整状态 Raft 日志条目。仅在每个投票者运行支持二进制文件后激活。 |
| `consensus.tls_cert_file`, `tls_key_file`, `tls_ca_file` | 非回环 Raft | 控制器到控制器 Raft 流量的相互 TLS 身份和私有 CA。这三个都需要一起使用。 |
| `consensus.peers[].api_address` | 推荐 | 用于将变更操作转发到该控制器的受信任 HTTPS 地址，当它是 Leader 时。它永远不会从入站 HTTP Host 头派生。 |
| `consensus.allow_insecure_transport` | 实验室专用 | 明确允许非回环明文 Raft 并发出启动警告。 |
| `agent` | VIP 变更操作 | 用于 VIP 所有权、角色状态和带内自隔离的受限签名节点命令传输。 |
| `fencing.agent_quorum_enabled` | MySQL 自动故障转移 | 使用短 Raft 多数授权和本地 Agent 关闭协调，在晋升前进行。 |
| `fencing` | 更强的隔离（可选） | 用于 BMC、PDU、云或虚拟化隔离的特定站点外部隔离/状态命令。 |
| `mysql.semi_sync_required` | 生产 MySQL 推荐 | 在发现、候选选择、预检查和后操作验证期间要求当前半同步源确认和副本准备就绪的证据。缺少证据会阻止晋升。 |

在多控制器部署中，`http_address` 必须监听由配置的对等 API 地址可访问的地址；仅回环监听器无法接收从跟随者到 Leader 的变更操作 RPC。

JSON 文件仅包含环境变量名称。在服务环境中设置秘密：

```bash
export CG_CONTROL_TOKEN='replace-with-a-control-api-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-read-only-secret'
export CG_POSTGRESQL_DISCOVERY_PASSWORD='replace-with-the-pg-monitor-secret'
export CG_POSTGRESQL_OPERATION_PASSWORD='replace-with-the-pg-operation-secret'
export CG_POSTGRESQL_REPLICATION_PASSWORD='replace-with-the-pg-replication-secret'
export CG_ORACLE_DISCOVERY_PASSWORD='replace-with-the-dedicated-sysdg-secret'
export CG_ORACLE_OPERATION_PASSWORD='replace-with-the-dedicated-sysdg-secret'
go run ./cmd/clusterguard --config configs/clusterguard.example.json
```

当启用的引擎缺少其必需的用户名 `password_env` 或解析后的密码时，服务器会拒绝启动。MySQL 和 PostgreSQL 密码通过环境变量传递给其客户端进程，不会放置在命令行参数、API 负载或持久化元数据中。

生产布局：

```text
/etc/clusterguard/clusterguard.json
/etc/clusterguard/clusterguard.env
/var/lib/clusterguard/metadata.json
/var/lib/clusterguard/admin-recovery.json  # one-time, normally absent
/var/log/clusterguard/
/usr/local/bin/clusterguard
/usr/local/bin/cgctl
```

### 平台登录

在空的元数据存储中，Leader 创建一个引导管理员：

```text
username: admin
temporary password: 见下方 bootstrap-admin-password
role: admin
MustChangePassword: true
```

若部署未设置 `bootstrap_admin_password_env`，该口令由 Leader 生成并写入
`/var/lib/clusterguard/bootstrap-admin-password`（权限 0600，与 `metadata.json` 同目录），
在它出现的控制节点上读取。设置了该环境变量的部署使用变量值，不生成文件。两种情况下
平台都会在首次改密成功后删除该文件。

首次登录只能进行到更改密码的步骤。在首次密码更改完成之前，所有其他平台 API 都返回 `password_change_required`。密码以 Argon2id 哈希形式存储在复制的元数据快照中；明文永远不会写入配置、环境文件、审计事件或报告中。

浏览器会话具有八小时的绝对生命周期。会话密钥保存在 HttpOnly SameSite Cookie 中，变更操作请求需要匹配的 `clusterguard_csrf` Cookie 和 `X-CSRF-Token` 头部，密码更改或注销会撤销会话。角色如下：

| 角色 | 访问 |
| --- | --- |
| `admin` | 完整平台访问，包括元数据和节点生命周期。 |
| `operator` | 读取访问加上受保护的数据库操作和发现刷新。 |
| `viewer` | 仅读取访问。 |

控制台从不请求控制令牌、生命周期令牌或一次性审批令牌。登录后的平台操作在其内部服务器中创建并消耗其计划绑定的一次性授权。

破坏性矩阵可以使用相同的会话路径：

```bash
export CG_PLATFORM_USERNAME='admin'
export CG_PLATFORM_PASSWORD='<current-platform-password>'

scripts/clusterguard-ha-matrix.sh \
  --api http://127.0.0.1:8088 \
  --clusters '<cluster-uuid>' \
  --platform-session
```

对于新的元数据存储，还应设置
`CG_PLATFORM_NEW_PASSWORD='<replacement-password>'`。矩阵更改引导密码，再次登录，然后执行时不需要
`approval_token` 字段。如果没有 `--platform-session`，矩阵保留用于验证服务客户端重放拒绝的显式一次性授权路径。

密码丢失恢复是故意本地化、一次性且关闭的。没有远程重置端点。首先暂停控制平面变更操作并备份当前元数据和 Raft 目录。在一个控制器上，以服务账户生成一个工件：

```bash
sudo -u clusterguard /usr/local/bin/clusterguard admin prepare-recovery
```

该命令仅打印一次强临时密码，并将 Argon2id 哈希写入 `/var/lib/clusterguard/admin-recovery.json`。将完全相同的工件复制到每个控制器，并安装为
`clusterguard:clusterguard`，模式为 `0600`。一次重启一个控制器服务，以确保多数可用。控制器仅在进程启动时读取工件；当前 Raft Leader 提交恢复 ID 一次，撤销所有管理员会话，要求更改密码，写入安全事件，然后每个控制器删除其本地工件。工件在 24 小时后过期。

使用临时密码以 `admin` 登录并立即替换它。不要创建共享默认密码，删除 `PlatformUser` 记录，编辑密码哈希，或仅重置一个控制器的元数据快照。如果一次性工件无法在多数情况下提交，请通过离线灾难恢复过程恢复受保护的元数据备份。

使用 `packaging/systemd/clusterguard-ha.service` 和
`packaging/systemd/clusterguard.env.example` 作为服务模板。当省略 `--config` 时，服务器二进制文件默认为 `/etc/clusterguard/clusterguard.json`。在重启控制器之前验证确切的生产文件：

```bash
/usr/local/bin/clusterguard \
  --config /etc/clusterguard/clusterguard.json \
  --check-config
```

打包的 systemd 单元以 `ExecStartPre` 运行此验证；无效的配置因此在服务进程替换健康控制器之前失败。

当将现有的 Raft 控制器集升级到支持快照内容比较和交换的版本时，在混合版本发布期间不要启用该协议。暂停元数据变更操作和周期性协调器，替换每个控制器上的二进制文件，然后在每个控制器上将 `consensus.snapshot_cas_enabled` 设置为
`true` 并重启整个控制器集。缺少该键或设置为 `false` 的新二进制文件仍然可读，但拒绝复制的元数据变更操作。在旧控制器仍可能成为 Leader 时，永远不要启用该键。

使用相同的两阶段发布流程
`consensus.replicated_log_compression_enabled`：首先在每个投票者上替换二进制文件，同时该键仍为 `false`，然后在每个控制器上启用该键并在当前 Leader 之前重启跟随者。新控制器继续读取未压缩条目，但旧二进制文件无法读取新压缩条目。Raft 存储大于 256 MiB 的在启动时打开之前原子压缩；在第一次滚动重启期间，确保有足够的空闲磁盘空间用于压缩副本。

控制器状态在磁盘、Raft 日志条目和 Raft 快照中限制为 16 MiB。升级前验证受保护的元数据文件是否低于该限制并进行备份。审计、报告、会话、审批、操作、生命周期和安全事件历史自动限制；当需要更长的历史记录时，将记录导出到外部保留系统。

对于 Raft mTLS 发布，安装私有 CA 和每个控制器的证书在更改配置之前。证书必须包括广告的对等 IP 或 DNS 名称以及客户端和服务器用途。一次重启一个控制器，并在每次重启后验证可写多数。不要在未经验证的步骤中旋转 CA 和所有控制器身份。

构建一个自验证的 Linux 包，然后在默认只读预检模式下运行安装程序，再允许变更操作：

```bash
./scripts/build-clusterguard-bundle.sh --output ./dist --version 1.0.0
tar -xzf ./dist/clusterguard-ha-1.0.0-linux-amd64.tar.gz
cd ./clusterguard-ha-1.0.0-linux-amd64
./scripts/clusterguard-install.sh \
  --bundle-dir "$PWD" --role mixed \
  --node-name cg-node-0001 --node-id <platform-node-uuid> \
  --config /secure/input/clusterguard.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent.json \
  --assets-dir /secure/input/assets
```

在查看计划后，使用 `--execute` 重复命令。运行时资产使用允许的布局：`tls/*.crt`, `tls/*.key`, `ssh/*_ed25519`, `ssh/*known_hosts`, 和 `mysql/*-client.cnf`。安装程序在更改主机之前拒绝符号链接、未知资产类型、错误校验和、无效 JSON 和可变节点名称。它在需要时仅将服务密钥组可读，而 MySQL 客户端凭证仍为 root 专用，用于受限数据节点代理。

对于数据和混合节点，安装启动受限代理但保持周期性 VIP 协调器禁用。注册集群 HA 端点，证明其规范所有者是当前可写主节点，建立多数所有权租约，并在每个节点上完成一次成功的协调。只有在那时，重复安装程序并使用 `--activate-agent-reconcile --execute`，或通过生命周期工作流启用计时器。这防止部分配置的部署在引导期间更改 MySQL 角色状态。

浏览器 `/api/v1/` 请求需要有效的平台会话；变更操作请求还需要 CSRF 和角色授权。外部服务客户端使用 `Authorization: Bearer <control-token>`。显式服务数据库执行还需要其匹配的一次性审批授权，并故意不接受控制凭证作为审批替代。缺少或无效的凭证在数据库访问之前失败。保留默认的回环监听器用于本地操作。在将 API 暴露在其他接口之前，启用 TLS 和网络访问控制；永远不要通过明文 HTTP 传输凭证。

## 2. 检查引擎和功能

```bash
curl -sS http://127.0.0.1:8088/api/v1/engines
curl -sS http://127.0.0.1:8088/api/v1/capabilities
```

这两条路由列出注册的引擎和每个功能的显式可用性、变更操作标志和原因。

## 3. 注册权威清单

在连接数据库端点之前，使用固定的全局唯一 `node_name` 一次性注册每个物理主机。平台返回一个不可变的 `resource_id`：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/nodes \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "resource_id":"<preallocated-platform-node-uuid>",
    "node_name":"cg-data-0001",
    "display_name":"MySQL host 1",
    "hostname":"mysql-a",
    "ip_address":"192.0.2.10",
    "kind":"data",
    "active":true
  }'
```

`resource_id` 可能由签名的安装清单预分配；当它被省略时，控制平面生成它。`node_name` 和 `resource_id` 从不更改。主机名或 IP 更改使用 `PUT /api/v1/nodes/{resource_id}` 与相同的 `node_name`；之前的坐标变为别名。重建请求必须提供原始节点 UUID 和固定名称，因此修复后的主机重用现有插槽而不是作为额外节点出现。

注册集群显示名称、引擎和控制器允许探测的每个数据库端点：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/clusters \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "display_name":"payments-mysql",
    "engine":"mysql",
    "endpoints":[
      {"hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},
      {"hostname":"mysql-b","ip_address":"192.0.2.11","port":3306},
      {"hostname":"mysql-c","ip_address":"192.0.2.12","port":3306}
    ]
  }'
```

响应返回新的集群平台 UUID 和端点 UUID。集群显示名称是唯一的，端点坐标不能与现有清单冲突，且至少需要一个端点。

列出和检查清单：

```bash
curl -sS http://127.0.0.1:8088/api/v1/clusters
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>
```

集群详细信息路由返回 `cluster`, `instances`, 和 `endpoints`。

管理员可以从控制台集群选择器旁边的 **集群管理** 执行相同的注册。聚焦对话框注册一个或多个发现端点，立即请求发现刷新，并选择新集群。数据库凭证保留在服务器端，浏览器从不收集它们。

对话框的 **退役集群** 选项卡在管理员输入确切显示名称后，从活动 ClusterGuard 管理中移除集群。退役不会停止数据库、删除数据库数据或变更操作操作系统 VIP。它移除实时清单和协调状态，同时保留审计、报告、操作和已完成生命周期历史。运行中的数据库操作、未过期的操作锁、活动的节点生命周期任务或任何活动的 HA 所有权租约会阻止退役。在退役具有管理 HA 端点的集群之前，停止其代理协调并等待所有权租约过期。这防止在清单消失后间接触发主机侧 VIP 更改。

等效的服务 API 是：

```bash
curl -sS -X DELETE \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid> \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{"confirm_display_name":"payments-mysql"}'
```

## 4. 刷新 MySQL 拓扑

仅刷新已注册的清单。发送一个完全空的 JSON 对象：

```bash
curl -sS -X POST \
  http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/discover \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{}'
```

不要发送凭证或端点坐标。服务器拒绝此类覆盖并从配置中解析 MySQL 凭证。刷新会探测所有活动的数据库端点，并原子提交实例、链接、探测证据、健康状况、异常和指标。

每个集群的并发刷新按顺序进行。提交需要以下两个条件：

- 观察时间戳比持久化集群水印更新；
- 在探测之前捕获的精确清单生成。

相等或更旧的观察或在进行中的刷新期间清单更改会返回 `409` 并不发布部分结果。

## 5. 检查控制平面健康和就绪状态

ClusterGuard 为服务管理器和负载均衡器暴露两个最小的、未认证的探测：

```bash
curl -fsS http://127.0.0.1:8088/healthz
curl -fsS http://127.0.0.1:8088/readyz
```

`/healthz` 仅证明 HTTP 进程是活跃的。`/readyz` 在 Raft 控制器没有已知 Leader、Leader 无法确认多数、跟随者无法识别受信任的 Leader API 或本地元数据仍在追赶时返回 HTTP `503`。数据库健康状况不会改变这两个控制平面探测。两个路由都支持 `HEAD`，并故意省略控制器地址、资源 ID、计数器和配置细节。

经过认证的操作员可以通过控制台设置页面、API 或 `cgctl` 检查完整状态：

```bash
cgctl --server http://127.0.0.1:8088 status
cgctl --server http://127.0.0.1:8088 --json status
curl -sS http://127.0.0.1:8088/api/v1/control-plane/status \
  -H 'Cookie: clusterguard_session=<session>'
```

详细响应包括本地角色、Leader 身份和地址、投票者数量、多数和变更操作权威、Raft 索引、持久化元数据修订、运行时间、活动操作、不确定操作和活动节点生命周期任务。每个 HTTP 响应都携带 `X-Request-ID`；调用者提供的安全 ID 在跟随者到 Leader 转发过程中保留，并且相同的值包含在 JSON 错误信封中。

`active_operations` 仅统计状态为 `running` 的记录。一个持久的
`planned` 记录是等待显式执行请求的历史工作；
它不会消耗主动操作限制，也不会使就绪状态看起来繁忙。`indeterminate_operations` 只统计尚未完成人工复核的 `indeterminate` 记录。已经复核的记录仍保留原状态和全部证据，但不会永久污染当前待处理计数。

背景所有权和自动恢复循环会立即报告新的故障。
未更改的故障随后被抑制，并每隔五分钟提醒一次；更改的故障会立即报告，一个成功的周期
会重置抑制状态。对于相同稳定事件的重试退避周期不被视为成功，因此 30 秒的安全重试仍然
保持活跃，而不会每隔 30 秒生成相同的日志条目。提醒状态仅在事件清除后重置。这限制了日志噪声，而不会
隐藏持续的事件或在恢复后延迟重复。

## 6. 读取拓扑、健康状况、候选者和指标

```bash
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/topology
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/health
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/candidates
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics
curl -sS http://127.0.0.1:8088/api/v1/clusters/<cluster-uuid>/metrics/prometheus
curl -sS http://127.0.0.1:8088/api/v1/monitoring/prometheus \
  -H "Authorization: Bearer ${CG_MONITORING_TOKEN}"
```

候选路由接受可选的有界策略值：

```text
?maximum_lag_seconds=10&require_gtid=true
```

候选评估需要恰好一个当前主节点和完整的探测
覆盖。它检查可达性证据、集群成员资格、角色、
维护、晋升资格、IO/SQL 复制线程、源
身份、延迟、GTID 模式和一致性、异常和缺失的事务、数据
丢失风险，以及 MySQL 版本系列。它只返回排名；它不能提升
节点。

快照派生读取接受 `observation_id=<RFC3339 timestamp>`。Web
控制台首先读取拓扑，并将健康状况、候选者和指标固定到该
相同的观察。如果并发刷新更改了快照，则完整
控制台读取会重试，而不是将来自不同周期的证据组合在一起。

MySQL 只读探测和指标路径由 5.7、8.0、8.4 和 9.7
固定装置覆盖。复制收集处理遗留和当前术语。

每个集群的 Prometheus 端点导出一个集群的数据库指标。
受保护的 `/api/v1/monitoring/prometheus` 全局端点导出所有
集群警报和数据库系列，以及本地控制平面状态。指标
名称包括：

```text
clusterguard_mysql_qps
clusterguard_mysql_tps
clusterguard_mysql_slow_queries_per_second
clusterguard_mysql_connections
clusterguard_mysql_running_threads
clusterguard_mysql_buffer_pool_hit_ratio
clusterguard_mysql_replication_lag_seconds
clusterguard_control_plane_ready
clusterguard_control_plane_leader
clusterguard_control_plane_quorum_confirmed
clusterguard_control_plane_metadata_revision
clusterguard_control_plane_operations
clusterguard_control_plane_lifecycle_tasks
```

当 `clusterguard_control_plane_ready` 是 `0` 时立即发出警报。具有
`clusterguard_control_plane_quorum_confirmed == 0` 的 Leader 不允许更改
元数据。按控制器跟踪元数据修订并调查未收敛的跟随者。
活动和不确定的操作仪表盘区分正常工作和需要人工审查的操作。

数据库系列使用稳定的平台 `cluster_id` 和
`instance_id` 值进行标记。控制平面仪表盘有意本地化到
被抓取的控制器，并且不携带可变的主机名标签；在 Prometheus 抓取目标配置中分配控制器
身份。ClusterGuard HA 运行时不需要外部导出器、
监控代理或指标数据库。

控制台操作日志最初加载 20 个事件，并在操作员选择 **加载更多** 时每次请求添加 20 个事件。对于一个自动恢复事件的重复阻塞重试，会显示为一个事件并带有尝试次数；
持久操作记录保持独立且可审计。手动操作和不同事件从不合并。默认情况下，原始请求和响应数据被折叠，事件在最新尝试时打开。持久快照保留最新的 128 个计划操作、512 个终端操作、
1,024 个审计事件、512 个报告和 2,048 个安全事件。当策略要求更长的审计保留期时，在这些限制之前导出记录。

对于一个不可变的外部归档，在达到限制之前，使用经过身份验证的控制会话或服务持有者以 NDJSON 格式获取当前审计窗口：

```bash
curl --fail --silent --show-error \
  -H "Authorization: Bearer $CG_CONTROL_TOKEN" \
  https://controller.example:3000/api/v1/audits/export \
  >> /secure/archive/clusterguard-audit.ndjson
```

导出器是只读的，从不返回凭证、审批密钥或
会话令牌。保留上限仍然是对
复制控制状态大小的有意保护；长期保留应属于专用的、仅追加的归档。

## 7. 使用 `cgctl`

`cgctl` 默认为 `http://127.0.0.1:8088` 并打印简洁的人类可读
输出：

```bash
go run ./cmd/cgctl status
go run ./cmd/cgctl engines
go run ./cmd/cgctl clusters
go run ./cmd/cgctl topology <cluster-uuid>
go run ./cmd/cgctl health <cluster-uuid>
go run ./cmd/cgctl candidates <cluster-uuid>
go run ./cmd/cgctl metrics <cluster-uuid>
go run ./cmd/cgctl refresh <cluster-uuid>
go run ./cmd/cgctl operation <operation-uuid>
go run ./cmd/cgctl approval issue --cluster <cluster-uuid> --engine mysql \
  --kind switchover --target <instance-uuid> --issued-by <administrator> --ttl 5m
go run ./cmd/cgctl approval list
go run ./cmd/cgctl approval show <grant-uuid>
```

全局标志必须出现在命令之前：

```bash
go run ./cmd/cgctl --json clusters
go run ./cmd/cgctl --json candidates <cluster-uuid>
go run ./cmd/cgctl --server http://127.0.0.1:8088 topology <cluster-uuid>
```

`cgctl` 将配置的控制凭证附加到经过身份验证的读取和
写入。`refresh` 发送 `POST`，其正文为 `{}`。审批发行构建
持久计划并返回一次明文授权。这两个命令都从 `CG_CONTROL_TOKEN` 读取
管理员 Bearer 凭证；在命令之前使用 `--token-env <name>` 选择另一个
环境变量。审批列表/显示从不暴露持久的令牌哈希。控制台使用其平台
会话，从不接收任何凭证。

## 8. 准备一个受保护的 MySQL 切换

### 浏览器控制台

经过身份验证的 `admin` 或 `operator` 选择候选者，解锁本地
反错误控制，并点击执行。请求包含集群、引擎、
操作类型、目标 UUID、幂等性密钥和登录用户名。它
不包含任何审批令牌。服务器持久化计划，内部发出一次性
授权，操作锁下使用它，并仅返回
操作结果。

### 外部服务 API

管理员首先选择确切的集群和候选者，并要求
ClusterGuard 构建持久计划并发出单次使用授权：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/approvals \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -d '{
    "cluster_id":"<cluster-uuid>",
    "engine":"mysql",
    "operation_kind":"switchover",
    "target_id":"<candidate-instance-uuid>",
    "issued_by":"platform-admin",
    "ttl_seconds":300,
    "idempotency_key":"change-20260712-001"
  }'
```

响应包含 `result.operation`、净化后的 `result.grant` 元数据，
和 `result.approval_token`。明文令牌仅返回一次。ClusterGuard
仅存储其 SHA-256 哈希，并将授权绑定到操作 UUID、
集群、引擎、类型、候选者 UUID、拓扑观察和计划摘要。
默认 TTL 是五分钟，最大是十五分钟。

目标是一个清单 UUID。调用者提供的主/目标主机名、IP、
或端口参数被拒绝。发行路线已经完成
预检查和计划。查看返回的操作或通过 UUID 获取它：

```bash
curl -sS http://127.0.0.1:8088/api/v1/operations/<operation-uuid>
go run ./cmd/cgctl operation <operation-uuid>
```

受保护的发布支持一个主节点和多个清单副本。检查需要一个健康的可写主节点、一个选定的合格副本、有界延迟、
运行中的 IO/SQL 线程、GTID 模式 `ON`、兼容的 GTID 历史、二进制日志、
当前探测覆盖、兼容的 MySQL 发布系列，以及经过验证的
写入端点提供者。不可变的计划包括所有必须
重新父化的跟随者。

操作资源响应还包括 `timeline.audits` 和
`timeline.reports`，按不可变操作 UUID 过滤。

DBA 在不发送管理员 Bearer 凭证的情况下使用授权：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/operations/<operation-uuid>/execute \
  -H 'content-type: application/json' \
  -d '{"approval_token":"cgag_<grant-uuid>.<secret>"}'
```

授权消耗和持久 `APPROVE` 转换在操作锁下是原子的。令牌不能被重复使用。过期、已使用、不匹配或
旧计划的授权被阻止，并需要新发行的授权。Web 控制台
从不接收令牌，并在每次尝试后重新锁定选定的目标。

当 Agent、Raft、HA 端点清单或操作凭证缺失时，
ClusterGuard HA 在发出更改 SQL 语句之前持久化一个被阻止或不支持的结果。在配置了这些依赖项后，执行会阻止源、提升选定的目标、重新父化可到达的跟随者、
转移 VIP、验证后置条件，并写入审计/报告时间线。

复制语句选择使用 `STOP/RESET SLAVE` 通过 MySQL 8.0.21
和 `STOP/RESET REPLICA` 从 MySQL 8.0.22 开始。在任何写入之前，内核
在操作锁下重新探测源和目标身份、角色、GTID 历史、复制线程、
延迟、二进制日志和发布兼容性。

## 9. 重新协调可变元数据

平台 UUID 和原生引擎身份是不可变的。对于 MySQL，原生
身份是 `engine_identity.server_uuid`。主机名、IP 地址、端口、显示
名称和别名是可变的坐标。

当已知的 MySQL 服务器移动时：

1. 从集群详细信息路由读取当前实例和端点 UUID。
2. 提交相同的平台实例 UUID、集群 UUID、引擎和
   `server_uuid` 与新坐标。
3. 当实例拥有多个数据库端点时，包括 `endpoint_id`。
4. 按操作员工作流程要求运行元数据 `precheck`、`plan`、`execute`，然后 `verify`。

路由：

```text
POST /api/v1/metadata/reconcile/precheck
POST /api/v1/metadata/reconcile/plan
POST /api/v1/metadata/reconcile/execute
POST /api/v1/metadata/reconcile/verify
GET  /api/v1/metadata/anomalies
```

协调从不更改 MySQL 服务器。它验证不可变的
原生身份，更新选定的端点和规范坐标在一个
仓库事务中，保留之前的坐标作为别名，增加
元数据修订和清单生成，使旧拓扑失效，并要求新的刷新。

观察到的已知 `server_uuid` 自动绑定到其现有的平台
资源 UUID。更改的主机名、IP 或端口不得创建重复的资源。原生身份不匹配、端点所有权重复、跨
集群更新和模糊端点选择被阻止。

## 10. 内置节点安装和同步

控制台 **节点** 视图驱动一个 ClusterGuard 生命周期，用于添加新
节点和重建物理替换的节点。这是一个控制平面
工作流程，而不是一组操作员运行的远程命令。它执行：

1. 清单、不可变节点 UUID、SSH 主机密钥、权限、端口和包
   预检查；
2. 从审批的离线仓库中选择 SHA-256 验证的包；
3. MySQL 或 PostgreSQL 安装，或协调已注册的
   现有安装；
4. 版本感知的数据复制和复制配置；
5. 原生身份、只读状态、复制、延迟和 VIP 缺失
   验证；以及
6. Raft 支持的元数据提交、审计和报告生成。

MySQL 选择允许的克隆、xtrabackup 或逻辑转储路径。PostgreSQL
在其安全前提条件满足时使用 `pg_rewind`，否则使用
`pg_basebackup`。一个任务暴露了单独的预检查、安装、同步、
配置复制、验证和元数据提交状态。在目标变更操作失败或不确定后，不提交任何元数据。

数据节点数量不受限制。控制器成员资格必须保持至少三个的奇数集合。因此，3 到 5 的扩展接受在一次受保护操作中两个不同的控制器目标，安装控制器和
适配器运行时在两者上，发出唯一的 API 和 Raft mTLS 身份，并要求当前 Leader 添加两者为投票者。失败的配对安装按逆序停止新阶段的控制器角色。部分 Raft 添加移除由该调用添加的投票者，然后返回失败。

对于动态控制器注册，将所有四个发行者文件放在
离线运行时资产目录中：

```text
assets/pki/api-issuer.crt       0644
assets/pki/api-issuer.key       0640 root:clusterguard
assets/pki/raft-issuer.crt      0644
assets/pki/raft-issuer.key      0640 root:clusterguard
```

API 发行者必须链接到 `tls_ca_file`；Raft 发行者必须链接到
`consensus.tls_ca_file`。一起配置所有四个发行者路径。发行者密钥
从不通过 API、浏览器、审计流、任务日志或
报告返回。

## 11. 自动故障转移和安全边界

原始配置加载器默认保持禁用，以防止不完整的手写配置
静默地变得破坏性。支持的多节点安装程序默认将 VIP 支持的 MySQL HA 部署设置为自动故障转移，使用 `fencing.agent_quorum_enabled=true`。操作员可以明确
选择 `--manual-failover-only`。
自动故障转移需要 Raft 共识、受限节点代理、
活动的 VIP 资源、所有三个特定用途的 MySQL 凭证、稳定的数据库故障证据和验证的旧主隔离。它不需要
人工审批令牌。启动时拒绝不完整的共识或隔离配置。

代理多数隔离不等于 TCP 故障与隔离。每个代理
接收一个最多 10 秒有效的Leader签名决策，并每 5 秒进行一次协调。在发布确切的故障转移转换租约后，控制器等待至少 15 秒，重新验证多数权威、稳定的故障证据和未更改的租约，并在旧主可到达但仍然拥有 VIP 或保持可写时拒绝提升。无法获得多数授权的节点释放 VIP 并在本地持久化只读意图。`--fencer FILE` 在代理或整个操作系统可能失败但数据库仍能处理流量时，作为可选的更强层保持可用。

对于生产 MySQL，在所有管理实例加载并启用源/副本半同步插件后，设置 `mysql.semi_sync_required=true` 在每个控制器上。ClusterGuard 然后将半同步视为必需的证据：当前主节点必须至少有配置数量的确认客户端，候选者必须主动确认其源并已拥有
提升侧源设置，且提升后的主节点必须在验证期间重新获得一个活动的确认客户端。未知、格式错误、禁用或过时的证据失败时阻断。管理的 MySQL 安装程序使用 `AFTER_SYNC` 配置一个确认副本，并为 MySQL 8.x 设置 10 秒的源超时（以及 5.7 的等效主/从名称）。

半同步显著减少了确认事务的丢失，但本身并不能证明严格的 RPO 零：在配置的超时后，MySQL 可能回退到异步提交，存储、操作系统和网络故障仍超出数据库确认协议。因此，生产 SLO 必须记录超时/回退策略，并在破坏性故障转移测试期间使用客户端事务 ID 进行验证。

恢复控制器仅在多数 Leader 上执行。发现连续记录 4 次当前主库失败，且观测跨度不少于 3 秒后，才生成一个事件。这里的 4 次与 3 秒是默认值，可按引擎用 `automatic_failover_minimum_observations`（`2` - `100`，默认 `4`）和 `automatic_failover_failure_window_seconds`（`1` - `3600`，默认 `3`）调整；单次切换的操作预算用 `automatic_failover_operation_timeout_seconds`（`30` - `3600`，默认 `300`）调整。控制器仅选择排名第一的合格候选者，并通过私有内部授权路径提交一个正常的持久 `failover` 操作。公共 JSON 无法选择此模式。事件 ID 在 `APPROVE` 上进行审计；安全防护、锁定、隔离、执行、验证、审计和报告仍然是强制性的。事件派生的幂等性密钥防止 Leader 变更后重复成功或不确定的故障转移。被阻止的尝试至少等待配置的重试周期。

操作锁存储在 Raft 复制的元数据快照中，并在其持有者仍然是多数 Leader 时更新。安全防护在锁定和审批前再次检查多数。VIP 所有权通过短的独占端点租约和集群范围的拥有者验证单独保护。

### 外部隔离合同

仅在安装了绝对、常规、可执行提供者路径后设置 `fencing.enabled=true`。启动时如果提供者缺失或不可执行则失败。ClusterGuard 使用恰好一个参数调用它，即 `fence`
或 `status`，并向 stdin 写入一个 JSON 对象：

```json
{
  "cluster_id": "<cluster-uuid>",
  "operation_id": "<operation-uuid>",
  "lease_id": "<lease-uuid-when-fencing>",
  "instance": {
    "resource_id": "<old-primary-uuid>",
    "hostname": "mysql-01",
    "ip_address": "192.0.2.10",
    "port": 3306
  }
}
```

提供者必须在 stdout 上输出一个 JSON 对象，且不输出其他内容：

```json
{"status":"ok","fenced":true,"message":"power isolation confirmed"}
```

日志应输出到 stderr。输出上限为 64 KiB，每次调用受
`fencing.timeout_seconds` 限制。成功的 `fence` 调用不足以：ClusterGuard 立即调用 `status`，并仅在该独立
调用也返回 `fenced:true` 时继续。任何超时、格式错误的输出、未知字段、
进程失败或模糊状态都会阻止提升。提供者应根据不可变的实例资源 UUID 键入状态，并验证一个真实的带外机制，如虚拟机管理程序、云、PDU 或 BMC；仅网络可达性不是隔离。

提供者不继承数据库密码、控制令牌或完整的
服务环境。将提供者特定的凭证放入以 `CG_FENCER_` 为前缀的变量中；仅将该前缀加上 `PATH`、区域和时区变量
传递给子进程。当提供者支持时，优先使用 root 所有凭证文件。

Oracle 和 SQL Server 的更改仅可通过其原生 HA
控制平面进行。Oracle 角色转换通过 DGMGRL 使用 Data Guard Broker。SQL Server 计划的角色转换通过 sqlcmd/T-SQL 使用 Always On 可用性组故障转移。一个 MySQL、PostgreSQL、Oracle 或 SQL Server 操作，其所需的执行器、Agent、端点、身份、拓扑、多数、隔离、凭证、锁、审批或验证证据缺失，会在不安全步骤之前被阻止。不支持或被阻止从不意味着部分成功。

受保护的内核将用于预检查的精确拓扑观测标记为`cluster_id@observed_at`，并在获取操作锁后重新验证它。发现发布使用相同的集群锁，因此在执行过程中无法替换已验证的观测。更改或失效的观测会阻止审批和执行。已完成的步骤是持久的。只有当此操作拥有持久的`fence_source`步骤时，已被隔离的源才被接受；恢复时会重新验证不可变的源和目标身份、隔离、GTID历史、二进制日志、发布兼容性、复制状态和端点所有权，然后再进行另一次变更操作。如果变更操作前日志写入失败，工作流将停止。如果变更操作提交点后日志写入失败，验证仍会运行，API返回`indeterminate`执行，并带有HTTP `500`。原子元数据重命名后紧接着目录同步警告也会被报告为已提交，但为`indeterminate`，包括响应中的已协调实例和端点。将`indeterminate`视为需要人工审核的状态；不要自动重试该操作。

提交后验证使用与调用者分离的有界上下文。验证失败后会持久化其检查，并保持为`indeterminate`。验证保留不可变的计划摘要和源/目标UUID范围，但在提升后接受更新的拓扑观测和元数据修订；刷新后的端点仍必须返回计划的原生身份和预期的实时角色。再次调用`verify`操作动作仅在显式验证证据通过时，才能将该记录协调为`succeeded`；所有其他终端状态保持不可变。终端操作记录、终端审计事件和操作报告发布在一个仓库快照中，因此读者无法观察到成功报告与运行中的操作或相反的情况。人工验证协调使用相同的原子最终化路径。

如果历史拓扑已经变化，原计划无法再通过完整验证，操作员可调用 `POST /api/v1/operations/{operation_uuid}/review`，请求体为 `{"note":"现场核验依据"}`。该接口仅接受 `indeterminate` 记录，复核说明不能为空且首次提交后不可覆盖；它原子写入复核元数据、审计和报告，但不改变操作状态。控制台“操作日志”的“标记已复核”使用同一接口。

集群注册和发现发布遵循相同的规则。重命名后的持久性警告返回HTTP `500`以及`result`中的已提交资源或观测。在重试之前，需要协调返回的集群/端点UUID或观测令牌`cluster_id@observed_at`；创建另一个集群或假装观测不存在进行发布可能会重复用户意图。非操作元数据工作流首先持久化一个保守的报告回退，然后在相同的报告UUID下将其替换为终端结果。

MySQL多源复制在此阶段被检测但未建模。如果`SHOW REPLICA STATUS`或其遗留等价物返回多于一个通道，发现将关闭并不发布部分健康、拓扑或提升资格。

## 12. 计划关闭和自动恢复

用于维护窗口、机架移动和MySQL主/副本集群的完全断电。平台在关闭前应用保护，系统启动后systemd单元会自动恢复集群，无需操作员干预。

### 12.1 关闭模式

| 模式 | 行为 | 使用场景 |
|------|----------|----------|
| `service` | 仅停止MySQL（先停止副本，最后停止主节点）；主机保持运行 | 软件升级、配置更改、短维护窗口 |
| `poweroff` | 并行关闭每个节点 | 机架电源维护、迁移 |

### 12.2 启动关闭

在Web控制台中使用 **拓扑 -> 电源生命周期 -> 一键关闭**。默认`service`模式停止数据库但保持主机运行。登录的平台管理员在服务器内部收到一个短暂的、单次使用的审批；没有审批密钥暴露给浏览器。

CLI自动化使用相同的API工作流，并需要显式发出的一次性审批令牌：

```bash
cgctl cluster shutdown --cluster <集群显示名> --mode service|poweroff \
  --approval-token <一次性令牌>
cgctl cluster shutdown --cluster <集群显示名> --mode service --dry-run
```

`--dry-run`执行预检查并立即取消临时生命周期；它不会更改数据库或主机状态。没有直接的shell绕过。

完整流程（任何失败都会中断并保持保护，参见11.4）：

1. 刷新拓扑（`discover`），并让每个已签名的Agent在`/etc/clusterguard/power-snapshots/<cluster-uuid>.json`（目录0700，文件0600）处原子地持久化其集群快照。同一主机上的多个集群不能互相覆盖。
2. 冻结自动恢复（`recovery-freeze`）：自动故障转移、重启引导和手动切换都受阻。
3. 标记每个实例为维护中。
4. 在每个节点上运行`SET PERSIST_ONLY read_only=ON; SET PERSIST_ONLY super_read_only=ON` —— 没有运行时影响，但跨重启持久化，因此恢复后的集群不能接受散写。
5. 按模式停止：`service`先停止副本再停止主节点；`poweroff`并行关闭所有节点。

### 12.3 重启后的自动恢复

两个单元都默认启用。它们在启动时扫描每个集群的快照目录，当目录为空时为无操作：

- `clusterguard-cluster-restore.service` —— 首先确认实时控制平面仍记录活跃的计划恢复状态，启动本地数据库服务，并等待就绪。对于MySQL，只有不可变ID指定的主节点会清除持久化和运行时只读标志。然后它报告启动/恢复并触发发现。在普通重启时，陈旧的快照在任何角色变更之前被拒绝。
- `clusterguard-cluster-finalize.service` —— 等待控制平面健康（`/healthz`，最多60秒），然后轮询拓扑直到主实例报告健康（每5秒，最多600秒）；成功后解除自动恢复冻结，清除集群维护保护，并将`recovered_at`写入该集群的快照。

`poweroff`不能自行重新启动物理断电的服务器。自动开机需要VMware自动启动/API、IPMI/iDRAC/iLO、Wake-on-LAN或AC恢复固件。一旦操作系统启动，ClusterGuard恢复是自动的。

### 12.4 超时和失败回退（关闭）

- 如果最终化超时且主节点仍不健康，保护 **不会** 被释放：脚本记录CRITICAL，干净退出并等待操作员。
- 确认问题已解决后，手动释放保护：

```bash
curl -sk -X POST -H "Authorization: Bearer ${CG_CONTROL_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"freeze":false}' https://127.0.0.1:3000/api/v1/clusters/<cluster-uuid>/recovery-freeze
```

- 删除快照会使两个单元变为无操作；对已最终化的快照（`recovered_at`存在）重新运行恢复/最终化也是幂等的无操作。

### 12.5 检查恢复状态

```bash
cgctl cluster restore-status [--cluster <cluster-uuid>]
```

打印快照存在情况、集群身份、`recovered_at`、恢复冻结状态（`frozen`/`active`）以及每个实例的角色/健康/复制延迟/维护状态，以及模式特定的建议。没有`--cluster`时，使用本地快照的集群UUID。

## 13. PostgreSQL HA

PostgreSQL是ClusterGuard原生的HA实现。它提供安全身份发现、主/备拓扑、健康、原生指标、时间线感知候选评估、受控切换、受保护故障转移、旧主节点回滚/重新加入、允许列表修复、Linux VIP耦合以及`pg_basebackup`节点同步。

可选的自动故障转移在PostgreSQL专用恢复控制器中运行。它需要四次连续的主节点故障观测、时间跨度不少于 3 秒，当前拓扑快照，一个已知零重放延迟的一级备用节点，RaftLeader和多数权威，受限Agent或外部隔离器证明旧主节点无法写入，以及通过验证、审计和报告的完整通用工作流。控制器从不将网络不可达作为隔离证据，也从不重试不确定的提升后结果。

执行从不单独根据引擎名称推断。ClusterGuard仅在存在专用操作和复制凭证、受限签名Agent策略、可执行端点提供者、当前拓扑证据和所需控制器多数时才宣传每个变更操作能力。故障转移还需要稳定的故障证据和外部隔离成功，当前主节点无法证明隔离时。

使用[PostgreSQL HA 操作](postgresql-ha.md)获取完整的身份 SQL、最小权限账户模型、`pg_hba.conf` 要求、控制器和 Agent 配置、节点生命周期设置、操作流程、指标和破坏性验收清单。未知延迟和可选指标保持未知；平台不会将缺失证据转换为虚构的零值，也不会报告模拟成功。

## 14. 适配器路线图

### MySQL

独立的MySQL路径现在包括发现、候选评估、计划切换、受保护故障转移、旧主节点重新加入、允许列表修复、Linux VIP所有权、自我隔离、分阶段节点生命周期、交付打包和破坏性三节点接受。实验室接受包括六个集群、重复的真实切换、多数丢失、主网络分区、旧主节点重新加入、完整主机重启、发散节点重建和可变主机名/IP/端口协调。参见`docs/mysql-feature-parity-acceptance.md`获取证据和包哈希。

用于自动故障转移的每个MySQL Agent策略必须声明`mysql_service`、`mysql_server_binary`和`mysql_server_defaults_file`。ClusterGuard在接受带内隔离之前检查本地systemd单元和有效的`mysqld --verbose --help`输出。`read_only=ON`和`super_read_only=ON`必须在每个管理实例上作为默认重启生效。Agent在尝试实时SQL变更操作之前记录隔离意图，因此停止的旧主节点可以被隔离和验证，而无需将网络不可达作为隔离信号。因此，恢复的实例以只读方式启动，仅通过由Leader支持的ClusterGuard角色转换才能变为可写。

生产部署必须通过上述隔离合同配置和演练特定站点的带外提供者，并验证物理复制方法，如Clone或XtraBackup。捆绑的逻辑转储重建是破坏性回退：在导入供体数据之前，它会删除目标上的所有非系统模式，因此目标数据无法在重置GTID历史后存活。

### PostgreSQL

发现、稳定身份、主/备拓扑、原生监控、时间线感知候选评估、受控切换/故障转移、提升/重新指向、独立验证、旧主节点回滚/重新加入、允许列表修复、VIP耦合和基础备份节点生命周期已实现。下一步生产步骤是跨支持的PostgreSQL发布系列和特定站点的隔离、服务、存储、TLS、备份和恢复布局的破坏性资格；未通过本地策略的能力将保持禁用，而不是模拟。

### Oracle

ClusterGuard通过受限签名节点Agent支持Data Guard Broker控制的切换。Agent将本地SQLPlus身份证据与Broker状态结合，因此即使主机名、IP地址、监听器端点或角色更改，`DBID + DB_UNIQUE_NAME`仍保持稳定。它以Oracle操作系统账户运行DGMGRL，并使用专用密码文件`SYSDG`用户连接。不要为常规平台操作配置`SYS`。

每次切换都需要一个健康的主节点、一个Broker健康的备用节点、零传输和应用延迟、`Ready for Switchover`、冻结的拓扑修订、控制器多数、安全防护、持久操作锁和绑定计划的一次性审批。变更操作仅由配置的Broker成员允许列表中的源节点Agent接受。完成需要两个节点的独立状态检查：目标必须为`PRIMARY`，旧主节点必须为备用节点，Broker状态必须为`SUCCESS`，延迟必须收敛到零。

控制器配置启用Oracle并引用秘密：

```json
{
  "oracle": {
    "enabled": true,
    "discovery_interval_seconds": 15,
    "discovery_timeout_seconds": 15,
    "discovery": {
      "username": "CLUSTERGUARD_DG",
      "database": "DB_UNIQUE_NAME",
      "password_env": "CG_ORACLE_DISCOVERY_PASSWORD"
    },
    "operation": {
      "username": "CLUSTERGUARD_DG",
      "database": "DB_UNIQUE_NAME",
      "password_env": "CG_ORACLE_OPERATION_PASSWORD"
    }
  }
}
```

每个Oracle节点使用其自己的Agent策略，具有相同的平台集群UUID、平台实例UUID、本地`DB_UNIQUE_NAME`、连接标识符、Oracle home、SID、Broker配置和完整的成员允许列表。仅在Agent环境中`CG_ORACLE_BROKER_PASSWORD`中存储数据库秘密。每个Broker成员上必须存在相同的账户和密码文件条目。

失败故障转移故意保持阻塞，直到配置了外部旧主节点隔离。ClusterGuard不直接编辑Oracle数据文件、归档日志或RAC资源。剩余的Oracle扩展工作是RAC实例建模、归档目标检查、监听器端点校正和带有特定站点隔离规则的破坏性接受矩阵。

### SQL Server

ClusterGuard支持SQL Server的受保护Always On计划故障转移路径。当控制器上存在`sqlcmd`或注入SQL Server执行器时，SQL Server适配器可以从AG DMVs发现本地Always On副本，读取同步健康和发送/重做队列指标，建模主到备拓扑，评估同步提交候选者，并预检查、计划、执行和验证`ALTER AVAILABILITY GROUP [name] FAILOVER`。不可变的操作计划在通用安全、锁、审批、执行、验证、审计和报告阶段之前固定AG和副本身份、拓扑观测、资源修订和计划摘要。

执行需要稳定的AG `group_id`、副本`replica_id`、健康的当前主节点证据、健康的提升候选次节点、同步提交、同步目标状态和持久操作租约。验证轮询直到观察到恰好一个主节点，确认所选目标拥有该角色，并确认每个AG副本报告健康。异步或同步的副本在适当情况下仍可见且健康，但不具有提升资格。强制故障转移默认保持阻塞，因为它可能导致数据丢失；在执行前需要单独的显式数据丢失审批策略。

剩余的SQL Server生产工作是原生监听器端点所有权建模、WSFC多数证据和强制故障转移的特定站点隔离。
