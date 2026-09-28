# 热修补丁台账

> 本文件由 `scripts/render-hotfix-catalog.cjs` 依据磁盘上真实存在的签名补丁包生成，
> 请勿手工编辑：重新构建补丁包后重新生成本文件。
> `tools/verify-hotfix-patch-catalog.cjs` 会在“修复提交没有补丁包”或“本文件与产物不一致”时失败。

`.cgupgrade` 携带完整 RPM，只能由滚动升级执行器应用；热修补丁包只携带它声明的那些 bug
修复真正改动的东西：重新构建的二进制、被修改的 systemd 单元、作为证据的源码差异，以及
一对 apply/rollback 脚本。针对已发布版本的每一个 bug 修复都必须被某个补丁包覆盖，否则
现场只能等下一个完整版本才能拿到修复。

补丁按“一次现场处理”打包，不按提交拆分：两个都替换 `/usr/local/bin/clusterguard` 的
补丁如果叠加，结果取决于安装顺序——后装旧的会把新修复盖掉。**只装你所在基线版本的最新一个包，
不要混装不同基线版本的包**——装错基线的包会把二进制悄悄降级回它自己的发布线。

| 补丁编号 | 严重级别 | 覆盖修复提交 | 构建树 | 产物 |
| --- | --- | --- | --- | --- |
| HF-2026-0928-05 | P0 | `ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47`、`9303e9d`、`18d738e` | `18d738e` | `release/2.2-104-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-05-2.2-104.x86_64.cgpatch` |
| HF-2026-0928-02 | P0 | `ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47` | `f90f995` | `release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-02-2.2-103.x86_64.cgpatch` |

## 应用补丁

```bash
tar -xzf release/<基线版本>-hotfixes/<产物文件名>.cgpatch
cd clusterguard-hotfix
bash apply.sh            # 备份、校验 SHA-256、安装、daemon-reload
systemctl restart <单元> # apply.sh 只打印需要重启的单元，不自动重启
bash rollback.sh         # 按最新备份清单回滚
```

## HF-2026-0928-05 — 2.2-104 现场修复合集（累积）：整机重启后集群不可用、写入者抖动、升级链前置、控制台原因与关机收尾、热修包误传控制台

