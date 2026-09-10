const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

(async () => {
  const { server, control, clusters } = createConsoleFixture();
  const output = process.env.CONSOLE_TEST_OUTPUT || path.resolve(__dirname, '../.build/console-overview-toolbar');
  fs.mkdirSync(output, { recursive:true });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const errors = [];
  try {
    const page = await browser.newPage();
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/#overview`);
    await page.waitForFunction(() => document.querySelector('#fleet-visible-count').textContent === '2 / 2');
    for (const width of [1920, 1440, 1024, 768, 390, 320]) {
      await page.setViewportSize({ width, height:1000 });
      const search = page.getByRole('search', { name:'筛选集群' }).getByRole('searchbox', { name:'筛选集群' });
      const health = page.getByRole('search').getByRole('combobox', { name:'健康状态' });
      await search.fill('mysql');
      assert.equal(await page.locator('#fleet-visible-count').textContent(), '1 / 2');
      assert.equal(await page.locator('.cluster-row-action').count(), 1);
      await search.fill('no-matching-cluster');
      assert.equal(await page.locator('#fleet-visible-count').textContent(), '0 / 2');
      assert.equal(await page.getByText('没有符合条件的集群').isVisible(), true);
      await search.fill('');
      await health.selectOption('attention');
      assert.equal(await page.locator('#fleet-visible-count').textContent(), '0 / 2');
      await health.selectOption('healthy');
      assert.equal(await page.locator('.cluster-row-action').count(), 2);
      await health.selectOption('all');
      await search.focus();
      await page.keyboard.press('Tab');
      assert.equal(await health.evaluate(el => el === document.activeElement), true, 'health filter follows search in keyboard order');
      const dimensions = await page.locator('.fleet-toolbar').evaluate(toolbar => {
        const box = toolbar.getBoundingClientRect();
        const heading = toolbar.querySelector('.fleet-heading').getBoundingClientRect();
        const search = toolbar.querySelector('input').getBoundingClientRect();
        const filter = toolbar.querySelector('select').getBoundingClientRect();
        return { fits:toolbar.scrollWidth <= toolbar.clientWidth && search.left >= box.left && filter.right <= box.right,
          aligned:Math.abs(search.top - filter.top) < 1 && Math.abs(search.height - filter.height) < 1,
          separated:search.right <= filter.left,
          headingSeparate:heading.right <= search.left || heading.bottom <= search.top,
          pageFits:document.documentElement.scrollWidth <= innerWidth };
      });
      assert.ok(Object.values(dimensions).every(Boolean), `toolbar layout at ${width}px: ${JSON.stringify(dimensions)}`);
      await page.locator('[data-i18n="overviewTitle"]').click();
      await page.screenshot({ path:path.join(output, `overview-${width}.png`), fullPage:true, animations:'disabled' });
    }
    await page.locator('.cluster-row-action').filter({ hasText:clusters[1].display_name }).click();
    await page.waitForFunction(name => document.querySelector('#topology-cluster-name').textContent === name && document.querySelector('#topology-risk').textContent === '健康', clusters[1].display_name);
    assert.ok(page.url().endsWith('#topology'), 'cluster rows must still open the selected topology');
    assert.deepEqual(errors, []);
    assert.equal(control.mutations, 0);
    const result = { status:'passed', widths:[1920,1440,1024,768,390,320], scope:'actual console HTML and isolated API fixtures', checks:['search', 'empty result', 'health filter', 'counts', 'keyboard focus', 'alignment', 'no overlap', 'cluster navigation'], output };
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify(result, null, 2) + '\n');
    console.log(JSON.stringify(result));
  } finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode = 1; });
