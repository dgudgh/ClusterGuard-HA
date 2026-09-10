# 操作锁与灾难恢复：修改前对照

日期：2026-09-09。实际工作树：`clusterguard-ha/.worktrees/platform-auth-session`，分支 `codex/2.2-postgresql`。本文在本次业务代码修改前写入。

## 旧版与当前证据

- 正常旧版 `dc1a57a` 的 `internal/api/console.html`：`updateExecutionButtons` 在第 2155–2164 行用 `switchUnlocked` 控制主库切换、旧主回挂和复制修复；`executeOperation` 第 2281–2282 行再次检查操作锁；集群读取开始时调用 `relockSwitch`。旧版没有灾难恢复功能。
- 当前 HEAD `bc0546a` 仍有上述锁定规则，也没有灾难恢复；灾难恢复来自后续工作树改动，不能虚构一个引入该功能的 Git 提交。
- 已发布 2.2-95 包内控制台与修改前工作树 SHA-256 均为 `fd00f56f589527d85daeb4f679f32188e51bdd6ba8a52263f1241198e64cfe16`。当前默认锁定状态依然是 `switchUnlocked:false`。
- `setOperationsSection` 第 4972 行设置 `switch-lock.hidden = disaster`，但全局按钮 `display:inline-flex` 覆盖浏览器的默认隐藏样式，用户实际仍看见锁。
- 第 4974 行灾难恢复入口仅判断支持引擎和管理员，未判断操作锁。`openDisaster`、`planDisaster`、`renderDisaster` 的执行条件和 `executeDisaster` 也未接入操作锁，后者只依赖 DOM 按钮的 disabled 状态。
- 这是新入口遗漏旧安全交互约束，不是日志工具栏样式变更导致的事件绑定损坏，也不能仅隐藏锁按钮来掩盖。

复核命令：

```sh
git log --all --format='%h %ad %s' --date=short -G 'switchUnlocked|openDisaster|switch-lock' -- internal/api/console.html
git show dc1a57a:internal/api/console.html | rg -n -A 20 -B 3 'const updateExecutionButtons|const executeOperation|const relockSwitch'
git show bc0546a:internal/api/console.html | rg -n 'openDisaster|disaster|switchUnlocked|switch-lock'
shasum -a 256 internal/api/console.html /Users/zhaolongjie/codex/clusterguard-ha/release/2.2-95/console-95.html
```

## 修改范围和不变量

1. 三个操作标签共用可见操作锁。锁定时恢复入口、重新预检及最终提交都不可用，事件处理函数必须独立检查，不能仅灰掉按钮。
2. 恢复操作绑定所选集群和当前管理员身份。锁定、切换集群、刷新或关闭弹窗后，旧确认不得复用，迟到的预检不得重新点亮执行按钮。
3. 保留确认集群名称、隔离风险勾选、预检通过、任务版本、重复提交保护和后台权限校验。已经提交的恢复任务不能因前端重新锁定或关闭窗口被取消，状态仍可查询。
4. 灾难恢复不能以数据库健康或正常切换候选可用为前提。全节点停止/拓扑不可用时，只要当前集群元数据已加载且一致，管理员仍能显式解锁，再由现有后端恢复预检决定是否可恢复。正常切换继续使用原有完整观测门禁，不放宽健康检查。
5. 不改数据库算法、fencing、多数派、writer lease、维护门禁或后端恢复授权；不改已确认的日志工具栏和分页联动。

## 验证计划

修改前用实际 2.2-95 HTML 和隔离 API 分别复现 MySQL/PG 锁定时入口可点、预检可发出、确认后执行可亮的错误。修改后复用相同用户路径，覆盖显式解锁、重新锁定、标签切换、关闭/重开、集群切换、慢预检晚到、登录/权限变化、缺失拓扑、重复提交与已提交任务状态查看；记录截图和 JSON 结果。仅使用模拟恢复端点，不对现场执行恢复。

业务代码修改前已执行 `tools/console-operation-lock-acceptance.cjs --baseline`，结果 `bug-reproduced`：两个引擎的 `locked_entry_blocked`、`locked_preflight_blocked`、`locked_execution_blocked` 均为 false；未执行任何恢复提交。证据在 `.build/operation-lock/before/result.json` 与对应截图。

