# 2.2-99 一键安装介质与 GitHub 提交：修改前记录

2026-09-09。用户要求提交 GitHub 并生成安装包。实际工作树 `.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD 与远端同为 `bc0546a`。GitHub 为 `dgudgh/ClusterGuard-HA` 私有仓库，当前默认分支另有其名；本次只推送当前产品分支，不替换默认分支、不强推。

## 基线核对

- 98 交付目录只有 RPM 和 95/96/97 → 98 升级包，缺独立一键安装介质。
- 对比 `git show bc0546a:scripts/build-clusterguard-bundle.sh`、更早 `b33b259` 及当前文件，tar builder 一直只使用 `-ldflags "-s -w"`，没有写入 buildinfo。当前 buildinfo 默认是 `development/0/unknown`。
- RPM builder 已正确注入 version/release/commit/time；offline-kit builder 调用 tar builder 时只有显示用 `--version`。因此完整介质内 RPM 与 tar 程序可能报告不同版本。
- 旧正式 2.2-1 离线介质含 PG 16.4 源码和独立 PG 编译依赖包；这不是所有 MySQL/PostgreSQL 数据库版本的原厂全量集合。

## 拟改范围和不变量

修正 tar builder 的版本元数据并让 offline-kit 显式传入 RPM version/release。保留现有安装脚本、计划优先、显式 execute、签名及依赖校验。不修改界面、数据库逻辑、现场环境或 98 已发布文件；使用新版本 99。

提交截至 98 已测试的功能源码、对应测试、发布与审计文档以及本次构建修复。无关图片删除、废弃设计预览、现场 JSON 输出、Python 缓存、私有凭据和构建产物不进入 Git 提交。以显式文件清单暂存，不使用 git add -A。

新增真实构建回归：解包后执行本机架构控制程序 `--version-json`，并用 `go version -m` 核对三个程序的构建参数，先记录旧版本不一致，再验证修复；最终 Linux x86_64 介质检查版本、二进制格式、入口脚本、内外 SHA256SUMS、公钥、依赖及配置。安装器的 MySQL/PG 计划测试在隔离环境运行，不连接现场主机。初版测试把 cgctl 也按本地 `--version-json` 调用，实际它的 version 子命令查询服务器，已修正测试，不改 CLI 行为也不将该测试错误当作产品缺陷。

99 是 ClusterGuard 平台与多节点安装器，不擅自选择/下载新的数据库版本。交付说明必须列出实际内置介质，缺少的数据库原厂软件或 PG 构建依赖不能写成已经包含。安装器默认 plan，现场部署由用户决定。

## 修改前复现

已在未修改的 builder 上运行 `tools/bundle-version-acceptance.cjs --baseline`。本机实际编译并解包，控制程序报告 `version=development, release=0, commit=unknown, built_at=unknown`，三个程序的 Go 构建参数均未携带发行版本，3 项失败。证据 `.build/offline-kit-99/before/result.json`。现在开始修改构建脚本。

## 2026-09-10 验证进展

实际 `go version -m` 输出在 trimpath 构建下不提供 ldflags。初版测试据此判断参数缺失不可靠；保留原始失败报告，改为包装真实 go 编译进程记录参数，同时核对解包后的模块/架构并执行控制程序 `--version-json`。不为测试给 cgctl 增加查询参数，不放宽产品行为。修复后默认版本标签和自定义标签两组共 6 项通过，控制程序均报告 2.2 / 99。

首次全仓测试在同时构建期间，未改动的 `TestCommandLauncherPublishesGroupReadableOutput` 触发 5 秒上限；对应生产和测试文件与 HEAD 无差异。该用例独立重复 20 次均通过，每次约 0.11-0.12 秒；未修改超时。单次失败尚不足以证明生产缺陷或排除调度问题，因此完整测试按单包并发重新运行，原失败不计入通过。

本轮 Chrome 隔离 API 的真实点击回归已通过 MySQL/PG 共 48 项，覆盖刷新、退出、过期请求、失败提交和结果不确定时禁止重复执行；它不等于真实数据库恢复验收。控制台 HTML SHA256 与 98 保持一致：`38cd47cd975b7e118bd49956d83d3762f0def29da13c88865573e2c221687ec1`。
