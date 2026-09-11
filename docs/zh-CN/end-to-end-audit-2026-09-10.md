# ClusterGuard 端到端检查与修复计划

2026-09-10。用户要求先生成计划，再从头到尾检查并修复。主代理自行执行，不委派其他模型。

## 范围与规则

基线为产品分支 `codex/2.2-postgresql`，已发布 99 对应 `19d02bcf076b6e294af0e1f30e2c34f0727e6ae4`。实际源码位于 `.worktrees/platform-auth-session`。已有图片删除、预览和现场输出保留，不纳入修改。

覆盖平台所有页面与核心链路：身份会话、集群上下文、拓扑发现、健康与候选、操作与灾难恢复、日志查询、升级维护与版本、安装介质。MySQL/PG 是完整恢复实现的主要核查对象；Oracle/SQL Server/PXC 逐项核对能力边界，不能用 UI 存在或适配器注册代表已实现。

只读检查与隔离临时环境测试可执行。2026-09-10 用户后续明确授权 SSH 到 152–154，取代初始阶段的 SSH 限制；仍不擅自停业务库、漂移 VIP、写现场业务数据、执行现场灾难恢复或安装软件。此前报告的现场结果只作历史线索，不作为当前版本实测证据。测试缺环境应查明依赖并列明阻塞，不能记作通过。现场阶段结果见 [SSH 检查记录](field-audit-2026-09-10.md)。

每个业务修复之前必须补充本文件的问题卡：旧版提交/代码、当前行为、复现、根因、改动范围和应保持的不变量。先失败再通过，禁止通过放宽 fencing、身份、权限、事务分支或健康规则解决测试。

## 执行清单

| 阶段 | 状态 | 工作与验收标准 |
| --- | --- | --- |
| A 基线与环境 | 通过（能力边界已列出） | 核对 68/88/99 差异、99 包一致性、历史证据、可用真实 DB/容器/安装测试环境，形成能力矩阵 |
| B 安全与后端 | 本地相关用例通过，现场发现问题 | 真实 API/Raft 对照一致；PG 无主、旧 Agent 配置及认证权限故障仍未恢复；AUDIT-04 诊断错误已修源码 |
| C 全页面 | 通过（隔离 API） | 四引擎八页 73 项、节点 31 项、首屏 4 项及原有浏览器组均通过；不等于四种数据库实机验收 |
| D 数据库原生 | 五项环境缺口已补测，业务 PG 不健康 | 三项实际 MySQL 容器测试及 PG 停止态/entrypoint 测试通过；现场 MySQL 50 轮健康、PG 50 轮降级无主。未对业务 PG 执行恢复 |
| E 安装与升级 | 本地脚本/旧包校验通过，真机未验收 | 99 旧包只读重验 12 项；不包含本轮修改，不作为新修复包交付。Linux 新装、现场验签与滚动未执行 |
| F 修复与回归 | 通过（不等于业务 PG 恢复） | SSH 阶段新增修复后全仓 2097 项通过；五个本地容器 skip 已在服务器补测。API/coordination race 423 项及 vet 通过；前端既有验收保留，新增现场数据回放 4 项通过 |
| G 验收与交付 | SSH 报告完成，现场恢复/发行仍阻断 | 已核实现场 98，不含本轮修复；未发布新包。下一步是受控 PG 恢复和可丢弃 Linux 的安装/升级验证，不再以 SSH 未授权为阻断 |

## 必测用户路径

- 登录、退出、角色降权、旧会话迟到响应；操作解锁必须绑定当前会话和集群。
- 总览/拓扑/操作/节点/指标/日志/关于/设置全部页面，顶部切换与子页状态一致；不展示旧集群为当前健康。
- 日志默认随顶部集群，首屏只取一页，加载更多不重页，原始详情按需读取；筛选覆盖全部历史。
- 锁定时入口与执行函数都拒绝危险操作；换目标、重锁、刷新、退出、失败及重复点击均覆盖。
- 上传前无旧包信息，验签/计划状态准确，按钮由实际条件启用，确认一次提交；错误在弹窗内可见。
- 升级上半部分固定、事件独立滚动；历史默认折叠；安装材料保留与日志保留分离。
- PG 原生身份与平台身份不同、旧 Bootstrap Env、发布顺序与 Raft 落盘；不能用旧观察掩盖新失败。
- MySQL/PG 恢复成功、分支冲突、证据缺失、超时取消、无多数派、恢复失败保持隔离，以及恢复提交后才放行入口。
- 未实现的 Oracle/SQL Server 节点重建和 PXC 专用恢复不得被前端误导为可执行能力。