- 严重级别：P0
- 覆盖修复提交：`ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47`、`9303e9d`、`18d738e`
- 构建树：`18d738e25a7bd71bb47fcb920ce45dc42411489b`（基线 `e01f5ce376f94e2595590358c72dd2585e7c09b4` + 上述修复，不含其它提交）
- 适用版本：2.2-104 → 2.2-104+hf-2026-0928-05（x86_64）
- 产物：`release/2.2-104-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-05-2.2-104.x86_64.cgpatch`
- SHA-256：`58cdc380163f07d5193d4af8b6fbc1d5b3965c7d6abf8c07270d05d9a667d9f0`
- 源码差异：`src/HF-2026-0928-05-18d738e.patch`
- 交付内容：
  - `payload/bin/clusterguard-agent` → `/usr/local/bin/clusterguard-agent`（0755）
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/systemd/clusterguard-agent-reconcile.service` → `/usr/lib/systemd/system/clusterguard-agent-reconcile.service`（0644）
  - `payload/systemd/clusterguard-update-helper.service` → `/usr/lib/systemd/system/clusterguard-update-helper.service`（0644）
  - `payload/scripts/clusterguard-cluster-finalize.sh` → `/usr/local/libexec/clusterguard-cluster-finalize.sh`（0755）
  - `payload/scripts/clusterguard-mysql-install.sh` → `/usr/local/libexec/clusterguard-mysql-install.sh`（0755）
  - `payload/scripts/clusterguard-postgresql-install.sh` → `/usr/local/libexec/clusterguard-postgresql-install.sh`（0755）
  - `payload/scripts/clusterguard-upgrade.sh` → `/usr/local/sbin/clusterguard-upgrade`（0750）
  - `payload/installer/install_clusterguard.sh` → `仅安装器，现场无对应路径`（0755）
- 需要重启：`clusterguard-ha.service`、`clusterguard-agent-reconcile.service`、`clusterguard-update-helper.service`

### 本包概要

在 HF-2026-0928-04（用户已应用后仍看到旧报错）的基础上修掉一个投递缺陷（18d738e）：HF-04 把带修复的 upgrade 脚本装到了 /usr/local/libexec/clusterguard-upgrade.sh，而控制台上传校验执行的是打包清单声明的 /usr/local/sbin/clusterguard-upgrade（0750 root:clusterguard）——包装得干干净净、每台都报「已安装」，运维验的是错的文件，控制台照旧报「范围外路径」。热修补丁的落点、模式、属主现在一律由 packaging/rpm/nfpm.yaml 决定（单一真源），未在打包清单声明的运行时脚本直接构建失败。只装这一个包（它完整包含并取代 HF-2026-0928-03/04），不要与其它基线混装。

### HF-2026-0928-05.1 热修补丁的落点由来源名猜出，修复被装到产品不读的路径（`18d738e`，P1）

- 现象：HF-04 应用后三台脚本、单元、二进制全部显示「已安装」，但控制台上传热修包仍报「升级包包含范围外路径：clusterguard-hotfix/」：带修复的脚本被装到 /usr/local/libexec/clusterguard-upgrade.sh（产品从不执行该路径），真正被调用的 /usr/local/sbin/clusterguard-upgrade 还是 9 月 24 日 RPM 装的旧版本。

- 根因：构建脚本按「scripts/clusterguard-*.sh → /usr/local/libexec/<同名>」猜落点，而打包清单里至少三个脚本不是这样：clusterguard-upgrade.sh 与 clusterguard-configure.sh 落在 /usr/local/sbin（前者 0750 root:clusterguard），clusterguard-agent-stdio.sh 还要去掉 .sh 后缀。门禁当时只校验 payload 里「有没有」对应文件，从不校验它「落到哪」。

- 修复：落点、模式、属主统一由 scripts/hotfix-payload-map.cjs 读 packaging/rpm/nfpm.yaml 解析（构建、门禁、测试共用同一真源）；payload 目录改为 payload/scripts，apply.sh 按声明的属主属组安装；未在打包清单声明的运行时脚本直接构建失败；门禁新增「每个修复必须抵达其现场路径」与「每个 payload 文件必须落在打包清单声明的路径」两条检查——后者在 HF-04 产物上实测报红，精确指出路径/模式/属主三项偏差。

- 何时需要应用：
  - 热修补丁应用后每台都报「已安装」，但现场行为没有任何变化
  - 控制台上传热修包仍然报「范围外路径：clusterguard-hotfix/」
  - 需要判断某个脚本到底哪一份副本在生效

### HF-2026-0928-05.2 热修补丁包误传控制台时只报「范围外路径」，不说是通道走错了（`9303e9d`，P1）

- 现象：运维把 clusterguard-ha-hotfix-*.cgpatch 传到版本更新对话框（文件选择器按 .cgpatch 后缀放行、实验室链验签也能通过），最后只得到「升级失败：升级包包含范围外路径：clusterguard-hotfix/」——没有任何文字说明热修包根本不该走这个通道，运维会反复重试。

- 根因：热修补丁包沿用了旧滚动升级包的 .cgpatch 后缀，两种不同用途的制品撞名；滚动执行器的白名单只判路径合法性，不识别制品类型；控制台对话框还写着「兼容旧 .cgpatch」，进一步诱导上传。

- 修复：clusterguard-upgrade.sh 在解包检查时识别 clusterguard-hotfix/ 顶层，直接报出正确入口（tar 解包后运行 clusterguard-hotfix/apply.sh）；控制台在文件选择时拦截文件名含 hotfix 的 .cgpatch 并给出同样指引，选择器与就绪文案改为「滚动包专属，热修包走命令行」；update-and-patch 双语文档明确两种 .cgpatch 是两条通道。新增防回归：TestPatchInspectorDivertsHotfixPackagesToTheCLI 真打包真跑 --inspect，控制台契约测试断言拦截逻辑存在，均经变异验证。

- 何时需要应用：
  - 把热修补丁包上传到版本更新对话框，报「升级包包含范围外路径：clusterguard-hotfix/」
  - 运维不确定 .cgpatch 热修包应该走哪条安装通道

### HF-2026-0928-05.3 整机重启后整个集群起不来：共享运行时目录权限与恢复冻结永不释放（`28e3b47`，P0）

- 现象：三台整机关机再开机后，控制台报「部分数据不可用：候选评估」「尚未发现主库」，三个实例全部显示「数据库未启动或不可达」，再次整机关机与故障切换都被阻断。clusterguard-mysql-3306.service 与 clusterguard-cluster-restore.service 双双进入崩溃重启循环，重启计数分别达到 2063/2069/2072 与 1034~1036。

- 根因：① update-helper 单元用 ExecStartPre 把共享目录 /run/clusterguard 建成 0750，而托管 MySQL 单元的 RuntimeDirectory=clusterguard/mysql/3306 嵌套在它下面、以非特权 mysql 用户运行（不属于 clusterguard 组）；/run 是 tmpfs，开机时 update-helper 先创建父目录，mysqld 连这层目录都穿不过去，建 socket 锁文件失败即 Aborting——InnoDB 其实已初始化成功，数据无损。安装当天能用只是因为碰巧 mysqld 先启动，开机顺序一反过来就是必现故障。② cluster-finalize 在 600 秒内等不到主库时以 exit 0 结束，而它是 oneshot + Restart=on-failure，退出码 0 等于宣告完成，恢复冻结此后只能人工解除——脚本自己打印的「修好后会自动恢复」并不成立。

- 修复：共享父目录改为 0755（保持可穿越），并让托管 MySQL/PostgreSQL 单元在自己的 ExecStartPre 里幂等修正该父目录，于是无论谁先创建父目录、无论开机顺序如何，引擎都能到达自己的 socket；cluster-finalize 的 fail-closed 超时路径改为 exit 1，让 systemd 每 30 秒重试，直到主库恢复并走完 power/complete 自动解冻。门禁同时修掉了把 0750 当成期望值的断言，并新增两条防回归检查（共享目录权限、finalize 退出码）。

- 何时需要应用：
  - 整机关机后重新开机，控制台报「数据库未启动或不可达」且没有主库
  - clusterguard-mysql-3306.service 反复重启，error.log 报 Could not create unix socket lock file
  - 计划关机或故障切换被阻断，power 生命周期停在 recovering 且 recovery_freeze 为 true

### HF-2026-0928-05.4 MySQL 写入者协调抖动：授权主库每 5 秒自隔离一次（`dd82ca5`，P0）

- 现象：集群长期 degraded、两条复制链路 unhealthy、候选评估 409、计划关机必被阻断。实测自安装起 9492 次自隔离，read_only 与 VIP 每约 10 秒同步翻转一次。

- 根因：① agent-reconcile 单元的 CapabilityBoundingSet 缺 CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH，root 也读不了 <datadir>/mysqld-auto.cnf（mysql:mysql 0640），IsolationStatus 报错即触发失败关闭；② convergeWritableRestartState 要求 RestartReadOnly 变为 false，但“重启后只读”是永久站点不变量，条件永不满足。

- 修复：收敛判定改为只看“隔离意图已清除 + 运行时可写”（writableRestartStateConverged），不再要求重启栅栏消失；单元补 CAP_DAC_OVERRIDE 与 CAP_DAC_READ_SEARCH（bounding 与 ambient 都补）。

- 何时需要应用：
  - 集群长期 degraded 且复制链路 unhealthy，但复制本身正常
  - journalctl -u clusterguard-agent-reconcile.service 反复出现 self-isolated 或 permission denied

### HF-2026-0928-05.5 升级执行链两个前置缺陷：SSH 私钥属主与 Helper 共用运行时目录（`d5f9491`，P0）

- 现象：版本更新始终 available=false，安装/上传升级包的三个控件全灰；即便手工补齐 update.json，升级执行器仍以“私钥权限过宽”拒绝；Helper 首启报 status=233，且停机时把 /run/clusterguard 整个删掉。

- 根因：① update.json 的 ssh_key 指向 clusterguard 属主的私钥，而 workspace check-file 要求 root 属主且非组/其他可写；② Helper 单元用 RuntimeDirectory=clusterguard 声明了一个共用目录，启动 chown/chmod 存在竞态，停止时 systemd 会删除该目录。

- 修复：安装器额外生成 root:root 0600 的私钥副本 /etc/clusterguard/updates/controller_ed25519 并写入 update.json；Helper 单元去掉 RuntimeDirectory，改为 ExecStartPre 幂等创建共享目录（不再“拥有”该目录，停机不删）。该目录的模式在 28e3b47 中进一步定为 0755。

- 何时需要应用：
  - 控制台版本更新长期 available=false
  - clusterguard-update-helper 首启失败或停机后 /run/clusterguard 丢失

### HF-2026-0928-05.6 控制台不解释“不可用”的原因（`ed9faca`，P1）

- 现象：版本更新面板只显示红徽标“不可用”，不写原因；集群加载横幅只报栏目名（“部分数据不可用：候选评估；操作已锁定”），运维无法判断下一步做什么。

- 根因：前端丢弃了后端给出的原因：`evidenceResult` 只保留布尔状态，409 响应体里的具体原因没有回填到面板与横幅，运维只能看到状态标签。

- 修复：面板内联渲染 `#software-update-panel-reason` 并在徽标上加 tooltip；`evidenceResult` 携带 `reason`，`evidenceReasonText()` 把 409 消息映射为中文，`evidenceUnavailableText()` 按栏目回填原因。

