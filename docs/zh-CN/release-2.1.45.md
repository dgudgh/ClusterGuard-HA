# ClusterGuard HA 2.1-45 发布说明

## 1. 发布定位

`2.1-45` 是 ClusterGuard HA 2.1 系列的最终封板版本。对应 Git 标签为
`v2.1.45`，对应源代码提交为：

```text
28425925a321655f38686942c6b081d235b0d288
```

GitHub Release：

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.1.45>

该版本发布后保持不可变。不得覆盖标签、替换附件或复用 `2.1-45` 文件名发布
不同内容。发现缺陷后应在后续版本线修复并重新发版。

## 2. 正式交付物

| 文件 | 用途 | SHA-256 |
| --- | --- | --- |
| `clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz` | 推荐的完整离线部署包 | `d4a46bdfa4c95bb641a7d19f063d2f43219177658b01b914a31a1cd5d06ec590` |
| `clusterguard-ha-2.1-45.x86_64.rpm` | ClusterGuard 节点软件包 | `1bc70109b556e973744bb05b5be0f53b075260910ae5def25518631ff8629a1c` |

完整离线包还附带两个 `.sha256` 文件。生产部署必须先校验摘要：

```bash
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.1-45-offline-linux-x86_64
sha256sum -c SHA256SUMS
```

只安装 RPM 不会自动形成三节点控制面、数据库复制和 VIP 仲裁。新建生产集群应
使用完整离线包中的 `install_clusterguard.sh`。

## 3. 2.1 支持范围

2.1 封板线的正式数据库支持范围是 MySQL 高可用控制，包括：

- 三个或更多奇数控制节点组成的 Raft 控制面；
- MySQL 拓扑发现、健康检查、候选评估和复制诊断；
- 受控主库切换、自动故障切换、主库与 VIP 一致迁移；
- 旧主回挂、增量恢复不可行时的全量重建；
- 固定平台节点身份、MySQL `server_uuid` 身份和端点别名修正；
- Safety Guard、操作锁、审批、验证、审计和报告；
- Agent 本地失联隔离、VIP 唯一所有权和重启收敛；
- 节点新增、修复、安装、同步及生命周期任务；
- 中文控制台、API、`cgctl`、systemd 和离线安装交付。

MySQL 兼容版本仍须以现场实际数据库包完成验收。版本名称相同但编译选项、认证
插件、系统库、存储和网络不同，不能直接视为同一套生产证据。

## 4. 不属于 2.1 的范围

下列能力不得以 `2.1-45` 名义承诺或交付：

- PostgreSQL 正式安装、切换、故障恢复和节点同步；
- Oracle Data Guard Broker 正式生产接管；
- SQL Server Always On 正式生产接管；
- 在已发布附件中追加新脚本、新依赖或新行为。

PostgreSQL 从 `2.2-1` 开始交付。Oracle 和 SQL Server 必须在各自完成独立测试
矩阵和现场验收后进入后续版本。

## 5. 新安装入口

完整参数及生产检查见[离线安装与部署手册](offline-rpm-install.md)。典型 MySQL
三节点混合部署先执行只读计划：

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -P 'SSH密码' \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --cluster-name production-mysql \
  --vip 192.168.102.155 \
  --interface ens160 \
  --known-hosts ./site/known_hosts \
  --plan
```

核对节点、版本、端口、VIP、网卡、数据目录和安装介质后，将末尾改为
`--execute`。不要把 `--execute` 单独放到下一条 Shell 命令中执行。

## 6. 生产准入

在正式业务接入前至少确认：

1. 三个控制节点健康，Leader 唯一且 Raft 多数可用。
2. 三个数据库实例身份唯一，复制线程正常且延迟满足业务要求。
3. VIP 只存在于当前主库，任一节点失去多数授权后能够本地撤销 VIP 并保持只读。
4. 计划切换、主库故障、旧主恢复、节点重启和整机关机流程均在现场通过。
5. 操作日志、审计、报告、监控接口和备份恢复流程可用。
6. 生产网络分区、存储异常和并发操作测试满足现场 RTO/RPO。

`2.1-45` 封板不等于免除现场验收。现场差异必须形成单独的验收记录。

## 7. 后续版本

- 2.1 系列停止增加功能。
- PostgreSQL 和后续改动进入 `codex/2.2-postgresql`。
- 第一个 2.2 正式包使用 `2.2-1`，后续每次交付依次使用 `2.2-2`、
  `2.2-3`，不得覆盖旧包。
- 详细规则见[版本与发版规范](version-release-policy.md)。
