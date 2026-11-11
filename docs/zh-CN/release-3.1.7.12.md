# ClusterGuard HA 3.1.7.12 完整离线安装包

2026-11-11。产品3.1.7.12，RPM安装基线2.2-116，Linux x86_64。本版修复IP更正后SSH主机密钥缓存未补齐，以及SSH失败被误报为root/systemd问题；新增修改时钟前的Chrony完整性检查。完整包位于release/3.1.7.12/，原3.1.7.11不覆盖。

## 本次修复

- 带`--accept-host-keys`重跑时逐个检查当前节点及SSH端口，仅采集缺失地址；已有主机密钥保留，变化仍由严格SSH检查拒绝。
- 显式`--known-hosts`文件保持只读且必须覆盖当前所有节点。空采集、失败或错误地址返回的密钥不写入原缓存。
- SSH连接/主机密钥错误保留原始诊断与退出码，不再直接断定为root权限或systemd缺失。
- 时钟工具新增`--check`，验证chronyd、chronyc和持久chronyd.service；所有节点预检成功后才改首节点的时区、Chrony配置与服务。

## 使用与续装

```bash
sha256sum -c clusterguard-ha-3.1.7.12-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.12-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.12-offline-linux-x86_64
sha256sum -c SHA256SUMS
./install_clusterguard.sh --help
```

使用核实后的`-l`/`-n`节点列表，复用原`--state-file`、`--secrets-file`、`--work-dir`；现存secrets中的MySQL密码优先，远程root Host策略须与原状态一致。完整参数见[离线安装命令参数](offline-installer-parameters.md)。主机密钥缓存更新不等于数据库身份或数据重建，不删除旧站点状态与数据库目录。

数据库原厂介质需现场自备，软件默认/usr/local/mysql，实例根默认/data/mysql/<端口>。本包不带独立企业网关，也不自动安装chrony/acl；目标节点须先具备匹配系统的chrony服务和setfacl命令。前置检查会明确拒绝缺依赖，不跳过时钟或安全核验。

## 验证边界与已知问题

构建及本地/制品回归结果见[本次任务记录](../development/offline-3.1.7.12-2026-11-11.md)，包旁verification.json只记录完整介质检查，不等于三台现场实装。旧11的安装成功日志不能代替12的验收；本次不替用户安装或发布新的GitHub Release。

跨Leader升级历史缺项、PG旧主回挂入口断连及缺acl时的条件安装失败仍未关闭。RPM的发行方数字签名、目标完整安装/故障矩阵保持未认证，不能仅凭打包成功声称全功能生产验收。原签名3.1.7.11热修仍绑定旧RPM2.2-113，与本完整介质的新安装基线不同。
