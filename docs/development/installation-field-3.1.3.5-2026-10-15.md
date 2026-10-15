# 3.1.3.5 三节点续装与真实部署测试

## 授权、基线和门禁

用户于2026-10-15明确要求接管三台服务器，执行完整安装部署测试；这次授权替代此前仅由用户自行安装的边界。任务开始时唯一主线codex/2.2-postgresql，HEAD207be6b，工作树干净；未修改业务代码。

已读门禁流程、完整强制契约v2、规则索引、操作安全、交付入口、完整介质、升级手册与发布清单。CG-UPGRADE-CONTRACT，contract_version=2，contract_loaded=true。适用范围为既有完整介质的真实测试部署与安装验收，不创建正式Release，不以现场健康替代签名ART。

安装前实际核对三台均为RPM2.2-112，三台MySQL运行、两个副本IO/SQL=Yes且延迟0，站点phase=registered；三个Raft成员就绪、唯一Leader且多数派有效，无活动操作或升级维护门禁。Agent与VIP收敛定时器尚未启用，VIP不存在，部署停在Helper配置阶段。

## 介质与实际执行

- 产品3.1.3.5、RPM2.2-113；构建提交2024567c9c4a4e038319a96f0bb531541c64545e。
- 完整tar为33,850,253字节，SHA256为809abbf457f851134471de8ad3689d9f66da66c91154c4bb12fc06da29dd4c85；安装机实际sha256sum校验通过，未覆盖重建制品。
- 在每台节点建立root专用配置备份，安装机额外保存原state/secrets/site的受保护备份；凭据和原配置备份未回传本机或提交仓库。
- 从原始介质执行完整安装器，复用原state、secrets、work-dir与原厂MySQL8.0.44完整介质，显式指定信任公钥。使用原有站点SSH身份，不在命令行或安装日志记录密码。
- 14:55:15开始，14:57:41安装器验证通过并退出0。依次完成三台RPM、控制器、Helper、Agent、VIP租约和收敛定时器；registered分支跳过已完成的MySQL初始化和首次复制同步，不清空数据、不改phase、不绕过多数派或维护检查。

本次是对原有部分完成现场的完整续装，并非清空三台后重新进行空盘初始化。此前失败证据与原数据均保留。

## 独立现场验收

| 实测项目 | 结果 |
| --- | --- |
| 安装器退出与最终状态 | exit0；state schema2、registered；集群和节点不可变身份保留 |
| RPM及运行版本 | 三台均2.2-113 / 3.1.3.5 / 2024567；rpm -V无差异 |
| 五程序与运行进程 | 五个二进制摘要逐台一致；HA和Helper运行中exe摘要等于已安装文件 |
| 服务 | 每台HA、Agent、Helper、MySQL、reconcile.timer均enabled+active；Agent是正常oneshot active/exited，定时器active/waiting |
| 实际Helper | 三台Unix socket healthz均ok；0755运行父目录、0660 socket、0700私有目录；陌生UID连接被拒绝 |
| 实际Helper重启 | 逐台重启后健康恢复；NoNewPrivs=1、独立mount namespace仍生效；MySQL进程PID不变 |
| 控制面API | 52项通过、0失败；三成员一致且全部ready，1 Leader+2 followers，Leader确认多数派，0活动操作、无升级维护门禁 |
| 更新通道 | 三台platform/updates均available=true |
| 数据库 | 唯一主库read_only/super_read_only=0/0；两副本1/1，IO/SQL=Yes、延迟0、Auto_Position=1，无IO/SQL错误 |
| 真实写入复制 | 新建独立验收库，写入唯一标记，两副本WAIT_FOR_EXECUTED_GTID_SET返回0且读到相同记录；仅清理该测试库，清理亦在两副本追平 |
| VIP | 网卡汇总仅一个owner且是可写主库；使用发现账户通过VIP执行真实SQL，返回同一主库server_uuid；API资源/端点/活动租约均绑定该主库 |
| 原配置保留 | 三台生产my.cnf和受管标记字节不变；node.json仅角色随Agent配置从controller完成为mixed，UUID等身份不变 |
| 时钟 | 三台chrony Leap status Normal，副节点跟随固定内部时钟源；不据此声明外部UTC可追溯性 |
| 真实登录后页面 | 用户自行处理证书并登录后，通过真实Chrome核对版本3.1.3.5、升级可用、历史0条；拓扑三实例健康、主库及VIP相符；状态页就绪、三投票节点、同一Leader、0活动/待复核操作，均与API一致 |

followers的quorum_confirmed=false是接口语义，不能按Leader规则误判。API单次快照中另有一项operations.append_only为unknown：当前操作历史为空，本次安装未执行签名升级操作，不能据此证明升级历史追加性。

## 证据与尚未完成的范围

私有原始证据保存在本机.build/field-install-3.1.3.5-20261015/及服务器root专用验收目录，包括完整安装输出、exit文件、三台节点原始JSON、实际API响应、真实写入/GTID/VIP记录、配置保留和Helper重启日志。第三节点安装前独立checksum文本未生成，前后比较直接取14:53原始配置备份的对应成员；另存备份自身摘要、原始时间与成员摘要清单说明来源，没有倒填安装前记录。验收脚本的现场Python3.6适配及客户端默认配置修正属于诊断工具调整，没有修改安装介质。

浏览器刷新最初遇到新站点证书警告，由用户亲自处理证书并登录。随后15:06至15:07在真实Chrome核对版本更新、拓扑和控制面状态页，并保留会话中的截图与可访问性原始输出；页面与权威API一致，未执行页面变更操作。该页面检查不代表已执行全部交互功能。隔离浏览器109项属于先前SOURCE回归，不替代这次真实页面核对。

本次未执行空盘重新初始化、灾难注入、主从切换或签名升级/回退。因此生产签名ART、升级完整FIELD契约及这些行为不声明通过。三台完整续装成功和上述真实安装验收结果独立成立，不等于已正式发布或可省略客户交付认证。