## 证据格式

本轮输出到 `.build/end-to-end-audit-20260910/`。每项记录执行时间、源码 SHA、输入场景、命令、退出状态、失败断言和产物位置。状态只用通过、失败、缺环境、未执行；避免“应该”“已支持”冒充执行记录。

## 问题卡与执行记录

以下在检查到证据后逐项追加。每张卡先保存失败证据，再修改业务代码。

### A：能力与环境核对

- 99 源码与 GitHub 提交一致，99 制品 verification.json 标明未现场验收；88 报告存在现场记录，但不能代替当前版本复验。
- MySQL/PG 有完整恢复路由；`internal/api/disaster_recovery.go` 对其他引擎明确拒绝。Oracle/SQL Server 已有适配器，其 CapabilityNodeSync 明确未实现。PXC 仅有参考文档，没有独立引擎和 bootstrap 执行器。
- 99 安装器支持 mysql/postgresql/none，不包含原厂数据库介质、Docker 镜像和完整 PG 编译依赖；不能宣称所有引擎全量安装。
- PATH 没有 postgres，但在 `.build/pg16-validation.dRbHlD/install/bin/` 找到完整 PG 16.4，实际 `postgres --version` 成功。上一轮未启用 CG_PG16_BIN 是验证遗漏，本轮补跑真实用例。
- 本机未找到 Docker CLI、Docker.app、Colima/Lima 或 MySQL server。MySQL 容器集成、Linux 新装、Oracle/SQL Server 实机验收缺隔离运行环境；不直接 SSH 现场绕过用户限制。
- 既有 `console-ui-acceptance.cjs` 只扫六个页面、两种引擎，未覆盖节点/关于及 Oracle/SQL Server。当前在补充跨引擎全导航检查，既有通过记录不能代表这些缺失场景。

### D：原生 PG 基线

设置 `CG_PG16_BIN` 后，agent/discovery/disaster 实际运行 23.068 秒，325 个测试及子测试通过。3 个 MySQL 容器测试、1 个 PG Docker 停库证据测试缺环境跳过；未计为通过。证据：`baseline/native-pg.log` 与 `baseline/results.json`。既有 UI/context/recovery 三组浏览器回归也通过。

### AUDIT-01 / P1：节点流程缺少独立确认与异步意图保护

- 旧版对照：`bc0546a` console.html 2486、3388、3442 行；现版 `19d02bc` 2751、3772、3838 行。同一缺口旧版已存在，98 仅给旧主重建入口加了 operationIntent，未覆盖节点页调用；不是新引入，也不能因此不修。
- 复现：`tools/console-node-safety-audit.cjs --baseline`，MySQL/PG 合计 14 项中 12 项失败。关闭弹窗仍可直接提交；重复调用各发出 2 次 execute；预检期间切集群、刷新、关闭弹窗仍提交旧请求。退出虽被 fetchResult 拦住提交，但旧弹窗仍覆盖登录页。
- 证据：`node-before/result.json`；所有请求发向本机隔离 fixture，不涉及现场。
- 根因：只禁用按钮，无处理函数重入保护；普通节点入口未绑定会话/集群/弹窗/表单快照，也没有独立解锁确认；showLogin 未关闭该弹窗。
- 修复范围：节点弹窗显式风险确认、一次性意图、提交前二次验证、迟到回调隔离和对话框生命周期。保持后端授权、预检、集群互斥和数据同步校验不变；旧主恢复入口继续沿用原 operationIntent。

### AUDIT-02 / P2：首次加载被无关集群和任务接口阻塞

- 旧版对照：`bc0546a` console.html 4290 与现版 4805，均先 Promise.all 所有辅助接口，再等待 loadFleetDetails 全部集群，最后才加载所选集群。97 删除全量日志阻塞，但此处仍保留旧耦合。
- 复现：`tools/console-bootstrap-audit.cjs --baseline`。另一个集群 topology 一直等待时，健康的已选集群 2 秒内仍不能显示；nodes/sync/tasks 返回 503 则已登录状态被清空。capabilities 失败时保持锁定的负向检查通过。
- 证据：`bootstrap-before/result.json` 及截图。
- 修复范围：所选集群先独立加载，后台填充全局目录；节点任务辅助故障可见但不退出登录。认证与关键能力读取不降级成可执行；异步结果绑定当前会话和加载代次。

