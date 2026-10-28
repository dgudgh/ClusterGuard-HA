const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {createConsoleFixture}=require('./console-ui-fixture.cjs');
(async()=>{
 const browser=await chromium.launch({channel:'chrome',headless:true});let checks=0;
 try{for(const width of [1440,390]){
  const f=createConsoleFixture(),{server,control,clusters}=f;let ready=true,delay=null,fail=false,stale=false;const executions=[];
  control.hook=async({req,url,cluster})=>{
   if(req.method==='POST'&&url.pathname.endsWith('/discover'))return {result:{}};
   if(req.method==='POST'&&url.pathname==='/api/v1/operations/execute'){let body='';for await(const chunk of req)body+=chunk;executions.push(JSON.parse(body));return {status:403,message:'isolated fixture: database mutations blocked'};}

   if(cluster&&url.pathname===`/api/v1/clusters/${cluster.resource_id}`){const d=f.detail(cluster);if(cluster===clusters[0]){d.ha_endpoints=[{resource_id:'gw-resource',endpoint_id:'gw-endpoint',provider:'gateway',owner_id:cluster.resource_id+'-instance-1'}];d.endpoints.push({resource_id:'gw-endpoint',kind:'service',active:true,hostname:'database.example',port:63306});d.runtime_profile.endpoint_providers=['gateway'];}return {result:d};}
   if(cluster&&url.pathname.endsWith('/gateway')){if(delay)await delay;if(fail)return {status:503,message:'fixture gateway unreachable'};return {result:{cluster_id:cluster.resource_id,ready,lease_valid:ready,gateways:[1,2].map(()=>({cluster_id:cluster.resource_id,active:ready,last_grant:{target_instance_id:`${cluster.resource_id}-instance-1`,expires_at:new Date(Date.now()+(stale?-1000:15000)).toISOString()}}))}};}
  };
  await new Promise(r=>server.listen(0,'127.0.0.1',r));const page=await browser.newPage({viewport:{width,height:1000}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
  try{
   await page.goto(`http://127.0.0.1:${server.address().port}/#operations`);
   await page.waitForFunction(()=>document.querySelector('#gateway-route-state').textContent.includes('路由与当前主库一致'));
   assert.equal(await page.locator('#gateway-route-panel').evaluate(el=>el.open),false);checks++;
   await page.locator('#gateway-route-panel summary').click();
   assert.equal(await page.locator('#gateway-route-rows tr').count(),2);checks++;
   assert.equal(await page.locator('#execute-switchover').isDisabled(),true);checks++;
   assert.match(await page.locator('#operation-vip').textContent(),/企业连接网关/);checks++;
   await page.locator('#candidate-select').selectOption(clusters[0].resource_id+'-instance-0');
   await page.locator('#switch-lock').click();
   assert.equal(await page.locator('#execute-switchover').isEnabled(),true);checks++;
   await page.locator('#execute-switchover').click();
   await page.waitForFunction(()=>!state.operationRunning);
   assert.equal(executions.length,1);assert.equal(executions[0].target_id,clusters[0].resource_id+'-instance-0');assert.equal(executions[0].operation.cluster_id,clusters[0].resource_id);checks+=3;
   assert.equal(await page.locator('#execute-switchover').isDisabled(),true);checks++;
   await page.evaluate(()=>{const s=document.querySelector('#language-select');s.value='en-US';s.dispatchEvent(new Event('change',{bubbles:true}));});
   assert.equal(await page.locator('#gateway-route-heading').textContent(),'Database connection gateway');checks++;
   assert.doesNotMatch(await page.locator('#gateway-route-panel').textContent(),/[\u3400-\u9fff]/);checks++;
   const b=await page.locator('#gateway-route-panel').boundingBox();assert.ok(b.x>=0&&b.x+b.width<=width+1);checks++;
   for(const cell of await page.locator('#gateway-route-rows tr:first-child td').all()){const r=await cell.boundingBox();assert.ok(r.x+r.width<=b.x+b.width+1)}checks++;
   await page.screenshot({path:`.build/gateway/browser-${width}.png`,fullPage:true});
   fail=true;await page.locator('#refresh-cluster').click();await page.waitForFunction(()=>document.querySelector('#gateway-route-state').textContent.includes('Gateway route read failed'),null,{timeout:5000});
   assert.equal(await page.locator('#execute-switchover').isDisabled(),true);checks++;
   fail=false;ready=false;await page.locator('#refresh-cluster').click();await page.waitForFunction(()=>document.querySelector('#gateway-route-state').textContent==='Gateway routes are not ready');checks++;
   await page.waitForRequest(r=>r.method()==='GET'&&r.url().endsWith('/gateway'),{timeout:7000});
   assert.equal(await page.locator('#execute-switchover').isDisabled(),true);checks++;
   ready=true;stale=true;await page.locator('#refresh-cluster').click();await page.waitForFunction(()=>document.querySelector('#gateway-route-state').textContent==='Gateway status needs a fresh read');checks++;
   let release;delay=new Promise(r=>release=r);
   const pending=page.waitForRequest(r=>r.url().endsWith('/gateway'));
   await page.locator('#refresh-cluster').click();await pending;
   // Switch context while a gateway read is pending; late gateway data must
   // neither display under the other cluster nor alter the operation lock.
   await page.evaluate(id=>{const s=document.querySelector('#cluster-select');s.value=id;s.dispatchEvent(new Event('change',{bubbles:true}));},clusters[1].resource_id);
   release();delay=null;
   await page.waitForFunction(()=>document.querySelector('#gateway-route-panel').hidden);
   assert.equal(await page.locator('#execute-switchover').isDisabled(),true);checks++;
   assert.equal(control.mutations,0);checks++;
   assert.deepEqual(errors,[]);checks++;
  }finally{await page.close();await new Promise(r=>server.close(r));}
 }}finally{await browser.close()}
 console.log(JSON.stringify({checks,failures:0,scope:'isolated browser and fixture API; no live database writes'}));
})().catch(e=>{console.error(e);process.exit(1)});
