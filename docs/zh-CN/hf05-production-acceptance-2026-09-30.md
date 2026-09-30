# HF-2026-0929-05 现场应用与验收（2026-09-30）

## 交付身份与执行

- 用户明确确认后，于 09:29 通过 192.168.102.152 的控制台上传并校验 `HF-2026-0929-05`，于 09:29:52 发起执行。现场包与本地台账的 SHA-256 均为 `259c23646eaac488da5bad24d7c8ed98dac95046c40b22d379dd85ba03d03bb6`，三台暂存包逐个核对一致；三台信任公钥 SHA-256 均为 `0ead75207e00556f6a65168319864f91e5c14056e53ea5aff179cc9118e68908`，与本地公钥一致。
- 应用前，三台 `/usr/local/bin/clusterguard --version-json` 均为 `2.2/105/d2e5d85`，二进制 SHA-256 均为 `26cf134ff79273581637ddf4befa8415615f04770b097e0ba74e9038bcb31204`；三台升级脚本旧摘要均为 `29657d8906e3034750b1fbd4cf32c01887e460f11e6584b19eb487a31ef2690d`。三台无 `/etc/clusterguard/update-maintenance.json` 和 `.cluster-update.lock`。控制台显示 Leader `.153`、3 个投票节点、0 个活动操作、数据库集群健康、两条复制链路延迟 0 秒。
- 控制台按 `.152`、`.154`、`.153` 执行；09:30:59 报 `succeeded`、3/3、`all node digests and maintenance release verified`、自动故障切换已恢复。三台新升级脚本 SHA-256 均为签名清单所列的 `67ec990d0f03d07bd5d6244de0c2932cd5878224a31e7f2fd1b1920ea7282581`，权限均为 `0750 root:clusterguard`；三台 `clusterguard-ha` 与 `clusterguard-update-helper` 均为 active、维护文件与锁均不存在。三台 HF-05 状态记录均为 `succeeded`、`maintenance_active=false`。
- 应用后控制台显示 Leader `.153`、3 个投票节点、0 个活动操作；MySQL 主库与两条复制链路均健康，复制延迟 0 秒、拓扑无异常。验收只要求 Leader 存在且唯一，不要求固定为 `.153`。

## 边界与后续

- 三台 `clusterguard-ha` 的 `ActiveEnterTimestamp` 均仍为 2026-09-29 16:23 左右，HF-05 没有重启控制面。`.153` 上 `clusterguard-update-helper` 在 09:31:05 停止并启动，日志显示正常的 systemd Stop/Start，`NRestarts=0`；源码 `scripts/clusterguard-update-job.sh` 会在任务记录终态后调度 Helper 自刷新。因此不能声称“所有服务均未重启”。
- 现场 HF-04 原始 `status.json` 仍为 `failed`，没有改写其 `status`、错误、进度或时间；恢复说明的追加式处置尚未执行。控制台当前仍把 HF-04 旧失败记录选作摘要卡，并把热修动作写成“滚动升级”；对应前端源码修复仅在本地分支，尚未构建或部署到现场。
- 未在生产制造失败热修验证自动回退。HF-05 的动态回退路径需要测试环境验证，或在下一次确需替换二进制的热修中验收。
