# ClusterGuard HA 2.2-100 修复补丁

适用现场：2.2-98，Linux x86_64。保留原白蓝界面，不包含新视觉设计。

## 修复内容

- PostgreSQL受控灾难恢复遗漏旧default_transaction_read_only配置，导致主库提升后
  仍只读、最终拓扑0links并阻断Recovery Commit。仅在WAL证据、授权、HBA guard
  验证后处理旧配置；不放宽健康、身份、多数派、writer lease或VIP保护。
- PG无主库时不再误入MySQL重启bootstrap，准确提示需要受控恢复。
- 纳入本次端到端审计已测试的节点危险操作锁、确认失效/重复提交保护、首屏加载隔离、
  Oracle/SQL Server页面能力与指标真实性修复；不宣称后两者支持未实现的灾难恢复。

## 手动更新

在设置的版本更新弹窗选择clusterguard-ha-2.2-98_to_2.2-100.x86_64.cgupgrade，
等待验签与计划就绪后按受控流程升级。需要维护窗口，自动切换在升级期间暂停。
包内有98回退RPM；此包不是二进制差分，含100目标RPM和98回退RPM。
它不是新装用全量离线安装包，不能用于99或其他来源版本。

## 验收结果与限制

- 现场PG已恢复为154主、152/153从；原生复制、槽、身份、VIP和Recovery Commit通过。
- MySQL保持153主，容器、GTID与VIP未改变。两个引擎分别50轮自然发现全部healthy、2links。
- 全仓2104项通过，另补跑1项Node路径跳过的测试通过；关键恢复模块race536项通过，vet通过。
- 新增真实PG旧只读fence复现，修改前失败，修改后真实启动/复制与业务隔离测试通过。
- 本地签名/篡改拒绝/包载荷/内嵌HTML校验通过；152使用现场公钥只读inspect通过。
- **未安装100，未执行现场上传和滚动升级。** 这些不等同已通过验收。
- PXC专用恢复、Oracle/SQL Server真实灾难恢复及历史泄露凭据轮换仍不在已完成范围内。

完整证据和服务器备份位置见[field-repair-2026-09-10.md](field-repair-2026-09-10.md)。

补丁SHA-256：ea95e445a0c24dc769413b72275d71d7bbe995a9a9392d5ad67a8d337b14ee36。