- 何时需要应用：
  - 控制台版本更新面板显示“不可用”但无原因
  - 集群加载横幅只报栏目名、不报原因

### HF-2026-0928-05.7 整机关机提交后控制台卡在无法交互的对话框上（`5ae2039`，P1）

- 现象：提交整机关机后主机断电、控制面随之消失，页面永久停在关机确认对话框上，只能手动关闭标签页；提交后响应丢失时还会误报为错误。

- 根因：poweroff 分支提交成功后只改了结果横幅，既不关闭对话框也不做收尾；主机断电后页面失去所有交互入口，而“已提交却丢了响应”本就是预期结果。

- 修复：新增 settlePoweroffConsole()：等 2.5s 后带 3s 超时探测 /power/status，不可达则显示离线层并 window.close()，仍可达则只关对话框并起 5s 看门狗；service 模式永不关页面。

- 何时需要应用：
  - 提交整机关机后控制台卡死、需要手动关闭标签页
  - 关机提交后偶发“无法连接控制 API”被当作失败

### 验证

- `ls -ld /run/clusterguard   # 必须是 drwxr-xr-x（0755），可被非特权引擎账户穿越`
- `ls -l /usr/local/sbin/clusterguard-upgrade   # 必须是 0750 root:clusterguard，mtime 为本次应用时间`
- `grep -c '热修补丁包' /usr/local/sbin/clusterguard-upgrade   # 必须为 1（控制台上传校验执行的就是这个文件）`
- `ls -l /usr/local/libexec/clusterguard-upgrade.sh   # 不应存在（HF-04 误装的位置，已改名为 .unused-*）`
- `systemctl cat clusterguard-update-helper.service | grep -c 'RuntimeDirectory=clusterguard'   # 必须为 0`
- `systemctl is-active clusterguard-mysql-3306 clusterguard-ha clusterguard-agent clusterguard-update-helper`
- `tail -20 /var/log/clusterguard/mysql/3306/error.log   # 不应再出现 Could not create unix socket lock file`
- `jq -c '.clusters[] | {recovery_freeze}' /var/lib/clusterguard/metadata.json   # 恢复完成后应为 false`
- `/usr/local/sbin/clusterguard-upgrade --patch /root/clusterguard-ha-hotfix-HF-2026-0928-05-2.2-104.x86_64.cgpatch --trust-key /etc/clusterguard/trust/patch-signing-public.pem --inspect   # 必须报「热修补丁包…apply.sh」而非「范围外路径」`
- `curl -sk https://192.168.102.155:3000/ | grep -c 'clusterguard-hotfix/apply.sh'   # 控制台页面必须已包含热修包拦截指引（浏览器需刷新）`

