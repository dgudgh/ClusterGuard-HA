const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');

(async () => {
  const output = path.resolve('.build/log-toolbar/reference-layout');
  fs.mkdirSync(output, {recursive:true});
  const {server, control, operations} = createConsoleFixture();
  const templates = [...operations];
  const now = Date.now();
  operations.splice(0, operations.length, ...Array.from({length:413}, (_,i) => ({
    ...templates[i%templates.length], resource_id:`toolbar-${i}`,
    created_at:new Date(now-i*10000).toISOString()
  })));
  operations[0].operation = {...operations[0].operation, requested_by:'clusterguard-automatic-recovery'};
  operations[0].idempotency_key = 'automatic-failover:toolbar:105';
  for (let i = 0; i < 104; i++) operations.push({...operations[0],resource_id:`retry-${i}`,created_at:new Date(now-i*1000-1000).toISOString(),idempotency_key:`automatic-failover:toolbar:${i+1}`});
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve));
  const browser = await chromium.launch({headless:true,channel:'chrome'});
  const page = await browser.newPage({viewport:{width:1440,height:1000}});
  const errors = [];
  page.on('pageerror',error => errors.push(error.message));
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}/#operation-log`);
    await page.waitForFunction(() => document.querySelectorAll('#operation-log-list [data-operation-id]').length === 20);
    await page.locator('#log-cluster-filter').selectOption('all');
    await page.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='20 / 413');
    assert.match(await page.locator('#log-visible-count').textContent(),/20 \/ 413 个事件517 条原始记录/);
    assert.equal(await page.locator('#log-visible-count').evaluate(el => el.classList.contains('badge')),false);
    const viewports = [1920,1440,1318,1024,901,900,768,680,520,390,320];
    const layouts = [];
    for (const width of viewports) {
      await page.setViewportSize({width,height:1000});
      const boxes = await page.locator('.log-filters input, .log-filters select').evaluateAll(items => items.map(el => {
        const box = el.getBoundingClientRect(); return {x:box.x,y:box.y,right:box.right,bottom:box.bottom,width:box.width,height:box.height};
      }));
      assert.ok(boxes.every(box => box.height === 36 && box.x >= 0 && box.right <= width));
      for (let i=0;i<boxes.length;i++) for(let j=i+1;j<boxes.length;j++) {
        const a=boxes[i],b=boxes[j]; assert.ok(a.right<=b.x || b.right<=a.x || a.bottom<=b.y || b.bottom<=a.y,'filter controls overlap');
      }
      if (width>680) assert.ok(boxes.every(box => Math.abs(box.y-boxes[0].y)<1));
      const count = await page.locator('#log-visible-count').boundingBox();
      const heading = await page.locator('.log-toolbar .fleet-heading').boundingBox();
      const toolbar = await page.locator('.log-toolbar').boundingBox();
      assert.ok(count.x >= heading.x && count.x+count.width <= heading.x+heading.width+1);
      if (width>1160) {
        assert.ok(Math.abs(count.y+count.height/2-boxes[0].y-boxes[0].height/2)<1);
        assert.ok(heading.x+heading.width<=boxes[0].x);
        assert.equal(toolbar.height,76);
      } else assert.ok(heading.y+heading.height<=boxes[0].y);
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
      layouts.push({width,controlHeight:36,overlap:false,inlineHeading:width>1160});
      if ([1440,1318,390].includes(width)) {
        await page.locator('.log-toolbar').screenshot({path:path.join(output,`toolbar-${width}.png`)});
        await page.screenshot({path:path.join(output,`page-${width}.png`)});
      }
    }
    await page.setViewportSize({width:1318,height:1000});
    await page.getByRole('searchbox',{name:'筛选操作日志'}).fill('not-a-node');
    await page.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='0 / 0');
    await page.locator('.log-toolbar').screenshot({path:path.join(output,'empty-filter.png')});
    await page.getByRole('searchbox',{name:'筛选操作日志'}).fill('');
    await page.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='20 / 413');
    await page.getByRole('combobox',{name:'状态',exact:true}).selectOption('failed');
    await page.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='0 / 0');
    assert.equal(control.mutations,0);

    const offline = await browser.newPage({viewport:{width:1440,height:1000}});
    const network = [];
    offline.on('request',request=>{if(/^https?:/.test(request.url())) network.push(request.url());});
    offline.on('pageerror',error=>errors.push(error.message));
    await offline.goto(pathToFileURL(path.resolve('preview/log-toolbar/index.html')).href+'#operation-log');
    await offline.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='3 / 3');
    await offline.locator('#log-cluster-filter').selectOption('all');
    await offline.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='6 / 6');
    await offline.getByRole('searchbox',{name:'筛选操作日志'}).fill('sample-pg16');
    await offline.waitForFunction(()=>document.querySelector('#log-visible-count strong')?.textContent==='3 / 3');
    await offline.locator('#operation-log-list details summary').first().click();
    await offline.waitForFunction(()=>document.querySelector('#operation-log-list pre').textContent.includes('resource_id'));
    assert.deepEqual(network,[]); assert.deepEqual(errors,[]);
    const result = {status:'passed',scope:'actual HTML and offline fixture preview, not field deployment',layouts,events:413,rawRecords:517,initialItems:20,offlineSearchAndDetail:true,externalRequests:0};
    fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));
    console.log(JSON.stringify(result));
  } finally { await browser.close(); server.closeAllConnections(); await new Promise(resolve=>server.close(resolve)); }
})().catch(error=>{console.error(error);process.exitCode=1;});
