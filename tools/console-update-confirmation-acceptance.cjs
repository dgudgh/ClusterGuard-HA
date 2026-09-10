const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

// Real console and browser interaction; isolated API replies, never a field upgrade.
const output = process.env.CONSOLE_TEST_OUTPUT || path.resolve(__dirname, '../.build/update-confirmation');
fs.mkdirSync(output, { recursive:true });
const baseline = process.env.CONSOLE_BASELINE === '1';
const results = [];
const deferred = () => {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
};

(async () => {
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  async function scenario(name, run) {
    const fixture = createConsoleFixture();
    const item = { package:{ patch_id:'cgupgrade-2.2-91-to-2.2-93-x86_64', source_version:'2.2-91', target_version:'2.2-93',
      signature_verified:true, rolling:true, rollback_available:true, uploaded_at:new Date().toISOString() } };
    const mock = { uploaded:false, extra:[], posts:[], statusReads:0, planGate:null, executeGate:null, auxiliaryGate:null, error:0, failReads:false, readGate:null };
    const releases = [];
    fixture.control.hook = async ({ req, url }) => {
      if (mock.auxiliaryGate && ['/api/v1/platform/version', '/api/v1/control-plane/status'].includes(url.pathname)) await mock.auxiliaryGate;
      if (url.pathname === '/api/v1/platform/updates' && req.method === 'POST') {
        for await (const _ of req) { /* consume the simulated package upload */ }
        mock.uploaded = true;
        return { status:201, result:item.package };
      }
      if (url.pathname === '/api/v1/platform/updates') {
        const snapshot = { available:true, packages:mock.uploaded ? [...mock.extra, structuredClone(item)] : [] };
        if (mock.readGate) await mock.readGate;
        return { result:snapshot };
      }
      if (url.pathname === `/api/v1/platform/updates/${item.package.patch_id}` && req.method === 'GET') {
        mock.statusReads++;
        return mock.failReads ? { status:503, message:'Leader status unavailable' } : { result:structuredClone(item) };
      }
      if (url.pathname.startsWith('/api/v1/platform/updates/') && req.method === 'POST') {
        let body = ''; for await (const chunk of req) body += chunk;
        const mode = url.pathname.split('/').pop();
        mock.posts.push({ mode, path:url.pathname, payload:JSON.parse(body) });
        if (mode === 'plan') {
          item.job = { patch_id:item.package.patch_id, mode, status:mock.planGate ? 'running' : 'planned', started_at:'2026-09-08T00:00:00Z', updated_at:'2026-09-08T00:00:00Z' };
          if (mock.planGate) mock.planGate.then(() => { item.job.status = 'planned'; });
        } else {
          if (mock.executeGate) await mock.executeGate;
          if (mock.error) return { status:mock.error, message:'test submission rejected' };
          item.job = { patch_id:item.package.patch_id, mode, status:'queued', started_at:'2026-09-08T00:01:00Z', updated_at:'2026-09-08T00:01:00Z' };
        }
        return { status:202, result:structuredClone(item.job) };
      }
    };
    await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
    const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const gate = () => { const value = deferred(); releases.push(value.resolve); return value; };
    const check = (condition, message) => {
      results.push({ scenario:name, check:message, passed:!!condition });
      if (!baseline) assert.ok(condition, `${name}: ${message}`);
    };
    try {
      await page.goto(`http://127.0.0.1:${fixture.server.address().port}/#settings`);
      await page.locator('#software-update-tab').click();
      await page.locator('#open-software-update-dialog').click();
      await page.locator('#software-update-package-file').setInputFiles({ name:'test.cgupgrade', mimeType:'application/octet-stream', buffer:Buffer.from('isolated upload fixture') });
      await page.locator('#upload-software-update').click();
      await page.waitForFunction(() => !document.querySelector('#execute-software-update').disabled);
      const confirm = async () => {
        await page.locator('#execute-software-update').click();
        await page.locator('#software-update-confirmation-dialog').waitFor({ state:'visible' });
        await page.locator('#software-update-confirmation-input').fill(item.package.patch_id);
      };
      await run({ page, mock, item, check, gate, confirm });
      check(errors.length === 0, 'no browser exceptions');
      await page.screenshot({ path:path.join(output, `${name}.png`) });
    } finally {
      releases.forEach(release => release());
      await page.close();
      fixture.server.closeAllConnections();
      await new Promise(resolve => fixture.server.close(resolve));
    }
  }
  try {
    await scenario('slow-submit', async ({ page, mock, check, gate, confirm }) => {
      await confirm();
      const pending = gate(); mock.executeGate = pending.promise;
      await page.locator('#confirm-software-update-action').click();
      await page.waitForTimeout(100);
      check(await page.locator('#confirm-software-update-action').isDisabled(), 'confirm disables immediately');
      check((await page.locator('#software-update-confirmation-state').textContent()).includes('提交'), 'visible submitting feedback');
      await page.locator('#confirm-software-update-action').evaluate(button => { for (let i = 0; i < 5; i++) button.click(); });
      check(mock.posts.filter(post => post.mode === 'execute').length === 1, 'rapid clicks send one execute request');
    });
    await scenario('accepted-slow-auxiliary', async ({ page, mock, check, gate, confirm }) => {
      await confirm();
      const pending = gate(); mock.auxiliaryGate = pending.promise;
      await page.locator('#confirm-software-update-action').click();
      await page.waitForTimeout(700);
      check(await page.locator('#software-update-confirmation-dialog').isHidden(), 'accepted response closes confirmation without ancillary reads');
      check(await page.locator('#software-update-progress-dialog').isVisible(), 'accepted response opens real returned job progress');
    });
    await scenario('visible-rejection', async ({ page, mock, check, confirm }) => {
      await confirm(); mock.error = 403;
      await page.locator('#confirm-software-update-action').click();
      await page.waitForTimeout(200);
      check((await page.locator('#software-update-confirmation-dialog').innerText()).includes('test submission rejected'), 'failure is visible inside topmost confirmation');
      check(mock.posts.filter(post => post.mode === 'execute').length === 1, 'failure is not automatically retried');
      if (!baseline) {
        await page.setViewportSize({ width:390, height:844 });
        const bounds = await page.locator('#software-update-confirmation-dialog').boundingBox();
        check(bounds.x >= 0 && bounds.x + bounds.width <= 390 && bounds.y >= 0 && bounds.y + bounds.height <= 844, 'mobile failure dialog stays within viewport');
        check((await page.locator('#confirm-software-update-action').boundingBox()).height <= 44, 'mobile buttons retain normal height');
        check(await page.locator('#software-update-confirmation-dialog').evaluate(dialog => dialog.scrollWidth <= dialog.clientWidth), 'real package ID does not overflow mobile dialog');
      }
    });
    await scenario('slow-plan', async ({ page, mock, check, gate }) => {
      const pending = gate(); mock.planGate = pending.promise;
      await page.locator('#execute-software-update').click();
      await page.waitForTimeout(12000);
      pending.resolve();
      await page.waitForTimeout(2500);
      check(await page.locator('#software-update-confirmation-dialog').isVisible(), 'plan beyond old ten-second limit opens confirmation without second click');
      check(mock.posts.filter(post => post.mode === 'plan').length === 1, 'one plan request');
      check(mock.posts.filter(post => post.mode === 'execute').length === 0, 'ready plan still requires typed confirmation');
    });
    if (!baseline) {
      await scenario('uncertain-submit', async ({ page, mock, item, check, confirm }) => {
        await confirm(); mock.error = 503;
        await page.locator('#confirm-software-update-action').click();
        await page.waitForFunction(() => document.querySelector('#confirm-software-update-action').textContent === '核对任务状态');
        check((await page.locator('#software-update-confirmation-state').textContent()).includes('提交结果待确认'), 'unknown outcome is not called failed or successful');
        await page.locator('#confirm-software-update-action').click();
        await page.waitForFunction(() => !document.querySelector('#confirm-software-update-action').disabled);
        check(mock.statusReads > 0, 'second click reads durable job only');
        check(await page.locator('#software-update-confirmation-dialog').isVisible(), 'old plan is not mistaken for accepted execution');
        await page.locator('#cancel-software-update-confirmation').click();
        await page.locator('#open-software-update-dialog').click();
        await page.locator('#software-update-confirmation-input').fill(item.package.patch_id);
        check((await page.locator('#confirm-software-update-action').textContent()) === '核对任务状态', 'reopening preserves uncertain submission lock');
        item.job = { patch_id:item.package.patch_id, mode:'execute', status:'running', started_at:'2026-09-08T00:01:00Z', updated_at:'2026-09-08T00:01:00Z' };
        await page.locator('#confirm-software-update-action').click();
        await page.locator('#software-update-progress-dialog').waitFor({ state:'visible' });
        check(mock.posts.filter(post => post.mode === 'execute').length === 1, 'durable reconciliation never resubmits mutation');
      });
      await scenario('network-disconnect', async ({ page, mock, check, confirm }) => {
        await confirm();
        await page.route('**/api/v1/platform/updates/*/execute', route => route.abort('connectionreset'));
        await page.locator('#confirm-software-update-action').click();
        await page.waitForFunction(() => document.querySelector('#confirm-software-update-action').textContent === '核对任务状态');
        mock.failReads = true;
        await page.locator('#confirm-software-update-action').click();
        await page.waitForFunction(() => document.querySelector('#software-update-confirmation-state').textContent.includes('Leader status unavailable'));
        check(await page.locator('#software-update-confirmation-dialog').isVisible(), 'Leader/read failures stay visible in confirmation');
        check(mock.posts.filter(post => post.mode === 'execute').length === 0, 'network error is not silently retried');
      });
      await scenario('submit-timeout', async ({ page, mock, check, gate, confirm }) => {
        await confirm();
        const pending = gate(); mock.executeGate = pending.promise;
        await page.clock.install();
        await page.locator('#confirm-software-update-action').click();
        await page.waitForTimeout(50);
        await page.clock.fastForward(31000);
        await page.waitForFunction(() => document.querySelector('#confirm-software-update-action').textContent === '核对任务状态');
        check((await page.locator('#software-update-confirmation-state').textContent()).includes('提交结果待确认'), 'timeout remains uncertain instead of inviting resubmission');
        check(mock.posts.filter(post => post.mode === 'execute').length === 1, 'timeout never retries execution');
      });
      await scenario('immutable-package', async ({ page, mock, item, check, confirm }) => {
        await confirm();
        mock.extra.push({ package:{ ...item.package, patch_id:'different-package', uploaded_at:new Date().toISOString() } });
        await page.evaluate(() => loadSoftwareUpdates());
        check((await page.locator('#software-update-confirmation-phrase').textContent()) === item.package.patch_id, 'poll cannot change confirmed package ID');
        await page.locator('#confirm-software-update-action').click();
        await page.locator('#software-update-progress-dialog').waitFor({ state:'visible' });
        const request = mock.posts.find(post => post.mode === 'execute');
        check(request.path.endsWith(`/${item.package.patch_id}/execute`) && request.payload.confirmation === item.package.patch_id, 'request and typed confirmation use pinned package');
      });
      await scenario('stale-status-response', async ({ page, mock, check, gate, confirm }) => {
        await confirm();
        const pending = gate(); mock.readGate = pending.promise;
        await page.evaluate(() => { void loadSoftwareUpdates(); });
        await page.waitForTimeout(100);
        await page.locator('#confirm-software-update-action').click();
        await page.locator('#software-update-progress-dialog').waitFor({ state:'visible' });
        pending.resolve(); mock.readGate = null;
        await page.waitForTimeout(100);
        check(await page.evaluate(() => latestSoftwareUpdate().job.status) === 'queued', 'pre-submission snapshot cannot overwrite acknowledged job');
      });
      await scenario('cancel-plan-wait', async ({ page, mock, check, gate }) => {
        const pending = gate(); mock.planGate = pending.promise;
        await page.locator('#execute-software-update').click();
        await page.waitForFunction(() => !!state.softwareUpdatePlanWait);
        await page.locator('#cancel-software-update-dialog').click();
        pending.resolve();
        await page.waitForTimeout(2500);
        check(await page.locator('#software-update-confirmation-dialog').isHidden(), 'cancelled wait never opens confirmation later');
        check(mock.posts.filter(post => post.mode === 'execute').length === 0, 'closing plan wait does not execute upgrade');
      });
      for (const mode of ['resume', 'rollback']) {
        await scenario(`${mode}-confirmation`, async ({ page, mock, item, check, gate }) => {
          item.job = { patch_id:item.package.patch_id, mode:'execute', status:'failed', started_at:'2026-09-08T00:00:00Z', updated_at:'2026-09-08T00:00:00Z' };
          await page.evaluate(() => loadSoftwareUpdates());
          await page.locator(`#${mode}-software-update`).click();
          await page.locator('#software-update-confirmation-input').fill('wrong-package');
          check(await page.locator('#confirm-software-update-action').isDisabled(), 'wrong typed ID remains locked');
          await page.locator('#software-update-confirmation-input').fill(item.package.patch_id);
          const pending = gate(); mock.executeGate = pending.promise;
          await page.locator('#confirm-software-update-action').click();
          check(await page.locator('#confirm-software-update-action').isDisabled(), 'dangerous action locks during submission');
          pending.resolve();
          await page.locator('#software-update-progress-dialog').waitFor({ state:'visible' });
          check(mock.posts.filter(post => post.mode === mode).length === 1, 'confirmed action submits once');
        });
      }
    }
  } finally { await browser.close(); }
  fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify({ scope:'isolated browser, simulated upload and jobs', baseline, results }, null, 2));
  console.log(JSON.stringify({ baseline, checks:results.length, failures:results.filter(row => !row.passed), output }));
})().catch(error => { console.error(error); process.exitCode = 1; });