交付前继续运行日志联动/分页、已有恢复弹窗交互、相关 Go 测试、竞态测试、vet、包签名/回退/HTML 一致性校验。现场安装版本未查询；签名补丁必须明确来源版本，用户手动安装，不直接 SSH 或触发生产恢复。

## 修复后结果

以上对照和修改前复现完成后，才修改业务代码。现已通过：

- `console-operation-lock-acceptance.cjs`：8 组用例，两种引擎均覆盖入口、预检、提交函数的独立检查；重锁后再解锁的迟到预检无效；刷新/换集群/关闭清除确认；缺失拓扑不阻止显式恢复预检；重复提交被阻止；已提交任务可重开查看；只读/操作员拒绝恢复，过期会话不能执行。
- `console-recovery-acceptance.cjs`：既有二次确认、预检失败、单次提交、持久化重开、事件独立滚动、移动端边界、阻断结果均通过。测试增加显式解锁步骤，未删除旧验收项目。
- 日志联动 9 项、分页 7 项、1,000 条日志上下文隔离回归均通过。
- `go test ./...`、`go vet ./...`、`go test -race ./internal/api -count=1` 和 `git diff --check` 通过。
- 2.2-94/95 → 2.2-96 签名包验签、篡改拒绝、回退 RPM、载荷摘要和只读 inspect 均通过；包内 HTML 与测试文件一致。2.2-95 与 2.2-96 的所有静态页面结构、CSS 均逐字一致。

本次操作锁仍是浏览器防误触锁，不将它冒充后端集群操作锁；后台权限、任务版本、隔离、维护门禁和多数派机制没有改动。浏览器恢复端点均为隔离模拟，未执行真实数据库恢复，未部署现场。

## 现场复核与继承测试补充

2026-09-09，用户再次报告未继承锁定功能后，先核对旧源码、已发布 2.2-96 HTML 和三台现场 HTTP 返回内容，再补测试；本轮不修改业务代码或覆盖已发布升级包。

- 三台 `192.168.102.152`、`.153`、`.154` 的 HTTPS 3000 根页面均返回 373938 字节，SHA-256 均为 `a23f1fbf2d705a3b56634f62eabf00136dc6b0b82ebcf7ec00a13812bd65c2d1`，与当前源码和已发布 `2.2-96/console-96.html` 一致。这仅证明服务器返回的页面一致，不冒充已查验现场 RPM 版本。
- 在用户已经打开的 Chrome 页面，刷新前主库切换、旧主恢复/复制修复禁用，但切到灾难恢复时，入口在锁定状态下仍可点。没有点击该危险入口或解锁。
- 使用浏览器“重新加载”后，锁控件从普通按钮变为带 `aria-pressed` 的切换按钮；MySQL 和 PostgreSQL 灾难恢复入口均明确为 disabled。由此确认当前浏览器刷新前后加载的行为不同，不将服务器已返回新代码等同于已打开的旧页面自动更新；未判定具体 HTTP 缓存原因。
- 扩展实际 HTML 的隔离浏览器测试：逐一验证 MySQL/PG 的主库切换、旧主恢复和复制修复在锁定时连强制事件也不能发请求；显式解锁后只提交一次；模拟后端拒绝后恢复锁定；更换候选重新锁定。继续保留原有灾难恢复、权限、慢预检及二次确认测试，不用新增测试替代旧验收。
- 测试增加 `CONSOLE_HTML_PATH`、`CONSOLE_TEST_OUTPUT` 参数，可直接验证现场读取的 HTML，并将新结果保存在独立目录。后端始终是隔离模拟，不向现场转发任何请求。

本轮现场只进行了 GET 读取、页面重载和标签/集群选择；未执行预检、数据库切换、复制修复、灾难恢复、安装或 SSH 操作。测试结果完成后补记，不预先宣称通过。

本轮测试已完成：直接以 `.152` 现场读取的 HTML 为输入，16 组用例全部通过；6 次普通操作提交由模拟后端明确拒绝，1 次灾难恢复为模拟提交，未产生任何意外写请求。结果为 `.build/operation-lock/field-inheritance/result.json`，两种引擎的锁定和确认截图位于同目录；已查看 PostgreSQL 锁定截图。`git diff --check` 通过。此结论是前端防误触功能验证，不是数据库灾难恢复演练或整个 P0 的验收。
