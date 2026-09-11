# 质量审查续轮：协议边界与会话隔离

## 修改前基线

- 实际源码：`.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`，HEAD `19d02bcf076b6e294af0e1f30e2c34f0727e6ae4`。
- 接续用户粘贴的上一轮审查记录，不把记录中的推测或既有修复重复算成本轮成果。
- 已读 `AGENTS.md`、`release-recovery-acceptance-checklist.md`、`quality-review-2026-09-10.md`。
- 比较历史 `bc0546a`（2.2-68）、相关后继 `c8795a3`（2.2-99）与当前工作树：执行 `git log`、`git show bc0546a:<path>`、`git diff bc0546a -- <path>`。
- 当前树包含其他未提交修改，包括 UI 评审稿、适配器和安全修复。全部保留；默认经典样式、可选 `?ui=review` 均不改。
- 本轮修改前控制台保存在 `.build/quality-followup-20260910.92s09T/console-before.html`。全仓基线测试日志为该目录的 `baseline-tests.jsonl`。
- 未重新核实现场安装版本；不接触生产、不安装、不覆盖或重打已交付版本。

## 问题卡与对比证据

### QF-01：单对象协议未拒绝尾随内容

- 链路：Helper `ServeHTTP` / Agent `LoadConfig` / Agent CLI stdin → `json.Decoder.Decode` → 作业启动、配置加载或 `Service.Handle`。
- 旧版和当前均只 Decode 一次，`DisallowUnknownFields` 只约束第一个对象，不验证 EOF。Helper 的 4096 字节上限也没有强制消费完整正文。
- 分类：历史输入边界缺陷，不是最近引入的回归。签名、UID、模式白名单仍在，不能据此宣称可绕过这些授权。
- 复现计划：实际 Helper HTTP handler、配置文件和 CLI 子进程输入合法首对象后追加第二对象、null、垃圾文本；Helper 另测超限尾随空白。拒绝时不得启动 launcher 或修改包权限。
- 修改范围：三个入口局部增加 EOF 验证，不合并全仓解码器，不改变请求字段、模式、签名或恢复授权。保留单对象加合法空白、四种合法升级模式与回滚终态。

### QF-02：登录切换保留旧会话数据

- 链路：实际退出按钮 → `logout` → `showLogin` → 实际新账号登录 → `showAuthenticatedConsole` → `loadConsoleAfterAuthentication` → `loadClusters`。
- `bc0546a` 的 `showLogin` 只隐藏外壳和关闭部分弹窗，没有清除集群、节点、升级信息。当前增加了请求 epoch、锁失效与日志缓存清理，但其他缓存/DOM 仍保留。
- 上一轮 QR-01 正确保留非 401 错误后的已认证外壳；`clearClusterView` 只清当前集群视图，不能清掉旧总览、节点、控制面、升级历史。
- 复现计划：A 账号加载具有明确标记的隔离数据；点击退出后用 B 账号登录，阻塞并失败新集群列表，检查 8 个路由和弹窗是否显示 A 的数据；另测迟到旧响应和失败重试。
- 拟改范围：统一清除会话私有缓存和渲染内容，保持语言等非敏感偏好；不取消已提交后台任务，不恢复旧授权，不把普通数据失败当作退出。

### QF-03：非对象 API 响应与无效正文的 401

- 旧版与当前 `fetchResult` 均先解析 JSON 再访问 `payload.password_change_required`，`null` 导致 TypeError；无效 JSON 的 401 在解析异常分支退出，未调用 `showLogin`。
- 复现计划：浏览器真实刷新路径接收 null / 数组 / 无效 JSON / 无效正文的 401，检查错误提示、重锁和会话失效；迟到旧 401 不得退出新会话。
- 拟改范围：API envelope 最低限度类型校验、401 不依赖正文可解析性；保留 staleSession 优先级、CSRF 和权限规则。

## 执行与验收计划

1. 完成当前树基线 build、vet、全仓测试，记录跳过项。
2. 新增可运行反例，保存修改前失败证据，再做局部修复。
3. 跑新增测试、相关包 race、既有浏览器安全回归、全仓最终测试和 diff 检查。
4. 按本轮 diff 逐项复核并记录未覆盖项，不将同一代理复读称作独立代理审计。
5. 明确区分源码修复、隔离浏览器、原生 PostgreSQL、现场和发布状态。

## 结果

### 已修复与反例

| 项目 | 修改前反例 | 本轮修改 | 当前验证 |
| --- | --- | --- | --- |
| QF-01 | Helper 16 个非法尾部组合返回 202 并改包权限；Agent 配置 3 个尾部被接受；CLI 3 个尾部进入 Service | 三个入口均要求第二次 Decode 返回 `io.EOF`，在配置加载、权限修改或 Service 调用前拒绝尾随内容；错误不回显尾随原文 | 新增 35 个子用例通过，覆盖合法空白、4 种模式、垃圾/第二对象/null、Helper 超限；其中 22 个在旧源码上失败 |
| QF-02 | 实际退出 A 后仍有 2 个集群、3 个节点、1 个包、2 个拓扑缓存；B 登录的列表接口阻塞/503 时仍显示 A 内容 | `clearSessionData` 在登录边界清缓存、选中项、旧表格、升级信息及确认输入，并关闭弹窗；保留服务端历史与任务 | 新脚本实际点击登录/退出、遍历 8 路由、检查隐藏包详情；通过 |
| QF-03 | JSON null 显示 TypeError；数组被当成功；无效 JSON 的 401 未退出 | 先验证响应所属会话，再按 HTTP 401 失效；非对象 envelope 报明确错误 | 真刷新按钮 5 类异常响应、迟到旧 401 不退出新会话，均通过 |

