const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || path.join('.build/operation-lock', baseline ? 'before' : 'after'));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  const tasks = new Map(), plans = [], executions = [], standardExecutions = [], errors = [], checks = [];
  let planGate = null, executeGate = null, missingTopology = false, role = 'admin', ready = true, standardPaths = !baseline;
  control.hook = async ({ req, url, cluster }) => {
    if (url.pathname === '/api/v1/auth/me') return { result:{ user:{ username:'fixture', role } } };
    if (missingTopology && url.pathname.endsWith('/topology')) return { result:null };
    if (standardPaths && cluster && url.pathname.endsWith('/topology')) {
      const result = fixture.topology(cluster);
      result.instances[0].health.replication = 'stopped';
      result.instances[0].replication.io_thread = 'stopped';
      return { result };
    }
    if (req.method === 'POST' && url.pathname.endsWith('/discover')) return { result:{} };
    if (req.method === 'POST' && url.pathname === '/api/v1/operations/precheck') return { result:{ checks:[] } };
    if (req.method === 'POST' && url.pathname === '/api/v1/operations/execute') {
      let body = ''; for await (const chunk of req) body += chunk;
      standardExecutions.push(JSON.parse(body));
      return { status:403, message:'fixture backend guard: no database operation is executed' };
    }
    if (!cluster || !url.pathname.includes('/recovery/')) return;
    if (url.pathname.endsWith('/status')) return { result:{ available:true, tasks:tasks.has(cluster.resource_id) ? [tasks.get(cluster.resource_id)] : [], recovery:tasks.has(cluster.resource_id) ? { task_id:tasks.get(cluster.resource_id).resource_id } : null } };
    if (url.pathname.endsWith('/plan')) {
      plans.push(cluster.resource_id);
      if (planGate) await planGate.promise;
      const task = { resource_id:`task-${cluster.resource_id}`, cluster_id:cluster.resource_id, metadata_revision:1, stage:'planned', members:fixture.detail(cluster).instances, events:[] };
      tasks.set(cluster.resource_id, task);
      return { result:{ task, ready, message:ready ? '' : 'fixture preflight blocked' } };
    }
    if (url.pathname.endsWith('/execute')) {
      let body = ''; for await (const chunk of req) body += chunk;
      const payload = JSON.parse(body);
      assert.equal(payload.confirm_cluster, cluster.display_name);
      assert.equal(payload.acknowledge_fencing, true);
      executions.push({ clusterID:cluster.resource_id, payload });
      if (executeGate) await executeGate.promise;
      tasks.set(cluster.resource_id, { ...tasks.get(cluster.resource_id), stage:'fencing', metadata_revision:2 });
      return { status:202, result:{ task_id:payload.task_id } };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless:true, channel:'chrome' });
  const page = await browser.newPage({ viewport:{ width:1667, height:1000 } });
  page.on('pageerror', error => errors.push(error.message));
  const loaded = () => page.waitForFunction(() => ['ready', 'partial'].includes(document.querySelector('#cluster-load-notice').dataset.state));
  const select = async index => { await page.locator('#cluster-select').selectOption(clusters[index].resource_id); await loaded(); };
  const unlock = async () => { await page.locator('#switch-lock').click(); assert.match(await page.locator('#switch-lock').textContent(), /已解锁/); };
  const confirm = async index => {
    await page.locator('#disaster-confirmation').waitFor({ state:'visible' });
    await page.locator('#disaster-confirm-name').fill(clusters[index].display_name);
    await page.locator('#disaster-confirm-impact').check();
  };
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}/#operations`); await loaded();
    if (!baseline) {
      for (const index of [0, 1]) {
        await select(index);
        for (const [section, button, kind] of [
          ['switchover', 'execute-switchover', 'switchover'],
          ['recovery', 'execute-rejoin', 'former_primary_rejoin'],
          ['recovery', 'execute-repair', 'replication_repair']
        ]) {
          await page.locator(`#operations-${section}-tab`).click();
          assert.equal(await page.locator(`#${button}`).isDisabled(), true);
          const beforeRequests = control.requests.filter(item => item.method !== 'GET').length;
          await page.evaluate(id => document.getElementById(id).dispatchEvent(new MouseEvent('click')), button);
          await page.waitForTimeout(100);
          assert.equal(control.requests.filter(item => item.method !== 'GET').length, beforeRequests);
          await unlock();
          assert.equal(await page.locator(`#${button}`).isEnabled(), true);
          await page.locator('#switch-lock').click();
          assert.equal(await page.locator(`#${button}`).isDisabled(), true);
          await page.evaluate(id => {
            document.getElementById(id).disabled = false;
            document.getElementById(id).click();
          }, button);
          await page.waitForTimeout(100);
          assert.equal(control.requests.filter(item => item.method !== 'GET').length, beforeRequests);
          await unlock();
          const beforeExecutions = standardExecutions.length;
          await page.locator(`#${button}`).click();
          await page.waitForFunction(() => !state.operationRunning);
          assert.equal(standardExecutions.length, beforeExecutions + 1);
          assert.equal(standardExecutions.at(-1).operation.cluster_id, clusters[index].resource_id);
          assert.equal(standardExecutions.at(-1).operation.kind, kind);
          assert.equal(await page.locator(`#${button}`).isDisabled(), true);
          assert.match(await page.locator('#switch-lock').textContent(), /操作锁定/);
          checks.push(`${clusters[index].engine}/${kind}: locked handler rejects forced clicks; explicit unlock submits once; backend rejection restores lock`);
        }
        await page.locator('#operations-switchover-tab').click();
        await unlock();
        const otherCandidate = await page.locator('#candidate-select').evaluate(select => [...select.options].find(option => option.value && option.value !== select.value)?.value);
        assert.ok(otherCandidate);
        await page.locator('#candidate-select').selectOption(otherCandidate);
        assert.equal(await page.locator('#execute-switchover').isDisabled(), true);
        assert.match(await page.locator('#switch-lock').textContent(), /操作锁定/);
        checks.push(`${clusters[index].engine}: changing the target restores the inherited lock`);
      }
      standardPaths = false;
      await select(0);
    }
    for (const index of [0, 1]) {
      if (index) await select(index);
      await page.locator('#operations-disaster-tab').click();
      assert.match(await page.locator('#switch-lock').textContent(), /操作锁定/);
      if (baseline) {
        const entryDisabled = await page.locator('#open-disaster-recovery').isDisabled();
        const before = plans.length;
        await page.locator('#open-disaster-recovery').click(); await confirm(index);
        checks.push({ engine:clusters[index].engine, locked_entry_blocked:entryDisabled, locked_preflight_blocked:plans.length === before, locked_execution_blocked:await page.locator('#disaster-execute').isDisabled() });
        await page.screenshot({ path:path.join(output, `${clusters[index].engine}-locked-but-confirmable.png`) });
        await page.locator('#cancel-disaster').click();
        continue;
      }
      assert.equal(await page.locator('#switch-lock').isVisible(), true);
      assert.equal(await page.locator('#switch-lock').getAttribute('hidden'), null);
      assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      const before = plans.length;
      await page.evaluate(() => { document.querySelector('#open-disaster-recovery').dispatchEvent(new MouseEvent('click')); });
      assert.equal(await page.locator('#disaster-dialog').isVisible(), false);
      assert.equal(plans.length, before);
      await page.screenshot({ path:path.join(output, `${clusters[index].engine}-locked.png`) });
      await unlock();
      for (const name of ['switchover', 'recovery', 'disaster']) {
        await page.locator(`#operations-${name}-tab`).click();
        assert.equal(await page.locator('#switch-lock').isVisible(), true);
      }
      await page.locator('#open-disaster-recovery').click(); await confirm(index);
      assert.equal(await page.locator('#disaster-execute').isEnabled(), true);
      await page.screenshot({ path:path.join(output, `${clusters[index].engine}-unlocked-confirmed.png`) });
      const executionCount = executions.length;
      await page.evaluate(() => document.querySelector('#switch-lock').dispatchEvent(new MouseEvent('click')));
      assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
      await page.evaluate(() => { document.querySelector('#disaster-execute').disabled = false; document.querySelector('#disaster-execute').click(); document.querySelector('#disaster-replan').dispatchEvent(new MouseEvent('click')); });
      assert.equal(executions.length, executionCount);
      assert.equal(plans.length, before + 1);
      await page.locator('#cancel-disaster').click();
      assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      checks.push(`${clusters[index].engine}: visible lock covers entry, preflight and submit; handlers reject forced clicks`);
      tasks.clear();
    }
    if (baseline) {
      assert.ok(checks.every(item => !item.locked_entry_blocked && !item.locked_preflight_blocked && !item.locked_execution_blocked));
    } else {
      await select(0); await unlock();
      planGate = deferred();
      await page.locator('#open-disaster-recovery').click();
      await page.waitForFunction(() => document.querySelector('#disaster-alert').textContent.includes('正在检查'));
      await page.evaluate(() => document.querySelector('#switch-lock').dispatchEvent(new MouseEvent('click')));
      await page.evaluate(() => document.querySelector('#switch-lock').dispatchEvent(new MouseEvent('click')));
      planGate.resolve(); planGate = null;
      await page.waitForFunction(() => !state.disaster.busy);
      assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
      assert.equal(await page.locator('#disaster-confirm-name').inputValue(), '');
      await page.locator('#disaster-replan').click(); await confirm(0);
      assert.equal(await page.locator('#disaster-execute').isEnabled(), true);
      checks.push('lock epoch rejects late preflight even after re-unlock; fresh preflight required');

      await page.evaluate(id => { const el = document.querySelector('#cluster-select'); el.value = id; el.dispatchEvent(new Event('change')); }, clusters[1].resource_id);
      await loaded();
      assert.equal(await page.locator('#disaster-dialog').isVisible(), false);
      assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      checks.push('cluster change closes stale confirmation and restores lock');
      tasks.clear();

      await unlock(); await page.locator('#open-disaster-recovery').click(); await confirm(1);
      await page.evaluate(() => loadSelectedCluster({ preserveOperationResult:true }));
      await loaded();
      assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
      await page.locator('#cancel-disaster').click();
      assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      checks.push('same-cluster refresh invalidates confirmation and closing keeps operations locked');
      tasks.clear();

      missingTopology = true; await select(0);
      assert.equal(await page.locator('#switch-lock').isEnabled(), true);
      await unlock();
      assert.equal(await page.locator('#execute-switchover').isDisabled(), true);
      await page.locator('#open-disaster-recovery').click(); await confirm(0);
      assert.equal(await page.locator('#disaster-execute').isEnabled(), true);
      checks.push('missing topology permits explicit recovery preflight, not normal switchover');
      executeGate = deferred();
      await page.locator('#disaster-execute').click();
      await page.evaluate(() => document.querySelector('#disaster-execute').dispatchEvent(new MouseEvent('click')));
      assert.equal(executions.length, 1);
      await page.locator('#cancel-disaster').click();
      assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      executeGate.resolve(); executeGate = null;
      await unlock(); await page.locator('#open-disaster-recovery').click();
      await page.waitForFunction(() => document.querySelector('#disaster-stage').textContent === '隔离全部节点');
      assert.equal(executions.length, 1);
      checks.push('duplicate submit blocked; closing relocks without cancelling accepted task; reopen reads status');
      await page.locator('#cancel-disaster').click(); tasks.clear(); missingTopology = false;

      for (const deniedRole of ['viewer', 'operator']) {
        role = deniedRole; await page.reload(); await loaded();
        await page.locator('#operations-disaster-tab').click();
        assert.equal(await page.locator('#switch-lock').isDisabled(), true);
        assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
      }
      role = 'admin'; await page.reload(); await loaded();
      await page.locator('#operations-disaster-tab').click(); await unlock(); await page.locator('#open-disaster-recovery').click(); await confirm(0);
      await page.evaluate(() => showLogin('fixture session expired'));
      const before = executions.length;
      await page.evaluate(() => { document.querySelector('#disaster-execute').disabled = false; document.querySelector('#disaster-execute').click(); });
      assert.equal(executions.length, before);
      assert.equal(await page.locator('#disaster-dialog').isVisible(), false);
      checks.push('viewer/operator denied; expired session closes confirmation and cannot execute');
    }
    assert.deepEqual(errors, []);
    const report = { status:baseline ? 'bug-reproduced' : 'passed', scope:'actual console with isolated operation/recovery fixture; no field operations', checks, simulated_executions:executions.length, standard_submissions_rejected_by_fixture:standardExecutions.length, unexpected_mutations:control.mutations };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report));
  } finally {
    planGate?.resolve(); executeGate?.resolve(); await browser.close(); await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
