const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');

// This is isolated browser acceptance with simulated API replies, not a claim
// that a field database recovered. It loads the actual shipped console file.
const root = path.resolve(__dirname, '..');
const html = fs.readFileSync(path.join(root, 'internal/api/console.html'));
const output = process.env.CONSOLE_TEST_OUTPUT || path.join(root, '.build/recovery-field-20260908/ui');
fs.mkdirSync(output, { recursive:true });
const cluster = { resource_id:'11111111-1111-4111-8111-111111111111', display_name:'recovery-browser-fixture', engine:'postgresql' };
const members = [1, 2, 3].map(i => ({ resource_id:`22222222-2222-4222-8222-22222222222${i}`, cluster_id:cluster.resource_id, engine:'postgresql', ip_address:`192.0.2.${i}`, port:5432 }));
let task = null, executions = 0, preflightReady = true, executeGate = null, deferBegin = false;
const makeTask = () => ({ resource_id:'33333333-3333-4333-8333-333333333333', cluster_id:cluster.resource_id, metadata_revision:1, engine:'postgresql', members, stage:'planned', events:[], message:'recovery planned' });
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (!url.pathname.startsWith('/api/')) { res.setHeader('Content-Type', 'text/html'); res.end(html); return; }
  let result = [];
  if (url.pathname === '/api/v1/auth/me') result = { user:{ username:'fixture-admin', role:'admin' } };
  else if (url.pathname === '/api/v1/operations' && url.searchParams.get('view') === 'context') result = { view:'context', cluster_id:url.searchParams.get('cluster_id') || '', operation_count:0, running_count:0, unreviewed_count:0, historical_source_ids:[], recent:[] };
  else if (url.pathname === '/api/v1/clusters') result = [cluster];
  else if (url.pathname === `/api/v1/clusters/${cluster.resource_id}`) result = { cluster, instances:members, endpoints:[] };
  else if (url.pathname.endsWith('/topology')) result = null;
  else if (url.pathname.endsWith('/recovery/status')) result = { available:true, tasks:task ? [task] : [], recovery:task && task.stage !== 'planned' ? { task_id:task.resource_id } : null };
  else if (url.pathname.endsWith('/recovery/plan')) { task ||= makeTask(); result = { task, ready:preflightReady, message:preflightReady ? '' : 'Agent 尚未升级，预检未通过' }; }
  else if (url.pathname.endsWith('/recovery/execute')) {
    let body = ''; for await (const chunk of req) body += chunk;
    const payload = JSON.parse(body);
    assert.equal(payload.confirm_cluster, cluster.display_name);
    assert.equal(payload.acknowledge_fencing, true);
    executions++;
    if (executeGate) await executeGate;
    if (!deferBegin) task = { ...task, stage:'fencing', metadata_revision:task.metadata_revision + 1, message:'isolating members' };
    result = { task_id:task.resource_id };
    res.statusCode = 202;
  } else if (url.pathname === '/api/v1/platform/updates') result = { available:false, packages:[] };
  else if (url.pathname.endsWith('/power/status')) result = null;
  else if (url.pathname.endsWith('/capabilities') && url.pathname !== '/api/v1/capabilities') result = { available:false };
  else if (url.pathname === '/api/v1/control-plane/status') result = { mode:'raft', role:'leader', ready:true, quorum_confirmed:true, voter_count:3 };
  else if (url.pathname === '/api/v1/platform/version') result = { version:'fixture' };
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({ status:'ok', result }));
});