### 回滚

执行 rollback.sh 恢复旧二进制、旧单元与旧运行时脚本，然后 systemctl daemon-reload 并 systemctl restart clusterguard-ha。注意：回滚会把 /run/clusterguard 重新交回 0750 的创建方，下一次整机重启会再次让集群起不来；仅在确认新版本有回归时使用，并在回滚后临时手工执行 chmod 0755 /run/clusterguard。

## HF-2026-0928-02 — 2.2-103 现场修复合集（累积）：整机重启后集群不可用、写入者抖动、升级链前置、控制台原因与关机收尾

- 严重级别：P0
- 覆盖修复提交：`ed9faca`、`d5f9491`、`dd82ca5`、`5ae2039`、`28e3b47`
- 构建树：`f90f995fb92c23d66723a5331742a551be7a93b6`（基线 `467e533` + 上述修复，不含其它提交）
- 适用版本：2.2-103 → 2.2-103+hf-2026-0928-02（x86_64）
- 产物：`release/2.2-103-hotfixes/clusterguard-ha-hotfix-HF-2026-0928-02-2.2-103.x86_64.cgpatch`
- SHA-256：`d0ae656aca4f576632deddc7880eda4b445ac81c6e9ef53a406036d26671277f`
- 源码差异：`src/HF-2026-0928-02-f90f995.patch`
- 交付内容：
  - `payload/bin/clusterguard-agent` → `/usr/local/bin/clusterguard-agent`（0755）
  - `payload/bin/clusterguard` → `/usr/local/bin/clusterguard`（0755）
  - `payload/systemd/clusterguard-agent-reconcile.service` → `/usr/lib/systemd/system/clusterguard-agent-reconcile.service`（0644）
  - `payload/systemd/clusterguard-update-helper.service` → `/usr/lib/systemd/system/clusterguard-update-helper.service`（0644）
  - `payload/libexec/clusterguard-cluster-finalize.sh` → `/usr/local/libexec/clusterguard-cluster-finalize.sh`（0755）
  - `payload/libexec/clusterguard-mysql-install.sh` → `/usr/local/libexec/clusterguard-mysql-install.sh`（0755）
  - `payload/libexec/clusterguard-postgresql-install.sh` → `/usr/local/libexec/clusterguard-postgresql-install.sh`（0755）
  - `payload/installer/install_clusterguard.sh` → `仅安装器，现场无对应路径`（0755）
