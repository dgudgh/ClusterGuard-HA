# ClusterGuard HA RPM 离线安装手册

本文面向无法访问互联网的 RHEL、Rocky Linux、AlmaLinux 8/9 环境，说明如何构建、校验、安装、配置、升级和卸载 ClusterGuard HA。

## 1. 交付边界

RPM 包含：

- `clusterguard` 控制面服务
- `cgctl` 管理命令
- `clusterguard-agent` 受限节点代理
- MySQL、PostgreSQL 节点安装与同步脚本
- systemd、logrotate、示例配置和中文手册
- 静态 Linux `jq`

RPM 不打包数据库服务端或厂商客户端。启用对应引擎前，目标控制节点必须另行准备：

| 引擎 | 控制节点客户端 |
| --- | --- |
| MySQL | `mysql` |
| PostgreSQL | `psql` |
| Oracle | 直连模式需要 `sqlplus`、`dgmgrl`；Agent 模式由数据库节点执行 |
| SQL Server | `sqlcmd` |

数据库服务端、补丁、许可证和客户端必须来自企业批准的软件源。

## 2. 联网构建机准备

建议在独立构建机固定 Go、nFPM 和 jq 版本。构建过程中不修改项目的 `go.mod`。

```bash
mkdir -p /opt/clusterguard-build/bin
GOBIN=/opt/clusterguard-build/bin \
  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.44.2
```

准备与目标架构一致的静态 Linux jq，例如 x86_64：

```bash
install -m 0755 jq-linux-amd64 /opt/clusterguard-build/bin/jq-linux-amd64
file /opt/clusterguard-build/bin/jq-linux-amd64
sha256sum /opt/clusterguard-build/bin/jq-linux-amd64
```

构建 RPM：

```bash
./scripts/build-clusterguard-rpm.sh \
  --output ./dist/rpm \
  --version 1.0.0 \
  --release 0.1.rc1 \
  --goarch amd64 \
  --nfpm-binary /opt/clusterguard-build/bin/nfpm \
  --jq-binary /opt/clusterguard-build/bin/jq-linux-amd64
```

输出：

```text
clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm.sha256
```

生产交付应使用企业 RPM 签名密钥签名。未签名测试包仍必须通过独立渠道核对 SHA-256。

## 3. 离线介质

同一交付目录建议包含：

```text
clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm.sha256
dependencies/
database-clients/
site/
  controller-152.json
  controller-153.json
  controller-154.json
  agent-152.json
  agent-153.json
  agent-154.json
  clusterguard.env
  assets/
    tls/
    ssh/
    mysql/
    postgresql/
```

`assets` 只接受以下类型，符号链接和其他文件会被拒绝：

- `tls/*.crt`
- `tls/*.key`
- `ssh/*_ed25519`
- `ssh/*known_hosts`
- `mysql/*-client.cnf`
- `postgresql/*.pass`

## 4. 安装前校验

在目标机核对摘要、签名、元数据和文件清单：

```bash
sha256sum -c clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm.sha256
rpm -K clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
rpm -qpi clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
rpm -qpl clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
rpm -qp --scripts clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
```

`rpm -K` 在未签名测试包上只能证明 RPM 摘要有效，不能证明发布者身份。正式环境必须看到企业签名验证成功。

检查操作系统、时钟、主机名、固定 IP、DNS 和防火墙。三控制节点至少放行：

| 方向 | 端口 | 用途 |
| --- | --- | --- |
| 运维端到控制节点 | 8088/TCP | HTTPS 控制台和 API |
| 控制节点互通 | 10009/TCP | Raft mTLS |
| 控制节点到数据节点 | 数据库端口 | 发现、诊断和受控操作 |
| 控制节点到数据节点 | 22/TCP | 受限 Agent 和节点生命周期 |

每台节点必须提前分配永久不变的平台 UUID 和固定节点名。修改操作系统 hostname、IP 或数据库端口时，不得修改平台 UUID。

## 5. 安装 RPM

先将依赖 RPM 放入本地目录，再安装：

```bash
dnf install -y ./dependencies/*.rpm
dnf install -y ./clusterguard-ha-1.0.0-0.1.rc1.x86_64.rpm
```

首次安装只创建用户、目录和 systemd 单元，不会启动未配置的服务。

```bash
rpm -q clusterguard-ha
ls -l /usr/local/bin/clusterguard /usr/local/bin/cgctl
systemctl is-enabled clusterguard-ha.service
```

默认目录：

- 配置：`/etc/clusterguard/`
- 状态：`/var/lib/clusterguard/`
- 日志：`/var/log/clusterguard/`
- 节点安装包：`/opt/clusterguard/packages/`
- 文档：`/usr/share/doc/clusterguard-ha/`

## 6. 生成站点配置

复制示例文件，不要直接修改 `.example`：

```bash
install -m 0600 /etc/clusterguard/clusterguard.env.example /secure/input/clusterguard.env
cp /etc/clusterguard/clusterguard.json.example /secure/input/controller-152.json
cp /etc/clusterguard/agent.json.example /secure/input/agent-152.json
```

