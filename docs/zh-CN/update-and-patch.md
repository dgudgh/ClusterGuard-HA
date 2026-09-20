# ClusterGuard HA 版本升级与回退手册

<!-- LANGUAGE-SWITCH -->
> **语言：** [English](../en-US/update-and-patch.md) | 简体中文
<!-- /LANGUAGE-SWITCH -->

本文说明客户现场如何升级 ClusterGuard HA 控制平面。控制软件升级与数据库升级严格分离：升级器不会调用 MySQL、PostgreSQL、Oracle 或 SQL Server 客户端，不会停止数据库，也不会修改数据库软件、数据目录、复制关系或 VIP 配置。

> **制品说明：** `.cgupgrade` 是完整的签名滚动升级包，并非二进制差分小补丁。它同时携带目标 RPM 和当前版本 RPM，以便失败时自动回退。`*-offline-linux-*.tar.gz` 是新装或重装介质，不能直接上传到滚动升级页面。旧版本发布的 `.cgpatch` 继续兼容。

> **版本基线：** `2.2.39` 是首个正式受控升级基线。标准 `2.2-38` 没有受控升级框架，实验室 `2.2-38.field3` 的 RPM 记录与二进制合同也不一致，两者都不能直接上传通用升级包进入 `2.2.39`。先按本手册的历史版本桥接流程逐节点迁移到正式 `2.2.39`；从后续正式版本开始使用以 `2.2.39` 为来源版本的 `.cgupgrade`。

> **重要：系统升级期间无法进行自动切换，请注意关注。**
>
> 请在维护窗口执行升级，并安排人员持续观察数据库主从、VIP、业务连接、Raft 多数派和控制节点状态。维护标记建立后，计划切换、故障切换、自动故障切换和节点变更都会被 Safety Guard 阻断；只读拓扑、健康、指标和操作日志仍可使用。只有全部节点升级并验证通过、维护标记成功释放后，自动切换才会恢复。升级失败或中断时标记会保留，平台保持 fail-closed，必须续跑或受控回退。

## 控制台图形化升级

管理员可在 **设置 → 版本更新** 完成全流程，无需在页面外拼接命令：

1. 上传正式发布的已签名 `.cgupgrade` 文件。后端先校验升级包格式、SHA-256、发布签名、架构、源版本、目标版本和状态协议，未通过验签的文件不会进入执行区。
2. 点击 **生成升级计划**，核对升级包 ID、源版本、目标版本、滚动顺序、回退能力和节点清单。
3. 点击 **执行滚动升级**。确认框会再次显示停用自动切换的警告，并要求输入完整升级包 ID，避免误触和错包执行。执行器先把同一份签名升级包和元数据分发到全部控制节点，逐台复核 SHA-256 并原子发布，全部成功后才建立维护门禁。
4. 升级期间控制台顶部持续显示“系统升级期间无法进行自动切换，请注意关注。”，页面实时展示节点事件、状态和原始输出。
5. 成功后确认维护提示消失并复核拓扑、VIP 和复制；失败时使用 **续跑升级** 或 **受控回退**，不要手工删除维护标记。

上传和编排 API 只接受已登录管理员，并通过 CSRF、Raft Leader 转发、签名信任、受限 root Helper 和审计链路。升级包保存在受保护的数据目录，同一升级包 ID 不允许被不同内容覆盖。

首次安装时应将升级包发布公钥交给安装器。公钥可以随正式离线包交付，私钥绝不能进入客户现场：

```bash
./install_clusterguard.sh \
  ... \
  --patch-trust-key ./trust/patch-signing-public.pem \
  --execute
```

安装器会在所有控制节点配置 `/etc/clusterguard/update.json`、发布公钥和 `clusterguard-update-helper.service`。未配置可信公钥时，控制台会明确显示版本更新不可用，不会降级为未验签安装。

### 升级包保留策略

升级材料默认保留最近 **3 个版本**。配置项位于每个控制节点的 `/etc/clusterguard/update.json`：

```json
{
  "trust_key": "/etc/clusterguard/trust/patch-signing-public.pem",
  "deployment_state": "/etc/clusterguard/deployment-state.json",
  "ssh_user": "root",
  "ssh_key": "/etc/clusterguard/ssh/controller_ed25519",
  "known_hosts": "/etc/clusterguard/ssh/controller_known_hosts",
  "ssh_port": 22,
  "api_port": 3000,
  "retained_versions": 3,
  "controllers": [],
  "data_nodes": []
}
```

只调整 `retained_versions` 一个键，**不要整体覆盖该文件**：`trust_key`、`deployment_state`、
`ssh_key`、`known_hosts` 由安装器写入，升级作业用 `jq -er` 强制读取，任一缺失都会直接拒绝执行升级。

