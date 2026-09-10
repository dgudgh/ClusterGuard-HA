const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || `.build/operation-intent/${baseline ? 'before' : 'after'}`);
const defer = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  let scenario = '', gate = null, waiting = false, submissions = [], prechecks = 0, caseNumber = 0;
  const checks = [], errors = [];
  control.hook = async ({ req, url, cluster }) => {
    if (cluster && url.pathname.endsWith('/topology')) {
      const result = fixture.topology(cluster);
      result.instances[0].health.replication = 'stopped';
      result.instances[0].replication.io_thread = 'stopped';
      return { result };
    }
    if (req.method !== 'POST') return;
    if (url.pathname.endsWith('/discover')) return { result:{} };
    if (url.pathname === '/api/v1/operations/precheck') {
      prechecks++;
      if (scenario.startsWith('rejoin-')) { waiting = true; await gate.promise; }
      return { result:{ checks:scenario === 'rebuild-change' ? [{ name:'rebuild_required', status:'fail' }] : [] } };
    }
    if (url.pathname === '/api/v1/nodes/sync/precheck') {
      waiting = true; await gate.promise; return { result:{ blocked:false } };
    }
    if (['/api/v1/operations/execute', '/api/v1/nodes/sync/execute'].includes(url.pathname)) {
      let raw = ''; for await (const part of req) raw += part;
      const payload = JSON.parse(raw);
      submissions.push({ path:url.pathname, payload });
      const first = submissions.length === 1;
      if (scenario === 'duplicate' || (scenario.startsWith('retry-') && first)) { waiting = true; await gate.promise; }
      if (scenario.startsWith('retry-') && first) return { status:409, message:'stale plan', result:{ failure_class:'stale_plan', status:'blocked', stage:'precheck' } };
      return { status:403, message:'fixture backend refusal; no database was changed' };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless:true, channel:'chrome' });
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  page.on('pageerror', error => errors.push(error.message));
  const loaded = () => page.waitForFunction(() => state.clusterDataReady);
  const settled = () => page.waitForFunction(() => !state.operationRunning);
  const waitGate = async () => {
    for (let i = 0; i < 200 && !waiting; i++) await new Promise(resolve => setTimeout(resolve, 25));
    assert.ok(waiting, 'fixture reached the deferred request');
  };
  const start = async (index, mode, section = 'switchover') => {
    scenario = mode; gate = defer(); waiting = false; submissions = []; prechecks = 0;
    console.log(`case: ${clusters[index].engine}/${mode}`);
    await page.goto(`http://127.0.0.1:${server.address().port}/?case=${++caseNumber}#operations`); await loaded();
    if (index) { await page.locator('#cluster-select').selectOption(clusters[index].resource_id); await loaded(); }
    await page.locator(`#operations-${section}-tab`).click();
    await page.locator('#switch-lock').click();
    assert.match(await page.locator('#switch-lock').textContent(), /已解锁/);
  };
  const record = (engine, name, expected) => {
    const result = { engine, name, expected_submissions:expected, actual_submissions:submissions.length };
    result.passed = result.actual_submissions === expected;
    checks.push(result);
    if (!baseline) assert.ok(result.passed, JSON.stringify(result));
  };
  try {
    for (const index of [0, 1]) {
      const engine = clusters[index].engine;
      await start(index, 'duplicate');
      await page.evaluate(() => {
        const button = document.querySelector('#execute-switchover');
        button.dispatchEvent(new MouseEvent('click'));
        button.dispatchEvent(new MouseEvent('click'));
      });
      await waitGate(); await page.waitForTimeout(100); gate.resolve(); await settled();
      record(engine, 'duplicate handler invocation', 1);
      for (const invalidation of ['relock', 'cluster', 'refresh', 'target', 'permission', 'session']) {
        await start(index, `rejoin-${invalidation}`, 'recovery');
        await page.locator('#execute-rejoin').click(); await waitGate();
        if (invalidation === 'cluster') {
          await page.locator('#cluster-select').selectOption(clusters[1 - index].resource_id); await loaded();
        } else if (invalidation === 'refresh') {
          await page.evaluate(() => loadSelectedCluster()); await loaded();
        } else if (invalidation === 'target') {
          await page.evaluate(() => document.querySelector('#rejoin-select').dispatchEvent(new Event('change')));
        } else if (invalidation === 'permission') {
          await page.evaluate(() => { state.currentUser.role = 'viewer'; updateExecutionButtons(); });
        } else if (invalidation === 'session') {
          await page.evaluate(() => showLogin('fixture expired'));
        } else await page.evaluate(() => relockSwitch());
        gate.resolve();
        // Session expiry clears operationRunning before the pending handler returns.
        await page.waitForTimeout(500); await settled();
        record(engine, `late rejoin preflight after ${invalidation}`, 0);
      }
      for (const cancelled of [false, true]) {
        await start(index, cancelled ? 'retry-cancel' : 'retry-valid');
        await page.locator('#execute-switchover').click(); await waitGate();
        if (cancelled) await page.evaluate(() => relockSwitch());
        gate.resolve(); await settled();
        record(engine, cancelled ? 'cancelled stale-plan retry' : 'valid same-intent stale-plan retry', cancelled ? 1 : 2);
      }
    }
    await start(0, 'rebuild-change', 'recovery');
    await page.evaluate(() => { state.lifecycleCapability = { available:true }; });
    await page.locator('#execute-rejoin').click(); await waitGate();
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id); await loaded();
    gate.resolve(); await page.waitForTimeout(500); await settled();
    record('mysql', 'late rebuild preflight after cluster change', 0);
    assert.deepEqual(errors, []);
    assert.equal(control.mutations, 0);
    const report = { status:baseline ? 'baseline-recorded' : 'passed', checks, failures:checks.filter(item => !item.passed).length, scope:'isolated fake APIs only; no field mutations' };
    if (baseline) assert.ok(report.failures > 0, 'baseline must reproduce a real defect');
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report));
  } finally {
    gate?.resolve(); await browser.close(); await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
