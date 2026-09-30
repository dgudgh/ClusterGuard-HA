# 升级动作对象错配与热修恢复语义缺失复盘（2026-09-30）

## 摘要

现场在 2026-09-30 13:30（北京）对一个**已经安装成功**的热修补丁发起“续跑升级”，结果是：三台控制节点里**只有作业实际执行的那一台**（Leader `.153`）的安装记录被改写成失败，另外两台仍记录成功。控制台读的是 Leader 那台，于是界面上长期显示“升级失败”，而磁盘上三台运行的载荷完全一致。

这不是“热修幂等重试实现失败”。它是三件事叠在一起：

1. **控制台把动作提交到了错的升级记录上**——按钮开关依据的记录与真正 POST 的 `patch_id` 不是同一个对象；
2. **恢复语义没有按包种类分流**——“普通升级失败 → 续跑”被无差别套用到热修补丁上，而热修补丁没有可续跑的断点，它的正确恢复动作是**重新执行同一个补丁**；
3. **服务端没有最后一道兜底**——即使前端修好，`kind=hotfix + mode=resume` 也必须在写任何文件之前被拒绝，否则一次“必然被拒绝”的动作会被记成这个补丁自己的结果。

三层都在 `HF-2026-0930-01` 里修复。其中第 3 层是这次复盘最关键的结论：**拒绝本身必须不写记录**。一个只造成拒绝的尝试若覆盖了真正完成安装的那次执行，操作者就再也无法从控制台回答“这个补丁到底装上没有”。

## 现场证据

### 三节点的安装记录不一致

`/var/lib/clusterguard/updates/HF-2026-0929-05/status.json`：

| 节点 | `mode` / `status` | 时间 | 文件 mtime |
| --- | --- | --- | --- |
| `.152` | `execute` / `succeeded` | `01:30:59Z` 结束 | 09-30 09:30:59 |
| `.153`（Leader） | **`resume` / `failed`** | `05:30:29Z` → `05:30:30Z` | 09-30 **13:30:30** |
| `.154` | `execute` / `succeeded` | `01:30:59Z` 结束 | 09-30 09:31:01 |

`.153` 的记录内容为 `升级任务失败或被阻断：热修补丁不支持 --resume：应用本身是幂等的，直接重新执行同一个补丁即可`。

### 事件日志三台完全一致

三台的 `events.jsonl` 都是 13 行、mtime 都停在 09-30 09:31、最后一条都是 `succeeded`（`all node digests and maintenance release verified`）。**被拒绝的那次尝试没有产生任何事件**。

### 由此得到的两个结论

- 记录覆盖**不是集群范围的**：升级包存储与作业目录是各控制节点本地的，`POST` 不经 Leader 转发，作业在操作者所连的那台机器上执行，所以只有 Leader 那台的记录被改写。**控制台显示的状态等于 Leader 那台的状态，不等于集群状态**——排查“这个补丁装上没有”必须三台分别读盘，不能只信控制台。
- 同一条记录上“事件链成功”与“状态失败”可以长期共存，因为二者来源不同：状态由作业封装器写入并可被后续操作覆盖，事件只由真正执行的那次尝试追加。

## 三层根因与修复

| 层 | 缺陷 | 修复 |
| --- | --- | --- |
| 一、控制台对象绑定 | 按钮的启用状态取自“最新的可操作记录”，而 `patch_id` 取自“列表里最新的一行”，二者不是同一个对象。于是画面显示“正在续跑 HF-04”，实际 POST 的是 HF-05 的 `/resume`。回退按钮有同样缺陷，且更危险：它可能回退一个操作者从未选中的包 | 引入单一 `softwareUpdateSubject`，渲染主体、动作主体、确认主体与 POST 的 `patch_id` 全部取自它；`packages[0]` 不再作为动作对象（`9fdb0e7`） |
| 二、恢复语义路由 | `scripts/clusterguard-update-job.sh` 是真正的上层操作编排器：它把 `mode` 翻译成升级器参数，但**不看包种类**，于是热修也被统一下发 `--resume`，而这个入口对热修**永远失败** | 按已快照的签名元数据 `package.json` 的 `kind` 分流：`resume + upgrade → --resume`；`resume + hotfix → 重新执行同一个补丁`，并在输出里说明这次演替（`8be2e3d`） |
| 三、服务端兜底 | `platformupdate.Manager.Start` 对 `ModeResume` 没有前置条件：它先写作业文件再启动 Helper，Helper 把启动失败改写成 `failed`，于是拒绝成了这个补丁的记录 | 在**写任何文件之前**拒绝 `mode == ModeResume && kind == hotfix`，返回 `ErrResumeUnsupported`，API 映射为 409；记录原样保留（`9fdb0e7`） |

第 3 层采用“直接拒绝”而不是“服务端把续跑规范化成重试”：`resume ≠ retry`，API 层不偷偷改变用户动作的语义。控制台对失败的热修显示「重新执行」而不是「续跑升级」，日志、审计、API 与操作者的理解因此一致。

### 门禁归属：同包可重入，异包必拒绝

热修补丁失败后可以重新执行同一个补丁，但**不能接管别的补丁留下的维护门禁**：`HF-04` 留下的锁，`HF-05` 不得因为“都是热修”就抢过来。判据只有 `patch_id`，不是 `mode`——热修写进 `/etc/clusterguard/update-maintenance.json` 的 `mode` 同样也是 `rolling_update`，所以 **marker 的 `mode` 从来不是热修/滚动的判据**。脚本层原有的两条接管路径本来就按包身份严格划分（自己的锁比对自己、失败的滚动升级锁要求 `≠` 自己），本次新增的只是在热修流程里**把持有者名字说出来**，不放松任何准入。