证据目录：`.build/quality-followup-20260910.92s09T/`。

- `input-before.jsonl`：Go overlay 使用 HEAD 中三份原文件运行新增测试，没有回退当前工作树。首次修改前也已直接运行并复现相同失败。
- `input-before-overlay.json`：冻结旧源码的替换映射，便于复跑。
- `browser-before-final/result.json`：同一新版测试脚本运行修改前 HTML，19 项检查中 14 项失败（`--baseline` 只记录反例，不把失败视作通过）。
- `browser-final/result.json`：19 项检查全部通过，包含真实点击以及延迟/失败响应。
- `browser-before-final/new-session-loading.png` / `browser-final/new-session-loading.png`：同尺寸前后截图。
- `browser-final/new-session-mobile.png`：390 × 844 的错误态，已查看；未据此宣称全部移动端交互都验收。

### 回归与复核

| 检查 | 状态 |
| --- | --- |
| 修改前全仓 `go test -json -p 1 ./... -count=1` | 37 个有测试包通过、3 个包无测试、5 个环境用例跳过；见 `baseline-summary.json` |
| 修改后全仓测试 | 37 个有测试包通过、3 个包无测试、5 个环境用例跳过；无失败，见 `final-tests.jsonl` / `final-summary.json` |
| `go build ./...` / `go vet ./...` | 通过 |
| 关联四包 `go test -race` | 通过；Helper、Agent、Agent CLI、API；见 `race.log` |
| `git diff --check` / Go 格式化 | 通过 |
| 本轮 HTML 结构与 CSS 不变 | 逐字比较 `<script>` 前内容，通过；见 `checks-summary.json` |
| 原有九组浏览器回归 | 最终源码复跑全部 exit=0；含 UI、操作意图、集群上下文、操作生命周期、节点安全、引擎页面、引导、升级确认、日志分页；见 `final-browser-regression/summary.json` |
| CI | 本轮未执行 |
| 152–154 现场、升级安装 | 本轮未执行 |
| Git 提交、推送、补丁包、安装包 | 本轮未执行，不覆盖既有 2.2-100 交付物 |

代码复核由当前代理再次按实际 diff 与完整调用链执行，不称作独立代理审计。复核关注：EOF 检查位于副作用之前；现有 SSH 传输用有限 `bytes.Reader` 输入，不依赖保持 stdin 打开；旧响应先校验 auth epoch，再处理 401；会话清理不发送取消后台恢复/升级的请求。未修改 fencing、租约、多数派、签名、审批或权限判定。

全仓测试实际启用了本地 PostgreSQL 16：三实例连续 50 个后台 discovery 周期、WAL 分支选择、分叉提交阻断、业务 guard、受保护启动及重建均在最终日志中通过。其中原生发现测试使用内存仓库，不等于现场 Raft/VIP 全链路验收；MySQL 适配器测试通过也不等于被跳过的容器恢复测试通过。

本轮改动文件仅为 `internal/platformupdate/helper.go`、`internal/agent/config.go`、`cmd/clusterguard-agent/main.go`、`internal/api/console.html` 以及新增的三个 Go 测试文件、`tools/console-session-boundary-audit.cjs` 和本文。其他既有脏工作树内容未回退、未暂存。

### 尚未闭环

1. **高优先级**：Helper 的 Linux `SO_PEERCRED` root/服务 UID 授权尚无本轮原生 Linux 验证；本机没有 `docker` 可执行文件。JSON 拒绝测试不替代特权进程授权验收。
2. **高优先级**：Helper 暂存目录、父目录符号链接与输出文件打开的权限边界仍需 Linux 专项检查；仅从 lexical 路径检查不能推断完整抗竞态安全，本轮未将其标成已修。
3. **高优先级**：现场 MySQL/PG 灾难恢复、滚动升级、信任密钥与 replication secret 轮换未验收。没有执行停库、重新选主或部署。
4. 5 个跳过用例：Docker PostgreSQL entrypoint、3 个 MySQL 实际容器恢复用例、已停 Docker PostgreSQL 现场只读证据用例。环境要求分别为 `CG_DOCKER_INTEGRATION_TESTS`、`CG_MYSQL_RECOVERY_TEST_IMAGE`、显式只读 Agent 配置与集群；不能记为测试通过。
5. Oracle/SQL Server/PXC 原生灾难恢复未验证。QR-02 的执行后刷新语义仍沿用现有安全规则，未放宽确认失效。
6. 其他上一轮提出的电源状态显示、日期/端口校验和大规模重构项，不在本轮已修复清单内。

**结论：本轮三项问题已复现、修复并通过上述本地回归；系统整体与现场发布验收仍为 partial，不宣称所有 P0 已关闭。**
