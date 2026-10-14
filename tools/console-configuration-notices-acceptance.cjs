// Real Chrome events against an isolated API fixture; no site writes.
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const fs = require('node:fs');
const path = require('node:path');

const approvalNote = '凭据只按环境变量引用展示：控制台不读取、也不返回其内容。已弃用：数据库操作与自动恢复不再使用它，请改用一次性操作授权；该键仅保留给旧的节点生命周期审批，且引用的环境变量为空时节点生命周期操作会被拒绝。';
const unreadableWarning = '配置文件当前不可读（open /etc/clusterguard/clusterguard.json: permission denied）；以下为进程启动时已生效的值。';
const unknownNote = 'native diagnostic <img src=x onerror="window.__unsafeNote=true"> {0}';

async function main() {
  const out = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/configuration-notices-browser');
  fs.mkdirSync(out, { recursive:true });
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const checks = [];
  const check = (width, name, ok) => {
    checks.push({ width, name, ok });
    console.log(`${ok ? 'PASS' : 'FAIL'} ${width} ${name}`);
    if (!ok) throw new Error(name);
  };
  try {
    for (const width of [1440, 390]) {
      const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
      const view = {
        file_present:true, path:'/etc/clusterguard/clusterguard.json',
        reload_supported:false, reload_note:'参数变更需要重启节点；重新读取只刷新显示。', warnings:[],
        sections:[{ key:'runtime', label:'控制面运行参数', values:[
          { key:'approval_token_env', value:'环境变量 CG_APPROVAL_TOKEN', source:'file', restart_required:true, credential_ref:true, note:approvalNote },
          { key:'opaque_diagnostic', value:'original value {0}', source:'file', restart_required:true, note:unknownNote }
        ] }]
      };
      fixture.control.hook = async ({ url, req }) => {
        if (req.method !== 'GET') throw new Error(`unexpected mutation ${req.method} ${url.pathname}`);
        if (url.pathname === '/api/v1/control-plane/configuration') return { result:view };
        if (url.pathname === '/api/v1/control-plane/configuration/node') return { result:{ fields:[], values:{}, ready:false } };
        if (url.pathname === '/api/v1/control-plane/configuration/distribution') return { result:{ members:[], tasks:[] } };
        return null;
      };
      await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
      const page = await browser.newPage({ viewport:{ width, height:900 } });
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      const approvalRow = () => page.locator('.configuration-table tr').filter({ has:page.getByText('approval_token_env', { exact:true }) });
      const diagnosticRow = () => page.locator('.configuration-table tr').filter({ has:page.getByText('opaque_diagnostic', { exact:true }) });
      const setLanguage = async language => {
        await page.locator('#settings-status-tab').click();
        await page.locator('#language-select').selectOption(language);
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !state.configurationLoading);
      };
      try {
        await page.goto(`http://127.0.0.1:${fixture.server.address().port}/#settings`);
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !state.configurationLoading && state.configuration);
        check(width, 'retired reload instruction is absent', await page.locator('#configuration-reload-note,.configuration-instruction').count() === 0);
        check(width, 'approval deprecation does not occupy the top warning strip', !await page.locator('#configuration-warnings').isVisible());
        await page.locator('details.configuration-section summary').click();
        check(width, 'Chinese parameter note records deprecation and empty-reference rejection', (await approvalRow().textContent()).includes(approvalNote));
        check(width, 'credential reference has no editable control', await approvalRow().locator('input').count() === 0);
        await setLanguage('en-US');
        const english = await approvalRow().locator('td').last().textContent();
        check(width, 'English parameter note has no Chinese residue', !/[\u3400-\u9fff]/.test(english));
        check(width, 'English note preserves deprecation and lifecycle risk', /Deprecated/.test(english) && /node lifecycle/i.test(english) && /empty/.test(english) && /rejected/.test(english));
        check(width, 'environment variable identifier is preserved', (await approvalRow().textContent()).includes('CG_APPROVAL_TOKEN'));
        check(width, 'unknown diagnostic stays literal and cannot execute markup', (await diagnosticRow().textContent()).includes(unknownNote) && await diagnosticRow().locator('img').count() === 0 && !await page.evaluate(() => window.__unsafeNote));
        check(width, 'runtime parameter value is unchanged', (await diagnosticRow().textContent()).includes('original value {0}'));
        view.file_present = false;
        view.warnings = [unreadableWarning];
        await page.locator('#reload-configuration').click();
        await page.waitForFunction(() => !state.configurationLoading);
        const warning = await page.locator('#configuration-warnings').textContent();
        check(width, 'real file error remains visible and follows English', await page.locator('#configuration-warnings').isVisible() && warning.startsWith('The configuration file is currently unreadable'));
        check(width, 'file-error evidence preserves its path and native reason', warning.includes('/etc/clusterguard/clusterguard.json: permission denied'));
        check(width, 'retired instruction stays absent during a file error', await page.locator('#configuration-reload-note,.configuration-instruction').count() === 0);
        await page.screenshot({ path:path.join(out, `notices-en-${width}.png`), fullPage:true });
        await setLanguage('zh-CN');
        check(width, 'language round trip restores the Chinese parameter note', (await approvalRow().textContent()).includes(approvalNote));
        check(width, 'language round trip preserves the Chinese file warning', (await page.locator('#configuration-warnings').textContent()) === unreadableWarning);
        view.file_present = true;
        view.warnings = [];
        await page.locator('#reload-configuration').click();
        await page.waitForFunction(() => !state.configurationLoading);
        check(width, 'warning strip disappears when the file recovers', !await page.locator('#configuration-warnings').isVisible());
        check(width, 'page has no horizontal overflow', await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        check(width, 'no browser exceptions or write requests', errors.length === 0 && fixture.control.mutations === 0);
      } finally {
        await page.close();
        fixture.server.closeAllConnections();
        await new Promise(resolve => fixture.server.close(resolve));
      }
    }
  } finally {
    await browser.close();
    fs.writeFileSync(path.join(out, 'acceptance.json'), JSON.stringify({ scope:'isolated Chrome; no live node writes', checks, failures:checks.filter(check => !check.ok) }, null, 2));
  }
  console.log(`${checks.length} assertions, 0 failures`);
}
if (require.main === module) main().catch(error => { console.error(error); process.exitCode = 1; });
