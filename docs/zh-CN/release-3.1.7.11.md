# ClusterGuard HA 3.1.7.11 完整离线安装测试包

2026-11-04。本版为本地内部测试候选，产品版本3.1.7.11，RPM2.2-115，Linux x86_64；完整安装及正式稳定验收未通过。包身份及摘要以同目录RELEASE-INFO与.sha256为准。

## 安装介质

文件为clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz，包含ClusterGuard RPM、Linux运行包、Rocky8运行依赖、原生产MySQL模板、离线安装器、完整中英文命令参数手册与补丁验签公钥。默认不嵌入数据库二进制；原厂介质由现场提供，显式-r优先，其次包内packages/database，再回退/opt。MySQL软件默认/usr/local/mysql，实例根默认/data/mysql/<端口>，身份及只读保护沿用现行规则。

```bash
sha256sum -c clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.11-offline-linux-x86_64
./install_clusterguard.sh --help
```

按[完整参数手册](offline-installer-parameters.md)准备现场参数并先执行--plan。完整安装介质用于新装/受控续装；已有集群更新仍使用正式签名升级通道。旧3.1.7.11.cgpatch的RPM兼容基线仍为2.2-113，原文件及签名不变，不能把本次RPM115当成旧补丁的兼容来源。企业网关须单独部署，HA介质不包含独立网关程序。

## 包含的源码改动及证据边界

包含Raft一致授权读取、PG源失联时的双端来源绑定和本机历史因果投影修复。上一轮相同生产源码的热修已三节点安装，并完成升级事务窗口、PG自动按权重选主/排除云库、正式回挂/切回与事务保留测试；这些不等于本完整安装包已实装。

已知未关闭：跨Leader时原Leader独有的部分升级尝试在新Leader的历史/API不可见；PG旧主回挂期间当前主库业务入口再次断连，实测约21秒后重连。另有一次云PG链路短暂延迟181秒观测，原失败保留，后续同条件62/50健康周期通过。详见[真实验收记录](../development/ha-remaining-fixes-2026-11-03.md)。不得将本包视为上述问题已修复或连续业务无中断认证。

2026-11-04追加审核：默认依赖未声明或携带acl，MySQL目录权限准备依赖setfacl；在该命令缺失的Linux子环境中已复现原函数exit=3。三个包不使用缓存的源码回归及两项Linux专用测试通过，不覆盖三台干净安装或上述现场缺陷。本版仍不具备稳定交付条件；此为包外追加说明，已生成介质及其包内说明保持原字节。详见[本次独立复核](../development/offline-3.1.7.11-2026-11-04.md)。

本次专用制品检查写入verification.json；新包干净节点完整安装、故障矩阵、目标OS的RPM/依赖签名信任及稳定发布均未据此关闭。SOURCE/制品通过与现场验收分别报告。本次不发布GitHub Release，不把签名补丁放入公开附件。