三个控制节点必须：

- 使用不同的 `consensus.local_id`
- 使用不同的本机证书和 `advertise_address`
- 使用相同且完整的 `peers`
- 使用同一套控制令牌、监控令牌和数据库凭据
- 仅初始建群节点设置 `bootstrap: true`
- 其余节点设置 `bootstrap: false`
- 使用同一私有 CA 签发带客户端和服务端用途的 Raft 证书

不要把 `admin` 的平台密码写入 `clusterguard.env`。平台密码以 Argon2id 哈希保存在 Raft 复制的元数据中。

## 7. 预检和正式配置

控制节点和数据节点合并部署时先运行只读预检：

```bash
clusterguard-configure \
  --role mixed \
  --node-name cg-node-0001 \
  --node-id 11111111-1111-4111-8111-111111111111 \
  --config /secure/input/controller-152.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent-152.json \
  --assets-dir /secure/input/assets
```

预检会检查 JSON、环境文件权限、资产白名单、数据库客户端和 Agent 依赖，不会修改系统。

确认输出后，使用完全相同的参数追加 `--execute`：

```bash
clusterguard-configure \
  --role mixed \
  --node-name cg-node-0001 \
  --node-id 11111111-1111-4111-8111-111111111111 \
  --config /secure/input/controller-152.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent-152.json \
  --assets-dir /secure/input/assets \
  --execute
```

可选角色：

- `controller`：只部署控制面，省略 `--agent-config`
- `data`：只部署 Agent，省略 `--config`
- `mixed`：控制面和数据节点同机

配置工具会先备份原配置。systemd 激活失败时自动恢复上一版配置和安全资产，并尝试恢复原服务。

## 8. 三控制节点上线顺序

1. 维护窗口内停止 ClusterGuard HA 元数据变更。
2. 配置并启动唯一的初始 bootstrap 控制节点。
3. 确认 `/healthz` 正常且 Raft Leader 已建立。
4. 依次配置两个 `bootstrap: false` 的控制节点。
5. 每加入一个节点都确认 voter 数、Leader、quorum 和 mutation authority。
6. 三节点就绪后再注册数据库集群。

```bash
curl --fail --cacert /etc/clusterguard/tls/ca.crt https://127.0.0.1:8088/healthz
curl --fail --cacert /etc/clusterguard/tls/ca.crt https://127.0.0.1:8088/readyz
cgctl --server https://127.0.0.1:8088 \
  --ca-file /etc/clusterguard/tls/ca.crt status
```

Agent 的 VIP 自动收敛默认禁用。必须先完成集群清单、VIP 唯一性、当前主库、Raft 多数租约和单次人工 reconcile 验证，再重新执行配置命令并增加：

```text
--activate-agent-reconcile --execute
```

## 9. 登录和首次初始化

新元数据存储的临时管理员为：

```text
用户名：admin
临时密码：admin123
```

首次登录必须立即修改密码。若页面未出现强制修改流程，应停止接入数据库并检查 Leader、Raft 元数据和浏览器会话，禁止手工编辑密码哈希。

## 10. 安装后检查

```bash
systemctl status clusterguard-ha.service --no-pager
systemctl status clusterguard-agent.service --no-pager
systemctl status clusterguard-agent-reconcile.timer --no-pager
journalctl -u clusterguard-ha.service -n 200 --no-pager
cgctl --server https://127.0.0.1:8088 \
  --ca-file /etc/clusterguard/tls/ca.crt engines
```

验收至少包括：

- 三控制节点只有一个 Leader
- voter 为奇数且 quorum 正常
- 控制台登录、修改密码和退出正常
- 四种引擎 capability 与实际实现一致
- 集群发现不会因 hostname、IP 或端口变化创建重复资源
- 计划切换后角色、复制、VIP/Listener/Service、审计和报告都通过验证

## 11. 升级

1. 导出审计和报告。
2. 备份 `/etc/clusterguard/`、`/var/lib/clusterguard/` 和当前 RPM。
3. 暂停自动切换与 Agent reconcile。
4. 先升级一个 follower，验证后升级第二个 follower，最后升级 Leader。
5. 每台执行 `dnf upgrade ./clusterguard-ha-新版本.rpm`。
6. 验证 `/readyz`、Raft quorum、发现、操作、审计和报告。

不要在混合版本阶段修改 Raft 协议或快照格式开关。

## 12. 卸载与回滚

卸载软件但保留配置和状态：

```bash
dnf remove clusterguard-ha
```

卸载脚本会停止并禁用服务，不删除 `/etc/clusterguard/` 和 `/var/lib/clusterguard/`。彻底清理必须经过变更审批并先备份。

应用回滚：

```bash
dnf downgrade ./clusterguard-ha-上一版本.x86_64.rpm
```

配置回滚可使用：

```text
/var/lib/clusterguard/config-backups/<UTC时间>/
```

禁止在仍运行的 Raft 集群中用单节点旧快照覆盖新快照。元数据灾难恢复必须先停止全部控制节点，确认恢复点一致，再按灾备流程整体恢复。
