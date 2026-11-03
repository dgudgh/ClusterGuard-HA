# 最低兼容协议修复补丁3.1.7.8（2026-11-03）

CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。构建/签名前完整读取强制契约、门禁流程、交付入口、版本/命名、打包清单及许可/公开渠道规则。适用PRE、SOURCE、ART、文档/许可和私有制品台账；用户要求打包，不进行现场安装或破坏性回退，不把本轮记为FIELD。

进入时唯一主线codex/2.2-postgresql、干净HEAD d3c0be0。旧源码对照和回归见[源码任务](policy-compatibility-floor-2026-11-03.md)。上次现场证据及原签名3.1.7.7均对应HA9bb5047/RPM2.2-113；本轮未重新声称现场版本/健康已核实。新包基线选9bb5047，build_commit/fix_commits只包含d3c0be0，不把此前修复冒充本次fix；仍保留兼容RPM基线2.2-113。

新ID/patch_version=3.1.7.8、revision=7，supersedes_artifact指向原3.1.7.7及实测SHA；旧包字节与历史结果不改。只在release/3.1.7.8/保留一份包和摘要，不经Downloads复制；私有签名包不上传公开GitHub。现场是否已安装本包、FIELD及正式稳定发布资格分别记实。

验证计划：构建前核算生产提交、身份唯一、公钥匹配及精确载荷；干净提交构建并验签；ART13含载荷/路径/属主/权限/重启单元、实际脚本对源、生成工具、摘要/篡改拒绝，补足全四程序保护栈的产物核对；SOURCE/变异、浏览器与Go已有d3c0be0结果按源码摘要验证，不伪造新现场结果。运行私有完整台账、许可/Markdown/HTML；任何失败保留并如实限制为内部候选。

范围：持久floor、关闭/退休/恢复不降、实际目标及原备份能力检查、API/Helper/Runner/生成工具提前拒绝和双语明确未受理提示。保存策略时原7.7不具备运行门禁协议，安装本保护后不能以关闭自动解除并回退7.7；兼容目标继续走原流程。已有的升级断写与PG停主后上游身份缺失缺陷仍未解决，不以本包代签。

## 签名候选封存与新身份

3.1.7.8构建后ART13及artifact strict通过，但私有目录门禁因supersedes_artifact.reason中包含数字与斜线而失败。构建前漏跑了这个既有文本判据；不能修改已签名清单或换字节重建。原包/规格/摘要和失败输出全部留存为未交付候选，新的最终身份改为3.1.7.9、revision=8，指向封存原件SHA，base/build/fix仍为9bb5047/d3c0be0。新理由只解释替换原因，并在签名前显式断言不含数字或路径分隔符；不修改门禁。

首次ART验证器对全部文件误设root组，实际升级器规范为root:clusterguard；修正为逐项匹配nfpm后通过，包字节不变。Linux临时载荷检查初次因Python36不支持capture_output而未执行程序；保留失败，改兼容参数后另跑，不作为现场操作。新身份必须重新验签、ART及运行载荷检查，不能复用旧身份验收。

## 原生产调用链的权限回归与补修计划

实际签名3.1.7.9在Linux root隔离执行，五程序身份与脚本能力探针通过，但新目标兼容检查因载荷属主UID501而失败。9bb5047的Builder直接tar本机stage，Runner本地/远端tar保留归档属主；当时没有root可信属主检查，未暴露。d3c0be0新增独立可信文件检查后，同样的macOS载体不再可用。这是实际跨系统交付回归，不能削弱trustedRegularFile的root限制。

修改前对照git show9bb5047及当前Builder/Runner相同归档/解包路径并读取change-policy。拟局部修改：Builder用BSD/GNU对应参数固定载体uid/gid零，Runner解包显式不沿用归档属主；保留nfpm目标owner/group/mode及签名/摘要。将现有目录门禁的修订理由限制前移到Builder签名前，防止再产生明知会被目录拒绝的签名身份。增加真实tar头回归、生产Node理由校验片段的正负回归与SOURCE变异，再签新身份3.1.7.10；封存8/9并记录原SHA，不覆盖。
