const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const { createReviewFixture, evidence } = require('./console-ux-review.cjs');

const output = path.join(evidence, 'states');
const checks = [];
const record = name => checks.push({ name, passed:true });

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createReviewFixture();
  const { server, control, clusters } = fixture;
  const originals = structuredClone(clusters);
  const templates = structuredClone(fixture.operations);
  fixture.operations.splice(0, fixture.operations.length, ...Array.from({ length:90 }, (_, i) => {
    const base = structuredClone(templates[i % templates.length]);
    return { ...base, resource_id:`review-event-${String(i).padStart(3,'0')}`, target_id:base.operation.target_id,
      created_at:new Date(Date.now() - i * 60000).toISOString(), status:i % 9 === 0 ? 'failed' : 'succeeded',
      message:i % 9 === 0 ? '后端验证未通过，原始证据保留。' : base.message };
  }));
  let role = 'admin', failure = '', long = false, gate = null;
  control.hook = async ({ req, url, cluster }) => {
    if (url.pathname === '/api/v1/auth/me') return { result:{ user:{ username:'review-fixture', display_name:'隔离评审', role } } };
    if (req.method !== 'GET') return null;
    if (failure === 'logs' && url.pathname === '/api/v1/operations' && url.searchParams.get('view') === 'page') return { status:503, message:'测试：日志读取失败' };
    if (failure === 'topology' && url.pathname.endsWith('/topology')) return { status:503, message:'测试：拓扑暂不可用' };
    if (gate && url.pathname.endsWith('/topology')) await gate;
    if (long && cluster && (url.pathname.endsWith('/topology') || url.pathname === `/api/v1/clusters/${cluster.resource_id}`)) {
      const result = url.pathname.endsWith('/topology') ? fixture.topology(cluster) : fixture.detail(cluster);
      result.instances.forEach(instance => { instance.hostname = '数据库生产集群跨区域复制节点长名称-' + 'very-long-hostname-'.repeat(4); });
      return { result };
    }
    if (failure === 'missing' && url.pathname.endsWith('/candidates')) return { result:[] };
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const url = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const errors = [];
  try {
    const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
    page.on('pageerror', error => errors.push(error.message));
    const ready = () => page.waitForFunction(() => state.clusterDataReady && !state.clusterLoading);
    const logs = () => page.waitForFunction(() => state.logLoaded && !state.logController);
    const shot = async name => {
      await page.waitForFunction(() => !document.querySelector(`[data-view="${location.hash.slice(1)}"]`).hidden);
      const geometry = await page.evaluate(() => ({
        overflow:document.documentElement.scrollWidth > innerWidth + 1,
        clipped:[...document.querySelectorAll('button,a,input,select')].filter(e => e.getBoundingClientRect().width && e.scrollWidth > e.clientWidth + 3).map(e => e.id || e.textContent.trim())
      }));
      assert.equal(geometry.overflow, false, `${name}: page overflow`);
      assert.deepEqual(geometry.clipped, [], `${name}: clipped controls`);
      await page.screenshot({ path:path.join(output, name + '.png'), fullPage:true });
    };
    await page.goto(url + '/?ui=review#operations'); await ready();
    assert.equal(await page.locator('html').getAttribute('data-ui'), 'review');
    for (let i = 0; i < clusters.length; i++) {
      await page.locator('#cluster-select').selectOption(clusters[i].resource_id); await ready();
      assert.equal(await page.locator('#execute-switchover').isEnabled(), false);
      const count = control.requests.filter(r => r.method !== 'GET').length;
      await page.evaluate(() => document.querySelector('#execute-switchover').dispatchEvent(new MouseEvent('click')));
      assert.equal(control.requests.filter(r => r.method !== 'GET').length, count);
      await page.locator('#switch-lock').click();
      assert.equal(await page.locator('#execute-switchover').isEnabled(), true);
      await shot(`unlocked-${clusters[i].engine}`);
      const options = await page.locator('#candidate-select option').evaluateAll(items => items.map(item => item.value));
      await page.locator('#candidate-select').selectOption(options.at(-1));
      assert.equal(await page.locator('#execute-switchover').isEnabled(), false);
      await page.locator('#switch-lock').click(); await page.locator('#switch-lock').click();
      assert.equal(await page.locator('#execute-switchover').isEnabled(), false);
      record(`${clusters[i].engine}: default lock, entry guard, unlock, target change and relock`);
    }
    await page.locator('#operations-switchover-tab').focus();
    await page.keyboard.press('ArrowRight');
    assert.equal(await page.locator('#operations-recovery-tab').getAttribute('aria-selected'), 'true');
    await page.keyboard.press('End');
    assert.equal(await page.locator('#operations-disaster-tab').getAttribute('aria-selected'), 'true');
    assert.equal(await page.locator('#open-disaster-recovery').isEnabled(), false);
    await page.keyboard.press('Home');
    assert.equal(await page.locator('#operations-switchover-tab').getAttribute('aria-selected'), 'true');
    record('operation tabs: arrows, Home, End and locked disaster action');
    await page.locator('#switch-lock').click();
    await page.locator('#execute-switchover').click();
    await page.waitForFunction(() => !state.operationRunning && document.querySelector('#operation-result').textContent.includes('失败'));
    assert.equal(await page.locator('#execute-switchover').isEnabled(), false);
    await shot('backend-rejection');
    record('real execute handler receives isolated 403, shows failure and relocks');
    await page.locator('[data-nav="overview"]').click();
    await page.locator('#fleet-search').fill('sample-pg16');
    assert.equal(await page.locator('.cluster-row-action').count(), 1);
    await page.locator('#fleet-search').fill('no-such-cluster');
    assert.equal(await page.locator('.cluster-row-action').count(), 0);
    await shot('overview-empty-search');
    await page.locator('#fleet-search').fill('');
    await page.locator('#fleet-health-filter').selectOption('attention');
    assert.equal(await page.locator('.cluster-row-action').count(), 0);
    await page.locator('#fleet-health-filter').selectOption('all');
    await page.locator('.cluster-row-action').first().click(); await ready();
    assert.equal(await page.locator('[data-view="topology"]').isVisible(), true);
    record('overview search, empty results, health filter and row navigation');
    await page.locator('[data-nav="operation-log"]').click(); await logs();
    assert.equal(await page.locator('#operation-log-list .log-card').count(), 20);
    const detailCount = () => control.requests.filter(r => /^\/api\/v1\/operations\/review-event-/.test(r.path)).length;
    const beforeDetail = detailCount();
    await page.locator('#operation-log-list details summary').first().click();
    await page.waitForFunction(() => document.querySelector('#operation-log-list pre').textContent.includes('resource_id'));
    assert.equal(detailCount(), beforeDetail + 1);
    await page.locator('#load-more-operation-log').click(); await logs();
    assert.equal(await page.locator('#operation-log-list .log-card').count(), 40);
    await page.locator('#log-status-filter').selectOption('failed'); await logs();
    assert.ok(await page.locator('#operation-log-list .log-card').count() < 20);
    await page.locator('#log-search').fill('not-a-real-node');
    await page.waitForFunction(() => state.logLoaded && !state.logController && state.allOperations.length === 0);
    await shot('log-empty');
    await page.locator('#log-search').fill('');
    await page.locator('#log-status-filter').selectOption('all'); await logs();
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id); await ready(); await logs();
    assert.equal(await page.locator('#log-cluster-filter').inputValue(), clusters[1].resource_id);
    assert.ok((await page.locator('#operation-log-list .log-card .log-cell:nth-child(2)').allTextContents()).every(t => t.includes(clusters[1].display_name)));
    record('logs: first 20, detail on expand, next page, server filter/search, empty and cluster following');
    failure = 'logs'; await page.locator('#refresh-operation-log').click();
    await page.waitForFunction(() => !!state.logError && !state.logController);
    await shot('log-error'); failure = ''; await page.locator('#refresh-operation-log').click(); await logs();
    record('log failure and explicit retry');
    await page.locator('#open-cluster-management-modal').click();
    await page.locator('#cluster-management-modal').waitFor({ state:'visible' });
    const beforeForm = control.requests.filter(r => r.method !== 'GET').length;
    await page.locator('#submit-cluster-registration').click();
    assert.match(await page.locator('#cluster-management-modal').textContent(), /请输入集群名称/);
    assert.equal(control.requests.filter(r => r.method !== 'GET').length, beforeForm);
    await shot('form-validation');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#cluster-management-modal').isVisible(), false);
    assert.equal(await page.evaluate(() => document.activeElement.id), 'open-cluster-management-modal');
    record('form validation without write, dialog Escape and focus return');
    role = 'viewer'; await page.goto(url + '/?ui=review&scenario=viewer#operations'); await ready();
    assert.equal(await page.locator('#switch-lock').isEnabled(), false);
    assert.equal(await page.locator('#execute-switchover').isEnabled(), false);
    assert.match(await page.locator('#ux-operation-permission').textContent(), /仅查看/);
    await shot('permission-denied'); record('viewer cannot unlock or execute'); role = 'admin';
    for (const width of [1440, 768, 390, 320]) {
      await page.setViewportSize({ width, height:width < 500 ? 844 : 1000 });
      long = true;
      clusters.forEach((cluster,i) => { cluster.display_name = originals[i].display_name + '-生产数据库跨区域长名称'.repeat(4); });
      await page.goto(url + `/?ui=review&scenario=long-${width}#operations`); await ready();
      await shot(`long-operations-${width}`);
      await page.locator('[data-nav="overview"]').click(); await shot(`long-overview-${width}`);
      await page.locator('[data-nav="operation-log"]').click(); await logs(); await shot(`long-log-${width}`);
      record(`${width}px: long cluster/host labels, context and audit list`);
    }
    long = false; clusters.forEach((c,i) => { c.display_name = originals[i].display_name; });
    await page.setViewportSize({ width:390, height:844 });
    failure = 'missing'; await page.goto(url + '/?ui=review&scenario=missing#operations'); await ready();
    assert.equal(await page.locator('#execute-switchover').isEnabled(), false); await shot('candidate-missing-mobile');
    failure = 'topology'; await page.reload();
    await page.waitForFunction(() => !state.clusterLoading && !!state.clusterLoadError);
    assert.equal(await page.locator('#switch-lock').isEnabled(), false); await shot('topology-error-mobile');
    failure = ''; record('missing candidate and unavailable topology keep execution blocked');
    let release; gate = new Promise(resolve => { release = resolve; });
    await page.reload(); await page.waitForFunction(() => state.clusterLoading);
    await shot('loading-mobile');
    release(); gate = null; await ready(); record('loading state and completed read');
    await page.emulateMedia({ reducedMotion:'reduce' });
    assert.equal(await page.locator('#switch-lock').evaluate(el => getComputedStyle(el).transitionDuration), '0s');
    await page.locator('#switch-lock').focus();
    assert.equal(await page.locator('#switch-lock').evaluate(el => getComputedStyle(el).outlineStyle), 'solid');
    await shot('keyboard-focus-mobile'); record('reduced motion and visible keyboard focus');
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(evidence, 'acceptance.json'), JSON.stringify({ status:'passed', checks, errors, isolatedRejectedMutations:control.mutations, productionMutations:0 }, null, 2));
    console.log(JSON.stringify({ status:'passed', checks:checks.length, errors, isolatedRejectedMutations:control.mutations }));
  } finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