## `HF-2026-0930-01` r1 交付物

| 项 | 值 |
| --- | --- |
| 产物 | `clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch` |
| SHA-256 | `9a5af9bda61e81f87af686fd551ee9b90d04808d1f160df259cc84f4253b8fcd` |
| 大小 | 8,758,333 |
| `--inspect` | `signature=verified` / `kind=hotfix` / `rollback=available` / `database_mutation=false` |
| 载荷 | 2 个二进制 + 2 个运行时脚本（`clusterguard-update-job.sh`、`clusterguard-upgrade`），与源码树逐字节一致 |
| 重启单元 | `clusterguard-ha.service`、`clusterguard-update-helper.service` |

这个包**会重启控制面**，所以应用期间控制台页面短时不可用是**预期行为**，不是升级失败。它取代 `revision 0`（`baf16ebc`）：r0 只堵住了控制台把动作打到错的记录上，没有改上层编排器的续跑语义；两次构建都没有交付任何现场，取代理由与该字段的取值规范见[热修补丁台账](hotfix-patches.md)。

## 现场上线与验收

### 上线步骤

上传 `HF-2026-0930-01` r1 → 正常执行 → 等待两个服务重启 → 重新登录或刷新控制台 → 按下面的顺序只读验证。

**上线之前，不要对任何失败的热修补丁点「续跑」**：现场仍运行旧控制台与旧编排器，动作会落到列表里最新的那条记录上。

### 只读验证（先看真实状态，不先看历史记录的颜色）

```bash
set -a; . /etc/clusterguard/agent.env; set +a
# 1. 三个控制节点的进程与服务
systemctl is-active clusterguard-ha.service clusterguard-update-helper.service
# 2. 控制面状态：就绪、投票者、唯一 Leader、维护门禁与活动操作
curl -sk -H "Authorization: Bearer $CG_CONTROL_TOKEN" https://127.0.0.1:3000/api/v1/control-plane/status
# 3. 两个运行时脚本在三台的摘要必须一致
sha256sum /usr/local/libexec/clusterguard-update-job.sh /usr/local/sbin/clusterguard-upgrade
```

逐项期望：三台 `clusterguard-ha.service`、`clusterguard-update-helper.service` 均为 `active`；`ready=true`；`voters=3`；`unique leader=1`；`update_maintenance_active=false`；`active_operations=0`；两个脚本在三台的摘要两两相同；本包的安装记录为 `succeeded`。

### 控制台回归（上线后第一件事，不是再升一次级）

| # | 场景 | 期望 |
| --- | --- | --- |
| A | 已成功的热修补丁 | **不出现**「续跑」，也不出现「重新执行」 |
| B | 失败的热修补丁 | 显示「重新执行」，**不显示**「续跑」 |
| C | 失败的滚动升级 | 显示「续跑」 |
| D | `HF-04` 为 failed、`HF-05` 为最新且 succeeded 时点击旧记录的动作 | POST 的必须是该记录自己的 `patch_id`，不是列表最新那条 |
| E | `HF-04` 的受控回退 | 必须操作 `HF-04`，**不得**回退到列表里最新的一行 |

D 与 E 是这次事故的核心回归项。服务端侧可独立复核：动作被拒时对应补丁目录下 `status.json` 的 mtime 与内容必须**完全不变**（这正是第 3 层的验收点，可在任意失败热修上直接验证）：

```bash
curl -sk -X POST -H "Authorization: Bearer $CG_CONTROL_TOKEN" -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' -d '{"confirmation":"<patch-id>"}' \
  https://127.0.0.1:3000/api/v1/platform/updates/<patch-id>/resume   # 期望 409
stat -c '%y' /var/lib/clusterguard/updates/<patch-id>/status.json    # 期望与调用前相同
```

## 未闭环

1. **`UPDATE-OPERATION-HISTORY-P0`：把“包安装状态”与“操作执行历史”分离。** 现在的升级记录是**可覆盖状态模型**：一个 `package` 只有一个作业槽，后来的操作覆盖先前结果。所以 `HF-05` 会呈现成“事件链完整成功 / 状态失败 / 结果栏只有续跑拒绝”，而正确的呈现应当是“包已安装”＋“最近一次操作：续跑被拒”。要求：包部署结果一旦完成就不可被后续操作改写，每次操作**追加**一条操作记录，而不是覆盖包的安装状态。这是从根上消除“成功包被一次错误操作改成失败包”的办法，也是本次三层修复的收尾（三层阻止了伤害发生，这一项消除伤害的**可记录空间**）。
2. **`kind` 不可判定时的恢复语义仍是猜的。** `platformupdate.Manager.Start` 的拒绝判据是 `Kind == PackageKindHotfix`，而 `Manager.Package()` 直接反序列化磁盘 `package.json`，**读侧没有兜底**；`--inspect` 侧的兜底是“没有 `kind` 就当滚动升级”。因此一个 `package.json` 缺 `kind` 的热修补丁，其续跑会被放过并进入编排器（`inspector.go` 的兜底只作用于 Inspect 返回路径，救不了这里）。当前现场不可达：三个热修补丁 `HF-2026-0928-06`/`HF-2026-0929-04`/`HF-2026-0929-05` 的 `package.json` 都带 `kind=hotfix`，唯一缺 `kind` 的是 2.2-103 期上传的滚动包 `cgupgrade-2.2-103-to-2.2-104-x86_64`（它确实该被当作滚动包）。要让它成为结构上的保证而不是当前的巧合，需要在**上传侧**拒绝 `kind` 无法判定的包（两种恢复语义相反，无法判定就不能安全恢复），此后读侧的判定才是墙。