- 需要重启：`clusterguard-ha.service`、`clusterguard-agent-reconcile.service`、`clusterguard-update-helper.service`

### 本包概要

本包累积覆盖 2.2-103 基线缺失的五个修复，替代 HF-2026-0928-01。只装这一个包，不要与旧包混用：多个修复都替换 /usr/local/bin/clusterguard，叠加时结果取决于安装顺序。构建树是 2.2-103 基线加上这五个修复（移植分支 hotfix/2.2-103-fixes，f90f995），不含开发线上未发布的功能提交。新增交付类型：运行时脚本进 payload/libexec/（现场 /usr/local/libexec/），这类脚本在下次被调用时生效，不需要重启服务。

### HF-2026-0928-02.1 整机重启后整个集群起不来：共享运行时目录权限与恢复冻结永不释放（`28e3b47`，P0）

- 现象：三台整机关机再开机后，控制台报「部分数据不可用：候选评估」「尚未发现主库」，三个实例全部显示「数据库未启动或不可达」，再次整机关机与故障切换都被阻断。clusterguard-mysql-3306.service 与 clusterguard-cluster-restore.service 双双进入崩溃重启循环，重启计数分别达到 2063/2069/2072 与 1034~1036。

- 根因：① update-helper 单元用 ExecStartPre 把共享目录 /run/clusterguard 建成 0750，而托管 MySQL 单元的 RuntimeDirectory=clusterguard/mysql/3306 嵌套在它下面、以非特权 mysql 用户运行（不属于 clusterguard 组）；/run 是 tmpfs，开机时 update-helper 先创建父目录，mysqld 连这层目录都穿不过去，建 socket 锁文件失败即 Aborting——InnoDB 其实已初始化成功，数据无损。安装当天能用只是因为碰巧 mysqld 先启动，开机顺序一反过来就是必现故障。② cluster-finalize 在 600 秒内等不到主库时以 exit 0 结束，而它是 oneshot + Restart=on-failure，退出码 0 等于宣告完成，恢复冻结此后只能人工解除——脚本自己打印的「修好后会自动恢复」并不成立。

