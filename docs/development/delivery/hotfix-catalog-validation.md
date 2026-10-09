# 私有制品台账与浏览器验收入口

先读[门禁流程](../../zh-CN/validation-gate-workflow.md)；维护制品、签名或执行更新前完整读[强制契约v2](../../zh-CN/upgrade-validation-chain.md)。本页只说明工具输入，不降低SOURCE/ART/FIELD要求。

## 公开源码与私有交付分别选择输入

默认`verify-hotfix-patch-catalog.cjs`读取公开仓库的台账、声明和中英文目录。私有签名制品未进入公开台账时，默认检查可报未登记、未声明目录或修复未覆盖；不能把红结果自动当成通过，也不能增加“已知红名单”。选择私有输入后运行同一批完整判据。

```bash
node tools/verify-hotfix-patch-catalog.cjs \
  --ledger /private/hotfix-publications.private.json \
  --spec-dir /private/specs \
  --catalog-en /private/catalog/en.md \
  --catalog-zh /private/catalog/zh.md \
  --public-key /private/trusted-public.pem
```

`--repo`仍指真实源码；`--artifact-root`仅在制品位于另一工作树时指定。私有声明目录只放当前声明JSON；历史替代声明放子目录，不让过期声明成为当前入口。私有目录须具备全部对应声明，不只是最新一份规格。工具打印所有输入路径；未知选项、缺值、丢失/非法台账、重复身份、坏声明仍拒绝。缺公钥明确SKIPPED，不冒充验签通过。

私有台账、声明、目录、签名包和摘要均留在受保护的本地交付目录；不得为了门禁变绿提交到公共GitHub。生成私有目录使用同一台账：

```bash
node scripts/render-hotfix-catalog.cjs \
  --ledger /private/hotfix-publications.private.json \
  --out-en /private/catalog/en.md --out-zh /private/catalog/zh.md
node --test tools/verify-hotfix-catalog-context.test.cjs
```

这验证已有制品身份，不证明当前所有未发布修改已经进入旧包。新修复须使用新包、新验收绑定；已签名产物不覆盖。

## 真实Chrome与隔离API浏览器测试

所需：Node、可加载的Playwright库、本机Google Chrome。测试使用已安装Chrome通道，Playwright浏览器缓存只有ffmpeg不说明Chrome不能运行。若依赖由Codex桌面提供，先读取桌面工作区依赖路径，在命令中使用返回的Node可执行路径及库目录`NODE_PATH`；不要以系统Node的查找结果推导另一个运行时不可用。

```bash
node -e "console.log(require.resolve('playwright'))"
node tools/console-configuration-distribution-acceptance.cjs
node tools/console-configuration-collapse-acceptance.cjs
```

Go全仓回归中的打包脚本也需要Node；若系统PATH没有Node，显式传`CG_NODE_BIN=/runtime/node go test ./...`，该路径须是已验证的Node可执行文件。

可设置`CONSOLE_TEST_OUTPUT`保存日志、截图和`acceptance.json`。这些是真实浏览器点击、键盘和视口回归，接口由隔离fixture提供，不向现场节点写入，不证明Helper可用、systemd实际重启、VIP或数据库健康。

## 现场证据

仅在获准的维护窗口下发；按[后端运行参数的首次现场清单](../backend/settings/configuration.md)记录实际Helper端点、逐节点重启和参数读回、多数派/VIP/数据库健康及维护释放。没有这些证据时FIELD保持OPEN。
