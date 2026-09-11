const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/end-to-end-audit-20260910/node-safety');
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, clusters, control } = fixture;
  let gate, prechecks, executions, bodies, mode;
  control.hook = async ({ req, url }) => {
    if (mode === 'task-refresh-failure' && executions && url.pathname === '/api/v1/nodes/sync/tasks') return { status:503, message:'fixture task refresh unavailable' };
    if (url.pathname === '/api/v1/nodes/sync/capabilities') return { result:{ available:true, capabilities:{ clone_available:true, postgresql_basebackup_available:true } } };
    if (url.pathname === '/api/v1/nodes/sync/precheck') { prechecks++; await gate.promise; return { result:{ blocked:false } }; }
    if (url.pathname === '/api/v1/nodes/sync/execute') {
      executions++;
      let raw = ''; for await (const data of req) raw += data;
      bodies.push(JSON.parse(raw));
      return { result:{ status:'succeeded' } };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  const checks = [], errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const ready = async () => {
    try { await page.waitForFunction(() => state.clusterDataReady); }
    catch (error) { console.error(await page.evaluate(() => ({ ready:state.clusterDataReady, login:byId('login-status').textContent, load:state.clusterLoadError })), errors, control.requests.slice(-15)); throw error; }
  };
  const record = (engine, scenario, passed) => { checks.push({ engine, scenario, passed, prechecks, executions }); };
  try {
    for (const index of [0, 1]) for (const scenario of ['closed', 'locked', 'real-click', 'auto-refresh', 'duplicate', 'cluster-change', 'refresh', 'logout', 'close-precheck', 'late-new-session', 'changed-target', 'relocked', 'global-relock', 'task-refresh-failure']) {
      console.log(`${clusters[index].engine}/${scenario}`);
      mode = scenario;
      gate = deferred(); prechecks = 0; executions = 0; bodies = [];
      await page.goto(`http://127.0.0.1:${server.address().port}/?case=${index}-${scenario}#nodes`); await ready();
      if (index) { await page.locator('#cluster-select').selectOption(clusters[index].resource_id); await ready(); }
      if (scenario !== 'closed') await page.locator('#open-node-lifecycle-modal').click();
      await page.evaluate(scenario => {
        byId('node-name').value = 'audit-target'; byId('node-hostname').value = 'audit-target'; byId('node-ip').value = '192.0.2.10';
        if (byId('node-lifecycle-unlock') && scenario !== 'locked') { byId('node-lifecycle-unlock').checked = true; byId('node-lifecycle-unlock').dispatchEvent(new Event('change')); }
        if (scenario !== 'real-click') window.auditNodeRun = executeNodeSync();
      }, scenario);
      if (scenario === 'real-click') await page.locator('#execute-node-sync').click();
      if (scenario === 'closed' || scenario === 'locked') {
        await page.waitForTimeout(120); gate.resolve(); await page.evaluate(() => window.auditNodeRun);
        record(clusters[index].engine, scenario, executions === 0 && prechecks === 0); continue;
      }
      for (let i = 0; i < 100 && !prechecks; i++) await page.waitForTimeout(20);
      assert.ok(prechecks > 0);
      if (scenario === 'duplicate') await page.evaluate(() => { window.auditNodeRun2 = executeNodeSync(); });
      if (scenario === 'cluster-change') await page.evaluate(id => { byId('cluster-select').value = id; return loadSelectedCluster(); }, clusters[1 - index].resource_id);
      if (scenario === 'refresh') await page.evaluate(() => loadSelectedCluster());
      if (scenario === 'auto-refresh') {
        await page.evaluate(() => { state.refreshIntervalMs = 20; scheduleAutoRefresh(); });
        await page.waitForTimeout(120);
        await page.evaluate(() => { state.refreshIntervalMs = 0; scheduleAutoRefresh(); });
      }
      if (scenario === 'close-precheck') await page.evaluate(() => byId('node-lifecycle-modal').close());
      if (scenario === 'changed-target') await page.locator('#node-ip').fill('192.0.2.11');
      if (scenario === 'relocked') await page.evaluate(() => { if (byId('node-lifecycle-unlock')) { byId('node-lifecycle-unlock').checked = false; byId('node-lifecycle-unlock').dispatchEvent(new Event('change')); } });
      if (scenario === 'global-relock') await page.evaluate(() => relockSwitch());
      if (scenario === 'logout') await page.evaluate(() => showLogin());
      if (scenario === 'late-new-session') await page.evaluate(() => { showLogin(); showAuthenticatedConsole({ username:'next-admin', role:'admin' }); return loadClusters(); });
      gate.resolve(); await page.evaluate(() => Promise.all([window.auditNodeRun, window.auditNodeRun2]));
      await page.waitForFunction(() => !state.lifecycleRunning);
      const modalClosed = await page.evaluate(() => !byId('node-lifecycle-modal').open);
      if (scenario === 'task-refresh-failure') {
        await page.evaluate(() => executeNodeSync());
        record(clusters[index].engine, scenario, executions === 1
          && /已完成.*刷新失败/.test(await page.locator('#node-lifecycle-result').textContent())
          && await page.locator('#execute-node-sync').isDisabled()
          && !await page.locator('#node-lifecycle-unlock').isChecked());
      } else {
        record(clusters[index].engine, scenario, ['duplicate', 'real-click', 'auto-refresh'].includes(scenario)
          ? prechecks === 1 && executions === 1 : executions === 0 && (scenario !== 'logout' || modalClosed));
      }
    }
    mode = 'layout';
    await page.goto(`http://127.0.0.1:${server.address().port}/?case=layout#nodes`); await ready();
    await page.evaluate(() => { state.switchUnlocked = true; updateExecutionButtons(); });
    await page.locator('#open-node-lifecycle-modal').click();
    checks.push({ engine:'all', scenario:'node dialog revokes unrelated operation unlock', passed:await page.evaluate(() => !state.switchUnlocked) });
    await page.locator('#cancel-node-lifecycle').click();
    for (const width of [1440, 390]) {
      await page.setViewportSize({ width, height:1000 });
      await page.locator('#open-node-lifecycle-modal').click();
      await page.screenshot({ path:path.join(output, `node-dialog-${width}.png`) });
      const dimensions = await page.locator('#node-lifecycle-modal').evaluate(dialog => ({ width:dialog.getBoundingClientRect().width, scroll:dialog.scrollWidth, client:dialog.clientWidth }));
      checks.push({ engine:'all', scenario:`dialog-layout-${width}`, passed:dimensions.width <= width && dimensions.scroll <= dimensions.client + 1 && await page.locator('#execute-node-sync').isDisabled(), dimensions });
      await page.locator('#cancel-node-lifecycle').click();
    }
    assert.deepEqual(errors, []); assert.equal(control.mutations, 0);
    const report = { status:baseline ? 'baseline' : checks.every(check => check.passed) ? 'passed' : 'failed', checks, failures:checks.filter(check => !check.passed).length };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify(report));
    if (!baseline) assert.equal(report.failures, 0);
  } finally { gate?.resolve(); await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