- 修复：共享父目录改为 0755（保持可穿越），并让托管 MySQL/PostgreSQL 单元在自己的 ExecStartPre 里幂等修正该父目录，于是无论谁先创建父目录、无论开机顺序如何，引擎都能到达自己的 socket；cluster-finalize 的 fail-closed 超时路径改为 exit 1，让 systemd 每 30 秒重试，直到主库恢复并走完 power/complete 自动解冻。门禁同时修掉了把 0750 当成期望值的断言，并新增两条防回归检查（共享目录权限、finalize 退出码）。

- 何时需要应用：
  - 整机关机后重新开机，控制台报「数据库未启动或不可达」且没有主库
  - clusterguard-mysql-3306.service 反复重启，error.log 报 Could not create unix socket lock file
  - 计划关机或故障切换被阻断，power 生命周期停在 recovering 且 recovery_freeze 为 true

### HF-2026-0928-02.2 MySQL 写入者协调抖动：授权主库每 5 秒自隔离一次（`dd82ca5`，P0）

- 现象：集群长期 degraded、两条复制链路 unhealthy、候选评估 409、计划关机必被阻断。实测自安装起 9492 次自隔离，read_only 与 VIP 每约 10 秒同步翻转一次。

- 根因：① agent-reconcile 单元的 CapabilityBoundingSet 缺 CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH，root 也读不了 <datadir>/mysqld-auto.cnf（mysql:mysql 0640），IsolationStatus 报错即触发失败关闭；② convergeWritableRestartState 要求 RestartReadOnly 变为 false，但“重启后只读”是永久站点不变量，条件永不满足。

- 修复：收敛判定改为只看“隔离意图已清除 + 运行时可写”（writableRestartStateConverged），不再要求重启栅栏消失；单元补 CAP_DAC_OVERRIDE 与 CAP_DAC_READ_SEARCH（bounding 与 ambient 都补）。

- 何时需要应用：
  - 集群长期 degraded 且复制链路 unhealthy，但复制本身正常
  - journalctl -u clusterguard-agent-reconcile.service 反复出现 self-isolated 或 permission denied

### HF-2026-0928-02.3 升级执行链两个前置缺陷：SSH 私钥属主与 Helper 共用运行时目录（`d5f9491`，P0）

- 现象：版本更新始终 available=false，安装/上传升级包的三个控件全灰；即便手工补齐 update.json，升级执行器仍以“私钥权限过宽”拒绝；Helper 首启报 status=233，且停机时把 /run/clusterguard 整个删掉。

- 根因：① update.json 的 ssh_key 指向 clusterguard 属主的私钥，而 workspace check-file 要求 root 属主且非组/其他可写；② Helper 单元用 RuntimeDirectory=clusterguard 声明了一个共用目录，启动 chown/chmod 存在竞态，停止时 systemd 会删除该目录。