完整升级或自动回退结束并释放维护门禁后，特权升级链路会在所有节点清理旧目录。控制节点的上传包、状态和事件位于 `/var/lib/clusterguard/updates/<patch_id>/`，各节点用于受控回退的 RPM 和配置备份位于 `/var/lib/clusterguard-update-private/history/<patch_id>/`。只生成计划不会触发清理，保证只读计划不修改节点。

这里的“3 个版本”只指升级安装包、回退 RPM、配置备份和对应的升级状态事件，不是操作日志条数。高可用切换与审计日志不参与升级包清理，也不会因为 `retained_versions` 被截断；控制台操作日志默认查看全部集群，并可单独按集群筛选。

当前执行包始终受保护。处于排队或运行状态、仍有维护门禁，或标记为“操作结果需要验证”的包不会计入 3 个普通保留名额，也不会被自动删除。因此事故未闭环时磁盘上可能暂时多于 3 个版本，安全状态处理完成后，下一次成功升级或完整回退会再次执行清理。修改 `retained_versions` 时必须使用大于 0 的整数。

## 1. 升级架构

每个正式 RPM 都内嵌以下不可变运行合同：

- 产品名、版本、发行序号和 Git 提交；
- 构建时间、操作系统和 RPM 架构；
- `state_format` 元数据格式；
- `update_protocol` 升级协议。

可通过以下命令读取：

```bash
clusterguard --version-json
cgctl --json version
curl --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:3000/api/v1/platform/version
```

签名升级包使用 `.cgupgrade` 后缀，包含目标 RPM、回退 RPM、包内引导升级器、兼容性清单、SHA-256 摘要和发布签名。现场只保存发布公钥，发布私钥必须离线保管，不得放入安装包或客户服务器。旧 `.cgpatch` 后缀只作为兼容入口保留。

```text
clusterguard-patch/
├── PATCH-MANIFEST.json
├── PATCH-MANIFEST.sig
├── SHA256SUMS
├── bootstrap/
│   └── clusterguard-upgrade.sh
└── payload/
    ├── clusterguard-ha-旧版本.rpm
    └── clusterguard-ha-新版本.rpm
```

当前节点已经安装的升级器只负责安全解包、发布签名和引导器 SHA-256 校验。计划、滚动、收敛等待、断点续跑和回退由验签后的包内引导升级器执行。这样修复升级编排逻辑时，不必等目标 RPM 安装完成后才能生效，也不会继续使用源版本中已经过时的滚动逻辑。引导器被篡改、清单缺失或协议不兼容时，升级在建立维护门禁前失败。

现场升级由七层合同共同约束：

1. **版本合同**：RPM、运行中二进制和升级包清单的版本、架构、`state_format`、`update_protocol` 必须一致。
2. **节点身份合同**：部署清单、每台节点 `/etc/clusterguard/node.json` 中的不可变 UUID、实时 Raft voter 和活动数据节点清单必须完全一致。hostname、IP 或节点数量变化不会靠猜测处理。
3. **维护事务合同**：升级前在所有控制节点建立同一 `patch_id` 的持久维护标记，滚动完成并复核后才整体释放。任一节点释放失败会触发全节点补偿回锁。
4. **引导执行合同**：新 `.cgupgrade` 必须携带清单声明且经过发布签名覆盖的引导升级器。控制台拒绝缺少有效引导器的新升级包；旧 `.cgpatch` 仅用于历史兼容。
5. **升级包驻留合同**：图形化执行、续跑和回退开始前，`package.cgpatch` 与 `package.json` 必须复制到每个控制节点的同一升级目录，远端 SHA-256 必须与本地已验签制品完全一致，再以原子重命名发布。任一节点分发或校验失败都会在维护门禁和 RPM 变更之前终止，已发布的相同只读副本可安全复用。
6. **任务证据合同**：升级目录对 `root` Helper 和 `clusterguard` 控制台保持受限协作写入，状态、输出和事件保持组可读。升级脚本已经写出的计划、成功、失败或回退终态不得再由通用进程退出错误覆盖。
7. **变更准入合同**：执行阶段先锁定当前 Leader，再锁定 followers，随后等待既有任务排空；释放阶段 followers 在前、当前 Leader 最后。这样自动恢复不会在门禁建立或释放的半完成窗口抢占升级。

初始安装器会先登记控制节点、数据节点和混合节点的不可变身份，再进行数据库集群发现。后续扩容、退役或替换节点必须通过节点生命周期流程更新资源清单，升级器不会遗漏清单外的活动节点。

## 2. 支持边界

