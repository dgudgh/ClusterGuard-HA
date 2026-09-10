const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/log-pagination/browser');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
const isPage = url => url.pathname === '/api/v1/operations' && url.searchParams.get('view') === 'page';

(async () => {
  fs.mkdirSync(output, {recursive:true});
  const {server, control, operations, clusters} = createConsoleFixture();
  const templates = [operations[0], operations[3]];
  operations.splice(0, operations.length, ...Array.from({length:83}, (_,i) => ({
    ...templates[i%2], resource_id:`audit-${String(i).padStart(4,'0')}`,
    created_at:new Date(Date.now()-i*1000).toISOString(),
    status:i === 82 ? 'indeterminate' : 'succeeded',
    target_id:i === 82 ? 'oldest-needle' : `node-${i}`,
    plan:{...templates[i%2].plan, steps:[{name:'full-retained-evidence', value:'x'.repeat(15000)}]}
  })));
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  const browser = await chromium.launch({headless:true, channel:'chrome'});
  const page = await browser.newPage({viewport:{width:1440,height:1000}});
  const errors = [], gates = [], responses = [];
  const checks = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => {
    if (isPage(new URL(response.url())) && response.status() === 200) {
      responses.push(response.json().then(body => ({count:body.result.items.length, bytes:JSON.stringify(body).length})));
    }
  });
  const reads = () => control.requests.filter(item => item.path === '/api/v1/operations' && item.query.includes('view=page'));
  const details = () => control.requests.filter(item => item.path.startsWith('/api/v1/operations/'));
  const count = n => page.waitForFunction(n => document.querySelectorAll('#operation-log-list [data-operation-id]').length === n && !document.querySelector('.operation-log-load-status'),n);
  const cards = page.locator('#operation-log-list [data-operation-id]');
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}/#topology`);
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'ready');
    assert.equal(reads().length,0);
    assert.equal(await page.locator('#fleet-operations').textContent(),'1');
    checks.push('no history on cluster refresh; oldest unreviewed record still counts');
    await page.locator('[data-nav="operation-log"]').click(); await count(20);
    assert.equal(reads().length,1); assert.equal(details().length,0);
    assert.equal(new URLSearchParams(reads()[0].query).get('cursor'),null);
    await delay(300); assert.equal(reads().length,1);
    checks.push('first request limited to 20; no prefetch or raw detail');
    await page.locator('#log-cluster-filter').selectOption('all'); await count(20);
    assert.equal(reads().length,2);

    const raw = cards.first().locator('details');
    await raw.locator('summary').click();
    await page.waitForFunction(() => document.querySelector('#operation-log-list pre').textContent.includes('full-retained-evidence'));
    await raw.locator('summary').click(); await raw.locator('summary').click();
    assert.equal(details().length,1);
    const pending = deferred(), requested = deferred(); gates.push(pending);
    control.hook = async ({url}) => { if (isPage(url) && url.searchParams.has('cursor')) { requested.resolve(); await pending.promise; return {status:503,message:'temporary page failure'}; } };
    await page.locator('#load-more-operation-log').click(); await requested.promise;
    // Multiple clicks arriving before the first page completes cannot request it twice.
    await page.locator('#load-more-operation-log').evaluate(button => { button.click(); button.click(); });
    assert.equal(reads().length,3); assert.equal(await page.locator('#load-more-operation-log').isDisabled(),true);
    pending.resolve();
    await page.waitForFunction(() => document.querySelector('.operation-log-load-status')?.textContent.includes('temporary page failure'));
    assert.equal(await cards.count(),20); assert.equal(await raw.getAttribute('open'),'');
    const failedCursor = new URLSearchParams(reads().at(-1).query).get('cursor');
    control.hook = null;
    await page.locator('#load-more-operation-log').click(); await count(40);
    assert.equal(new URLSearchParams(reads().at(-1).query).get('cursor'),failedCursor);
    assert.equal(await raw.getAttribute('open'),''); assert.equal(details().length,1);
    checks.push('duplicate click guarded; failed page retries same cursor; expanded raw detail retained');

    for (const n of [60,80,83]) { await page.locator('#load-more-operation-log').click(); await count(n); }
    const ids = await cards.evaluateAll(items => items.map(item => item.dataset.operationId));
    assert.equal(new Set(ids).size,83); assert.equal(ids.at(-1),'audit-0082');
    assert.equal(await page.locator('#load-more-operation-log').isVisible(),false);
    checks.push('all 83 historical records reachable exactly once');

    const beforeSearch = reads().length;
    await page.locator('#log-search').fill('oldest-nee'); await page.locator('#log-search').fill('oldest-needle');
    await count(1);
    assert.equal(reads().length,beforeSearch+1);
    assert.equal(await cards.first().getAttribute('data-operation-id'),'audit-0082');
    assert.equal(new URLSearchParams(reads().at(-1).query).get('q'),'oldest-needle');
    assert.equal(new URLSearchParams(reads().at(-1).query).has('cursor'),false);
    await page.locator('#log-search').fill(''); await count(20);
    await page.locator('#log-status-filter').selectOption('indeterminate'); await count(1);
    assert.equal(await cards.first().getAttribute('data-operation-id'),'audit-0082');
    await page.locator('#log-status-filter').selectOption('all'); await count(20);
    checks.push('debounced server search and status filter include oldest history');

    const oldFilter = deferred(), oldRequested = deferred(); gates.push(oldFilter);
    control.hook = async ({url}) => { if (isPage(url) && url.searchParams.get('q') === 'oldest') { oldRequested.resolve(); await oldFilter.promise; } };
    await page.locator('#log-search').fill('oldest'); await oldRequested.promise;
    await page.locator('#log-search').fill('no-such-record'); await count(0);
    oldFilter.resolve(); control.hook = null; await delay(200);
    assert.equal(await cards.count(),0);
    await page.locator('#log-search').fill(''); await count(20);
    await page.locator('#log-cluster-filter').selectOption(clusters[1].resource_id); await count(20);
    assert.match(await page.locator('#log-visible-count').textContent(),/41 个事件/);
    assert.equal(new URLSearchParams(reads().at(-1).query).get('cluster_id'),clusters[1].resource_id);
    checks.push('stale filter response discarded; cluster filter starts from first page');
    await page.screenshot({path:path.join(output,'page-20.png')});
    await page.setViewportSize({width:390,height:844});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),true);
    await page.screenshot({path:path.join(output,'page-mobile.png')});
    checks.push('desktop and mobile render without horizontal overflow');
    assert.deepEqual(errors,[]); assert.equal(control.mutations,0); assert.equal(operations.length,83);
    assert.equal(control.requests.filter(item => item.path === '/api/v1/operations' && !/view=(page|context)/.test(item.query)).length,0);
    const pages = await Promise.all(responses);
    assert.ok(pages.every(item => item.count <= 20 && item.bytes < 50000));
    const result = {status:'passed', scope:'isolated actual console HTML, not field acceptance',checks, pages:pages.length,maxPageBytes:Math.max(...pages.map(item => item.bytes)),retained:operations.length};
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2)); console.log(JSON.stringify(result));
  } finally {
    gates.forEach(gate => gate.resolve()); await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  }
})().catch(error => {console.error(error);process.exitCode=1;});
