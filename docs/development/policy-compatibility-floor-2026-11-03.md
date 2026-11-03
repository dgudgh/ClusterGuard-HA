# 策略持久化兼容性门禁（2026-11-03）

CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。修改前完整读取门禁执行流程、强制契约、规则索引/change-policy/operation-safety、backend/settings/instance-failover及update-actions。适用SOURCE、隔离API/Helper/Runner/生成器回归与文档许可；本任务不将这些结果标成新包ART或现场FIELD，不覆盖现有签名制品。

主线codex/2.2-postgresql，HEAD9bb5047。进入时四份上轮暂存文档保留。与c2cc78a前后对照：旧版无实例策略，新增策略提交复制了策略但未持久化最低运行能力；API/Manager/Helper/Runner无策略降级检查，原子回退仅保证文件恢复。上一轮隔离真实签名包+实际API/Manager四条保存策略负向断言均红、两项无策略对照绿，原Runner三个控制函数到达三节点恢复边界。原始失败和源摘要在.build/policy-downgrade-20261103/，不能改写为通过。现场最后核实HA3.1.7.7，MySQL revision13、PG revision3；另有升级授权断写和PG自动候选缺陷，本任务不代签它们。

实现计划：策略保存同一次Raft提交持久化runtime compatibility floor；关闭自动、删除集群和恢复旧快照不得降低。老数据存在策略时推导有效最低要求，首次新策略提交写入显式门槛。程序version-json明确声明运行门禁/实例策略协议；旧版或未知能力不能当兼容。

API/Manager在写job前核对，Helper独立重验可信配置/元数据和实际签名包/原备份，Runner在锁前、锁后和恢复边界重查。检查真实HA/Helper/Runner/job wrapper候选，不只看包名。签名、backup包身份、目录/文件类型、摘要与权限约束仍成立；无强制跳过参数。新生成节点apply/rollback工具也调用同一检查。首次新协议上线需全投票节点报告门禁能力；旧Helper未知接口拒绝，不能默认为支持。

回归计划：先保存开启、再关闭、重启/复制/退休、旧快照降门槛、未知门槛拒绝；真实API链路旧目标拒绝/兼容目标允许/无策略对照；Helper独立直接请求拒绝、目标缺失/符号链接/备份身份错误/能力缺失拒绝；Runner先拒绝后才可能锁和恢复、自动回退同样保护；生成脚本含执行前检查，变异及双对照；全仓Go、受影响race/vet、SOURCE strict/self-test、许可、MD台账与HTML重建。按实际输出追加结果。

追加前端范围：改动前读取frontend/README、rules/frontend-common、frontend/settings/updates；只增加兼容拒绝消息的中英映射与语言切换回归，不改变按钮、确认对象或页面布局。

收尾发现并补齐：旧store.Open可在当前坏快照/未来未知floor时载入前一代，掩盖首次策略提交。当前字节带策略或floor标记时改为明确拒绝fallback，新增三项真实磁盘代际回归；保留原始失败日志。完整滚动RPM现按签名源/目标RPM内实际程序检查，Linux系统rpm2cpio六项均通过；不安装RPM、不执行scriptlet。

## 实际验证与交付边界

实现已落在本地主线源码，不分叉、不覆盖已交付包。拒绝提示随控制台中英文设置切换，原始API诊断/旧操作记录不改。

| 检查 | 实际结果与范围 |
| --- | --- |
| 原红例重跑 | `.build/policy-downgrade-20261103/source-fixed/`隔离当前源码；实际API保存策略、Manager/CommandInspector验签原3.1.7.1包。MySQL/PG保存开启、保存后关闭四项均409、记录型Helper调用0、策略未变；从未保存的两项202。不是现场旧程序恢复试验。 |
| 独立Helper和目标 | Linux root私有临时目录7组、30子例全部通过、0skip；真实HTTP Helper/Manager组合、签名/原备份/未知目标，以及系统rpm2cpio六项。仅解码临时RPM夹具与运行能力探针，不安装RPM、重启服务或修改数据库/VIP。临时目录核对测试程序摘要后已清除，回执留本地。 |
| Runner/生成器 | 9项真实Bash边界回归通过：锁前拒绝、兼容允许、无策略对照、锁前后变化、自动热修/RPM回退，以及新生成工具的检查顺序。外部节点/健康/锁I/O为隔离夹具，不能当真实三节点升级。 |
| 存储 | 原子策略/审计/floor、持久重启/复制、关闭/退休不降、旧快照拒绝、未来协议/摘要错误/缺失及坏当前代不能掩盖为旧代均通过。 |
| 完整Go与静态检查 | `go test ./...`、`go vet ./...`返回0；Linux-only RPM实解码用例在macOS跳过，已由上述Linux root实跑覆盖。全仓测试不等于所有外部数据库集成前置均具备。 |
| 语言 | `console-language.test.cjs` 6/6，含已知拒绝及中英往返、原始证据不改；另执行下列真实浏览器点击。 |
| SOURCE门禁 | strict 61项通过；189个变异全部捕获、两个正常对照符合预期；契约仍v2，不改变签名或隔离要求。 |
| 文档与许可 | Markdown台账11项、许可68项通过；118个入库生成页已重建。按需单功能入口在backend/settings/runtime-compatibility.md，双语用户页保留旧现场红例。 |

原始红例、首次夹具缺少新检查所需状态、RPM夹具空签名头导致的解码失败以及修正后的重跑日志均保留，不覆盖旧报告。新API原始六例在`fixed-api/`，Runner、Linux、全仓、race/vet、门禁输出另存，`fixed-source-binding.json`按文件摘要绑定实际工作源码；`fixed-verdict.json`另记新结果，旧`verdict.json`仍保持失败。

原签名3.1.7.1/3.1.7.7摘要分别仍为`477a5456…`/`4ead76ce…`。本任务未签名生成、安装、发布新包，未进行现场不兼容降级；上次核实的HA3.1.7.7不含本修复。首次保护栈上线上需要全关键程序齐全，再执行ART及独立FIELD拒绝验收。升级断写与PG自动接管两项故障仍待修复，本任务不能代签。

真实浏览器追加：Chrome隔离真实console、实际点击回退和输入ID确认，发现普通409被当作不确定提交。新增仅准入拒绝的明确标记；只有标记且未committed才显示已阻止。1440/390的已知拒绝与中英往返、无标记和committed对照共36项全部通过（原首轮失败保留）。共享fixture原来忽略committed顶层字段，修正为显式透传true后再跑对照，不以夹具丢字段当产品成功。未执行现场回退。

最终race：store/platformupdate/api/runtime/runtimecompat五包返回0；最后新增明确响应标记后，API补跑race/vet及全仓Go，实际结果单独留存。
