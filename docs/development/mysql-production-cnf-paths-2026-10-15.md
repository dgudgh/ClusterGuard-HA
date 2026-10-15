# 生产 MySQL cnf 基线与离线路径调整

## 请求与修改前证据

- 用户指定的 `my-master.cnf`（209 行）和 `my-slave.cnf`（194 行）是生产配置，非路径参数以原文件为准；不得擅自调整内存、连接、复制、半同步、隔离级别或初始化参数。
- 源码：本仓库唯一主线 `codex/2.2-postgresql`；开始时 HEAD 为 `d0ce098d26468410acb7e26e5761f019d4e5ddce`，工作树干净，领先远端 6 个提交。
- 旧版对比：`git log` 和 `git diff e08506c..HEAD -- scripts/install_clusterguard.sh scripts/clusterguard-mysql-install.sh scripts/clusterguard-mysql-sync.sh`。现场 3.1.2.10 的构建树为 `e08506c`；后续 `03eb74c` 加入重装前停止自身托管服务的保护，必须保留。
- 旧实现：安装入口支持 `-nd/--database-data`，默认 `/data`；底层安装/同步脚本仍回退到 `/var/lib/clusterguard`，软件、配置、日志和运行目录分别硬编码在 `/opt`、`/etc`、`/var/log`、`/run`。`calculate_memory_tuning` 重写生产参数。
- 当前现场只读核对：三台配置的数据目录为 `/data/mysql/3306/data`，程序为 `/opt/clusterguard/mysql/3306/software`；本次不迁移现有实例、不执行安装。

## 已读门禁与适用阶段

- 已读 `docs/zh-CN/validation-gate-workflow.md`、完整 `docs/zh-CN/upgrade-validation-chain.md`、规则入口、修改与证据规则、后端入口、节点生命周期说明、操作授权规则和离线介质说明。
- `CG-UPGRADE-CONTRACT`；`contract_version=2`；`contract_loaded=true`。
- 本次为 SOURCE：路径规划、模板保真、安装器配置生成、同步及 Agent 引用、离线介质源码映射。ART/FIELD 均未执行，不声称已交付或现场通过。

## 方案与不变量

- 默认实例目录 `/data/mysql/<端口>/`，下设 `data/`、`conf/`、`log/`、`binlog/`、`relaylog/`、`run/`、`tmp/`；数据根目录可自定义。
- 软件默认 `/usr/local/mysql/`，支持单独指定软件目录。实际路径贯通安装、同步、客户端、Agent、诊断及系统服务。
- 生产 cnf 原件保持字节不变；渲染只允许改变路径和经用户明确确认的节点身份/安全角色字段。不得因去重、弃用、性能建议而改写其它参数。
- 用户已明确确认保留逐节点身份和只读启动/writer lease，随后指定 ID 默认为 IP 拼端口：按生产样例采用 IPv4 最后一段拼接端口，`.152:3306` 为 `1523306`，集群重复 ID 在计划阶段拒绝，不再使用哈希。
- 保留托管身份核验、重装停止顺序、非托管数据拒绝、授权、隔离、writer lease、多数派与现有恢复保护。
- 软件与配置由 root 管理，数据库可写目录单独授权；不得递归把软件和私有凭据交给 mysql。既有实例禁止靠移动文件或更改配置直接迁移。

## 验证计划与当前状态

1. 用两份原件逐字节验证入库基线；对渲染结果逐项比较，路径白名单以外的有效参数和原始行必须保留。
2. 默认 `/data`、自定义数据根与软件目录、非默认端口、非法路径、缺模板、重装保护、安装/同步/Agent 生成及介质映射回归。
3. Shell 语法、受影响 Go 测试、source strict 与变异、许可、Markdown 台账及 HTML 重建。
4. 真实离线 Linux 安装、MySQL 启动、参数读回、重启、主从切换和恢复均为 FIELD OPEN；不拿隔离夹具代替现场。

### 软件默认目录纠正（修改前记录）

- 用户补充指定软件默认 `/usr/local/mysql`。已核对分支、HEAD 和工作树；本任务上一版改动已暂存，未提交，保留全部现有改动。
- 对照暂存源码，唯一默认来源 `cg_mysql_layout` 当前回退到 `/data/mysql/<端口>/software`，安装入口帮助和双语手册也沿用该值。本次只将软件默认改为 `/usr/local/mysql`，保留显式自定义选项；实例数据目录继续默认 `/data/mysql/<端口>/data`，生产原件与其它参数不变。
- 验证计划：运行默认/自定义布局、主从模板渲染及实际离线计划/载荷/Agent路径回归；核对默认 `basedir` 和 `plugin_dir`，运行Shell语法、SOURCE严格门禁、许可、Markdown台账并重建生成页。下文原有全量和原生解析记录属于上一版；本次单独记录结果，不视为新默认路径已在现场安装。
- 本轮结果：相关Go回归通过（1.518s），覆盖默认及自定义布局、主从模板逐行保真、`basedir=/usr/local/mysql`和插件路径、离线计划、载荷及Agent路径；四个Shell脚本语法通过，SOURCE严格门禁37/37、许可68项通过。三份本地渲染预览已刷新为新默认，双语HTML已重建。未重新执行原生MySQL解析、打包或现场安装。

已完成原件字节保真、逐行非路径参数保留、默认/自定义路径、IP拼端口和重复ID拒绝、实际安装配置生成及Agent/载荷路径一致性回归。现场实际 MySQL 8.0.44 二进制在独立 `/tmp/cg-cnf-validate-8fi3yeue` 目录解析两份渲染配置，`--validate-config` 均返回 0；未启动数据库、未改现场配置或服务，因此不是安装/FIELD通过。全量 `go test ./scripts -count=1` 通过（102.168s），Agent配置测试通过；source strict 37/37、83个失败变异及两个对照、许可68项通过。初次全量测试因沙箱禁止本地HTTP监听受阻，允许本地测试监听后重新完整执行并通过；没有把环境失败计为通过。ART/FIELD保持未执行，新离线介质未构建。


## 交付边界与复核入口

- 两份原件入库为 `configs/mysql/my-master.cnf`、`configs/mysql/my-slave.cnf`，SHA-256分别为 `100c5354517e774421cdb0ea713a607288d8d298ab0a1c8eb515d01ceaa91d1b`、`ae4bf82b60620165731d27744817980ae1bcd50865c4a1b008c4817039b4069c`。
- `scripts/clusterguard-mysql-layout.sh` 负责唯一的路径和ID规则；生产模板随离线介质、运行包和RPM一起入包，制品校验新增三处字节一致检查。只更新构建映射，不把未构建的介质记为ART通过。
- 系统服务登记文件留在 `/etc/systemd/system/` 以便数据盘挂载前可读；其启动参数引用实际目录并声明 `RequiresMountsFor`。数据库参数、凭据及轮转规则实际位于实例 `conf/`。自定义软件目录必须为空或属于同一实例，拒绝覆盖其他实例的软件。
- 针对 `set -u` 下独立配置生成发现并修正旧 `local host=... target=...${host}...` 同行展开问题，避免依赖调用者的动态作用域。
- 本地日志和三台渲染预览位于不入库的 `.build/mysql-production-cnf-20261015/`。这不是升级补丁或离线介质交付目录；已发布制品未重建。

文档台账11项通过；双语安装手册对应生成页已重建（生成器共104页）。已确认唯一主线，未推送、未构建新介质，现场安装与HA验收仍为OPEN。
