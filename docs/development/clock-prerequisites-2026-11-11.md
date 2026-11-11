# 离线安装Chrony前置依赖与IP更正后的SSH信任（2026-11-11）

CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。修改前已读门禁执行流程、完整强制契约v2、修改证据规则、后端/交付入口及操作安全规则。适用SOURCE与安装脚本行为回归；未执行新制品构建或现场部署，不声明ART/FIELD通过。

## 进入事实及旧码对照

唯一主线codex/2.2-postgresql，HEAD f732db4，工作树干净且与远端一致。现场运行已发布3.1.7.11/RPM2.2-115；完整包不可重建。旧b33b259、abf5782及当前脚本均直接stop/enable/restart chronyd，并在enable之前备份/改写chrony.conf和时区。当前安装入口先配置时钟，随后才安装应用RPM与依赖；最小离线依赖生成器只收集三项MySQL库，RPM依赖也没有chrony/acl。

用户报错发生于10.9.30.118/mysql-db-01，enable时提示chronyd.service不存在。后续给的是mysql-db-02输出：Rocky8.10、chronyd单元存在且enabled，手动启动成功。两份输出来自不同节点，不能据后者确认118已好；118的包/单元状态待核实。尚不能把实际根因断定成服务别名或软件包缺失。

用户随后纠正“ip写错了”，目标报错变为10.9.30.115的严格SSH校验找不到ECDSA主机密钥；安装器又将SSH传输失败笼统报为root/systemd不满足。由此撤回将118视为期望目标的假设，Chrony在实际正确节点的状态仍待现场确认。只读源码确认第二个独立缺陷：prepare_known_hosts遇非空installer_known_hosts直接return，没有检查更正后的all_nodes是否被覆盖。旧f732db4和当前原函数都存在这条早退；用户已显式使用--accept-host-keys。

SSH局部修复范围：复用缓存时逐个检查当前地址及端口；仅显式--accept-host-keys可采集并追加缺失地址，不重采样或替换已有节点的密钥。显式--known-hosts文件保持只读并要求覆盖；采集/验证失败不发布部分缓存。保留StrictHostKeyChecking=yes和旧记录。增加原函数真实ssh-keygen/临时生成测试密钥的回归，覆盖IP更正、端口、显式文件/拒绝、采集失败及不改原文件。原state/secrets/work-dir不删除，不把重采集当作重建站点身份。

## 拟改范围和验证计划

先增加只读Chrony依赖检查：chronyd/chronyc可执行文件及持久systemd单元须同时成立；缺失时在时区、配置、停服务、硬件时钟和防火墙动作前明确拒绝。安装器先检查所有节点，再配置首个节点；独立时钟工具也执行同一保护。缺依赖错误必须提示修复该报错节点及保留原state/secrets/work-dir，不能通过创建空单元、软链或跳过时钟检查继续。

保持既有Rocky/RHEL chronyd服务名、/etc/chrony.conf、固定内部源、UTC/偏差校验、显式时钟权威确认与RTC持久化边界。没有实际其他发行版证据时不扩展RPM安装器的OS支持。依赖收集器和RPM声明补齐chrony/acl属于后续介质准备项，本轮未实现或声称已自动安装这些包；新增完整介质仍需新的身份和实际匹配系统依赖仓库，不能在旧11介质中手改脚本或补摘要冒充原包。

先以真实脚本及模拟systemd/命令边界复现缺单元时过晚失败，再验证缺chronyd、缺chronyc、缺单元/加载失败、masked、正常enabled/disabled、节点一正常节点二失败时全部拒绝且无配置动作。保留本地SOURCE/模拟边界与实际现场的区别。执行相关Go脚本回归、clock门禁、SOURCE严格门禁、shell语法、许可/Markdown/HTML；记录未执行项。


## 本地结果与现场边界

当前新增回归覆盖11个Chrony子场景、4个全节点检查顺序场景、11个SSH缓存子场景，均为实际脚本/真实ssh-keygen配合临时文件及系统边界模拟，不是现场NTP或SSH连接验收。旧函数先运行回归为红，新函数通过。Chrony两个守卫删除变异和旧基线均被捕获，无关注释对照通过；首次隔离变异准备时因TestMain相对路径找不到真实Helper构建目录而失败，原日志保留，修正隔离目录布局后重跑才记录有效结果。

SOURCE严格阶段65项、clock门禁和相关shell语法结果单独保存在.build/clock-prerequisites-20261111/；新源码尚未进入任何新安装介质，也未上传、安装或改动用户现场。已发布3.1.7.11包保持原字节。用户更正IP后，旧包可使用指向安装器专用known_hosts的人工SSH首次指纹确认补齐新地址，保留原state/secrets/work-dir续装；不修改或清空既有数据库。

用户随后提供对10.9.30.115的人工SSH输出：首次ECDSA信任确认后记录已添加到该专用known_hosts，远端id -u返回0，systemctl路径为/usr/bin/systemctl。这证明该节点的主机密钥信任和root/systemd只读检查已通过，不等于其他节点、Chrony配置或后续完整安装已通过；原部署状态保持复用。

最终本地检查：scripts全包不缓存回归通过（220.997秒），新增26子场景另行复跑通过；go vet ./scripts、两份修改shell语法、clock门禁、SOURCE65、许可68、Markdown11及HTML重建通过。变异记录与首次准备失败均保留。本轮仅提交本地源码/文档；不推送新发布、不构建或覆盖3.1.7.11，不接管现场续装。
