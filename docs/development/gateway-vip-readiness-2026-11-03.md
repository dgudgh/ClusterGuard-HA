# 网关VIP探测与升级维护隔离修复

## 请求与事前事实（2026-11-03）

用户要求修复155在探针失败后撤下的问题。HA主线codex/2.2-postgresql、HEAD679c313，工作树干净；独立网关codex/main、HEAD5dcc3be，工作树干净。现场HA3.1.7.1，151网关/探针0.1.0-preview.3。上一轮日志确认10:11:53探针退出1→Keepalived FAULT撤155，10:12:02恢复；正式HA升级10:12:21~10:14:41无撤VIP，两库各132次入口SQL成功。没有当时HTTP/拨号错误和grant有效性证据，不能宣称已证实哪条探针内部分支失败。

已读validation-gate-workflow、change-policy、操作规则；完整CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。独立组件AGENTS、design/protocol/development/frontend已读。适用本次独立网关源码/行为/制品检查及HA文档许可门禁，涉及现场安装时再记录真实FIELD；不重建3.1.7.1、不改其已验SOURCE/ART/FIELD报告。网关现场更新会短暂中断单入口，已向用户单独确认更新范围，未获回复不执行部署。

## 旧版对照与修复设计（先于代码修改）

对照网关6e4b5ca、75b7860与a377a83：原LB readyz绑定有效路由与500ms后端TCP，a377a83新增Keepalived探针时复用该探针；CLI750ms、Keepalived interval/timeout1s、fall1/rise2。单次后端TCP/探测异常会把地址冗余误绑后端瞬时连接状态；历史退出1未写结构化原因，无法区分过期、撤权、HTTP错误与拨号失败。HA当前OwnershipKeeper在独立多数派授权下持续续稳定writer与gateway route，不由自动切换开关暂停。

新增明确VIP就绪用途：只依据同一进程当前仍有效的既有授权，保留受保护集群启用标记和loopback HTTP，不主动拨号后端；LB readyz保留原TCP语义。Keepalived显式选择VIP用途，保持fall1及既有超时，不靠延长过期时间或缓存上次成功保VIP。撤权、授权到期、进程/健康端口失败和重启仍立即失败；不新建grant、不读磁盘旧grant复活路由、不改变数据库选主/隔离。记录有限原因码，不输出私钥/证书/凭据/授权正文。单网关自身升级仍有短暂断连，不承诺零中断。

## 验证计划

先用原实现运行真实TCP/HTTP回归复现：有效路由+后端停止应令LB失败，但VIP仍取决于现役授权；过期/撤权/重启/未知集群/标记错误/重定向仍拒绝。测试CLI目的显式选择、生成配置一致、诊断原因与不泄漏内容。再执行独立组件全量race/vet、变异、Linux命名空间真实网关/Keepalived，覆盖后端短暂不可达但同授权、授权到期、网关进程死及双网关故障。新身份preview.4构建，旧preview.3摘要不变；源码/原生隔离和现场分别记录。获准安装后先备份/校验，分开更新两套网关并验证有效授权/入口角色/复制/探针失败原因和维护锁期间的续期，不以工具exit0代替验收。

## 实现与本轮结果

独立源码af5b0d3新增VIP授权用途，只读当前进程有效激活授权；LB保留TCP检查。CLI显式purpose=vip并要求保护标记，生成器两个执行位置一致；诊断限长且不返回授权正文。原HTTP新行为回归先404红后绿，全量race/vet、11/11行为变异通过。新行为不是通过调大超时、缓存成功或删除安全检查实现。

实际Linux命名空间采用旧preview.3和新preview.4原件：旧版在后端仅TCP故障时撤VIP，新的VIP用途保持授权下地址而LB503；网关进程故障1492ms漂移、授权过期仍撤VIP。首次实验因tar保留macOS UID令Keepalived拒绝路径，不作为通过；私有实验目录属主改正后复跑红/绿通过，不修改真实155/156。

用户明确允许更新151测试网关后，2026-11-03 10:45:04~10:45:34依次更新MySQL/PG两个进程及双Keepalived的周期/启动用途。原配置/四程序在/root/cg-gateway-preview4-backup-20261103保留，新运行进程/proc/exe与4文件SHA匹配新归档；受保护标记、未知集群拒绝、限长syslog和每引擎3轮真实HA路由续期通过，155/156仍在151。原TTL、fall1/timeout1、授权水位未放宽或回退。

HA保持3.1.7.1；三个HA和数据库PID/启动时间及writer未变，MySQL63/PG50个独立后台观测healthy、一主三副本零延迟。182次每引擎入口SQL中180成功，计划单网关重启窗口各2次失败；结束后MySQL164/PG165次全成功，PG verify-full TLS。没有真实故障切主、重跑HA滚动升级或现场回退。不能从新测试倒推10:11历史探针的具体错误，也不保证本机HTTP/进程无响应时不撤VIP。

新归档build_commit=af5b0d3、21文件/4个ELF通过，旧preview.1/.2/.3摘要不变。只留独立项目release/0.1.0-preview.4/，包内未部署是封板时状态，原包不修改；新FIELD-20261103.json及evidence-20261103保存实际阶段证据。既有原始失败和本轮日志在HA项目.build/gateway-vip-readiness-20261103与独立项目.build/vip-readiness-20261103。未公开发布或推送GitHub，不拿HA.cgpatch门禁冒充独立组件制品检查。
