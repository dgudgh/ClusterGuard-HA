# 3.1.7.11完整离线安装介质（2026-11-04）

CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。开始构建前完整读取门禁流程、强制契约v2、交付入口、完整介质专用流程、版本发版规范、打包清单、许可和公开渠道规则。仅按用户请求生成本地完整离线介质，不安装现场或公开发布。SOURCE、完整介质制品检查和实际载荷分别记录；既有热修FIELD失败不能改成通过。

## 进入事实与旧版对照

唯一主线codex/2.2-postgresql，进入HEAD4da3423、工作树干净。生产源码6b059e7之后只有已登记文档；git diff 6b059e7..4da3423确认无生产变化。旧完整介质3.1.3.6/RPM2.2-114绑定9339a6a；115未使用。3.1.7.11已有不可变签名补丁（RPM兼容113），尚无同版本完整tar、RPM或RELEASE-INFO；本次首次生成该类型文件，使用产品3.1.7.11与新RPM2.2-115，不覆盖任何旧文件。两种制品的RPM基线不同，不能互换安装流程。

没有修改业务源码或构建脚本。先记录任务、更新中英候选说明及Markdown台账，文档门禁/许可/HTML通过并提交后，从该干净提交的detached工作树构建，唯一最终位置release/3.1.7.11/。不在下载目录另建副本。旧签名11、旧完整3.1.3.6及RPM114摘要留在.build/offline-3.1.7.11-20261104/old-artifacts-before.json，末尾逐项复核。

## 介质与验证计划

正式构建器build-clusterguard-offline-kit.sh，显式product-version/bundle-version=3.1.7.11、version=2.2、release=115、goarch=amd64、release-channel=candidate。只携带补丁公钥，绝不携带私钥/现场配置/口令；默认数据库包数量0，现场按既有规则提供原厂数据库介质，主介质包含Rocky8运行依赖与原生产cnf。独立企业网关由另一组件部署，本HA安装介质不替代它。

SOURCE严格阶段、显式本机PG16的Go全测/vet及shell语法；包后用绑定源码的verify-offline-kit检查内外摘要、五程序产品身份、ELF架构、RPM构建身份、嵌入HTML、原生产cnf/参数手册/脚本逐字节一致、许可、敏感文件、依赖及安装器help；另比对实际载荷权限/目标和公开密钥指纹。Linux原生程序执行是否可用另行实测，未执行不算通过；新包三台干净安装与RPM签名信任保持未验。

进入时主线领先origin40个提交；当前只请求本地打包，不把这些历史提交或制品自动公开。发布血缘门禁若报告local-only，按内部候选保留OPEN，不能宣称已远端追溯或GitHub发布。本版已知历史缺项、PG回挂入口二次断连和一次短暂延迟观测均沿用真实[现场记录](ha-remaining-fixes-2026-11-03.md)。新包不声称这些问题已修复，也不能拿旧补丁的局部现场通过代替完整介质新安装验收。

构建前SOURCE65、Markdown11、许可68、Go全测（设置CG_PG16_BIN，当前结果为缓存命中）、go vet及34份shell语法检查均通过。HTML重建将新发布页加入全站导航，已核对现有页仅产生对应导航更新。Docker守护进程不可用的原始检查保留；不据此声称Linux原生载荷已执行。构建工具、公钥指纹与3份Rocky运行依赖输入已单独记录；公钥DER摘要与既有受信值一致。


## 实际打包与校验结果

干净构建提交f1fd4b026acad8a8cf952112c5e28895fba85c20（生产代码仍6b059e7），仅正式构建器输出release/3.1.7.11/clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz：34215397字节，SHA256 718673d73647c3ebd46a1194b84bbf5415b06ea2442a105b0f9f80ecfa2406c1，RPM2.2-115。SOURCE65、完整介质专用15项通过；79个RPM声明文件与实包集合/路径/模式/属主/组/摘要精确一致，73项静态源字节比对通过。原签名11及旧3.1.3.6介质/RPM114等5份相关原件复算未变。

