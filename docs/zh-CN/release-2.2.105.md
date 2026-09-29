# ClusterGuard HA 2.2-105 发布说明

## 本次变更

本版把 2.2-104 之后在现场暴露并修复的一批缺陷固化进正式介质。相对 2.2-104 的构建提交
`e01f5ce`，代码基线 `b383092` 含 27 个提交，按主题分为四组。**这些修复此前只以签名热修包
（`.cgpatch`）的形式在现场存在，没有进过任何安装介质**；本版是它们第一次随正式版本交付。

### 1. 集群时钟（本版主要变更）

- **修正集群时钟不再让集群进入停机态。** 修复前，把一台走时偏快的机器拨回真实时间，会把拓扑
  观测水位留在未来；此后每一次拓扑刷新都被"观测时间不在前一次之后"拒绝，拓扑永久不新鲜，
  VIP 归属租约不再续期，数据节点按设计摘掉 VIP 并把实例设为只读。**修正时钟这个动作本身会把
  集群推进停机态，且现场无法自解。** 现在超过任何合理排序误差的差距
  （`observationWatermarkRewindTolerance`，5 分钟）被当作"一口已不存在的时钟"：观测放行、
  水位落到新观测、严格顺序从该点重新生效；窗口内的顺序违规仍然拒绝，原有保护不变。
- 时钟回拨时重新锚定法定人数租约，避免租约在回拨瞬间被判失效。
- 隔离部署（无上游时间源）的时钟权威改为**显式选择**，控制台显示时区固定，不再由本机时间
  推导；集群时间配置增加门禁，安装时不再把未校验的本机时间静默升格为权威。
- `clusterguard-clock-mesh.sh` 改为随 RPM 交付（此前是 installer-only），现场可通过正常升级
  路径拿到修正后的副本，而不是长期停留在首次复制的那一份。

### 2. 写入者协调与整机重启可恢复性

- **停止 MySQL 写入者 reconcile 抖动。** 修复前主库每约 10 秒被节点 Agent 自我隔离一次
  （`read_only` 与 VIP 同步抖动），集群长期 degraded、复制链路反复 unhealthy、切换与关机预检
  被阻断。根因是收敛判据要求"重启后只读"这个永久站点不变量变为 false，而它按设计永远为真。
- 共享运行时目录保持可穿越，整机关机后重启不会再出现控制面与恢复单元连环重启的僵局。
- 主机断电时控制台页面自我收尾，不再停在一个已经消失的后端上。

### 3. 控制台

- **"不可用"的原因直接显示在面板与分区横幅上**，不必再靠推断；候选评估等环节被锁定时能直接
  读到服务端给出的判定输入。
- **「账户与偏好」并入「状态设置」**，功能页签回到三个。此前四个页签在常规宽度下换行，
  账户那一枚孤悬一行。账户与显示偏好成为状态面板内的一块，切到其他页签时随之离屏。

### 4. 在线升级与热修补丁通道

- **可从控制台「版本更新 → 上传升级包」安装签名补丁。** 控制器按 `kind=hotfix` 识别并交给
  更新 Helper 逐台应用，与滚动 `.cgupgrade` 走同一条上传接口但互不替代；补丁安装器自身也被签名。
- 更新 Helper 的 SSH 私钥由 root 持有（0600），运行时目录不再要求单元自有——修掉首次启动的
  目录竞态，同时避免停机删除共用目录。
- 跨基线的 `.cgpatch` 拒绝信息改为**明确命名陷阱**（"这个包属于别的基线，装上去会把二进制降级
  回它的发布线"），而不是含糊报错。

> 交付工具链本身（一次现场处理出一个累积补丁包、双语热修台账、相应防漏门禁）也在本版范围内，
> 但它不改变产品运行时行为，只影响我们如何把修复送到现场。

## 验证范围

- `go build ./...`
- `go vet ./...`
- `go test ./... -count=1`（含 `scripts` 包的交付链路测试）
- 全部 shell 脚本 `bash -n` 语法检查
- 观测水位容忍、时钟网格、热修台账等门禁脚本
- `verify-license-consistency.cjs` 许可一致性——**本版改动了 `packaging/rpm/nfpm.yaml`**
  （新增一个随包交付的脚本），许可文件与 RPM 元数据需保持一致
- 离线介质的独立只读校验（`verify-offline-kit.cjs`），以及包内许可文件与仓库原文的逐字节比对

## 介质范围

- MySQL：8.0.44 官方 Linux glibc2.17 x86_64 minimal 通用二进制。
- PostgreSQL：16.4 官方源码包。**源码编译依赖不在主介质内**：明确选择 PostgreSQL 源码时，
  安装器默认从构建节点已配置、启用签名校验的软件源联网安装到一次性隔离根，不升级生产宿主机；
  软件源不可用时，上传与目标发行版、主版本、架构匹配的
  `clusterguard-ha-*-postgresql-build-deps-*.tar.gz`，解压后用 `--postgresql-dependencies` 重试。
- `dependencies/` 只带控制面与 MySQL 启动所需的小型运行依赖闭包（`libaio`、`ncurses-compat-libs`、
  `numactl-libs`，共 3 个 RPM）及其 `repodata`，与 2.2-104 相同。这是构建脚本的既有设计
  （见包内 `dependencies/README.txt` 与 `RELEASE-INFO` 的
  `postgresql_build_dependencies=separate-online-first`），MySQL 场景不受影响。
- ClusterGuard Linux x86_64 RPM、Linux amd64 tar 运行程序、多节点 `install_clusterguard.sh`、
  MySQL/PostgreSQL 接入脚本、示例配置与离线文档中心（`docs/`）。
- 静态 Linux `jq` 与内外两层 SHA256 校验清单。**不包含签名私钥、现场凭据或生产补丁签名公钥**
  （`trust/` 为空目录）。

## 使用入口

```bash
sha256sum -c clusterguard-ha-2.2-105-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.2-105-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.2-105-offline-linux-x86_64
sha256sum -c SHA256SUMS
bash install_clusterguard.sh --help
```

安装器默认只生成计划，显式 `--execute` 才修改主机。首次登录前先取得初始口令：部署设置了
`bootstrap_admin_password_env` 时用该值；未设置时读取控制节点上 `<metadata_path>` 同目录的
`bootstrap-admin-password`（0600，仅 root 可读）。安装器在交互终端下会先从 Leader 读回真实口令
再显示，不打印固定口令。

**已有集群不要用新装命令覆盖部署状态、Raft 数据、数据库目录或身份文件。** 本压缩包不能上传到
升级弹窗；现场升级请使用对应来源版本的签名 `.cgupgrade`（本版为
`clusterguard-ha-2.2-104_to_2.2-105.x86_64.cgupgrade`）。

## 现场验收状态

本版介质已在本地按发版门禁构建并核验，**未上传 GitHub**。三节点滚动升级、真实故障切换与
VIP 自动接管**均未执行**；控制台的 HTTP 层登录与 multipart 上传路径也**未做真实浏览器验收**。
本包不替代现场验签与滚动升级验收，不能仅凭较高版本号当作已发布版本。
