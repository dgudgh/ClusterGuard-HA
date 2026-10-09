// Real Chrome, isolated API replies. Does not modify a live node.
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const fs = require('node:fs');
const path = require('node:path');
async function main() {
  const baseline = process.env.CONSOLE_BASELINE === '1';
  const out = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/configuration-groups-browser');
  fs.mkdirSync(out, { recursive: true });
  const checks = [];
  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  try {
    for (const width of [1440, 390]) {
      const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
      const errors = [];
      let releaseCapabilities;
      const delayed = new Promise(resolve => { releaseCapabilities = resolve; });
      let failRead = false;
      const local = { node_id: 'controller-one', ready: true, restart_available: true,
        fields: [
          { path: 'node_lifecycle.control_certificate_validity_days', minimum: 1, maximum: 3650, scope: 'cluster' },
          { path: 'agent.max_concurrent_sessions', minimum: 1, maximum: 128, scope: 'node' }
        ], values: { 'node_lifecycle.control_certificate_validity_days': 825, 'agent.max_concurrent_sessions': 2 } };
      const value = (key, text, extra = {}) => ({ key, value: text, source: 'file', restart_required: true, ...extra });
      const view = { file_present: true, path: '/etc/clusterguard/clusterguard.json', reload_supported: false, reload_note: '参数变更需要重启节点；重新读取只刷新显示。', warnings: [], sections: [
        { key: 'runtime', label: '控制面运行参数', values: [value('http_address', '0.0.0.0:3000')] },
        { key: 'node_lifecycle', label: '节点生命周期与受管凭据', values: [
          value('enabled', '启用'), value('executor_path', '/usr/local/libexec/node.sh'),
          value('control_certificate_validity_days', '825 天'),
          value('ssh_password_env', 'CG_TEST_SECRET', { credential_ref: true })
        ] },
        { key: 'agent', label: 'Agent', values: [value('max_concurrent_sessions', '2'), value('unsafe_nonrestart', 'no', { restart_required: false })] },
        { key: 'cluster_policy', label: '集群策略（复制存储）', values: [value('engine:mysql', 'policy summary', { source: 'policy', restart_required: false })] }
      ] };
      fixture.control.hook = async ({ url, req }) => {
        const root = '/api/v1/control-plane/configuration';
        if (url.pathname === root) return { result: view };
        if (url.pathname === root + '/node') { await delayed; return { result: local }; }
        if (url.pathname === root + '/distribution') return failRead ? { status: 503, message: 'read failed' } : { result: { members: [{resource_id: 'controller-one'}], tasks: [] } };
        if (req.method !== 'GET') throw Error('unexpected write ' + req.method + ' ' + url.pathname);
        return null;
      };
      await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      page.on('pageerror', e => errors.push(e.message));
      const check = async (name, ok) => { checks.push({ width, name, ok }); console.log((ok ? 'PASS ' : 'FAIL ') + width + ' ' + name); if (!ok && !baseline) throw Error(name); };
      try {
        await page.goto('http://127.0.0.1:' + fixture.server.address().port + '/#settings');
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => state.configuration?.sections?.length === 4);
        await check('unread capabilities are explicitly unconfirmed', await page.locator('[data-configuration-category="pending"]').count() === 1);
        releaseCapabilities();
        await page.waitForFunction(() => !!state.configurationDistribution?.local?.fields);
        const editable = page.locator('[data-configuration-category="editable"]');
        const readonly = page.locator('[data-configuration-category="readonly"]');
        const input = page.locator('[data-configuration-path="node_lifecycle.control_certificate_validity_days"]');
        await check('editable groups precede special-workflow parameters', await page.evaluate(() => {
          const top = document.querySelector('[data-configuration-category="editable"]');
          const bottom = document.querySelector('[data-configuration-category="readonly"]');
          return !!top && !!bottom && !!(top.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING);
        }));
        await check('all editors occupy the final column, separate from effective values', await page.locator('input[data-configuration-path]').evaluateAll(inputs => inputs.length === 2 && inputs.every(i => i.parentElement.matches('td:last-child') && i.closest('tr').children[1].querySelector('input') === null)));
        await page.screenshot({ path: path.join(out, baseline ? 'before-' + width + '.png' : 'collapsed-' + width + '.png'), fullPage: true });
        if (baseline) continue;
        await check('capability arrival replaces the pending category', await page.locator('[data-configuration-category="pending"]').count() === 0);
        await check('mixed module is split without losing or duplicating rows', await page.locator('.configuration-table tbody tr').count() === 8 && await editable.locator('details[data-configuration-section="node_lifecycle"] tbody tr').count() === 1 && await readonly.locator('details[data-configuration-section="node_lifecycle"] tbody tr').count() === 3);
        await check('read-only rows never gain inputs, including nonrestart fields', await readonly.locator('input').count() === 0 && (await readonly.textContent()).includes('unsafe_nonrestart'));
        await check('policy editor remains in the upper area with its independent save action', await editable.locator('#configuration-policy').count() === 1 && await readonly.locator('#configuration-policy').count() === 0 && await page.locator('#save-cluster-policy').count() === 1);
        await check('all split groups default to collapsed', await page.locator('details.configuration-section[open]').count() === 0);
        const topGroup = editable.locator('details[data-configuration-section="node_lifecycle"]');
        const bottomGroup = readonly.locator('details[data-configuration-section="node_lifecycle"]');
        await topGroup.locator('summary').click();
        await check('split copies retain independent expansion states', await topGroup.getAttribute('open') !== null && await bottomGroup.getAttribute('open') === null);
        await check('editing column is labeled and locked without changing classification', (await topGroup.locator('th').last().textContent()) === '修改值' && await input.isDisabled());
        await page.locator('#configuration-unlock').click();
        await input.fill('830');
        await page.locator('#cluster-policy-observations').fill('5');
        await page.evaluate(() => loadConfiguration());
        await check('independent policy draft survives parameter table redraw', await page.locator('#cluster-policy-observations').inputValue() === '5');
        await check('reload preserves the draft and independent group expansion', await input.inputValue() === '830' && await topGroup.getAttribute('open') !== null && await bottomGroup.getAttribute('open') === null);
        await bottomGroup.locator('summary').focus();
        await page.keyboard.press('Enter');
        await check('read-only copy can be expanded independently by keyboard', await bottomGroup.getAttribute('open') !== null);
        failRead = true;
        await page.evaluate(() => loadConfigurationDistribution());
        await check('capability read error retains categories and disables editing', await input.isDisabled() && await editable.locator('[data-configuration-path]').count() === 2 && await readonly.locator('input').count() === 0);
        failRead = false;
        await page.evaluate(() => loadConfigurationDistribution());
        await input.focus();
        await page.evaluate(() => loadConfigurationDistribution());
        await check('unchanged polling preserves input focus and clears the read failure notice', await input.evaluate(n => document.activeElement === n) && !(await page.locator('#configuration-distribution-message').textContent()).includes('状态读取失败'));
        await page.locator('#settings-status-tab').click();
        await page.locator('#language-select').selectOption('en-US');
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !state.configurationLoading);
        await check('category headings and final edit column follow English preference', (await editable.locator('.configuration-category-heading').textContent()) === 'Change from this page' && (await readonly.locator('.configuration-category-heading').textContent()) === 'Requires a dedicated change workflow' && (await topGroup.locator('th').last().textContent()) === 'New value');
        await check('language change preserves the draft and both expansion states', await input.inputValue() === '830' && await topGroup.getAttribute('open') !== null && await bottomGroup.getAttribute('open') !== null);
        await page.screenshot({ path: path.join(out, 'english-' + width + '.png'), fullPage: true });
        await page.locator('#settings-status-tab').click();
        await page.locator('#language-select').selectOption('zh-CN');
        await page.locator('#settings-configuration-tab').click();
        await page.waitForFunction(() => !state.configurationLoading);
        await page.screenshot({ path: path.join(out, 'editing-' + width + '.png'), fullPage: true });
        await check('narrow tables scroll inside their group with no page overflow', await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1) && await topGroup.locator('.configuration-table-scroll').evaluate(n => getComputedStyle(n).overflowX === 'auto'));
        await page.evaluate(() => clearSessionData());
        await check('session cleanup removes prior values and keeps the policy editor mount', await page.locator('#configuration-sections details').count() === 0 && await page.locator('#configuration-policy').count() === 1 && await page.evaluate(() => state.configurationExpandedSections.size === 0 && Object.keys(state.configurationDraft).length === 0));
        await check('no browser exceptions or write requests', errors.length === 0 && fixture.control.mutations === 0);
      } finally { releaseCapabilities(); await page.close(); fixture.server.closeAllConnections(); await new Promise(resolve => fixture.server.close(resolve)); }
    }
  } finally { await browser.close(); fs.writeFileSync(path.join(out, 'acceptance.json'), JSON.stringify({ scope: 'isolated Chrome; no live writes', baseline, checks, failures: checks.filter(c => !c.ok) }, null, 2)); }
  if (checks.some(c => !c.ok)) process.exitCode = 1;
}
if (require.main === module) main().catch(e => { console.error(e); process.exitCode = 1; });
