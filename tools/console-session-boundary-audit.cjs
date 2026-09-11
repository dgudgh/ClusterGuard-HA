const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/session-boundary-audit');
const routes = ['overview', 'topology', 'operations', 'nodes', 'metrics', 'operation-log', 'about', 'settings'];

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control, clusters } = fixture;
  clusters.forEach(cluster => { cluster.display_name = `SESSION_A_${cluster.engine}`; });
  fixture.packageItem.package.patch_id = 'SESSION_A_PACKAGE';
  let secondSession = false, release, blocked = false;
  control.hook = async ({ url }) => {
    if (url.pathname === '/api/v1/auth/logout') return { result:{} };
    if (url.pathname === '/api/v1/auth/login') {
      secondSession = true;
      return { result:{ user:{ username:'session-b', display_name:'Session B', role:'viewer' } } };
    }
    if (secondSession && url.pathname === '/api/v1/clusters') {
      blocked = true;
      await new Promise(resolve => { release = resolve; });
      return { status:503, message:'fixture second-session bootstrap unavailable' };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const checks = [];
  const check = (name, passed, evidence) => checks.push({ name, passed, evidence });
  const base = `http://127.0.0.1:${server.address().port}`;
  const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  try {
    await page.goto(`${base}/#overview`);
    await page.waitForFunction(() => state.clusterDataReady && state.nodesLoaded && state.softwareUpdates.packages.length > 0);
    assert.match(await page.locator('#console-shell').innerText(), /SESSION_A/);
    await page.locator('#account-menu-toggle').click();
    await page.locator('#logout-button').click();
    await page.locator('#login-shell').waitFor({ state:'visible' });
    const caches = await page.evaluate(() => ({ clusters:state.clusters.length, nodes:state.nodes.length, packages:state.softwareUpdates.packages.length, directory:state.clusterDirectory.size, topology:state.fleetTopology.size, selected:state.selectedClusterId, controlPlane:state.controlPlane, loggedIn:!!state.currentUser }));
    check('logout-clears-session-caches', !caches.loggedIn && !caches.clusters && !caches.nodes && !caches.packages && !caches.directory && !caches.topology && !caches.selected && !caches.controlPlane, caches);
    check('logout-clears-hidden-package-details', !(await page.locator('body').textContent()).includes('SESSION_A'));
    await page.locator('#login-username').fill('session-b');
    await page.locator('#login-password').fill('fixture-password');
    await page.locator('#login-submit').click();
    await page.waitForFunction(() => state.currentUser?.username === 'session-b' && !byId('console-shell').hidden);
    assert.ok(blocked, 'new session list request must be held by the fixture');
    const loading = await page.locator('#console-shell').innerText();
    check('new-session-loading-no-old-data', !loading.includes('SESSION_A'), loading.slice(0,1800));
    await page.screenshot({ path:path.join(output,'new-session-loading.png') });
    release(); release = null;
    await page.waitForFunction(() => byId('live-status').textContent.includes('集群列表读取失败'));
    for (const route of routes) {
      await page.locator(`[data-nav="${route}"]`).click();
      await page.waitForFunction(route => document.querySelector(`[data-nav="${route}"]`).getAttribute('aria-current') === 'page', route);
      const text = await page.locator('#console-shell').innerText();
      check(`new-session-failed-bootstrap-${route}`, !text.includes('SESSION_A'), text.slice(0,1800));
    }
    check('new-session-remains-locked', await page.evaluate(() => !state.switchUnlocked && byId('execute-switchover').disabled && !state.clusterDataReady));
    await page.setViewportSize({ width:390, height:844 });
    await page.screenshot({ path:path.join(output,'new-session-mobile.png') });

    // Use the real refresh button with malformed API bodies, not direct state calls.
    secondSession = false;
    for (const scenario of [
      { name:'null', status:200, body:'null' },
      { name:'array', status:200, body:'[]' },
      { name:'malformed', status:200, body:'not-json' },
      { name:'expired-malformed', status:401, body:'not-json' },
      { name:'expired-null', status:401, body:'null' },
    ]) {
      await page.setViewportSize({ width:1440, height:1000 });
      await page.goto(`${base}/?case=${scenario.name}#topology`);
      await page.waitForFunction(() => state.clusterDataReady);
      const routePattern = '**/api/v1/clusters/*/discover';
      await page.route(routePattern, route => route.fulfill({ status:scenario.status, contentType:'application/json', body:scenario.body }));
      await page.locator('#refresh-cluster').click();
      await page.waitForFunction(() => !state.clusterLoading || !state.currentUser);
      const evidence = await page.evaluate(() => ({ loggedIn:!!state.currentUser, locked:!state.switchUnlocked, message:byId('live-status').textContent, login:byId('login-status').textContent }));
      check(scenario.name, evidence.locked && (scenario.status === 401 ? !evidence.loggedIn : evidence.loggedIn && evidence.message.includes('无效数据')), evidence);
      await page.unroute(routePattern);
    }
    control.hook = async ({ url }) => {
      if (url.pathname === '/api/v1/auth/logout') return { result:{} };
      if (url.pathname === '/api/v1/auth/login') return { result:{ user:{ username:'session-b', display_name:'Session B', role:'admin' } } };
    };
    await page.goto(`${base}/?case=late-expiry#topology`);
    await page.waitForFunction(() => state.clusterDataReady);
    let reached;
    const held = new Promise(resolve => { reached = resolve; });
    const gate = new Promise(resolve => { release = resolve; });
    await page.route('**/api/v1/clusters/*/discover', async route => {
      reached(); await gate;
      await route.fulfill({ status:401, contentType:'text/plain', body:'old-session-expired' });
    });
    await page.locator('#refresh-cluster').click(); await held;
    await page.locator('#account-menu-toggle').click(); await page.locator('#logout-button').click();
    await page.locator('#login-shell').waitFor({ state:'visible' });
    await page.locator('#login-username').fill('session-b'); await page.locator('#login-password').fill('fixture-password');
    await page.locator('#login-submit').click();
    await page.waitForFunction(() => state.currentUser?.username === 'session-b' && state.clusterDataReady);
    const oldResponse = page.waitForResponse(response => response.url().endsWith('/discover'));
    release(); release = null; await oldResponse; await page.waitForTimeout(100);
    check('late-invalid-401-cannot-expire-new-session', await page.evaluate(() => state.currentUser?.username === 'session-b' && state.clusterDataReady && !state.switchUnlocked && byId('login-shell').hidden));
    check('no-unhandled-browser-errors', errors.length === 0, errors);
    const report = { status:baseline ? 'baseline' : checks.every(item => item.passed) ? 'passed' : 'failed', checks, failures:checks.filter(item => !item.passed).length, scope:'isolated browser; no production requests' };
    fs.writeFileSync(path.join(output,'result.json'), JSON.stringify(report,null,2)+'\n');
    console.log(JSON.stringify(report));
    if (!baseline) assert.equal(report.failures,0);
  } finally {
    release?.();
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