- 修复：安装器额外生成 root:root 0600 的私钥副本 /etc/clusterguard/updates/controller_ed25519 并写入 update.json；Helper 单元去掉 RuntimeDirectory，改为 ExecStartPre 幂等创建共享目录（不再“拥有”该目录，停机不删）。该目录的模式在 28e3b47 中进一步定为 0755。

- 何时需要应用：
  - 控制台版本更新长期 available=false
  - clusterguard-update-helper 首启失败或停机后 /run/clusterguard 丢失

### HF-2026-0928-02.4 控制台不解释“不可用”的原因（`ed9faca`，P1）

- 现象：版本更新面板只显示红徽标“不可用”，不写原因；集群加载横幅只报栏目名（“部分数据不可用：候选评估；操作已锁定”），运维无法判断下一步做什么。

- 根因：前端丢弃了后端给出的原因：`evidenceResult` 只保留布尔状态，409 响应体里的具体原因没有回填到面板与横幅，运维只能看到状态标签。

- 修复：面板内联渲染 `#software-update-panel-reason` 并在徽标上加 tooltip；`evidenceResult` 携带 `reason`，`evidenceReasonText()` 把 409 消息映射为中文，`evidenceUnavailableText()` 按栏目回填原因。

- 何时需要应用：
  - 控制台版本更新面板显示“不可用”但无原因
  - 集群加载横幅只报栏目名、不报原因

### HF-2026-0928-02.5 整机关机提交后控制台卡在无法交互的对话框上（`5ae2039`，P1）

- 现象：提交整机关机后主机断电、控制面随之消失，页面永久停在关机确认对话框上，只能手动关闭标签页；提交后响应丢失时还会误报为错误。

- 根因：poweroff 分支提交成功后只改了结果横幅，既不关闭对话框也不做收尾；主机断电后页面失去所有交互入口，而“已提交却丢了响应”本就是预期结果。

- 修复：新增 settlePoweroffConsole()：等 2.5s 后带 3s 超时探测 /power/status，不可达则显示离线层并 window.close()，仍可达则只关对话框并起 5s 看门狗；service 模式永不关页面。

- 何时需要应用：
  - 提交整机关机后控制台卡死、需要手动关闭标签页
  - 关机提交后偶发“无法连接控制 API”被当作失败

### 验证

- `ls -ld /run/clusterguard   # 必须是 drwxr-xr-x（0755），可被非特权引擎账户穿越`
- `systemctl cat clusterguard-update-helper.service | grep -c 'RuntimeDirectory=clusterguard'   # 必须为 0`
- `systemctl show clusterguard-mysql-3306 -p NRestarts   # 应用后应停止增长`
- `systemctl is-active clusterguard-mysql-3306 clusterguard-ha clusterguard-agent`
- `tail -20 /var/log/clusterguard/mysql/3306/error.log   # 不应再出现 Could not create unix socket lock file`
- `/usr/local/bin/clusterguard --version`
- `/usr/local/bin/clusterguard-agent --version`
- `systemctl show clusterguard-agent-reconcile.service -p CapabilityBoundingSet`
- `journalctl -u clusterguard-cluster-finalize --since '-10min' | tail   # 主库未恢复时必须非零退出并重试，不得打印 Succeeded`
- `jq -c '.clusters[] | {recovery_freeze}' /var/lib/clusterguard/metadata.json   # 恢复完成后应为 false`

### 回滚

执行 rollback.sh 恢复旧二进制、旧单元与旧运行时脚本，然后 systemctl daemon-reload 并 systemctl restart clusterguard-ha。注意：回滚会把 /run/clusterguard 重新交回 0750 的创建方，下一次整机重启会再次让集群起不来；仅在确认新版本有回归时使用，并在回滚后临时手工执行 chmod 0755 /run/clusterguard。

