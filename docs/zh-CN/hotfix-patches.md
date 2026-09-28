# 热修补丁台账

> 本文件由 `scripts/render-hotfix-catalog.cjs` 依据磁盘上真实存在的签名补丁包生成，
> 请勿手工编辑：重新构建补丁包后重新生成本文件。
> `tools/verify-hotfix-patch-catalog.cjs` 会在“修复提交没有补丁包”或“本文件与产物不一致”时失败。

`.cgupgrade` 携带完整 RPM，只能由滚动升级执行器应用；热修补丁包只携带它声明的那些 bug
修复真正改动的东西：重新构建的二进制、被修改的 systemd 单元、作为证据的源码差异，以及
一对 apply/rollback 脚本。针对已发布版本的每一个 bug 修复都必须被某个补丁包覆盖，否则
现场只能等下一个完整版本才能拿到修复。

补丁按“一次现场处理”打包，不按提交拆分：两个都替换 `/usr/local/bin/clusterguard` 的
补丁如果叠加，结果取决于安装顺序——后装旧的会把新修复盖掉。**只装最新的那一个包。**

| 补丁编号 | 严重级别 | 覆盖修复提交 | 构建树 | 产物 |
| --- | --- | --- | --- | --- |
| HF-2026-0928-01 | P0 | `ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039` | `d296f5f` | `clusterguard-ha-hotfix-HF-2026-0928-01-2.2-103.x86_64.cgpatch` |

## 应用补丁

```bash
tar -xzf release/2.2-103-hotfixes/<产物文件名>.cgpatch
cd clusterguard-hotfix
bash apply.sh            # 备份、校验 SHA-256、安装、daemon-reload
systemctl restart <单元> # apply.sh 只打印需要重启的单元，不自动重启
bash rollback.sh         # 按最新备份清单回滚
```

## HF-2026-0928-01 — 2.2-103 现场修复合集：写入者抖动、升级链前置、控制台原因与关机收尾

