// Real Chrome, isolated API responses. No requests are sent to production nodes.
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const fs = require('node:fs');
const path = require('node:path');

async function main() {
  const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/configuration-layout-browser');
  fs.mkdirSync(output, { recursive:true });
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const checks = [];
  const check = (width, name, ok) => {
    checks.push({ width, name, ok }); console.log(`${ok ? 'PASS' : 'FAIL'} ${width} ${name}`);
    if (!ok) throw new Error(name);
  };
  try {
    for (const width of [1440, 390]) {
      const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
      const errors = [], writes = [];
      let failPolicy = false;
      let policy = { engines:{ postgresql:{ automatic_failover_suppressed:true } } };
      const value = (key, text, extra = {}) => ({ key, value:text, source:'file', restart_required:true, ...extra });
      const configuration = { file_present:true, path:'/etc/clusterguard/clusterguard.json', warnings:[], sections:[
        { key:'engine:mysql', label:'MySQL', values:[value('automatic_failover_minimum_observations','4'), value('automatic_failover_failure_window_seconds','3 秒'), value('automatic_failover_operation_timeout_seconds','300 秒')] },
        { key:'engine:postgresql', label:'PostgreSQL', values:[value('automatic_failover_minimum_observations','5'), value('automatic_failover_failure_window_seconds','10 秒'), value('automatic_failover_operation_timeout_seconds','360 秒')] },
        { key:'agent', label:'Agent', values:[value('max_concurrent_sessions','2')] },
        { key:'runtime', label:'控制面运行参数', values:[value('control_token_env','CG_CONTROL_TOKEN',{ credential_ref:true })] },
        { key:'cluster_policy', label:'集群策略（复制存储）', values:[value('cluster_policy','summary',{ source:'policy', restart_required:false })] }
      ] };
      const local = { node_id:'controller-one', ready:true, restart_available:true, fields:[{ path:'agent.max_concurrent_sessions', minimum:1, maximum:128, scope:'node' }], values:{ 'agent.max_concurrent_sessions':2 } };
      fixture.control.hook = async ({ req, url }) => {
        const root = '/api/v1/control-plane/configuration';
        if (url.pathname === root) return { result:configuration };
        if (url.pathname === root + '/node') return { result:local };
        if (url.pathname === root + '/distribution') return { result:{ members:[{ resource_id:'controller-one', api_address:'https://192.0.2.1:3000' }], tasks:[] } };
        if (url.pathname === '/api/v1/cluster-policy') {
          if (req.method === 'GET') return failPolicy ? { status:503, message:'policy unavailable' } : { result:{ policy, summary:[] } };
          let body = ''; for await (const part of req) body += part;
          policy = JSON.parse(body); writes.push({ method:req.method, path:url.pathname, body:policy });
          return { result:{ policy, summary:[] } };
        }
        if (req.method !== 'GET') throw Error(`unexpected mutation: ${req.method} ${url.pathname}`);
        return null;
      };
      await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
      const page = await browser.newPage({ viewport:{ width, height:1000 } });
      page.on('pageerror', error => errors.push(error.message));
      try {
        await page.goto(`http://127.0.0.1:${fixture.server.address().port}/#settings`);
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !!state.configurationDistribution?.local?.fields && !!state.clusterPolicy);
        check(width, 'node mode is selected by default', await page.locator('#configuration-nodes-tab').getAttribute('aria-selected') === 'true');
        check(width, 'policy summary is not duplicated in node parameter groups', await page.locator('[data-configuration-section="cluster_policy"]').count() === 0);
        check(width, 'policy form has one stable mount outside node categories', await page.locator('#configuration-policy-panel #configuration-policy').count() === 1 && await page.locator('#configuration-sections #configuration-policy').count() === 0);
        check(width, 'parameter groups are initially collapsed', await page.locator('details.configuration-section[open]').count() === 0);
        await page.locator('[data-configuration-category="editable"] summary').click();
        await page.locator('#configuration-unlock').click();
        const input = page.locator('[data-configuration-path="agent.max_concurrent_sessions"]');
        await input.fill('8');
        check(width, 'parameter input remains in the final column', await input.evaluate(node => node.closest('td') === node.closest('tr').lastElementChild));
        await page.screenshot({ path:path.join(output, `nodes-zh-${width}.png`), fullPage:true });
        await page.locator('#configuration-policy-tab').click();
        check(width, 'policy mode hides targets and the node dispatch bar', !await page.locator('#configuration-targets').isVisible() && !await page.locator('#configuration-action-bar').isVisible());
        check(width, 'switching modes sends no mutations', writes.length === 0);
        check(width, 'current value is from the actual node projection', await page.locator('#cluster-policy-effective-observations').textContent() === '4' && (await page.locator('#cluster-policy-source-observations').textContent()).includes('配置文件'));
        check(width, 'policy scope is all clusters of the selected engine', await page.locator('#cluster-policy-scope').textContent() === '作用于所有 MySQL 集群');
        await page.locator('#cluster-policy-observations').fill('6');
        await page.locator('#configuration-policy-tab').press('ArrowLeft');
        check(width, 'keyboard mode switch restores node actions and preserves drafts', await page.locator('#configuration-action-bar').isVisible() && await input.inputValue() === '8' && await page.locator('[data-configuration-category="editable"] details').getAttribute('open') !== null);
        await page.locator('#configuration-nodes-tab').press('ArrowRight');
        check(width, 'policy draft survives task-view switching', await page.locator('#cluster-policy-observations').inputValue() === '6');
        await page.locator('#save-cluster-policy').click();
        await page.waitForFunction(() => !state.clusterPolicySaving && state.clusterPolicy?.policy?.engines?.mysql?.automatic_failover_minimum_observations === 6);
        check(width, 'policy save preserves the other engine and uses only its own endpoint', writes.length === 1 && writes[0].path === '/api/v1/cluster-policy' && writes[0].body.engines.postgresql.automatic_failover_suppressed === true);
        check(width, 'committed override is displayed with its true source', await page.locator('#cluster-policy-effective-observations').textContent() === '6 次' && await page.locator('#cluster-policy-source-observations').textContent() === '来自引擎策略覆盖');
        check(width, 'a successful policy save does not expose node dispatch controls', !await page.locator('#configuration-action-bar').isVisible());
        await page.locator('#settings-status-tab').click();
        await page.locator('#language-select').selectOption('en-US');
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !state.configurationLoading && !!state.clusterPolicy);
        check(width, 'language redraw preserves policy mode', await page.locator('#configuration-policy-tab').getAttribute('aria-selected') === 'true');
        check(width, 'English policy has no authored Chinese residue', !/[\u3400-\u9fff]/.test(await page.locator('#configuration-policy').innerText()));
        await page.locator('#cluster-policy-engine').selectOption('postgresql');
        check(width, 'engine switch refreshes scope, suppression and node fallback values', (await page.locator('#cluster-policy-scope').textContent()).includes('PostgreSQL') && await page.locator('#cluster-policy-suppressed').isChecked() && await page.locator('#cluster-policy-effective-observations').textContent() === '5');
        configuration.sections.find(section => section.key === 'engine:postgresql').values = [];
        await page.locator('#reload-configuration').click();
        await page.waitForFunction(() => !state.configurationLoading);
        check(width, 'missing projected values stay unconfirmed instead of using defaults', await page.locator('#cluster-policy-effective-budget').textContent() === 'Unconfirmed');
        await page.screenshot({ path:path.join(output, `policy-en-${width}.png`), fullPage:true });
        await page.locator('#clear-cluster-policy').click();
        await page.waitForFunction(() => !state.clusterPolicySaving && Object.keys(state.clusterPolicy?.policy?.engines || {}).length === 0);
        check(width, 'clear explicitly removes overrides for every engine', writes.length === 2 && Object.keys(writes[1].body.engines).length === 0);
        failPolicy = true;
        await page.locator('#settings-status-tab').click();
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => document.querySelector('#cluster-policy-status').textContent.includes('Policy read failed'));
        check(width, 'policy read failure disables save and clear and marks values unconfirmed', await page.locator('#save-cluster-policy').isDisabled() && await page.locator('#clear-cluster-policy').isDisabled() && await page.locator('#cluster-policy-effective-observations').textContent() === 'Unconfirmed');
        check(width, 'both view layouts have no page horizontal overflow', await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        await page.evaluate(() => clearSessionData());
        check(width, 'session cleanup resets mode and removes previous node values', await page.evaluate(() => state.configurationMode === 'nodes' && Object.keys(state.configurationDraft).length === 0) && await page.locator('#configuration-sections details').count() === 0);
        check(width, 'no browser exceptions or parameter-dispatch mutations', errors.length === 0 && writes.every(write => write.path === '/api/v1/cluster-policy'));
      } finally {
        await page.close(); fixture.server.closeAllConnections(); await new Promise(resolve => fixture.server.close(resolve));
      }
    }
  } finally {
    await browser.close();
    fs.writeFileSync(path.join(output, 'acceptance.json'), JSON.stringify({ scope:'isolated Chrome and fixture API; no live node writes', checks, failures:checks.filter(check => !check.ok) }, null, 2));
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
