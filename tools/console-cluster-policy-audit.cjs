const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/cluster-policy-audit');

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const fixture = createConsoleFixture(process.env.CONSOLE_HTML_PATH);
  const { server, control } = fixture;
  let failRead = true;
  let policy = { engines:{ postgresql:{ automatic_failover_suppressed:true } } };
  const writes = [];
  control.hook = async ({ req, url }) => {
    if (url.pathname === '/api/v1/control-plane/configuration') {
      return { result:{ file_present:true, path:'/etc/clusterguard/clusterguard.json', reload_supported:false, reload_note:'需要重启', sections:[], warnings:[] } };
    }
    if (url.pathname === '/api/v1/cluster-policy' && req.method === 'GET') {
      return failRead ? { status:503, message:'policy unavailable' } : { result:{ policy, summary:[] } };
    }
    if (url.pathname === '/api/v1/cluster-policy' && req.method === 'PUT') {
      let body = '';
      for await (const chunk of req) body += chunk;
      const submitted = JSON.parse(body);
      writes.push(submitted);
      policy = submitted;
      return { result:{ policy, summary:[] } };
    }
  };
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  try {
    const page = await browser.newPage({ viewport:{ width:1280, height:900 } });
    await page.goto(`http://127.0.0.1:${server.address().port}/#settings`);
    await page.waitForFunction(() => state.clusterDataReady);
    await page.locator('#settings-configuration-tab').click();
    await page.waitForFunction(() => document.getElementById('cluster-policy-status').textContent.includes('策略读取失败'));
    assert.equal(await page.locator('#save-cluster-policy').isDisabled(), true);
    assert.equal(await page.locator('#clear-cluster-policy').isDisabled(), true);
    assert.equal(writes.length, 0);

    failRead = false;
    await page.locator('#settings-status-tab').click();
    await page.locator('#settings-configuration-tab').click();
    await page.locator('#save-cluster-policy').waitFor({ state:'visible' });
    await page.waitForFunction(() => !document.getElementById('save-cluster-policy').disabled);
    await page.locator('#cluster-policy-observations').fill('1');
    await page.locator('#save-cluster-policy').click();
    assert.match(await page.locator('#cluster-policy-status').textContent(), /请检查数值范围/);
    assert.equal(writes.length, 0);
    await page.locator('#cluster-policy-observations').fill('6');
    await page.locator('#save-cluster-policy').click();
    await page.waitForFunction(() => document.getElementById('cluster-policy-status').textContent.includes('已读取当前策略'));
    assert.equal(writes.length, 1);
    assert.equal(writes[0].engines.mysql.automatic_failover_minimum_observations, 6);
    assert.equal(writes[0].engines.postgresql.automatic_failover_suppressed, true);
    await page.screenshot({ path:path.join(output, 'settings-configuration-desktop.png'), fullPage:true });
    await page.setViewportSize({ width:390, height:844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), true);
    await page.screenshot({ path:path.join(output, 'settings-configuration-mobile.png'), fullPage:true });
    console.log(JSON.stringify({ status:'passed', checks:['failed read blocks writes', 'invalid input blocks writes', 'save preserves other engine', 'mobile has no horizontal overflow'] }));
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