追加验证发现：拆分首屏后，用户切到正在后台读取的 PG，浏览器只向服务端发出一次同 URL topology 请求，前台请求仍等待未返回的后台请求。`regression/console-bootstrap-audit.log` 保存失败。使用完全相同源码/fixture，仅在浏览器诊断注入 fetch cache=no-store 后，4 项全部通过（`bootstrap-cache-experiment/result.json`）。据此修复 API fetch 的缓存模式，后台读取也使用已有 15 秒超时；最后必须不带诊断注入复验。此项为首屏拆分后继续检查发现的路径，不能算此前已经验收通过。

### AUDIT-03 / P2：其他引擎的修复按钮和指标错误套用 MySQL

- 旧版对照：`bc0546a` console.html 3180 的指标定义只有 PostgreSQL/else MySQL；现版 3581 仍为此结构。旧版旧主恢复函数 2494 已限制 MySQL/PG，现版普通操作就绪判断 2494 未按 kind 区分引擎，导致 UI 可亮但 handler/backend 不支持。
- 复现：四引擎、八页、1440/390 宽度浏览器共 71 项，64 项页面可见及宽度断言通过，Oracle/SQL Server 各出现修复按钮可用、指标显示 QPS/TPS 全部为空，共 4 项失败。隔离 fixture 的指标 key 对照真实 `adapters/oracle/oracle.go:451` 和 `adapters/sqlserver/sqlserver.go:251`；不是虚构数据库已实测。
- 证据：`engine-pages-before/result.json` 和 64 张截图。
- 修复范围：重挂/复制修复限定现有 MySQL/PG 实现，保留 Oracle/SQL Server 已有受控切换能力；分别呈现 Data Guard 延迟/Broker 状态与 Always On 队列/同步指标。未知值仍为缺失，不能格式化成健康或 0。

### 修复中间结果

AUDIT-01 新增 20 项节点弹窗场景全部通过，包含锁定、重入、刷新、切集群、退出、重锁、改目标和迟到会话。AUDIT-02 的两项原失败场景及关键能力失效保持锁定共 3 项通过。最终还须在全部代码收敛后完整复验，不能用本段中间结果替代交付验收。

追加节点真实按钮点击、自动刷新不打断当前节点流程、全局重锁及弹窗窄屏检查；首屏新增迟到全局目录不能覆盖当前集群新观测的检查。全仓首轮发现两项静态页面测试误报：consoleView 用 data-view 子串定位命中 CSS；限定实际 section 起始标签后重跑，不删除功能断言。新增节点标题/刷新按钮保持单行，原页面配色和主体布局不改。

AUDIT-01 继续扩展跨流程检查：`node-scope-before/result.json` 29 项中 1 项失败。普通操作页已解锁后打开节点弹窗，旧 switchUnlocked 仍为 true。旧版 openNodeLifecycleModal 与现版均未 relock；修复为打开节点弹窗收回其他操作的解锁，并使普通操作/灾难恢复与节点执行互斥。保留旧主恢复内部已有的受控 NodeSync 授权。

AUDIT-01 补充完成后的读取故障：`node-refresh-before/result.json` 31 项中 2 项失败，MySQL/PG 均为 execute 已成功，但后续任务列表返回 503 后弹窗仍显示执行中。旧版将执行和刷新合并在同一 try/catch，本轮意图保护的 catch 又将辅助能力失效当作整个上下文过期，吞掉提示。修复必须区分操作结果和查询结果，保留已完成事实、明确刷新失败、重新锁定按钮；会话或集群真正变化仍须丢弃旧回调。不得因此重复 execute。

全仓补跑（Node 已加入 PATH）2094 个测试及子测试通过，5 项缺容器环境跳过。后续后台读请求增加超时后，一项静态断言仍查找 fetchResult 字串而非 readClusterResult，导致 API/race 失败；更新该函数契约并继续保留拓扑/健康读取与新鲜度断言，浏览器同时覆盖真实迟到请求。

### 现场环境确认

用户补充测试地址 192.168.102.152/153/154。三台 HTTPS 3000 均返回 HTTP 401，证明 API 可达且认证生效，不代表内部服务健康或版本已核实。因之前明确不直接 SSH，本轮已询问是否允许 SSH 创建独立测试实例；确认前未登录主机、未停库、未启动恢复、未安装或覆盖任何包。

三台公开根页面另行只读下载到 `served-console-152.html`、`served-console-153.html`、`served-console-154.html`，SHA-256 均为 `38cd47cd975b7e118bd49956d83d3762f0def29da13c88865573e2c221687ec1`，与 98/99 包内页面一致。因此本轮前端失败复现具有现场源码对应关系；这不证明后端一定是 99，也不代替认证后的数据库验收。

### 可复查的执行方式

