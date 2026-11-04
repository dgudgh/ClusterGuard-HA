# ClusterGuard HA 3.1.7.11 完整离线安装包

2026-11-04。按用户本次明确确认，本版本作为 **MySQL现场生产可用版本** 发布。产品版本 **3.1.7.11**，RPM安装基线 **2.2-115**，Linux x86_64；[GitHub v3.1.7.11](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.7.11)。生产使用确认范围及尚未完成的验证见下文，不表示所有数据库、拓扑和故障场景均已认证。

## 安装介质与身份

完整包为 `clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz`，34,215,397字节，SHA-256：

```text
718673d73647c3ebd46a1194b84bbf5415b06ea2442a105b0f9f80ecfa2406c1
```

构建提交为 `f1fd4b026acad8a8cf952112c5e28895fba85c20`，发布标签绑定该提交；发布后的说明更新位于主线，不改变包内构建身份。介质包含ClusterGuard RPM、运行包、Rocky8运行依赖、原生产MySQL模板、安装器、完整中英命令参数说明和补丁验签公钥。默认不包含数据库原厂二进制及独立企业网关。

数据库介质由现场提供：显式 `-r` 优先，其次包内 `packages/database/`，最后 `/opt`。本次现场使用MySQL8.0.44原厂包。MySQL软件默认 `/usr/local/mysql`，实例根默认 `/data/mysql/<端口>`；节点身份和只读启动保护保持启用，由授权流程开放主库写入。

```bash
sha256sum -c clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.11-offline-linux-x86_64
sha256sum -c SHA256SUMS
./install_clusterguard.sh --help
```

按[完整参数手册](offline-installer-parameters.md)准备现场命令，并先以 `--plan` 检查。MySQL初始root密码由 `--mysql-root-password` 或安全环境输入指定；远程root由 `--mysql-root-remote-host` 显式开启，默认关闭。续装复用原state、secrets和work-dir；已有secrets时沿用原密码。本文不保存或公开任何现场凭据。

完整介质用于新装/受控续装；已有集群通过正式签名更新通道升级。旧3.1.7.11签名热修的兼容基线仍为RPM2.2-113，与本介质的新安装基线115不同。签名升级/热修包仅本地及企业交付，不作为公开附件。

## 已验证范围与用户确认

- 用户提供的2026-11-04现场日志显示：三节点MySQL8.0.44安装完成，三台严格配置验证、初始化、数据库启动及副本同步完成；HA、Helper、Agent和VIP收敛部署完成。
- 安装器15:15:15报告“安装验证通过”；其最终检查包含控制面ready和多数派、数据库拓扑连续健康且唯一主库、VIP恰好一处、HA/Agent/Helper服务与Helper socket。确认执行后耗时约8分35秒。
- 用户在该安装记录之后明确确认“此版本生产可用”并授权GitHub发布。此为用户现场安装与使用确认，不伪称Codex重新连接三台独立验收或全故障矩阵通过。
- 完整介质专用校验15项、实际Linux载荷执行与RPM文件映射检查通过；公开附件大小、摘要和下载回读记录由本次发布流程补充。

包内 `RELEASE-INFO` 的 `release_channel=candidate`、`verification.json` 中 `deployed=false` / `field_acceptance=false` 和旧包内说明是**构建时快照**。本次保留原字节，将后续用户安装和发布记录补充在本说明；不原地重建、重签或修改同名制品。RPM目前只有头/载荷摘要，没有发行方RPM数字签名；不能把补丁验签公钥当成RPM签名。

## 包含的改动与已知问题

本版包含Raft授权一致读取、PG源失联时的双端来源绑定，以及本机升级历史因果投影。旧热修曾执行三节点升级、事务保留、PG按权重自动选主及回挂/切回等场景；其通过与失败记录均保留，不代替本完整介质的所有验收。

以下问题**未修复**，安装成功或本次生产使用确认不关闭它们：

1. **缺少acl依赖**：默认RPM依赖及介质未覆盖 `acl`；MySQL目录权限准备需要 `setfacl`，缺该命令时已复现退出3。新目标应先确认 `command -v setfacl`；严格离线环境应预备对应系统的可信acl RPM与依赖。
2. **跨Leader升级历史缺项**：原Leader独有的部分尝试在新Leader查询中不可见，原始文件仍保留。
3. **PG旧主回挂入口断连**：此前现场当前主库业务入口再次中断，实测约21秒后重连；另一次云PG延迟181秒观测与后续恢复分别保留。

完整包本版的网络分区、并发/故障全矩阵、目标OS正式签名信任等未全部独立验收；不能宣称全场景稳定认证、零中断或PG问题已关闭。详情见[安装/发布记录](../development/offline-3.1.7.11-2026-11-04.md)和[原现场失败记录](../development/ha-remaining-fixes-2026-11-03.md)。