| 变更类型 | 处理方式 |
| --- | --- |
| 同一版本线修复，例如 `2.2-28` 到 `2.2-29` | 使用签名 `.cgupgrade` 滚动升级 |
| 功能版本升级且状态合同不变 | 完成兼容验收后可使用签名升级包 |
| `state_format` 或 `update_protocol` 不兼容 | 当前升级协议拒绝执行，必须使用专用迁移版本 |
| MySQL/PostgreSQL 等数据库升级 | 使用独立数据库升级流程，不得混入控制面升级包 |
| 控制节点不足多数派、存在活动操作或节点任务 | 阻断升级 |

当前升级协议只允许清单声明的状态格式范围，不执行破坏性元数据降级。未来状态格式变化采用“先扩展读取、再切换写入、最后清理旧格式”的两阶段迁移版本，不允许普通升级包直接改写不可逆状态。

## 3. 发布侧构建签名升级包

从干净、已测试的提交分别构建旧版和新版 RPM，再使用离线发布私钥签名：

```bash
scripts/build-clusterguard-patch.sh \
  --from-rpm dist/clusterguard-ha-2.2-28.x86_64.rpm \
  --to-rpm dist/clusterguard-ha-2.2-29.x86_64.rpm \
  --signing-key /secure/offline/clusterguard-patch-signing.key \
  --expected-public-key site-trust/patch-signing-public.pem \
  --output dist/clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade
```

发布前必须从现场所有控制节点读取 `update.json` 指向的实际受信公钥，并确认三节点公钥指纹、离线私钥导出的公钥指纹和升级包验签公钥指纹完全一致。不得因为历史发布目录中的公钥文件名看起来正确，就把它当作现场信任链。构建完成后还必须登录当前 Leader 的控制台真实上传一次，只生成“已上传并通过签名校验”的记录，不执行升级。现场上传没有成功前，不得把升级包标记为可交付。

`2.2-59` 到 `2.2-60` 的现场验签失败、流程根因和强制发布门禁见 [升级包验签失败复盘（2026-08-31）](update-signature-incident-2026-08-31.md)。

发布物必须同时交付升级包、升级包 SHA-256、发布说明和独立渠道提供的签名公钥指纹。不得覆盖同名升级包。

## 4. 现场准备

1. 将发布公钥预置到受保护目录：

   ```bash
   install -d -m 0750 -o root -g clusterguard /etc/clusterguard/trust
   install -m 0640 -o root -g clusterguard clusterguard-patch-signing-public.pem \
     /etc/clusterguard/trust/patch-signing-public.pem
   ```

2. 准备安装器生成的 `clusterguard-deployment-state.json`。节点发生扩容、退役或替换后，应使用当前清单，或在升级命令中显式传入完整的 `--controllers` 和 `--data-nodes`；显式参数优先于旧状态文件。
3. 确认全部控制节点在线、Raft 为奇数且至少三个、只有一个 Leader。
4. 确认没有正在执行或结果不确定的数据库操作，也没有节点增删、重建或同步任务。
5. 备份 `/etc/clusterguard/`、`/var/lib/clusterguard/` 和现场部署状态文件。
6. 确认 SSH 主机密钥可信；生产环境优先使用专用 SSH 私钥。

## 5. 四步升级

### 验证升级包

此步骤不连接远端节点：

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --inspect
```

必须看到 `signature=verified`、正确的源/目标版本、`rollback=available`、`database_mutation=false`、`bootstrap=available` 和 `bootstrap_protocol=1`。

执行计划或升级时，原版本升级器完成同样的验签后会输出“签名引导升级器校验通过，切换到升级包内执行器”。未出现该记录时不得执行正式变更。

### 生成现场计划

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /etc/clusterguard/ssh/controller_ed25519 \
  --known-hosts /etc/clusterguard/ssh/controller_known_hosts \
  --plan
```

计划固定为：控制节点 Follower、仅数据 Agent 节点、最后处理当前 Leader。所有节点必须处于升级包清单允许的源版本或目标版本，并通过二进制版本合同校验。升级器会读取每台数据节点的固定 UUID，并把控制节点 UUID 与实时 Raft voter、数据节点 UUID 与实时活动数据节点清单分别逐项比对。任一控制节点观测不同、重复 UUID、节点角色不符或清单过期，都会在建立维护锁之前阻断，避免漏升级、串节点和部分门禁。

### 执行滚动升级

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /etc/clusterguard/ssh/controller_ed25519 \
  --known-hosts /etc/clusterguard/ssh/controller_known_hosts \
  --execute
