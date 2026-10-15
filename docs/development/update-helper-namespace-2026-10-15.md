# 更新Helper首次启动的226/NAMESPACE

> 本文保留构建时的隔离验证与用户自行安装边界。其后用户明确授权接管三台，3.1.3.5完整续装及真实数据库/Helper/VIP验收已执行，见[后续现场记录](installation-field-3.1.3.5-2026-10-15.md)；不能把下面的历史未执行状态当作当前状态。

## 修改前基线与请求

- 用户人工安装3.1.3.4/RPM2.2-112，三台MySQL安装及两个副本同步已完成，控制面启动后在.152配置Helper时失败。新的日志覆盖此前旧残留问题，本轮不得移动任何数据库目录。
- 唯一主线codex/2.2-postgresql，HEAD5e9495a，修改前工作树干净。实际读取现场：MySQL服务active，3306主库read_only/super_read_only为0；HA active，Helper因控制进程226反复重启，/run/clusterguard不存在，其他ReadWritePaths目录存在，systemd239。
- 对照d5f9491、28e3b47与当前unit：为避免共享RuntimeDirectory的所有权及删除问题，旧版改成普通ExecStartPre创建目录；28e3b47只将0750修为0755。两个版本均遗漏ReadWritePaths会在执行普通ExecStartPre前建立namespace，首次/重启后父目录不存在会先失败，根本无法执行创建命令。
- 已读门禁执行流程、修改/操作安全规则、后端升级入口；完整强制契约v2沿用会话已读且字节未变的版本。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。

## 最小修复与不变量

- 仅ReadWritePaths中的易失共享/run/clusterguard使用缺失容忍前缀，普通ExecStartPre仍在namespace内将其创建为0755。所有持久路径保持强制存在，Helper的PrivateTmp、ProtectHome、NoNewPrivileges、UDS身份检查及签名/私有工作区验证不变。systemd239官方缺失路径前缀语义已核对。
- 安装器在enable之前也显式准备父目录；失败给出有界systemd状态/目录诊断。不得通过删除ReadWritePaths、关闭SELinux、给Helper主进程加+或恢复共享RuntimeDirectory绕过问题。
- 用户仍负责人工安装与续装，不替其重启真实Helper或执行部署；只在独立命名诊断单元和路径复现并验证，保留真实数据库、全局配置与原日志。
- 新制品使用3.1.3.5/RPM113，不覆盖3.1.3.4。源码、原生隔离、结构ART与真实FIELD分别记录；签名ART和整体交付人工验收仍OPEN。

尝试过只给ExecStartPre加+，在独立systemd239单元仍复现226；核对v239 execute.c发现ReadWritePaths在该版本即使+仍传入setup_namespace，因此未采用或封板此方案。原始失败诊断保留。仅使用运行目录缺失前缀，不能把其他持久目录全部变为可忽略。

## 验证计划

1. 真systemd239且独立runtime父目录不存在时，旧unit复现226；修复unit启动真实Helper并读healthz，验证主进程仍有namespace与NoNewPrivs。
2. 停止不删除共享目录/邻居sentinel，删除仅自建runtime后再次启动以模拟重启清空/run；陌生UID不能连接。
3. Go定向与全仓、源码门禁/变异、许可、Markdown台账、HTML及发布清单Chrome回归；新完整介质结构与包内unit/安装器字节比对。
4. 给用户可直接使用的新包与复用原state/secrets/work-dir的续装命令；已完成且验证通过的数据库不得重初始化。

## 实跑结果

- 真systemd239独立单元复现旧unit在预命令之前exit226；最终unit启动实际已安装Helper，healthz200，删除仅自建runtime后的再次启动也200。停止后共享目录及邻居sentinel原字节保留。
- 主进程mount namespace与PID1不同，NoNewPrivs=1，socket660、private700；陌生UID实际连接被拒绝。另将仅测试私有目录保留改名，最终unit仍以226拒绝，证明仅易失runtime暂缺可容忍，持久私有路径没有放行。所有精确命名诊断单元及路径已清理，真实Helper仍旧auto-restart，3306未改动。
- 全仓Go40包通过、3包无测试；go vet及Shell语法通过。定向Helper/引擎共享目录/安装器回归通过。
- SOURCE42项、114变异与2控制通过；原就绪门禁增加诊断后受固定12行窗口及分号影响的盲点一并修正，删除真实wait循环的变异仍必须命中。许可68项、Markdown11项通过，HTML重建116页。
- 本轮重新执行真实Chrome的node-safety31、bootstrap5、engine-pages73，共109项通过。使用本机隔离API，未连接现场，不计FIELD。私有原始证据在.build/helper-namespace-20261015/。
- .152当前只读确认.cluster.phase=registered，MySQL active且主库RO/SRO=0，真实/run/clusterguard仍缺失；未代用户修改真实Helper或执行任何续装。完整三节点最终验收及签名ART保持OPEN。

systemd239参考：[缺失路径前缀](https://github.com/systemd/systemd/blob/v239/man/systemd.exec.xml)、[执行namespace实现](https://github.com/systemd/systemd/blob/v239/src/core/execute.c)。本次以真实239复现为准，未把较新手册或单纯加+的猜测算作修复完成。

## 完整候选封板

从已推主线的干净2024567构建3.1.3.5/RPM113；tar33,850,253字节，SHA256=809abbf457f851134471de8ad3689d9f66da66c91154c4bb12fc06da29dd4c85。实际介质结构15项与安装器、运行包unit、RPM unit三处字节比对通过。旧3.1.3.4 tar仍为0600b1d0摘要，未改包；其本地辅助人工指南只补当前数据库已完成、旧改名步骤不再适用的提示。

安装器实际远端命令通过夹具捕获与Bash解析，证明helper_path在远端展开，运行父目录创建先于enable，失败诊断含正确路径。3.1.3.5只在release/3.1.3.5保留一份，附原三节点完整续装命令；没有复制Downloads、没有代用户运行安装或修改真实Helper，没有创建GitHub Release。ART签名和整体三节点FIELD未完成，仍为人工测试候选。
