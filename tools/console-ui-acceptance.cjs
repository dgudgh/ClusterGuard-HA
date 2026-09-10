const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const root = path.resolve(__dirname, '..');
const baseline = process.argv.includes('--baseline');
const fixture = createConsoleFixture(baseline ? path.join(root, '.build/console-before-paper-theme.html') : undefined);
const { server, control, clusters } = fixture;
const output = process.env.CONSOLE_TEST_OUTPUT || path.join(root, '.build/console-classic-90', baseline ? 'baseline' : 'verified');
fs.mkdirSync(output, { recursive:true });
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return { promise, resolve }; };
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless:true, channel:'chrome' });
  const errors = [];
  try {
    const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
    page.on('pageerror', error => errors.push(error.message));
    const url = `http://127.0.0.1:${server.address().port}`;
    await page.goto(url + '/#topology');
    await page.waitForFunction(() => document.querySelectorAll('.node-card').length === 3);
    const selected = async index => {
      await page.locator('#cluster-select').selectOption(clusters[index].resource_id);
      await page.waitForFunction(name => document.querySelector('#topology-cluster-name').textContent === name && document.querySelectorAll('.node-card').length === 3 && document.querySelector('#topology-risk').textContent === '健康', clusters[index].display_name);
    };
    const gate = deferred(), started = deferred();
    control.hook = async ({ url:request, cluster }) => {
      if (cluster === clusters[1] && request.pathname.endsWith('/topology')) { started.resolve(); await gate.promise; }
    };
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id);
    await started.promise;
    if (baseline) {
      assert.equal(await page.locator('#topology-cluster-name').textContent(), '未选择集群');
      assert.equal(await page.locator('#topology-risk').textContent(), '健康');
      assert.equal(await page.locator('.node-card').count(), 0);
      await page.screenshot({ path:path.join(output, 'reproduced-empty-healthy.png') });
      gate.resolve();
      console.log(JSON.stringify({ result:'reproduced', issue:'selected cluster cleared during loading while previous healthy badge remains', output }));
      return;
    }
    assert.equal(await page.locator('#topology-cluster-name').textContent(), clusters[1].display_name);
    assert.equal(await page.locator('#topology-risk').textContent(), '读取中');
    assert.equal(await page.locator('#topology-connector').isVisible(), false);
    assert.equal(await page.locator('#switch-lock').isDisabled(), true);
    gate.resolve(); control.hook = null;
    await page.waitForFunction(() => document.querySelector('#topology-risk').textContent === '健康');

    // A late response from the previous cluster cannot change the new selection.
    const late = deferred(), lateStarted = deferred();
    control.hook = async ({ url:request, cluster }) => {
      if (cluster === clusters[0] && request.pathname.endsWith('/topology')) { lateStarted.resolve(); await late.promise; }
    };
    await page.locator('#cluster-select').selectOption(clusters[0].resource_id);
    await lateStarted.promise;
    await selected(1);
    late.resolve(); control.hook = null;
    await delay(100);
    assert.equal(await page.locator('#topology-cluster-name').textContent(), clusters[1].display_name);

    // Failure must preserve a same-cluster snapshot, but mark it unverified and locked.
    control.hook = ({ url:request }) => request.pathname.endsWith('/topology') ? { status:503, message:'fixture topology unavailable' } : null;
    await page.locator('#inspect-cluster').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'error');
    assert.equal(await page.locator('.node-card').count(), 3);
    assert.equal(await page.locator('#topology-risk').textContent(), '未验证');
    assert.equal(await page.locator('#switch-lock').isDisabled(), true);
    assert.match(await page.locator('#cluster-load-message').textContent(), /上次观测/);
    await page.screenshot({ path:path.join(output, 'refresh-failed-preserved.png') });
    control.hook = null;
    await page.locator('#retry-cluster-load').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);

    control.hook = ({ url:request, cluster }) => cluster === clusters[0] && request.pathname.endsWith('/topology') ? { status:503, message:'fixture cluster unavailable' } : null;
    await page.locator('#cluster-select').selectOption(clusters[0].resource_id);
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'error');
    assert.equal(await page.locator('.node-card').count(), 0, 'a failed new selection cannot display the previous cluster');
    assert.equal(await page.locator('#topology-cluster-name').textContent(), clusters[0].display_name);
    assert.equal(await page.locator('#topology-risk').textContent(), '未验证');
    control.hook = null;
    await page.locator('#retry-cluster-load').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);

    // Auxiliary metrics/audit failures cannot erase successfully loaded topology.
    control.hook = ({ url:request }) => request.pathname.endsWith('/metrics') || request.pathname === '/api/v1/operations' ? { status:503, message:'fixture auxiliary unavailable' } : null;
    await page.locator('#inspect-cluster').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'partial');
    assert.equal(await page.locator('.node-card').count(), 3);
    assert.match(await page.locator('#cluster-load-message').textContent(), /性能指标.*操作上下文/);
    control.hook = null;
    await page.locator('#retry-cluster-load').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);

    let conflicts = 0;
    control.hook = ({ url:request }) => request.pathname.endsWith('/health') && conflicts++ < 2 ? { status:409, message:'topology observation changed' } : null;
    await page.locator('#inspect-cluster').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);
    assert.equal(conflicts, 3, 'retry the entire coherent snapshot after observation conflicts');
    control.hook = null;

    control.hook = async ({ url:request }) => {
      if (request.pathname.endsWith('/topology')) await delay(16000);
    };
    await page.locator('#inspect-cluster').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').dataset.state === 'error', null, { timeout:20000 });
    assert.match(await page.locator('#cluster-load-message').textContent(), /超时/);
    assert.equal(await page.locator('#switch-lock').isDisabled(), true);
    control.hook = null;
    await page.locator('#retry-cluster-load').click();
    await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);

    // Power responses are subject to the same selection generation as topology.
    const power = deferred(), powerStarted = deferred();
    control.hook = async ({ url:request, cluster }) => {
      if (cluster === clusters[1] && request.pathname.endsWith('/power/status')) {
        powerStarted.resolve(); await power.promise;
        return { result:{ outage_classification:{ kind:'unexpected_failure', database_state:'failed' } } };
      }
    };
    await selected(1); await powerStarted.promise;
    await selected(0);
    await page.waitForFunction(() => document.querySelector('#power-database-state').textContent === '运行中');
    power.resolve(); control.hook = null; await delay(100);
    assert.equal(await page.locator('#power-database-state').textContent(), '运行中');

    for (let i = 0; i < 12; i++) await selected(i % 2);
    for (const width of [1440, 1024, 768, 390]) {
      await page.setViewportSize({ width, height:1000 });
      for (const view of ['overview', 'topology', 'operations', 'metrics', 'operation-log', 'settings']) {
        await page.locator(`[data-nav="${view}"]`).click();
        if (view === 'settings') await page.locator('#software-update-tab').click();
        if (view === 'operations') {
          const tabs = page.getByRole('tablist', { name:'高可用操作功能区' }).getByRole('tab');
          const boxes = await tabs.evaluateAll(items => items.map(el => ({ y:el.getBoundingClientRect().y, width:el.getBoundingClientRect().width, clipped:el.scrollWidth > el.clientWidth })));
          assert.ok(boxes.every(box => Math.abs(box.y - boxes[0].y) < 1 && Math.abs(box.width - boxes[0].width) < 1 && !box.clipped));
          for (let index = 0; index < 3; index++) { await tabs.nth(index).click(); assert.equal(await tabs.nth(index).getAttribute('aria-selected'), 'true'); }
        }
        await delay(350);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${view} overflows at ${width}px`);
        await page.screenshot({ path:path.join(output, `${view}-${width}.png`), fullPage:true });
      }
    }
    await page.setViewportSize({ width:1440, height:1000 });
    await page.locator('#open-software-update-dialog').click();
    assert.equal(await page.locator('#software-update-package').isVisible(), false, 'completed historical package must not appear as a fresh upload');
    await delay(350);
    await page.screenshot({ path:path.join(output, 'upgrade-dialog.png') });
    await page.locator('#cancel-software-update-dialog').click();
    assert.equal(await page.locator('#software-update-events').isVisible(), false);
    await page.locator('#toggle-software-update-progress').click();
    assert.equal(await page.locator('#software-update-events').isVisible(), true);
    fixture.packageItem.job.status = 'running';
    fixture.packageItem.job.finished_at = '';
    fixture.packageItem.job.progress = { phase:'updating', total:3, current:1, percent:38 };
    fixture.packageItem.job.maintenance_active = true;
    await page.reload();
    await page.locator('#software-update-progress-dialog').waitFor({ state:'visible' });
    await delay(350);
    const head = await page.locator('#software-update-progress-dialog .dialog-head').boundingBox();
    await page.locator('#software-update-progress-event-list').hover();
    await page.mouse.wheel(0, 650); await delay(100);
    assert.equal((await page.locator('#software-update-progress-dialog .dialog-head').boundingBox()).y, head.y);
    assert.ok(await page.locator('#software-update-progress-event-list').evaluate(el => el.scrollTop > 0));
    await page.screenshot({ path:path.join(output, 'upgrade-progress.png') });
    await page.setViewportSize({ width:390, height:844 });
    await page.screenshot({ path:path.join(output, 'upgrade-progress-mobile.png') });
    const bounds = await page.locator('#software-update-progress-dialog').boundingBox();
    assert.ok(bounds.y >= 0 && bounds.y + bounds.height <= 844 && bounds.x >= 0 && bounds.x + bounds.width <= 390);
    const events = await page.locator('#software-update-progress-event-list').boundingBox();
    assert.ok(events.height > 60, 'mobile log area must remain usable');
    const rowsFit = await page.locator('.software-update-progress-event').evaluateAll(rows => rows.every(row => {
      const bounds = row.getBoundingClientRect();
      const time = row.querySelector('time').getBoundingClientRect();
      const label = row.querySelector('strong').getBoundingClientRect();
      const detail = row.querySelector('span').getBoundingClientRect();
      return detail.top >= Math.max(time.bottom, label.bottom) && detail.bottom <= bounds.bottom && detail.right <= bounds.right;
    }));
    assert.ok(rowsFit, 'each wrapped log entry must fit without overlapping its time or the next row');
    const palette = await page.evaluate(() => {
      const style = getComputedStyle(document.documentElement);
      return Object.fromEntries(['ink', 'muted', 'canvas', 'panel', 'accent', 'accent-strong', 'danger', 'danger-soft', 'warning', 'warning-soft'].map(key => [key, style.getPropertyValue('--' + key).trim()]));
    });
    assert.equal(palette.canvas, '#f5f5f7');
    assert.equal(palette.panel, '#ffffff');
    assert.equal(palette.accent, '#0071e3');
    assert.equal(await page.locator('body').evaluate(el => getComputedStyle(el).backgroundImage), 'none');
    assert.match(await page.locator('body').evaluate(el => getComputedStyle(el).fontFamily), /sans-serif/);
    const luminance = color => color.slice(1).match(/../g).map(value => parseInt(value, 16) / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4).reduce((sum, value, index) => sum + value * [.2126, .7152, .0722][index], 0);
    for (const [foreground, background] of [['ink', 'canvas'], ['muted', 'canvas'], ['panel', 'accent'], ['panel', 'accent-strong'], ['danger', 'danger-soft'], ['warning', 'warning-soft']]) {
      const a = luminance(palette[foreground]), b = luminance(palette[background]);
      assert.ok((Math.max(a, b) + .05) / (Math.min(a, b) + .05) >= 4.5, `${foreground} / ${background} must have readable contrast`);
    }
    await page.emulateMedia({ reducedMotion:'reduce' });
    assert.equal(await page.locator('#software-update-progress-dialog').evaluate(el => getComputedStyle(el).animationName), 'none');
    assert.deepEqual(errors, []);
    assert.equal(control.mutations, 0, 'acceptance must not send database mutations');
    const result = { result:'passed', scope:'actual console HTML, isolated API fixtures, no field database mutation', assertions:['loading identity','retained snapshot','retry','auxiliary failure isolation','coherent observation retry','out-of-order topology and power','12 cluster switches','24 responsive views','three working tabs','empty upload','collapsed logs','fixed progress header','reduced motion'], output };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2));
    console.log(JSON.stringify(result));
  } finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); server.closeAllConnections(); server.close(); process.exitCode = 1; });