- 严重级别：P0
- 覆盖修复提交：`ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`
- 构建树：`d296f5f1abf1bb754d1287a34473a1c13ee8505f`（基线 `467e533` + 上述修复，不含其它提交）
- 适用版本：2.2-103 → 2.2-103+hf-2026-0928-01（x86_64）
- 产物：`release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-01-2.2-103.x86_64.cgpatch`
- SHA-256：`12557ee6affc13127070ce200a67af83052415bde8e3365642030de82202f95c`
- 源码差异：`src/HF-2026-0928-01-d296f5f.patch`
- 交付内容：
  - `payload/bin/clusterguard-agent` → `/usr/local/bin/clusterguard-agent`（0755）
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/systemd/clusterguard-agent-reconcile.service` → `/usr/lib/systemd/system/clusterguard-agent-reconcile.service`（0644）
  - `payload/systemd/clusterguard-update-helper.service` → `/usr/lib/systemd/system/clusterguard-update-helper.service`（0644）
  - `payload/installer/install_clusterguard.sh` → `仅安装器，现场无对应路径`（0755）
- 需要重启：`clusterguard-ha.service`、`clusterguard-agent-reconcile.service`、`clusterguard-update-helper.service`

### 本包概要

一个包覆盖 2.2-103 基线上缺失的四个修复。只装这一个包即可，不要与其它补丁混用：两个修复都替换 /usr/local/bin/clusterguard，多包叠加会因顺序不同而互相覆盖。构建树是 2.2-103 基线加上四个修复（移植分支 hotfix/2.2-103-fixes，d296f5f），不含开发线上未发布的功能提交。

### HF-2026-0928-01.1 MySQL 写入者协调抖动：授权主库每 5 秒自隔离一次（`dd82ca5`，P0）

- 现象：集群长期 degraded、两条复制链路 unhealthy、候选评估 409、计划关机必被阻断。实测自安装起 9492 次自隔离，read_only 与 VIP 每约 10 秒同步翻转一次。

- 根因：① agent-reconcile 单元的 CapabilityBoundingSet 缺 CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH，root 也读不了 <datadir>/mysqld-auto.cnf（mysql:mysql 0640），IsolationStatus 报错即触发失败关闭；② convergeWritableRestartState 要求 RestartReadOnly 变为 false，但“重启后只读”是永久站点不变量，条件永不满足。

- 修复：收敛判定改为只看“隔离意图已清除 + 运行时可写”（writableRestartStateConverged），不再要求重启栅栏消失；单元补 CAP_DAC_OVERRIDE 与 CAP_DAC_READ_SEARCH（bounding 与 ambient 都补）。

- 何时需要应用：
  - 集群长期 degraded 且复制链路 unhealthy，但复制本身正常
  - journalctl -u clusterguard-agent-reconcile.service 反复出现 self-isolated 或 permission denied

### HF-2026-0928-01.2 升级执行链两个前置缺陷：SSH 私钥属主与 Helper 共用运行时目录（`d5f9491`，P0）

- 现象：版本更新始终 available=false，安装/上传升级包的三个控件全灰；即便手工补齐 update.json，升级执行器仍以“私钥权限过宽”拒绝；Helper 首启报 status=233，且停机时把 /run/clusterguard 整个删掉。

- 根因：① update.json 的 ssh_key 指向 clusterguard 属主的私钥，而 workspace check-file 要求 root 属主且非组/其他可写；② Helper 单元用 RuntimeDirectory=clusterguard 声明了一个共用目录，启动 chown/chmod 存在竞态，停止时 systemd 会删除该目录。

- 修复：安装器额外生成 root:root 0600 的私钥副本 /etc/clusterguard/updates/controller_ed25519 并写入 update.json；Helper 单元去掉 RuntimeDirectory，改为 ExecStartPre=/usr/bin/install -d -m 0750 -o root -g clusterguard /run/clusterguard（幂等且不再“拥有”该目录）。

- 何时需要应用：
  - 控制台版本更新长期 available=false
  - clusterguard-update-helper 首启失败或停机后 /run/clusterguard 丢失

### HF-2026-0928-01.3 控制台不解释“不可用”的原因（`ed9faca`，P1）

- 现象：版本更新面板只显示红徽标“不可用”，不写原因；集群加载横幅只报栏目名（“部分数据不可用：候选评估；操作已锁定”），运维无法判断下一步做什么。

- 根因：前端丢弃了后端给出的原因：`evidenceResult` 只保留布尔状态，409 响应体里的具体原因没有回填到面板与横幅，运维只能看到状态标签。

- 修复：面板内联渲染 `#software-update-panel-reason` 并在徽标上加 tooltip；`evidenceResult` 携带 `reason`，`evidenceReasonText()` 把 409 消息映射为中文，`evidenceUnavailableText()` 按栏目回填原因。

- 何时需要应用：
  - 控制台版本更新面板显示“不可用”但无原因
  - 集群加载横幅只报栏目名、不报原因

### HF-2026-0928-01.4 整机关机提交后控制台卡在无法交互的对话框上（`5ae2039`，P1）

- 现象：提交整机关机后主机断电、控制面随之消失，页面永久停在关机确认对话框上，只能手动关闭标签页；提交后响应丢失时还会误报为错误。

- 根因：poweroff 分支提交成功后只改了结果横幅，既不关闭对话框也不做收尾；主机断电后页面失去所有交互入口，而“已提交却丢了响应”本就是预期结果。

- 修复：新增 settlePoweroffConsole()：等 2.5s 后带 3s 超时探测 /power/status，不可达则显示离线层并 window.close()，仍可达则只关对话框并起 5s 看门狗；service 模式永不关页面。

- 何时需要应用：
  - 提交整机关机后控制台卡死、需要手动关闭标签页
  - 关机提交后偶发“无法连接控制 API”被当作失败

### 验证

- `/usr/local/bin/clusterguard --version`
- `/usr/local/bin/clusterguard-agent --version`
- `systemctl is-active clusterguard-ha`
- `systemctl show clusterguard-agent-reconcile.service -p CapabilityBoundingSet`
- `systemctl cat clusterguard-update-helper.service | grep -c RuntimeDirectory`
- `journalctl -u clusterguard-agent-reconcile.service --since '-10min' | grep -c self-isolated`
- `jq -c '.topology_snapshots[] | {observed_at, health: .health.state}' /var/lib/clusterguard/metadata.json`

### 回滚

执行 rollback.sh 恢复旧二进制与旧单元后 systemctl daemon-reload 并 systemctl restart clusterguard-ha。注意：回滚会重新引入写入者抖动与升级链阻塞，仅在确认新二进制有回归时使用。