```

执行器在每台节点前重新检查多数派、Leader、就绪状态、活动操作和节点任务。升级开始后，会在所有控制节点写入 `/etc/clusterguard/update-maintenance.json`：

在写入维护标记之前，控制台升级器先把完整签名升级包和元数据分发到全部控制节点，并在远端重新校验 SHA-256。只有全部副本原子发布成功才继续；复制中断、磁盘不足、权限错误或摘要不一致都不会进入维护态，也不会安装任何 RPM。这样升级过程中即使 Raft Leader 变化，新 Leader 仍能从本机受保护目录读取同一签名包并执行续跑或回退。

- 页面和 API 的变更请求返回 `423 Locked`；
- 自动故障切换经过同一 Safety Guard，被一致阻断；
- 节点增删、重建和同步任务被阻断；
- 拓扑、健康、指标、日志和控制面状态仍可读取；
- 每次只重启当前正在升级的 ClusterGuard 服务，不重启数据库。

节点升级完成不只检查 RPM 版本。控制节点必须重新加入并看到稳定 Leader；数据节点必须同时恢复 `clusterguard-agent.service` 和 `clusterguard-agent-reconcile.timer`。控制与数据同机的混合节点必须同时满足两组条件，任何一组失败都会停止后续滚动并进入回退。

### 验证结果

完成后检查：

```bash
cgctl --json version
cgctl --json status
systemctl --no-pager --full status clusterguard-ha
test ! -e /etc/clusterguard/update-maintenance.json
```

执行目录会生成：

```text
clusterguard-update-补丁ID.json
clusterguard-update-补丁ID.events.jsonl
```

前者记录当前结果，后者逐行记录开始、节点更新、验证、失败、回退和完成事件。应与补丁、发布说明和变更工单一起归档。

## 6. 中断续跑与自动回退

正常错误、终止信号或节点失败会停止后续节点，并使用升级包内旧 RPM 逆序回退本次已更新节点。失败节点本身也会进入回退集合。

若升级终端被强制杀死或管理机断电，维护标记会故意保留，使平台保持 fail-closed。恢复后使用同一个签名升级包续跑：

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /etc/clusterguard/ssh/controller_ed25519 \
  --known-hosts /etc/clusterguard/ssh/controller_known_hosts \
  --resume --execute
```

`--resume` 只接受锁中相同的 `patch_id`。图形化升级在建立锁前已经把同一升级包复制到全部控制节点，因此 Leader 变化后可由新 Leader 使用本机副本续跑；包摘要或元数据不一致时仍会拒绝执行。不要手工删除维护标记或锁；这样会绕过版本和多数派复核。正常结束时，升级器会先确认所有控制节点上的锁和维护标记都属于当前升级包，再开始释放；若释放过程中任何节点失败，升级器会把相同锁补偿写回全部控制节点并保持 fail-closed，待连通性恢复后再续跑。

需要主动回到旧版本时：

```bash
clusterguard-upgrade \
  --package clusterguard-ha-2.2-28_to_2.2-29.x86_64.cgupgrade \
  --trust-key /etc/clusterguard/trust/patch-signing-public.pem \
  --state ./clusterguard-deployment-state.json \
  --ssh-key /etc/clusterguard/ssh/controller_ed25519 \
  --known-hosts /etc/clusterguard/ssh/controller_known_hosts \
  --rollback --execute
```

## 7. 首次纳入受控升级

早于受控升级协议的历史版本没有 `--version-json` 和统一维护门禁，不能直接执行 `.cgupgrade`。首次升级必须使用对应版本发布说明中的桥接维护流程，停止自动变更入口后逐节点安装桥接 RPM，并验证所有节点均满足：

```bash
clusterguard --version-json
cgctl --json status | jq '{maintenance:.result.update_maintenance_active, controllers:.result.controller_members, data_nodes:.result.data_node_members}'
```

全部节点进入同一 `state_format` 和 `update_protocol` 后，后续版本才能使用本文的签名滚动升级包。升级器遇到旧二进制时会明确拒绝，不会降级为无版本校验安装。

## 8. 生产准入清单

- 升级包来自正式 Release，签名和 SHA-256 均通过；
- 公钥指纹通过独立渠道核对；
- 当前版本、目标版本、架构和状态合同一致；
- 三个或更多奇数控制节点健康且多数派稳定；
- 固定节点 UUID、实时 Raft voter、活动数据节点和升级目标清单完全一致；
- 无活动操作、无结果不确定操作、无节点生命周期任务；
- 混合节点的控制服务、Agent 和 reconcile timer 均可用；
- 已在与生产一致的预发布环境完成升级、回退、断点续跑和 Leader 最后升级测试；
- 已准备管理机故障、节点重启、磁盘空间不足和网络中断处置窗口；
- 升级后重新执行数据库拓扑、VIP 唯一性、自动故障切换和旧主恢复抽样验收。
