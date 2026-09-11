const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const sharp = require('sharp');
const { buildPreview } = require('./build-console-preview.cjs');

const output = path.resolve(process.env.CONSOLE_TEST_OUTPUT || '.build/console-design-review/verified');
const views = ['overview', 'topology', 'operations', 'nodes', 'metrics', 'operation-log', 'about', 'settings'];
const tabs = {
  topology:['topology-map-tab', 'topology-power-tab', 'topology-identity-tab'],
  operations:['operations-switchover-tab', 'operations-recovery-tab', 'operations-disaster-tab'],
  settings:['settings-status-tab', 'software-update-tab', 'settings-account-tab']
};

(async () => {
  fs.mkdirSync(output, { recursive:true });
  const { destination } = await buildPreview();
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const errors = [], network = [], screenshots = [];
  try {
    const page = await browser.newPage({ viewport:{ width:1440, height:1000 } });
    page.on('pageerror', error => errors.push(error.message));
    page.on('request', request => { if (/^https?:/.test(request.url())) network.push(request.url()); });
    await page.goto(pathToFileURL(destination).href);
    await page.waitForFunction(() => document.querySelectorAll('.node-card').length === 3 && document.querySelector('#cluster-load-notice').hidden);
    const capture = async name => {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name}: page overflow`);
      const invalid = await page.locator('button:visible, .nav-link:visible').evaluateAll(elements => elements.filter(el => el.clientWidth && el.scrollWidth > el.clientWidth + 2).map(el => el.id || el.textContent.trim()));
      assert.deepEqual(invalid, [], `${name}: clipped controls`);
      const target = path.join(output, name + '.png');
      await page.screenshot({ path:target, fullPage:true }); screenshots.push(name);
    };
    for (const width of [1440, 1024, 768, 390, 320]) {
      await page.setViewportSize({ width, height:width < 500 ? 844 : 1000 });
      for (const view of views) {
        await page.locator(`[data-nav="${view}"]`).click();
        await page.locator(`[data-view="${view}"]`).waitFor({ state:'visible' });
        if (tabs[view]) {
          for (const tab of tabs[view]) {
            await page.locator('#' + tab).click();
            assert.equal(await page.locator('#' + tab).getAttribute('aria-selected'), 'true');
            await capture(`${view}-${tab}-${width}`);
          }
          await page.locator('#' + tabs[view][0]).click();
        }
        await capture(`${view}-${width}`);
      }
      if (![1440, 390].includes(width)) continue;
      const dialogCases = [
        ['overview', null, 'open-cluster-management-modal', 'cluster-management-modal', 'cancel-cluster-management'],
        ['topology', 'topology-map-tab', 'open-metadata-modal', 'metadata-modal', 'cancel-metadata'],
        ['topology', 'topology-power-tab', 'open-power-shutdown', 'power-shutdown-dialog', 'cancel-power-shutdown'],
        ['nodes', null, 'open-node-lifecycle-modal', 'node-lifecycle-modal', 'cancel-node-lifecycle'],
        ['settings', 'settings-account-tab', 'settings-change-password', 'password-modal', 'cancel-password-change'],
        ['settings', 'software-update-tab', 'open-software-update-dialog', 'software-update-dialog', 'cancel-software-update-dialog'],
        ['operations', 'operations-disaster-tab', 'open-disaster-recovery', 'disaster-dialog', 'cancel-disaster']
      ];
      for (const [view, tab, open, dialog, close] of dialogCases) {
        await page.locator(`[data-nav="${view}"]`).click();
        await page.locator(`[data-view="${view}"]`).waitFor({ state:'visible' });
        if (tab) await page.locator('#' + tab).click();
        await page.locator('#' + open).click();
        await page.locator('#' + dialog).waitFor({ state:'visible' });
        const bounds = await page.locator('#' + dialog).boundingBox();
        assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width + 1, `${dialog}: width`);
        await capture(`${dialog}-${width}`);
        await page.locator('#' + close).click();
        await page.locator('#' + dialog).waitFor({ state:'hidden' });
      }
    }
    await page.setViewportSize({ width:1440, height:1000 });
    await page.locator('[data-nav="overview"]').click();
    await page.locator('[data-view="overview"]').waitFor({ state:'visible' });
    assert.equal(await page.locator('#overview-recent-operations .recent-operation').count(), 5);
    await page.locator('.overview-section .text-link').click();
    await page.locator('[data-view="operation-log"]').waitFor({ state:'visible' });
    assert.equal(await page.locator('.log-card').count(), 6, 'recent summary must not truncate audit history');
    for (const value of ['11111111-1111-4111-8111-111111111110', '11111111-1111-4111-8111-111111111111']) {
      await page.locator('#cluster-select').selectOption(value);
      await page.waitForFunction(() => document.querySelector('#cluster-load-notice').hidden);
      await page.locator('[data-nav="metrics"]').click();
      await page.locator('[data-view="metrics"]').waitFor({ state:'visible' });
      assert.equal(await page.locator('#metric-comparisons progress').count(), 9);
      await capture(`metrics-${value.endsWith('0') ? 'mysql' : 'pg'}`);
    }
    assert.deepEqual(errors, []);
    assert.deepEqual(network, [], 'standalone preview must make no network requests');
    const tiles = [];
    for (const [i, view] of views.entries()) tiles.push({ input:await sharp(path.join(output, `${view}-1440.png`)).resize({ width:720, height:500, fit:'cover', position:'top' }).toBuffer(), left:(i % 2) * 720, top:Math.floor(i / 2) * 500 });
    await sharp({ create:{ width:1440, height:2000, channels:3, background:'#fff' } }).composite(tiles).png().toFile(path.join(output, 'contact.png'));
    const report = { result:'passed', preview:destination, screenshots:screenshots.length, mainPages:views, widths:[1440,1024,768,390,320], networkRequests:network.length, runtimeErrors:errors.length, safety:'Read-only isolated examples; no field deployment or mutations' };
    fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify(report));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