(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless:true, channel:'chrome' });
  try {
    const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/#operations`);
    for (const width of [1440, 1024, 768, 390]) {
      await page.setViewportSize({ width, height:1000 });
      const tabs = page.getByRole('tablist', { name:'高可用操作功能区' }).getByRole('tab');
      assert.equal(await tabs.count(), 3);
      const boxes = await tabs.evaluateAll(items => items.map(el => ({ top:el.getBoundingClientRect().top, width:el.getBoundingClientRect().width, clipped:el.scrollWidth > el.clientWidth })));
      assert.ok(boxes.every(box => Math.abs(box.top - boxes[0].top) < 1 && Math.abs(box.width - boxes[0].width) < 1 && !box.clipped), `operation tabs must be equal-width in one row at ${width}px`);
      for (let i = 0; i < 3; i++) {
        await tabs.nth(i).click();
        assert.equal(await tabs.nth(i).getAttribute('aria-selected'), 'true');
        const panel = await tabs.nth(i).getAttribute('aria-controls');
        assert.equal(await page.locator('#' + panel).isVisible(), true);
      }
      await page.screenshot({ path:path.join(output, `operations-tabs-${width}.png`) });
    }
    await page.setViewportSize({ width:1440, height:1000 });
    await page.locator('#operations-disaster-tab').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'partial');
    assert.equal(await page.locator('#open-disaster-recovery').isDisabled(), true);
    await page.locator('#switch-lock').click();
    await page.locator('#open-disaster-recovery').click();
    await page.locator('#disaster-confirmation').waitFor({ state:'visible' });
    assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
    await page.locator('#disaster-confirm-name').fill('wrong-name');
    await page.locator('#disaster-confirm-impact').check();
    assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
    await page.locator('#disaster-confirm-name').fill(cluster.display_name);
    assert.equal(await page.locator('#disaster-execute').isEnabled(), true);
    await page.screenshot({ path:path.join(output, 'desktop-confirmation.png') });
    let release; executeGate = new Promise(resolve => { release = resolve; });
    await page.locator('#disaster-execute').click();
    assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
    await page.waitForTimeout(100);
    assert.equal(executions, 1, 'submission must not be duplicated');
    release(); executeGate = null;
    await page.waitForFunction(() => document.querySelector('#disaster-stage').textContent === '隔离全部节点');
    task = { ...task, stage:'rebuilding_replicas', primary_id:members[1].resource_id, events:Array.from({ length:60 }, (_, i) => ({ at:new Date().toISOString(), instance_id:members[0].resource_id, stage:'rebuilding_replicas', message:`Node replication event ${i}: observed streaming and native identity` })) };
    await page.waitForFunction(() => document.querySelectorAll('.disaster-event').length === 60);
    const before = await page.locator('#disaster-stage').boundingBox();
    await page.locator('#disaster-events').hover();
    await page.mouse.wheel(0, 700);
    await page.waitForTimeout(150);
    assert.equal((await page.locator('#disaster-stage').boundingBox()).y, before.y, 'progress header must not scroll with events');
    assert.ok(await page.locator('#disaster-events').evaluate(el => el.scrollTop > 0), 'events must scroll independently');
    await page.screenshot({ path:path.join(output, 'desktop-progress.png') });
    await page.locator('#cancel-disaster').click();
    await page.locator('#switch-lock').click();
    await page.locator('#open-disaster-recovery').click();
    await page.waitForFunction(() => document.querySelector('#disaster-stage').textContent === '重建从库');
    assert.equal(executions, 1, 'reopening must not start recovery again');
    await page.setViewportSize({ width:390, height:844 });
    const bounds = await page.locator('#disaster-dialog').boundingBox();
    assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= 390 && bounds.y + bounds.height <= 844);
    const events = await page.locator('#disaster-events').boundingBox();
    const footer = await page.locator('#disaster-dialog .dialog-actions').boundingBox();
    assert.ok(events.y + events.height <= footer.y + 1, 'events may not overlap footer');
    await page.screenshot({ path:path.join(output, 'mobile-progress.png') });
    task = { ...task, stage:'blocked', message:'independent business COMMIT histories detected' };
    await page.waitForFunction(() => document.querySelector('#disaster-stage').textContent === '恢复已阻断');
    assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
    await page.locator('#disaster-replan').click();
    await page.locator('#disaster-confirmation').waitFor({ state:'visible' });
    await page.locator('#disaster-confirm-name').fill(cluster.display_name);
    await page.locator('#disaster-confirm-impact').check();
    deferBegin = true;
    await page.locator('#disaster-execute').click();
    await page.waitForTimeout(2300);
    assert.equal(await page.locator('#disaster-replan').isHidden(), true, 'queued retry must not stop polling at the old blocked revision');
    task = { ...task, metadata_revision:task.metadata_revision + 1, stage:'fencing', message:'retry began' };
    await page.waitForFunction(() => document.querySelector('#disaster-stage').textContent === '隔离全部节点');
    assert.equal(executions, 2, 'queued retry must only be submitted once');
    await page.locator('#cancel-disaster').click();
    task = null; preflightReady = false;
    await page.locator('#switch-lock').click();
    await page.locator('#open-disaster-recovery').click();
    await page.getByRole('alert').filter({ hasText:'Agent 尚未升级' }).waitFor();
    assert.equal(await page.locator('#disaster-confirmation').isHidden(), true);
    assert.equal(await page.locator('#disaster-execute').isDisabled(), true);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ result:'passed', scope:'isolated browser UI with simulated API', assertions:['confirmation','preflight failure','single submission','durable reopen','independent event scroll','mobile bounds','blocked outcome'], output }));
  } finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); server.close(); process.exitCode = 1; });