### AUDIT-04 / P2：PG 无主错误混入 MySQL 重启诊断

- 现场证据：152 的控制器日志为 `topology has no current healthy writable primary; reboot bootstrap blocked: reboot bootstrap instance scope is invalid`。原生查询确认 pg01 服务 0/0，pg02/pg03 均为 standby 且无 receiver。此时不具备恢复写入条件。
- 修改前对照：`git show bc0546a:internal/coordination/ownership_keeper.go` 的 reconcileCluster 在 primaryErr 后无引擎判断即调用 RebootBootstrapCandidate；HEAD 同样存在。后者检查 instance.Engine 必须为 MySQL。`git log` 显示重启入口来自 fa6cee8，后续多引擎支持没有将这个回退路径限定为 MySQL。属于旧有诊断缺陷，不是本轮前端改动引入。
- 拟复现：两台 PG standby、完整 VIP 观测、canonical 元数据一致；要求不产生 lease/owner commit，并返回 PG 需受控恢复的原因，不能返回 MySQL 实例范围错误。同时核对健康 PG writer 仍能续租。
- 拟改范围：ownership keeper 在无可写主库时，仅 MySQL 进入已有 reboot bootstrap；PG 保持拒绝并报告受控恢复要求。不得修改健康证据、启动 PG、选主、放开 VIP/writer lease 或改动 MySQL bootstrap 验证。

测试输出根目录为 `.build/end-to-end-audit-20260910/`。`baseline/` 保存首次 PG/浏览器结果，`node-before/`、`bootstrap-before/`、`engine-pages-before/` 保存修复前反例；`regression/` 保留首轮完整回归（包括失败和跳过）；`final-go/` 保存全仓补跑；`final-ui/` 保留随后扩展检查暴露的失败；`final-acceptance/` 保存最后一版的 16 组全部通过结果。所有运行记录列明命令、退出码、时间、HTML SHA-256，不覆盖失败证据。

本机 Node 不在默认 PATH；Go 的升级状态测试因此在首轮跳过，已在补跑中将实际 Node 目录加入 PATH。PG 同样必须显式设置 `CG_PG16_BIN`。缺少可执行程序时不得只看 go test 的退出码，应逐项审查 skip。

```sh
export PATH=/Users/zhaolongjie/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin:$PATH
export NODE_PATH=/Users/zhaolongjie/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules
export CG_PG16_BIN="$PWD/.build/pg16-validation.dRbHlD/install/bin"
go test -p 1 -json ./... -count=1
go vet ./...
go test -race ./internal/api ./internal/disaster ./internal/workflow ./internal/store -count=1
node tools/console-node-safety-audit.cjs
node tools/console-bootstrap-audit.cjs
node tools/console-engine-pages-audit.cjs
```

## 本轮验收快照

### SSH 现场阶段授权更新

用户在下一轮明确授权通过 SSH 充分检查 192.168.102.152–154。此前 SSH 访问阻断解除，开始只读基线采集；不沿用旧报告的版本或主库结论。现场阶段依次执行：服务/版本/容量与时钟、认证后 API 耗时与一致性、原生 MySQL/PG 复制和拓扑对照、脱敏日志与升级记录、独立目录/端口的隔离测试。禁止直接运行旧 `recovery-field-acceptance.py` 的 prepare/stop/recovery 动作，因为该脚本会修改现有 Swarm 服务和 VIP 门禁。SSH 密码只用于认证，不写入代码、报告或命令行参数。

结论：本地已确认缺陷已修；整体现场验收为 **部分完成**，不是“系统从此没有问题”。SSH 阶段新增 PG 诊断错误修复，保持拒绝授权的安全策略；未修改业务数据库数据或现场服务，未 Git 提交/推送，也未生成含本轮修复的新包。新增全仓 2097 项、API/coordination race 423 项及 vet 均通过，见 field-regression 及独立现场记录。

最终 HTML SHA-256：`b94a46042fac672a0dce56ea8cfef73238f5743b6faa81cb96e98420811b0309`。`final-acceptance/results.json` 的 16 项均使用此文件，退出码均为 0。全仓 Go 补跑在最后几个前端细化之前完成，2094 含子测试，不与随后 API 300 重复相加；变更后重新测试了嵌入 HTML 的 API 包和全部浏览器组。

