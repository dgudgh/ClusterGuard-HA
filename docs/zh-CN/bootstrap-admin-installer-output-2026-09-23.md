# 安装器首次管理员口令提示回归记录

## 基线与现象

- 源码基线：`772fbfb^` 的 `internal/runtime/runtime.go` 在未配置专用环境变量时使用 `DefaultBootstrapPassword`，安装器末尾打印 `admin123` 与当时行为一致。
- 回归提交：`772fbfb` 将服务端默认行为改为 `ReadOrCreateBootstrapPassword`，在控制节点的元数据目录生成仅 root 可读的随机口令文件，但未同步修改 `scripts/install_clusterguard.sh` 的安装完成提示。
- 当前已发布包：`release/2.2-103/RELEASE-INFO` 对应 `467e533cd39a9c4224a6fdae70e1c561cf6d980b`；从离线包直接抽出的安装脚本仍打印 `admin123`。现场完成安装后该口令无法登录，与服务端生成随机口令的源码行为一致。现场口令文件本身尚未读取或验证，不声称已完成现场登录验收。
- 当前工作树已有未提交的安装脚本修改，将提示改为手工读取口令文件；本次修改必须保留这些改动，并在其基础上实现用户要求的安装完成时直接显示实际口令。

## 调用链与不变量

`main` -> `wait_control_plane` 确认 Raft Leader -> `verify_installation` -> 安装完成提示。服务端以 `clusterguard` 用户在 Leader 上创建 `/var/lib/clusterguard/bootstrap-admin-password`（路径由 `metadata_path` 推导，权限 0600）；创建管理员时仅持久化口令哈希。首次改密后删除明文文件。已有管理员不应被重新初始化。

必须保持：不恢复公开固定默认口令；不向状态文件、站点秘密、日志或 API 写入明文；不从非权威节点的残留文件猜测口令；读不到可信口令时明确提示，不打印假密码；重复安装不得重置已有管理员。安装已成功与口令展示失败必须分别报告，不能把已完成的远端部署伪装为未执行。

## 拟改范围与验证

只调整 `scripts/install_clusterguard.sh` 的末尾凭据展示及聚焦测试、相应安装文档。安装验收后重新确认 Leader，从该节点归属 `clusterguard` 服务用户且权限为 0600 的文件读取，严格检查口令形态，仅在交互终端直接显示；非交互作业只显示安全读取指令。若文件不存在，说明可能是已有管理员或口令已修改，指导使用受控恢复流程，绝不回退到 `admin123`。测试新装、文件缺失、读取失败、非交互输出与不泄漏到持久化文件。无现场自动重装或真实登录测试时须如实注明。

## 本地验证

- `bash -n scripts/install_clusterguard.sh` 通过。
- 聚焦 Go 测试和 `TestMultiNodeInstaller` 测试组通过；伪终端运行完成提示时显示了合成测试口令，非交互测试未显示它。
- `node tools/verify-license-consistency.cjs` 通过 68 项检查。
- 默认 `PATH` 下的全量 `go test ./scripts -count=1` 未通过：`TestAdapterRuntimeUsesManagedMySQLClientAndWritesReadinessMarker` 解析到了本机 Homebrew 的 MySQL 客户端。以 `PATH=/usr/bin:/bin:/usr/sbin:/sbin /opt/homebrew/bin/go test ./scripts -count=1` 重跑，全部通过（89.060 秒）。
- 未构建或替换已经交付的 `2.2-103` 安装包，未在现场重装或验证首次登录；现场当前密码仍需在生成文件的控制节点上读取。
