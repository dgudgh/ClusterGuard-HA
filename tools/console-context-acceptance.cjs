const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/console-refresh-context/performance');
const before = path.resolve(process.env.CONSOLE_BASELINE || '.build/console-refresh-context/before.html');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const isHistory = url => url.pathname === '/api/v1/operations' && url.searchParams.get('view') !== 'context';
const isContext = url => url.pathname === '/api/v1/operations' && url.searchParams.get('view') === 'context';

async function run(browser, baseline) {
  const { server, control, clusters, operations } = createConsoleFixture(baseline ? before : undefined);
  const templates = [operations[0], operations[3]];
  operations.splice(0, operations.length, ...Array.from({ length:1000 }, (_, i) => ({
    ...templates[i % 2], resource_id:`operation-${String(i).padStart(5, '0')}`,
    created_at:new Date(Date.now() - i * 1000).toISOString(),
    status:i === 998 ? 'running' : i === 999 ? 'indeterminate' : 'succeeded',
    plan:{ ...templates[i % 2].plan, steps:[{ name:'retained-evidence', evidence:'x'.repeat(7500) }] }
  })));
  control.hook = async ({url}) => { if (isHistory(url)) await delay(2400); };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const page = await browser.newPage({ viewport:{width:1440, height:1000} });
  const errors = [], gates = [];
  page.on('pageerror', error => errors.push(error.message));
  const ready = () => page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'ready');
  const select = async index => {
    const start = Date.now();
    await page.locator('#cluster-select').selectOption(clusters[index].resource_id);
    await ready();
    assert.equal(await page.locator('#topology-cluster-name').textContent(), clusters[index].display_name);
    assert.equal(await page.locator('#switch-lock').isDisabled(), false);
    return {engine:clusters[index].engine, readyMs:Date.now() - start};
  };
  try {
    const start = Date.now();
    await page.goto(`http://127.0.0.1:${server.address().port}/#topology`); await ready();
    const startupMs = Date.now() - start;
    const switches = [];
    for (const index of [1, 0, 1]) switches.push(await select(index));
    if (baseline) {
      assert.ok(switches.every(item => item.readyMs >= 2300));
      return {version:'pre-fix-local-91', startupMs, switches};
    }
    assert.ok(startupMs < 1500 && switches.every(item => item.readyMs < 1000), 'refresh still waits for history');
    assert.equal(control.requests.filter(item => item.path === '/api/v1/operations' && !item.query.includes('view=context')).length, 0);
    assert.equal(await page.locator('#fleet-operations').textContent(), '2', 'old running/unreviewed records must count beyond recent five');

    // History can remain outstanding while both engines finish real UI refresh.
    const history = deferred(), requested = deferred(); gates.push(history);
    control.hook = async ({url}) => { if (isHistory(url)) { requested.resolve(); await history.promise; } };
    await page.locator('[data-nav="operation-log"]').click(); await requested.promise;
    assert.match(await page.locator('#operation-log-list').textContent(), /正在读取/);
    await page.locator('[data-nav="topology"]').click();
    const withHistoryPending = [await select(0), await select(1)];
    assert.ok(withHistoryPending.every(item => item.readyMs < 1000));
    history.resolve(); control.hook = null;
    await page.locator('[data-nav="operation-log"]').click();
    await page.waitForFunction(() => document.querySelector('#log-visible-count').textContent.includes('500 条原始记录'));
    await page.locator('#log-cluster-filter').selectOption('all');
    await page.waitForFunction(() => document.querySelector('#log-visible-count').textContent.includes('1000 条原始记录'));
    await page.locator('#load-more-operation-log').click();
    await page.waitForFunction(() => document.querySelectorAll('#operation-log-list .log-card').length === 40);
    const visible = await page.locator('.log-card').count();
    const historyReads = control.requests.filter(item => item.path === '/api/v1/operations' && item.query.includes('view=page')).length;
    const raw = page.locator('#operation-log-list details').first();
    await raw.locator('summary').click();
    await page.waitForFunction(() => document.querySelector('#operation-log-list pre').textContent.includes('retained-evidence'));
    await page.locator('[data-nav="topology"]').click(); await select(1);
    await page.locator('[data-nav="operation-log"]').click();
    assert.equal(await page.locator('.log-card').count(), visible, 'topology refresh reset log pagination');
    assert.equal(await raw.getAttribute('open'), '', 'hidden log DOM was rebuilt');
    assert.equal(control.requests.filter(item => item.path === '/api/v1/operations' && item.query.includes('view=page')).length, historyReads);

    // Audit failure is isolated; retry must preserve access to every record.
    control.hook = ({url}) => isHistory(url) ? {status:503, message:'history offline'} : null;
    await page.locator('#refresh-operation-log').click();
    await page.waitForFunction(() => document.querySelector('.operation-log-load-status')?.textContent.includes('读取失败'));
    assert.equal(await page.locator('#operation-log-list .log-card').count(), visible);
    await page.locator('[data-nav="topology"]').click(); await select(1);
    assert.equal(await page.locator('#switch-lock').isDisabled(), false);
    control.hook = null;
    await page.locator('[data-nav="operation-log"]').click();
    await page.locator('#refresh-operation-log').click();
    await page.waitForFunction(() => !document.querySelector('.operation-log-load-status'));
    await page.waitForFunction(() => document.querySelector('#log-visible-count').textContent.includes('1000 条原始记录'));
    await page.screenshot({path:path.join(output, 'complete-history.png')});

    // Missing/malformed/old-server context never becomes successful empty evidence.
    for (const result of [{status:503, message:'context unavailable'}, {result:[]}, {result:{view:'context', cluster_id:'other', operation_count:0, running_count:0, unreviewed_count:0, historical_source_ids:[], recent:[]}}]) {
      control.hook = ({url}) => isContext(url) && url.searchParams.has('cluster_id') ? result : null;
      await page.locator('#inspect-cluster').click();
      await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'partial');
      assert.equal(await page.locator('#switch-lock').isDisabled(), true);
      assert.equal(await page.locator('.node-card').count(), 3);
      assert.match(await page.locator('#cluster-load-message').textContent(), /操作上下文/);
    }
    await page.screenshot({path:path.join(output, 'missing-context-locked.png')});
    control.hook = null;
    await page.locator('#retry-cluster-load').click(); await ready();

    // An obsolete cluster's context cannot overwrite the newly selected cluster.
    const context = deferred(), contextRequested = deferred(); gates.push(context);
    control.hook = async ({url}) => {
      if (isContext(url) && url.searchParams.get('cluster_id') === clusters[0].resource_id) { contextRequested.resolve(); await context.promise; }
    };
    await page.locator('#cluster-select').selectOption(clusters[0].resource_id); await contextRequested.promise;
    assert.equal(await page.locator('#switch-lock').isDisabled(), true);
    await select(1); context.resolve(); control.hook = null; await delay(100);
    assert.equal(await page.locator('#topology-cluster-name').textContent(), clusters[1].display_name);
    assert.equal(await page.locator('#switch-lock').isDisabled(), false);
    assert.deepEqual(errors, []); assert.equal(control.mutations, 0); assert.equal(operations.length, 1000);
    return {version:'context-fix', startupMs, switches, withHistoryPending, retainedRecords:1000, historyIndependent:true, contextFailClosed:true, paginationAndRawDetailsPreserved:true, staleContextRejected:true};
  } finally {
    gates.forEach(gate => gate.resolve()); await page.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
}

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const browser = await chromium.launch({headless:true, channel:'chrome'});
  try {
    const result = {scope:'isolated actual console HTML, 1000 records and identical simulated 2400 ms audit latency; not field acceptance', before:await run(browser, true), after:await run(browser, false), status:'passed'};
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2)); console.log(JSON.stringify(result));
  } finally { await browser.close(); }
})().catch(error => {console.error(error); process.exitCode = 1;});
