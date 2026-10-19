# 完整安装介质与签名升级包的验收路由复核

## 请求与修改前基线

用户要求将3.1.3.6提交GitHub，并指出已经验证。修改前唯一主线codex/2.2-postgresql，HEAD daa30cb，工作区干净。包绑定构建提交9339a6a、产品3.1.3.6/RPM2.2-114，SHA256为22b7adc5d5576b0404d0ce29c0c2a70b317ba6a5e094686a0e601b4d62c98b8f。

已读取门禁执行流程、完整升级强制契约v2、修改与证据规则、阅读优先级、交付入口、完整介质、公开渠道、发布清单及中英发版规范。CG-UPGRADE-CONTRACT；contract_version=2；contract_loaded=true。适用阶段为源码规则复核、完整介质制品校验和GitHub预发布；本次不执行现场安装或签名升级。

用git log/show对照12179fc、48453db、13d677b及当前源码：升级证据校验器tools/upgrade-acceptance-evidence.cjs要求至少一份.cgupgrade或.cgpatch，并检查ART-001..013；契约Package.kind只定义hotfix和rolling_upgrade。完整介质另有tools/verify-offline-kit.cjs，检查完整tar、RPM、嵌入程序和页面、各层摘要、配置及文档。发布清单却把所有“新包”统一指向升级artifact strict，造成完整tar无法提交对应报告的适用范围错误。

## 已有证据及本次复核

- 3.1.3.6完整介质已有15项passed和3份实际脚本载荷比对；45项SOURCE、128条变异/2控制、40个Go包、109项隔离浏览器及真实SSH/SCP缓存回归已有原始记录。不是未验证制品。
- 2026-10-19从包绑定的9339a6a建立干净独立检出，重新对同一归档运行完整介质校验，15项通过，摘要未变。新报告放.build/release-3.1.3.6-recheck-20261019/，不覆盖旧制品或旧验证报告。
- 3.1.3.5真实三节点续装验收有原始证据，但不能替代3.1.3.6的现场安装/升级验收。用户本次“已经验证”按其原话记录，不补写未提供的版本、节点状态或测试明细。本次仅发布预发布，不宣称完整现场验证完成。
- 升级artifact strict的原始OPEN日志保留。它表示缺少签名更新包报告，不能改写成“完整安装tar检查失败”，也不能伪造一份.cgpatch来关闭。

## 拟改范围、不变量与验收计划

仅修正消费文档路由：签名.cgupgrade/.cgpatch继续通过升级artifact/field strict；完整离线介质使用SOURCE strict、包绑定源码上的verify-offline-kit、实际脚本比对及公开资产门禁。完整介质现场验收未完成时只允许预发布，正式稳定发布仍须完成本版全部适用实装、签名及故障矩阵要求。

不修改v2契约、schema、消费者或升级校验器，不新增豁免参数，不降低任何更新包强制门禁，不重建或修改3.1.3.6的tar/RPM/sidecar。仅纠正现有两类制品的文档适用范围，无契约语义或机器接口变化。

验证计划：SOURCE strict、原有失败变异与控制、许可及Markdown台账通过；重建入库HTML。独立审查消费文档的边界。发布前后检查公开资产，显式列出完整介质/RPM/各自摘要/RELEASE-INFO/verification.json/中英说明，不上传私有证据或升级包。预发布标签指向包内9339a6a，不指向后续文档回执。先草稿上传、下载回读全部附件并核对摘要，再公布为prerelease，保留未执行现场范围。

## 状态

适用范围的独立审查完成；已修复审查发现的台账变更类型及正式发布步骤仍无条件要求签名更新包的矛盾。SOURCE45项、128变异/2控制、许可68项、Markdown台账11项通过；完整介质15项及实际载荷3处复核通过，HTML重建。

## GitHub预发布回执

消费文档修正提交8a27baf已推唯一主线。标签v3.1.3.6绑定包构建提交9339a6a，先建草稿、按8件公开白名单上传，下载全部附件逐一核对大小和SHA256，再用下载到的tar执行完整介质15项复验，全部通过后公布为[GitHub预发布](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.3.6)，未设为Latest。

公布后确认8个附件的GitHub资产node_id未变，服务端digest与本地/实际下载摘要一致。发布前后公开资产门禁通过，现7个Release、43个附件，无企业升级包，每个公开Release均含完整介质；5个旧版本缺历史来源伴随文件的warning保留，未篡改历史发布。发布血缘检查19条、0失败；原有Latest仍为v2.2.39。

完整tar保持33,851,103字节及原22b7adc5…摘要，RPM保持18,843,620字节及b84f5103…摘要，没有重建。公开附件仅完整tar/RPM/两份摘要/RELEASE-INFO/verification.json/中英说明，未上传私有验收、站点日志、凭据或.cgupgrade/.cgpatch。发布结果另存release/3.1.3.6/GITHUB-PUBLICATION-20261019.json；本次未安装/重启服务器，也未关闭签名更新或本版完整现场验收。