| 检查 | 结果和边界 | 证据 |
| --- | --- | --- |
| 全仓 Go | 36 包、2094 测试及子测试通过，5 跳过；含静态契约、单元及部分真实原生测试，不统称端到端 | `final-go/full.log` |
| 最终 API / race / vet | API 300 通过，API race 300 通过，vet 通过；store/disaster/workflow 在前轮 race 分别通过，源码未修改 | `final-acceptance/results.json`、`regression/go-race.log` |
| 节点流程 | MySQL/PG 31 项通过；预检期间改变上下文不再提交，真实执行按钮一次提交，完成后列表 503 明确提示并重锁 | `final-acceptance/console-node-safety-audit/result.json` |
| 首屏/刷新 | 4 项通过；无关慢请求不阻塞所选集群、辅助任务故障不登出、关键能力失败保持锁定、迟到全局结果不覆盖新观测 | `final-acceptance/console-bootstrap-audit/result.json` |
| 四引擎页面 | MySQL/PG/Oracle/SQL Server，8 页、2 宽度的 64 张截图和能力/指标断言共 73 项通过；人工复查节点窄屏与 SQL Server 指标页无重叠 | `final-acceptance/console-engine-pages-audit/result.json` |
| 既有浏览器流程 | UI、context、recovery、operation-lock、operation-intent、operation-lifecycle、log-scope、log-pagination、update-confirmation 九组通过 | `final-acceptance/results.json` |
| 原生 PG | 真实 PG 16.4 三节点，50 次不同时间后台发现均 2 links/healthy；暂停回放后降级并撤销不安全 link；真实分支 COMMIT、缺 WAL、prepared transaction、受控重建测试通过 | `final-go/full.log`、`baseline/native-pg.log` |
| Recovery Commit / Raft | 两引擎提交落盘/重开、失败保持 freeze、拒绝陈旧授权及观测、独立 Raft 三节点和 quorum 测试通过。PG 原生 50 轮使用 Memory store；不是原生 PG + 现场 Raft + API 全链验收 | `internal/store/disaster_test.go`、`internal/consensus/raft_test.go` 对应执行记录 |
| 安装/签名 | 包版本组通过；脚本包 143 个顶层测试含计划/拒绝缺介质/签名篡改/凭据保护；99 离线包 12 项只读验证通过，不执行安装 | `final-acceptance/bundle-version-acceptance.log`、`final-go/full.log`、`kit99-readonly.json` |

### 原始缺口与现场补测

以下 5 项在原本地全仓运行中确实跳过。SSH 阶段已逐项真实执行通过，记录见 field-audit-2026-09-10.md；原 skip 日志保留，不能倒改：

1. `TestEntrypointPreservesDynamicPostgreSQLRole`：缺 Docker 容器运行环境。
2. `TestRecoveryMySQLActualGuardedClone`：缺 MySQL Docker 实例。
3. `TestRecoveryMySQLActualThreeNodeRelayDrainAndSelection`：缺 MySQL Docker 实例。
4. `TestRecoveryMySQLActualExecutorRebuild`：缺 MySQL Docker 实例。
5. `TestRecoveryPostgreSQLReadOnlyStoppedDockerEvidence`：缺明确配置的停止态 Docker 现场。

另外：SSH 已核实三台为 98，读取了数据库健康和 VIP/控制面状态，发现 PG 无主。业务集群实际灾难恢复、网络分区/断电恢复、Linux 一键新装、上传验签和现场滚动仍未验收；此前泄漏的 replication secret 本轮未旋转。Oracle/SQL Server 的本轮检查是 UI/适配器契约，不是原生安装或灾难恢复验收。PXC 专用恢复尚未实现，不能用 MySQL 异步复制测试代替。

### 下一步现场卡

1. 已完成：SSH 授权和三台认证通过；凭据未写入文档、命令行参数或代码。
2. 已完成：主机、版本、原生数据库、Raft、VIP、50 轮观测及页面依赖接口只读取证，无原始连接串或口令输出。
3. 已完成本轮五项环境补测：独立 MySQL 网络/容器、PG entrypoint 临时挂载、停止态 PG 只读取证，结束后测试资源零残留。旧 5432 policy 和业务 Swarm PG 无主待处理。
4. 组合验证发现、身份、Link、Raft 持久化、API 和 Recovery Commit；恢复完成后 50 个后台周期持续检查唯一主库、两条复制链和健康，不以短时手动刷新代替。
5. 在可丢弃 Linux 环境验证一键新装、错误签名/错误来源版本拒绝、滚动升级失败回退与门禁保持；结束前比对业务数据与密钥脱敏。
6. 现场卡闭环后再新建版本，按 AGENTS 发布阻断清单核对源码、二进制、签名、公钥、回退载荷和测试结果。既有 99 不覆盖，用户手动升级，不擅自现场安装。
