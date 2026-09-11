const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const output = process.env.CONSOLE_TEST_OUTPUT || path.resolve(__dirname, '../.build/console-latency-91/performance');
const baseline = path.resolve(__dirname, '../.build/console-latency-91/release-90.html');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };

async function measure(browser, oldVersion) {
  const fixture = createConsoleFixture(oldVersion ? baseline : undefined);
  const { server, control, clusters, operations } = fixture;
  const templates = [operations[0], operations[3]];
  operations.splice(0, operations.length, ...Array.from({length:400}, (_, i) => ({
    ...templates[i % 2], resource_id:`00000000-0000-4000-8000-${String(i + 1).padStart(12, '0')}`,
    created_at:new Date(Date.now() - i * 1000).toISOString(),
    plan:{ ...templates[i % 2].plan, steps:[{ name:'full-history-evidence', evidence:'x'.repeat(7500) }] }
  })));
  const fullHistoryBytes = Buffer.byteLength(JSON.stringify(operations));
  // Reproduce the field's 3 MB audit transfer without changing database behavior.
  control.hook = async ({url}) => {
    if (url.pathname === '/api/v1/operations' && !['summary', 'context', 'page'].includes(url.searchParams.get('view'))) await delay(2400);
  };
  const gates = [];
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const page = await browser.newPage({ viewport:{width:1440, height:1000} });
  const errors = [], aborted = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('requestfailed', request => aborted.push(request.url()));
  const ready = () => page.waitForFunction(() => document.querySelector('#topology-risk').textContent === '健康');
  try {
    const startup = Date.now();
    await page.goto(`http://127.0.0.1:${server.address().port}/#topology`);
    await ready();
    const startupMs = Date.now() - startup;
    const switches = [];
    for (const index of [1, 0, 1]) {
      const start = Date.now();
      await page.locator('#cluster-select').selectOption(clusters[index].resource_id);
      await page.waitForFunction(name => document.querySelector('#topology-cluster-name').textContent === name && document.querySelectorAll('.node-card').length === 3, clusters[index].display_name);
      const firstPaintMs = Date.now() - start;
      await ready();
      switches.push({firstPaintMs, readyMs:Date.now() - start});
    }
    if (!oldVersion) {
      assert.ok(switches.every(item => item.firstPaintMs < 600), 'topology must not wait for full audit evidence');
      assert.equal(control.requests.filter(item => item.path === '/api/v1/operations' && !/view=(summary|context|page)/.test(item.query)).length, 0);

      const slow = deferred(); gates.push(slow);
      control.hook = async ({url}) => {
        if (url.pathname === '/api/v1/operations' && url.searchParams.has('cluster_id')) await slow.promise;
      };
      const start = Date.now();
      await page.locator('#cluster-select').selectOption(clusters[0].resource_id);
      await page.waitForFunction(name => document.querySelector('#topology-cluster-name').textContent === name && document.querySelectorAll('.node-card').length === 3, clusters[0].display_name, {timeout:1000});
      const slowEvidenceFirstPaintMs = Date.now() - start;
      assert.equal(await page.locator('#switch-lock').isDisabled(), true);
      assert.equal(await page.locator('#topology-risk').textContent(), '读取中');
      assert.equal(await page.locator('#retry-cluster-load').isVisible(), false);
      assert.equal(await page.locator('#live-status').isVisible(), false, 'previous success toast must be cleared');
      await page.screenshot({path:path.join(output, 'slow-evidence-topology-visible.png')});
      slow.resolve(); control.hook = null; await ready();

      const obsolete = deferred(), started = deferred(); gates.push(obsolete);
      control.hook = async ({url, cluster}) => {
        if (cluster === clusters[1] && url.pathname.endsWith('/topology')) { started.resolve(); await obsolete.promise; }
      };
      await page.locator('#cluster-select').selectOption(clusters[1].resource_id); await started.promise;
      await page.locator('#cluster-select').selectOption(clusters[0].resource_id); await ready();
      await page.waitForTimeout(100);
      assert.ok(aborted.some(url => url.includes(clusters[1].resource_id) && url.endsWith('/topology')), 'switching must cancel the obsolete fetch');
      obsolete.resolve(); control.hook = null;

      await page.locator('[data-nav="operation-log"]').click();
      await page.locator('#log-cluster-filter').selectOption('all');
      await page.waitForFunction(() => document.querySelector('#log-visible-count').textContent.includes('400 条原始记录'));
      assert.match(await page.locator('#log-visible-count').textContent(), /400 条原始记录/);
      const detailCount = () => control.requests.filter(item => item.path.startsWith('/api/v1/operations/')).length;
      assert.equal(detailCount(), 0, 'closed raw evidence must not fetch details');
      const details = page.locator('#operation-log-list details').first();
      let failures = 0;
      control.hook = ({url}) => url.pathname.startsWith('/api/v1/operations/') && failures++ === 0 ? {status:503, message:'temporary detail failure'} : null;
      await details.locator('summary').click();
      await details.getByRole('button', {name:'重试读取'}).waitFor({state:'visible'});
      await details.getByRole('button', {name:'重试读取'}).click();
      await page.waitForFunction(() => document.querySelector('#operation-log-list details pre').textContent.includes('full-history-evidence'));
      assert.equal(detailCount(), 2);
      await details.locator('summary').click(); await details.locator('summary').click();
      assert.equal(detailCount(), 2, 'reopening the same raw evidence must reuse its fetched content');
      assert.equal(await details.getByRole('button', {name:'重试读取'}).isVisible(), false);
      await page.locator('#load-more-operation-log').click();
      assert.match(await page.locator('#log-visible-count').textContent(), /400 条原始记录/);
      assert.equal(operations.length, 400);
      assert.deepEqual(errors, []);
      assert.equal(control.mutations, 0);
      return {version:'fixed-91', fullHistoryBytes, startupMs, switches, slowEvidenceFirstPaintMs, preservedRecords:400, detailsLazyAndRetryable:true, obsoleteRequestsCancelled:true};
    }
    assert.ok(switches.every(item => item.firstPaintMs >= 2300), 'release 90 should reproduce the full-log wait');
    return {version:'released-90', fullHistoryBytes, startupMs, switches};
  } finally {
    gates.forEach(gate => gate.resolve()); await page.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
}

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const browser = await chromium.launch({headless:true, channel:'chrome'});
  try {
    const before = await measure(browser, true);
    const after = await measure(browser, false);
    const result = {status:'passed', scope:'released 90 HTML vs modified HTML; 400-record isolated fixture with simulated 2.4-second full-log latency, not field upgrade acceptance', before, after};
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2) + '\n');
    console.log(JSON.stringify(result));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
