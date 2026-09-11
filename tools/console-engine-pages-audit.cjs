const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/end-to-end-audit-20260910/engine-pages');

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  for (const [index, engine] of ['oracle', 'sqlserver'].entries()) clusters.push({ resource_id:`11111111-1111-4111-8111-11111111111${index + 2}`, display_name:`sample-${engine}`, engine, health:{ state:'healthy' } });
  const values = {
    oracle:{ broker_status_healthy:1, transport_lag_seconds:5, apply_lag_seconds:7, role_primary:0 },
    sqlserver:{ always_on_healthy:1, connected:1, synchronized:1, synchronous_commit:1, log_send_queue_bytes:131072, redo_queue_bytes:262144 }
  };
  const normalize = (result, engine) => {
    if (values[engine]) for (const [index, instance] of result.instances.entries()) {
      instance.hostname = `${engine}-${index + 1}`; instance.port = engine === 'oracle' ? 1521 : 1433;
      instance.role = index === 1 ? 'primary' : engine === 'oracle' ? 'standby' : 'replica';
      instance.engine_metadata = { version:engine === 'oracle' ? '19c' : '2022' };
    }
    return result;
  };
  control.hook = async ({ url, cluster }) => {
    if (cluster && url.pathname === `/api/v1/clusters/${cluster.resource_id}`) return { result:normalize(fixture.detail(cluster), cluster.engine) };
    if (cluster && url.pathname.endsWith('/topology')) {
      const result = normalize(fixture.topology(cluster), cluster.engine);
      // A disconnected follower makes unsupported repair actions visible for this audit.
      result.instances[0].replication.io_thread = 'stopped'; result.instances[0].health.replication = 'stopped';
      return { result };
    }
    if (cluster && values[cluster.engine] && url.pathname.endsWith('/metrics')) return { result:{ observed_at:new Date().toISOString(), instances:fixture.detail(cluster).instances.map(instance => ({ instance_id:instance.resource_id, values:values[cluster.engine] })) } };
    if (cluster && values[cluster.engine] && url.pathname.endsWith('/recovery/status')) return { status:400, message:'disaster recovery supports mysql and postgresql only' };
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  const checks = [], errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const check = (engine, scenario, passed, evidence) => checks.push({ engine, scenario, passed, evidence });
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}/#overview`); await page.waitForFunction(() => state.clusterDataReady);
    for (const cluster of clusters) {
      await page.locator('#cluster-select').selectOption(cluster.resource_id); await page.waitForFunction(() => state.clusterDataReady);
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height:1000 });
        for (const view of ['overview', 'topology', 'operations', 'nodes', 'metrics', 'operation-log', 'about', 'settings']) {
          await page.locator(`[data-nav="${view}"]`).click();
          await page.locator(`[data-view="${view}"]`).waitFor({ state:'visible' });
          const dimensions = await page.evaluate(view => ({ visible:!document.querySelector(`[data-view="${view}"]`).hidden, width:document.documentElement.scrollWidth, viewport:innerWidth }), view);
          check(cluster.engine, `${view}/${width}`, dimensions.visible && dimensions.width <= dimensions.viewport + 1, dimensions);
          await page.screenshot({ path:path.join(output, `${cluster.engine}-${view}-${width}.png`), fullPage:true });
        }
      }
      await page.setViewportSize({ width:1440, height:1000 });
      await page.locator('[data-nav="operations"]').click(); await page.locator('#operations-recovery-tab').click();
      await page.locator('#switch-lock').click();
      if (values[cluster.engine]) {
        check(cluster.engine, 'unsupported repairs stay disabled', await page.locator('#execute-repair').isDisabled() && await page.locator('#execute-rejoin').isDisabled());
        await page.locator('#operations-disaster-tab').click();
        check(cluster.engine, 'unsupported disaster recovery stays disabled', await page.locator('#open-disaster-recovery').isDisabled());
        await page.locator('[data-nav="metrics"]').click();
        const summary = await page.locator('#metric-summary').textContent();
        const rows = await page.locator('#metrics-instances').textContent();
        check(cluster.engine, 'engine-specific metrics rendered', !summary.includes('QPS') && (cluster.engine === 'oracle' ? summary.includes('传输延迟') && summary.includes('7') : summary.includes('发送队列') && summary.includes('384') && rows.includes('128')), summary);
        check(cluster.engine, 'missing and false boolean samples stay distinct', await page.evaluate(() => formatMetricValue({ format:'boolean' }, null) === '-' && formatMetricValue({ format:'boolean' }, 0) === '否'));
      }
    }
    check('all', 'no uncaught errors or mutations', !errors.length && control.mutations === 0, errors);
    const report = { status:baseline ? 'baseline' : checks.every(check => check.passed) ? 'passed' : 'failed', checks, failures:checks.filter(check => !check.passed).length, scope:'real browser rendering and mocked API contracts, not native database validation' };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(report, null, 2) + '\n'); console.log(JSON.stringify({ ...report, checks:checks.filter(check => !check.passed) }));
    if (!baseline) assert.equal(report.failures, 0);
  } finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
