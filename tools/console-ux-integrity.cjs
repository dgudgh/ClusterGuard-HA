const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const { createReviewFixture, evidence, views } = require('./console-ux-review.cjs');

function contrast(foreground, background) {
  const luminance = color => color.match(/[\d.]+/g).slice(0,3).map(Number).map(v => {
    const n = v / 255; return n <= .04045 ? n / 12.92 : ((n + .055) / 1.055) ** 2.4;
  }).reduce((sum, n, i) => sum + n * [.2126, .7152, .0722][i], 0);
  const a = luminance(foreground), b = luminance(background);
  return (Math.max(a,b) + .05) / (Math.min(a,b) + .05);
}

(async () => {
  const fixture = createReviewFixture();
  await new Promise(resolve => fixture.server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ channel:'chrome', headless:true });
  const origin = `http://127.0.0.1:${fixture.server.address().port}`;
  const classic = [], contrasts = [], errors = [];
  try {
    const before = await browser.newPage(), after = await browser.newPage();
    for (const [page, prefix] of [[before, '/before/'], [after, '/']]) {
      page.on('pageerror', error => errors.push(error.message));
      await page.goto(origin + prefix + '#overview');
      await page.waitForFunction(() => state.clusterDataReady);
    }
    const geometry = page => page.evaluate(() => {
      const selectors = '.content,.page-header,.view:not([hidden]),.view:not([hidden]) [id],.context-cell,.log-toolbar,.log-card';
      return [...document.querySelectorAll(selectors)].filter(el => el.getBoundingClientRect().width > 0 && !el.classList.contains('review-only') && !el.classList.contains('ux-plan')).map(el => {
        const r = el.getBoundingClientRect();
        return { key:el.id || [...el.classList].filter(name => !name.startsWith('ux-')).join(' '), box:[r.x+scrollX,r.y+scrollY,r.width,r.height].map(n => Math.round(n*10)/10) };
      });
    });
    for (const width of [1440,1024,768,390]) {
      for (const page of [before,after]) await page.setViewportSize({ width,height:width<500?844:1000 });
      for (const view of views) {
        for (const page of [before,after]) {
          await page.locator(`[data-nav="${view}"]`).click();
          await page.locator(`[data-view="${view}"]`).waitFor({state:'visible'});
          if (view === 'operation-log') await page.waitForFunction(() => state.logLoaded && !state.logController);
          await page.evaluate(() => scrollTo(0,0));
        }
        const a = await geometry(before), b = await geometry(after);
        assert.deepEqual(b,a,`default layout differs: ${view} at ${width}px`);
        classic.push({view,width,passed:true});
      }
    }
    await after.goto(origin + '/?ui=review#operations');
    await after.waitForFunction(() => state.clusterDataReady);
    const samples = await after.evaluate(() => {
      const values = ['#operations-title','#operations-section-summary','#ux-operation-permission','#ux-operation-observation','#ux-operation-reason','#execute-switchover'];
      return values.map(selector => {
        const el = document.querySelector(selector), style = getComputedStyle(el);
        let parent = el, background = 'rgb(255, 255, 255)';
        while (parent) {
          const bg = getComputedStyle(parent).backgroundColor;
          if (!bg.endsWith(', 0)') && bg !== 'transparent') { background = bg; break; }
          parent = parent.parentElement;
        }
        return {selector,foreground:style.color,background};
      });
    });
    for (const sample of samples) {
      const ratio = contrast(sample.foreground,sample.background);
      assert.ok(ratio >= 4.5,`${sample.selector} contrast ${ratio}`);
      contrasts.push({...sample,ratio:Number(ratio.toFixed(2))});
    }
    await after.locator('[data-nav="metrics"]').click();
    await after.locator('[data-view="metrics"]').waitFor({state:'visible'});
    const scrollable = await after.evaluate(() => {
      const view = document.querySelector('[data-view="metrics"]');
      const container = [...view.querySelectorAll('*')].find(el => el.scrollWidth > el.clientWidth + 10 && ['auto','scroll'].includes(getComputedStyle(el).overflowX));
      if (!container) return null;
      container.scrollLeft = container.scrollWidth;
      return {scrolled:container.scrollLeft > 0, pageOverflow:document.documentElement.scrollWidth > innerWidth + 1};
    });
    assert.ok(scrollable?.scrolled,'mobile long table must scroll inside its container');
    assert.equal(scrollable.pageOverflow,false);
    assert.deepEqual(errors,[]);
    const report = {status:'passed',classic,contrasts,mobileTable:scrollable,errors,mutations:fixture.control.mutations};
    fs.writeFileSync(path.join(evidence,'integrity.json'),JSON.stringify(report,null,2));
    console.log(JSON.stringify(report));
  } finally { await browser.close(); fixture.server.closeAllConnections(); await new Promise(resolve => fixture.server.close(resolve)); }
})().catch(error => { console.error(error); process.exitCode=1; });
