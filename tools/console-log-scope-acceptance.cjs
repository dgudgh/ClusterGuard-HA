const {chromium} = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createConsoleFixture,operationFixturePage} = require('./console-ui-fixture.cjs');
const baseline = process.argv.includes('--baseline');
const output = path.resolve('.build/log-scope',baseline?'before':'after');
const deferred = () => {let resolve; const promise=new Promise(done=>{resolve=done;});return {promise,resolve};};
const delay = ms=>new Promise(resolve=>setTimeout(resolve,ms));
const isPage = url=>url.pathname==='/api/v1/operations' && url.searchParams.get('view')==='page';

(async()=>{
  fs.mkdirSync(output,{recursive:true});
  const {server,control,clusters,operations}=createConsoleFixture();
  const templates=[operations[0],operations[3]];
  operations.splice(0,operations.length,...Array.from({length:100},(_,i)=>({
    ...templates[i%2],resource_id:`scope-${String(i).padStart(3,'0')}`,created_at:new Date(Date.now()-i*1000).toISOString()
  })));
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  const browser=await chromium.launch({headless:true,channel:'chrome'});
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  const errors=[],gates=[],checks=[];
  page.on('pageerror',error=>errors.push(error.message));
  const reads=()=>control.requests.filter(item=>item.path==='/api/v1/operations' && item.query.includes('view=page'));
  const lastQuery=()=>new URLSearchParams(reads().at(-1).query);
  const ready=()=>page.waitForFunction(()=>document.querySelector('#cluster-load-notice').dataset.state==='ready');
  const loaded=n=>page.waitForFunction(n=>document.querySelectorAll('#operation-log-list [data-operation-id]').length===n && !document.querySelector('.operation-log-load-status'),n);
  const checkRows=async index=>{
    const ids=await page.locator('#operation-log-list [data-operation-id]').evaluateAll(items=>items.map(item=>item.dataset.operationId));
    assert.ok(ids.length>0 && ids.every(id=>operations.find(item=>item.resource_id===id)?.operation.cluster_id===clusters[index].resource_id),'mixed cluster rows');
    assert.equal(await page.locator('#log-cluster-filter').inputValue(),clusters[index].resource_id);
    assert.equal(lastQuery().get('cluster_id'),clusters[index].resource_id);
  };
  try {
    await page.goto(`http://127.0.0.1:${server.address().port}/#operation-log`); await ready(); await loaded(20);
    if(baseline){
      checks.push({name:'default follows header',passed:lastQuery().get('cluster_id')===clusters[0].resource_id});
      const before=reads().length;
      await page.locator('#cluster-select').selectOption(clusters[1].resource_id); await ready();
      checks.push({name:'header change reloads scoped history',passed:reads().length>before && lastQuery().get('cluster_id')===clusters[1].resource_id});
      await page.screenshot({path:path.join(output,'mixed-history.png')});
      const report={baseline:true,checks}; fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(report,null,2)); console.log(JSON.stringify(report)); return;
    }
    await checkRows(0); assert.equal(reads().length,1);
    checks.push('initial load scoped once to header cluster');
    await page.locator('#load-more-operation-log').click(); await loaded(40); await checkRows(0);
    const gate=deferred(),started=deferred();gates.push(gate);
    control.hook=async({url})=>{if(isPage(url) && url.searchParams.get('cluster_id')===clusters[1].resource_id){started.resolve();await gate.promise;}};
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id); await started.promise;
    assert.equal(await page.locator('#operation-log-list [data-operation-id]').count(),0);
    assert.equal(lastQuery().has('cursor'),false);
    await ready(); gate.resolve();control.hook=null;await loaded(20);await checkRows(1);
    checks.push('header change clears old rows and cursor immediately; slow logs do not block topology');

    await page.locator('#load-more-operation-log').click();await loaded(40);
    const beforeRefresh=reads().length;
    await page.locator('#inspect-cluster').click();await ready();
    await page.locator('[data-nav="operation-log"]').click();await loaded(40);
    assert.equal(reads().length,beforeRefresh);await checkRows(1);
    checks.push('same-cluster refresh retains loaded pages without history reload');

    await page.locator('[data-nav="topology"]').click();
    await page.waitForFunction(()=>document.querySelector('[data-view="operation-log"]').hidden);
    await page.locator('#cluster-select').selectOption(clusters[0].resource_id);await ready();
    assert.equal(reads().length,beforeRefresh);
    await page.locator('[data-nav="operation-log"]').click();await loaded(20);await checkRows(0);
    checks.push('hidden log scope follows header without fetching until log page opens');

    const late=deferred(),lateStarted=deferred();gates.push(late);
    control.hook=async({url})=>{if(isPage(url)&&url.searchParams.has('cursor')){lateStarted.resolve();await late.promise;}};
    await page.locator('#load-more-operation-log').click();await lateStarted.promise;
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id);await loaded(20);await checkRows(1);
    late.resolve();control.hook=null;await delay(100);await loaded(20);await checkRows(1);
    checks.push('late old-cluster page cannot append into new cluster');

    await page.locator('#log-cluster-filter').selectOption('all');await loaded(20);
    assert.equal(lastQuery().has('cluster_id'),false);
    assert.match(await page.locator('#log-visible-count').textContent(),/100 个事件/);
    await page.locator('#cluster-select').selectOption(clusters[0].resource_id);await loaded(20);await checkRows(0);
    checks.push('all-cluster history requires explicit choice; header change restores scoped logs');
    for(let i=0;i<12;i++) {const index=(i+1)%2;await page.locator('#cluster-select').selectOption(clusters[index].resource_id);await loaded(20);await checkRows(index);}
    checks.push('12 MySQL/PostgreSQL header switches contain only selected cluster');

    control.hook=({url})=>{if(!isPage(url))return null;const wrong=new URL(url);wrong.searchParams.set('cluster_id',clusters[1].resource_id);return {result:operationFixturePage(operations,wrong,clusters)};};
    await page.locator('#log-search').fill('sample');
    await page.waitForFunction(()=>document.querySelector('.operation-log-load-status')?.textContent.includes('所选集群'));
    assert.equal(await page.locator('#operation-log-list [data-operation-id]').count(),0);
    control.hook=null;await page.locator('#refresh-operation-log').click();await loaded(20);await checkRows(0);
    checks.push('wrong-cluster API response rejected; retry recovers correct scope');
    await page.screenshot({path:path.join(output,'mysql-only.png')});
    await page.locator('#cluster-select').selectOption(clusters[1].resource_id);await loaded(20);await checkRows(1);
    await page.screenshot({path:path.join(output,'pg-only.png')});

    control.hook=({url})=>url.pathname==='/api/v1/clusters'?{result:[]}:null;
    const empty=await browser.newPage();const beforeEmpty=reads().length;
    await empty.goto(`http://127.0.0.1:${server.address().port}/#operation-log`);
    await empty.waitForFunction(()=>document.querySelector('#cluster-select').options[0]?.textContent==='未发现已注册集群');
    await delay(100);assert.equal(reads().length,beforeEmpty);
    assert.equal(await empty.locator('#operation-log-list [data-operation-id]').count(),0);
    checks.push('no header cluster never silently queries global history');
    assert.deepEqual(errors,[]);assert.equal(control.mutations,0);assert.equal(operations.length,100);
    const result={status:'passed',scope:'actual console, isolated API, no field deployment',checks};fs.writeFileSync(path.join(output,'result.json'),JSON.stringify(result,null,2));console.log(JSON.stringify(result));
  } finally {gates.forEach(gate=>gate.resolve());await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));}
})().catch(error=>{console.error(error);process.exitCode=1;});