Linux152仅在root私有临时目录解包、执行版本接口、Helper契约、两脚本协议与安装器help，16项检查通过；RPM五程序与运行包三程序产品/提交符合当前构建，HA/Helper还通过JSON核对RPM基线115。三份运行依赖在该节点签名/摘要通过。现役七程序摘要、HA/Helper/MySQL/PG/timer的PID及状态前后相同，没有安装、启动或重启服务。临时目录已限定前缀删除并有回执；Docker不可用未绕过或更改其配置。

三次Linux探针准备错误均保留：给文本版本程序传了不存在的JSON选项、错误假设运行包也带五程序、把兼容协议JSON当数字。按实际源码CLI/构建目标/协议修正验证器后才通过，没有改包字节或业务源码。新构建器始终输出candidate。RPM自身只有头/载荷摘要，没有发行方数字签名；目标安装和正式签名信任未认证。

verify-release-records实跑status=passed，但新记录仍local-only，且旧门禁按RPM名称找说明而报note-absent；本包实际产品版双语说明已在f1fd4b0和介质内。该宽松输出不证明远端可达，当前不推送既有40个历史提交、不创建公开Release。签名热修FIELD两项失败原样保留，不用完整介质15项校验覆盖；本包仅满足本次本地候选打包交付。详细证据在release/3.1.7.11/offline-evidence-20261104/、verification.json及offline-delivery-result.json；后续交付说明提交不改变包内构建提交。

## 用户质疑后的独立复核（2026-11-04）

进入HEAD329c1b0，唯一主线干净、领先远端42。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true；重读门禁流程、完整契约、修改证据、模块/交付与操作安全规则。此次审核不重建11的原件，不把已有失败改绿，不改现役数据库或重装三台。安装器、MySQL helper/layout及原生产cnf从旧完整介质基线9339a6a到当前HEAD没有差异；旧原生helper验收e7b9e2bd与当前helper摘要不同，不能把那份原生成功直接算作当前包的安装验收。

本次验证计划：不使用缓存运行scripts、coordination、platformupdate三个关键包；检查跳过项及其平台限制；在Linux私有临时目录仅执行本次跨编译的两项root/RPM专用测试，不调用安装器或安装RPM，不启停现场服务；复算不可变介质摘要与包内脚本；保留实际FIELD失败。后续修复仍须单独完成旧码对照、红绿行为回归和新身份构建，不能因本次审核自动生成另一个声称可交付的版本。

已确认的安装风险：MySQL helper的ensure_mysql_traverse在root拥有的0750实例/配置目录上依赖setfacl，但packaging/rpm/nfpm.yaml的depends没有acl，默认离线依赖目录只有libaio/ncurses-compat-libs/numactl-libs，前检也未覆盖该依赖。Linux条件复现使用真实runuser/文件系统，仅在子进程PATH移除setfacl；从当前源码提取的原函数实际exit=3并报setfacl is required，临时目录随退出删除。此项是已复现的缺依赖条件缺陷，不是本轮三台现场安装失败；现场已有setfacl不能证明干净目标具备它。

交付判定仍为未通过：跨Leader升级历史缺项、PG旧主回挂入口断连尚未修复，当前tar未经过三台干净安装、失败续装和完整业务矩阵。关键回归通过也不能关闭这些阻断项。新证据放.build/offline-assurance-20261104/，与旧证据分开。

不使用缓存的三个包实跑完成：coordination 0.743秒、platformupdate 6.668秒、scripts 152.658秒；821条通过事件包含父用例及子用例，不能当821项独立验收义务。macOS跳过两项root/RPM专用测试，随后将同一源码跨编译的测试程序放Linux root私有临时目录，补跑两项共9个子场景全部通过，无跳过。只使用临时签名/RPM夹具，不安装RPM或重启服务；现役7程序摘要和服务PID/启动状态前后相同，临时目录已清除，SSH控制连接关闭。

重跑现有签名补丁的field严格门禁实际exit=1：65项源码成立，但FIELD-011未通过及最终验收义务OPEN，原报告的FIELD-012失败也原样保留。此报告属于已有热修，不代替完整tar安装验证；同样不能用65项源码通过掩盖已知失败。完整tar复算仍为718673d73647c3ebd46a1194b84bbf5415b06ea2442a105b0f9f80ecfa2406c1，未重建或修改。此次只补充审核结论；生产修复及新安装包尚未完成，不宣称已修复。
