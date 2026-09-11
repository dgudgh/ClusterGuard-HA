const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/end-to-end-audit-20260910/bootstrap');

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  let mode, release, blocked = false;
  control.hook = async ({ url, cluster }) => {
    if (['slow-other-cluster', 'late-fleet'].includes(mode) && cluster === clusters[1] && url.pathname.endsWith('/topology')) {
      if (!blocked) { blocked = true; await new Promise(resolve => { release = resolve; }); }
      else { const result = fixture.topology(cluster); result.observed_at = '2026-09-10T10:00:00Z'; return { result }; }
    }
    if (mode === 'task-service-failure' && url.pathname === '/api/v1/nodes/sync/tasks') return { status:503, message:'fixture unavailable task service' };
    if (mode === 'capability-service-failure' && url.pathname === '/api/v1/capabilities') return { status:503, message:'fixture unavailable capabilities' };
    if (mode === 'cluster-list-failure' && url.pathname === '/api/v1/clusters') return { status:503, message:'fixture unavailable cluster list' };
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  const checks = [];
  try {
    for (const scenario of ['slow-other-cluster', 'task-service-failure', 'capability-service-failure', 'cluster-list-failure', 'late-fleet']) {
      mode = scenario; blocked = false;
      await page.goto(`http://127.0.0.1:${server.address().port}/?case=${scenario}#topology`);
      let ready = true;
      try { await page.waitForFunction(() => state.clusterDataReady, null, { timeout:2000 }); }
      catch { ready = false; }
      const evidence = await page.evaluate(() => ({ ready:state.clusterDataReady, loggedIn:!!state.currentUser, login:byId('login-status').textContent, locked:!state.switchUnlocked, executeDisabled:byId('execute-switchover').disabled, shellVisible:!byId('console-shell').hidden, loginHidden:byId('login-shell').hidden, liveStatus:byId('live-status').textContent }));
      let passed;
      if (scenario === 'capability-service-failure') passed = !ready && evidence.executeDisabled;
      // A cluster-list read failure is a data error, not session expiry: the
      // authenticated shell must survive and show a retryable error message.
      else if (scenario === 'cluster-list-failure') passed = evidence.loggedIn && evidence.shellVisible && evidence.loginHidden && /集群列表读取失败/.test(evidence.liveStatus);
      else passed = ready && evidence.loggedIn;
      if (scenario === 'task-service-failure') passed &&= await page.evaluate(() => !byId('node-load-error').hidden && byId('open-node-lifecycle-modal').disabled);
      if (scenario === 'late-fleet' && ready) {
        await page.locator('#cluster-select').selectOption(clusters[1].resource_id);
        try { await page.waitForFunction(() => state.clusterDataReady, null, { timeout:3000 }); }
        catch (error) { console.error(await page.evaluate(() => ({ error:state.clusterLoadError, loading:state.clusterLoading, status:byId('live-status').textContent })), control.requests.slice(-12)); throw error; }
        release?.(); release = null; await page.waitForTimeout(100);
        passed &&= await page.evaluate(() => state.fleetTopology.get(state.selectedClusterId).observed_at === '2026-09-10T10:00:00Z');
      }
      checks.push({ scenario, passed, blocked, ...evidence });
      release?.(); release = null;
      await page.screenshot({ path:path.join(output, `${scenario}.png`) });
    }
    const report = { status:baseline ? 'baseline' : checks.every(check => check.passed) ? 'passed' : 'failed', checks, failures:checks.filter(check => !check.passed).length };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify(report));
    if (!baseline) assert.equal(report.failures, 0);
  } finally { release?.(); await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
