const {chromium}=require('playwright');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {createConsoleFixture}=require('./console-ui-fixture.cjs');
const out=path.resolve(process.env.CONSOLE_TEST_OUTPUT||'.build/instance-failover-20261103/browser');fs.mkdirSync(out,{recursive:true});
(async()=>{
 const f=createConsoleFixture(process.env.CONSOLE_HTML_PATH),policies=new Map(),writes=[],errors=[];let failLoad=false,failWrite=false,releaseWrite=null,holdWrite=false;
 const view=c=>{const d=f.detail(c),instances=Object.fromEntries(d.instances.map(i=>[i.resource_id,{automatic_eligible:true,weight:100}]));return {cluster_id:c.resource_id,automatic_available:true,engine_suppressed:false,effective_automatic_enabled:false,policy:policies.get(c.resource_id)||{revision:0,automatic_enabled:false,instances}}};
 f.control.hook=async({req,url})=>{
  const c=f.clusters.find(c=>url.pathname===`/api/v1/clusters/${c.resource_id}`||url.pathname===`/api/v1/clusters/${c.resource_id}/failover-policy`);
  if(!c)return;
  if(req.method==='GET')return failLoad?{status:503,message:'fixture read failure'}:{result:{...f.detail(c),failover_policy:view(c)}};
  if(req.method==='PUT'){
   let raw='';for await(const b of req)raw+=b;const body=JSON.parse(raw);writes.push({cluster:c.resource_id,body});
   if(holdWrite)await new Promise(r=>releaseWrite=r);
   if(failWrite)return {status:503,message:'fixture write failure'};
   const old=view(c);if(body.expected_revision!==old.policy.revision)return {status:409,message:'revision changed'};
   policies.set(c.resource_id,{revision:old.policy.revision+1,automatic_enabled:body.automatic_enabled,instances:body.instances});return {result:view(c)};
  }
 };
 await new Promise(r=>f.server.listen(0,'127.0.0.1',r));const browser=await chromium.launch({channel:'chrome',headless:true});let checks=0;
 try{
  const page=await browser.newPage({viewport:{width:1440,height:1050}});page.on('pageerror',e=>errors.push(e.message));await page.goto(`http://127.0.0.1:${f.server.address().port}/#settings`);await page.waitForFunction(()=>state.clusterDataReady);
  await page.locator('#settings-configuration-tab').click();await page.locator('#configuration-policy-tab').click();await page.locator('#instance-failover-policy').waitFor({state:'visible'});
  assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);checks++;
  await page.locator('#instance-failover-unlock').click();await page.locator('#instance-failover-enabled').check();
  const rows=page.locator('#instance-failover-rows tr');assert.equal(await rows.count(),3);checks++;
  await rows.nth(0).locator('input[type="checkbox"]').uncheck();await rows.nth(2).locator('input[type="number"]').fill('900');await page.locator('#instance-failover-save').click();await page.waitForFunction(()=>document.getElementById('instance-failover-status').textContent.includes('已保存'));
  assert.equal(writes.length,1);assert.equal(writes[0].body.automatic_enabled,true);assert.equal(writes[0].body.instances[`${f.clusters[0].resource_id}-instance-0`].automatic_eligible,false);assert.equal(writes[0].body.instances[`${f.clusters[0].resource_id}-instance-2`].weight,900);assert.equal(writes[0].cluster,f.clusters[0].resource_id);checks+=5;
  assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);checks++;await page.screenshot({path:path.join(out,'desktop-policy-saved.png'),fullPage:true});
  await page.locator('#instance-failover-unlock').click();await rows.nth(1).locator('input[type="number"]').fill('1001');await page.locator('#instance-failover-save').click();assert.equal(writes.length,1);assert.match(await page.locator('#instance-failover-status').textContent(),/0到1000/);checks+=2;
  await page.locator('#cluster-select').selectOption(f.clusters[1].resource_id);await page.waitForFunction(()=>state.clusterDataReady&&state.clusterDetail.cluster.engine==='postgresql');assert.equal(await page.locator('#instance-failover-enabled').isChecked(),false);assert.equal(await rows.nth(1).locator('input[type="number"]').inputValue(),'100');assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);checks+=3;
  // A late response to the first cluster cannot populate or unlock the second.
  await page.locator('#instance-failover-unlock').click();await rows.nth(0).locator('input[type="number"]').fill('500');holdWrite=true;await page.locator('#instance-failover-save').click();await page.waitForFunction(()=>state.instanceFailoverSaving);await page.locator('#cluster-select').selectOption(f.clusters[0].resource_id);await page.waitForFunction(()=>state.clusterDataReady&&state.clusterDetail.cluster.engine==='mysql');releaseWrite();holdWrite=false;await page.waitForTimeout(100);
  assert.equal(await page.locator('#instance-failover-enabled').isChecked(),true);assert.equal(await rows.nth(2).locator('input[type="number"]').inputValue(),'900');assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);assert.equal(writes[1].cluster,f.clusters[1].resource_id);checks+=4;
  // Save failures lock the draft and do not claim success.
  failWrite=true;await page.locator('#instance-failover-unlock').click();await page.locator('#instance-failover-save').click();await page.waitForFunction(()=>document.getElementById('instance-failover-status').textContent.includes('保存失败'));assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);checks++;
  await page.screenshot({path:path.join(out,'desktop.png'),fullPage:true});
  await page.locator('#settings-status-tab').click();await page.locator('#language-select').selectOption('en-US');await page.locator('#settings-configuration-tab').click();await page.locator('#configuration-policy-tab').click();await page.waitForFunction(()=>document.documentElement.lang==='en-US');assert.match(await page.locator('#instance-failover-policy').innerText(),/Selected cluster automatic failover/);assert.doesNotMatch(await page.locator('#instance-failover-policy').innerText(),/[\u3400-\u9fff]/);checks+=2;
  await page.setViewportSize({width:390,height:900});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),true);await page.screenshot({path:path.join(out,'english-mobile.png'),fullPage:true});checks++;
  failLoad=true;await page.locator('#cluster-select').selectOption(f.clusters[1].resource_id);await page.waitForFunction(()=>!!state.clusterLoadError);assert.equal(await page.locator('#instance-failover-save').isDisabled(),true);checks++;
  assert.deepEqual(errors,[]);checks++;fs.writeFileSync(path.join(out,'result.json'),JSON.stringify({passed:true,checks,errors,scope:'isolated browser and fixture API; no live node writes'},null,2));console.log(JSON.stringify({passed:true,checks,errors}));
 }finally{await browser.close();await new Promise(r=>f.server.close(r))}
})().catch(e=>{console.error(e.stack);process.exitCode=1});
