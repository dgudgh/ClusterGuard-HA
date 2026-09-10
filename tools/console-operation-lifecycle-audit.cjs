const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || `.build/operation-audit/${baseline ? 'before' : 'after'}`);
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  let mode, gate, arrived, preflightGate, preflightArrived, submissions, task, operation, selectedPrimary, operationKeys, sequence = 0;
  const checks = [], errors = [];
  control.hook = async ({ req, url, cluster }) => {
    if (cluster && url.pathname.endsWith('/topology')) {
      const result = fixture.topology(cluster);
      result.instances.forEach((instance, index) => { instance.role = index === selectedPrimary ? 'primary' : cluster.engine === 'mysql' ? 'replica' : 'standby'; });
      result.instances[0].replication.io_thread = 'stopped';
      result.instances[0].health.replication = 'stopped';
      return { result };
    }
    if (url.pathname === '/api/v1/audit-old-session') { arrived = true; await gate.promise; return mode === 'late-success' ? { result:{ old:true } } : { status:401, message:'fixture expired request' }; }
    if (url.pathname === '/api/v1/auth/logout') { arrived = true; await gate.promise; return { status:503, message:'fixture logout failure' }; }
    if (url.pathname.endsWith('/discover')) {
      if (mode.startsWith('refresh')) { arrived = true; await gate.promise; }
      return mode === 'refresh-failure' ? { status:503, message:'fixture discovery failure' } : { result:{} };
    }
    if (url.pathname === '/api/v1/operations/precheck') {
      if (mode.startsWith('preflight-')) { preflightArrived = true; await preflightGate.promise; }
      return { result:{ checks:[] } };
    }
    if (url.pathname === '/api/v1/operations/execute') {
      let raw = ''; for await (const data of req) raw += data;
      const body = JSON.parse(raw);
      // Chromium can retry a dropped transport. Model the API's idempotency contract.
      if (operationKeys.has(body.idempotency_key)) return { result:operation };
      operationKeys.add(body.idempotency_key); submissions++;
      if (body.operation.kind === 'switchover' && mode !== 'unverified') selectedPrimary = 0;
      operation = { resource_id:'audit-operation', target_id:body.target_id, operation:body.operation, status:'succeeded', stage:'report', verification:{ passed:mode !== 'unverified' } };
      if (mode === 'lost-response') { req.socket.destroy(); return { result:operation }; }
      return { result:operation };
    }
    if (url.pathname === '/api/v1/operations' && url.searchParams.has('idempotency_key')) return { result:operation };
    if (cluster && url.pathname.endsWith('/recovery/status')) return { result:{ available:true, tasks:task ? [task] : [], recovery:null } };
    if (cluster && url.pathname.endsWith('/recovery/plan')) {
      if (mode.startsWith('disaster-')) { preflightArrived = true; await preflightGate.promise; }
      task = { resource_id:'audit-recovery', cluster_id:cluster.resource_id, stage:'planned', metadata_revision:1, members:fixture.detail(cluster).instances, events:[] };
      return { result:{ task, ready:true } };
    }
    if (cluster && url.pathname.endsWith('/recovery/execute')) {
      submissions++;
      if (mode.startsWith('reject-')) return { status:Number(mode.slice(7)), message:'fixture explicit rejection' };
      return { status:202, result:{ task_id:task.resource_id } };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  page.on('pageerror', error => errors.push(error.message));
  const ready = () => page.waitForFunction(() => state.clusterDataReady);
  const start = async (index, scenario) => {
    mode = scenario; gate = deferred(); arrived = false; preflightGate = deferred(); preflightArrived = false; submissions = 0; task = null; operation = null; selectedPrimary = 1; operationKeys = new Set();
    console.log(`case: ${clusters[index].engine}/${scenario}`);
    await page.goto(`http://127.0.0.1:${server.address().port}/?case=${++sequence}#operations`); await ready();
    if (index) { await page.locator('#cluster-select').selectOption(clusters[index].resource_id); await ready(); }
  };
  const waitArrival = async () => {
    for (let i = 0; i < 100 && !arrived; i++) await page.waitForTimeout(20);
    assert.ok(arrived, 'request reached fixture');
  };
  const record = (engine, name, passed) => {
    checks.push({ engine, name, passed });
    if (!baseline) assert.ok(passed, `${engine}: ${name}`);
  };
  const waitPreflight = async () => {
    for (let i = 0; i < 100 && !preflightArrived; i++) await page.waitForTimeout(20);
    assert.ok(preflightArrived, 'preflight reached fixture');
  };
  const clickLogout = async () => {
    await page.locator('#account-menu-toggle').click(); await page.locator('#logout-button').click(); await waitArrival();
  };
  try {
    for (const index of [0, 1]) {
      const engine = clusters[index].engine;
      for (const [scenario, section, button] of [
        ['success-switch', 'switchover', 'execute-switchover'],
        ['success-rejoin', 'recovery', 'execute-rejoin'],
        ['success-repair', 'recovery', 'execute-repair'],
        ['unverified', 'switchover', 'execute-switchover'],
        ['lost-response', 'switchover', 'execute-switchover']
      ]) {
        await start(index, scenario); await page.locator(`#operations-${section}-tab`).click();
        await page.locator('#switch-lock').click(); await page.locator(`#${button}`).click();
        await page.waitForFunction(() => !state.operationRunning);
        const message = await page.locator('#operation-result').textContent();
        if (scenario === 'lost-response') console.log(JSON.stringify({ scenario, submissions, message }));
        record(engine, scenario, submissions === 1 && (scenario === 'unverified' ? /失败|未完成验证/.test(message) : /成功|完成/.test(message)) && await page.locator('#execute-switchover').isDisabled());
      }
      for (const scenario of ['refresh-success', 'refresh-failure']) {
        await start(index, scenario); await page.locator('#switch-lock').click(); await page.locator('#refresh-cluster').click(); await waitArrival();
        const lockedWhilePending = await page.locator('#execute-switchover').isDisabled();
        gate.resolve(); await page.waitForTimeout(200); if (scenario === 'refresh-success') await ready();
        record(engine, scenario, lockedWhilePending && await page.locator('#execute-switchover').isDisabled());
      }
      await start(index, 'refresh-stale'); await page.locator('#refresh-cluster').click(); await waitArrival();
      await page.locator('#cluster-select').selectOption(clusters[1 - index].resource_id); await ready();
      await page.locator('#switch-lock').click(); gate.resolve(); await page.waitForTimeout(250);
      record(engine, 'old refresh completion cannot relock or reload the new cluster', await page.locator('#execute-switchover').isEnabled());

      await start(index, 'logout-pending'); await page.locator('#switch-lock').click();
      await page.locator('#account-menu-toggle').click(); await page.locator('#logout-button').click(); await waitArrival();
      const logoutLocked = await page.locator('#execute-switchover').isDisabled();
      gate.resolve(); await page.waitForTimeout(200);
      record(engine, 'logout pending and failed must stay locked', logoutLocked && await page.locator('#execute-switchover').isDisabled());

      await start(index, 'late-session');
      await page.evaluate(() => { window.auditOldRequest = fetchResult('/api/v1/audit-old-session').catch(() => null); });
      await waitArrival(); await page.evaluate(() => showAuthenticatedConsole({ username:'new-session', role:'admin' }));
      gate.resolve(); await page.evaluate(() => window.auditOldRequest);
      record(engine, 'late 401 must not log out a newly authenticated user', await page.evaluate(() => state.currentUser?.username === 'new-session' && !document.querySelector('#console-shell').hidden));

      await start(index, 'late-success');
      await page.evaluate(() => { window.auditOldRequest = fetchResult('/api/v1/audit-old-session').then(() => 'accepted', error => error.staleSession ? 'ignored' : 'unexpected'); });
      await waitArrival(); await page.evaluate(() => showAuthenticatedConsole({ username:'new-session', role:'admin' }));
      gate.resolve();
      record(engine, 'late success cannot update a new session', await page.evaluate(() => window.auditOldRequest) === 'ignored');

      for (const action of ['refresh', 'logout']) {
        await start(index, `preflight-${action}`); await page.locator('#operations-recovery-tab').click();
        await page.locator('#switch-lock').click(); await page.locator('#execute-rejoin').click(); await waitPreflight();
        if (action === 'refresh') { await page.locator('#refresh-cluster').click(); await ready(); }
        else await clickLogout();
        preflightGate.resolve(); await page.waitForFunction(() => !state.operationRunning);
        record(engine, `${action} during real rejoin preflight must prevent submission`, submissions === 0 && await page.locator('#execute-rejoin').isDisabled());
        gate.resolve(); await page.waitForTimeout(100);

        await start(index, `disaster-${action}`); await page.locator('#operations-disaster-tab').click(); await page.locator('#switch-lock').click();
        await page.locator('#open-disaster-recovery').click(); await waitPreflight();
        // Close the dialog before clicking the real page-level action; do not cancel the server request.
        await page.locator('#close-disaster').click();
        if (action === 'refresh') { await page.locator('#refresh-cluster').click(); await ready(); }
        else await clickLogout();
        preflightGate.resolve(); await page.waitForTimeout(150);
        record(engine, `closed recovery preflight cannot restore confirmation after ${action}`, submissions === 0 && await page.evaluate(() => !state.disaster?.ready && !byId('disaster-dialog').open));
        gate.resolve(); await page.waitForTimeout(100);
      }

      for (const status of [400, 403, 404, 409, 422, 423, 429]) {
        await start(index, `reject-${status}`); await page.locator('#operations-disaster-tab').click(); await page.locator('#switch-lock').click();
        await page.locator('#open-disaster-recovery').click(); await page.locator('#disaster-confirmation').waitFor({ state:'visible' });
        await page.locator('#disaster-confirm-name').fill(clusters[index].display_name); await page.locator('#disaster-confirm-impact').check();
        await page.locator('#disaster-execute').click(); await page.waitForTimeout(150);
        record(engine, `recovery ${status} must not remain pending forever`, await page.evaluate(() => !state.disaster.busy && !state.disaster.ready && state.disaster.pendingRevision == null) && await page.locator('#disaster-execute').isDisabled() && submissions === 1);
      }
      for (const scenario of ['reject-500', 'accepted-pending']) {
        await start(index, scenario); await page.locator('#operations-disaster-tab').click(); await page.locator('#switch-lock').click();
        await page.locator('#open-disaster-recovery').click(); await page.locator('#disaster-confirmation').waitFor({ state:'visible' });
        await page.locator('#disaster-confirm-name').fill(clusters[index].display_name); await page.locator('#disaster-confirm-impact').check();
        await page.locator('#disaster-execute').click(); await page.waitForTimeout(2200);
        record(engine, `${scenario} must track unknown outcome without resubmitting`, await page.evaluate(() => state.disaster.busy && !state.disaster.ready && state.disaster.pendingRevision === 1) && submissions === 1 && await page.locator('#disaster-execute').isDisabled());
      }
    }
    assert.deepEqual(errors, []); assert.equal(control.mutations, 0);
    const report = { status:baseline ? 'baseline-recorded' : 'passed', checks, failures:checks.filter(item => !item.passed).length, scope:'real UI clicks with isolated APIs; no field database operations' };
    if (baseline) assert.ok(report.failures > 0);
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify(report));
  } finally { gate?.resolve(); preflightGate?.resolve(); await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
