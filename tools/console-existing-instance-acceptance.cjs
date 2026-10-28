const assert = require('node:assert/strict');
const { chromium } = require('playwright');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
(async()=>{
 const browser=await chromium.launch({executablePath:process.env.CHROME_PATH||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
 let checks=0;
 try {
 for (const width of [1440,390]) {
  const fixture=createConsoleFixture();const {server,control,clusters}=fixture;
  const captured=[];let attached=false,failDiscovery=false;
  control.hook=async({req,url,cluster})=>{
   if(req.method==='POST'&&url.pathname.endsWith('/database-endpoints')){
    let data='';for await(const chunk of req)data+=chunk;
    captured.push({path:url.pathname,body:JSON.parse(data)});attached=true;return {result:{endpoints:[]}};
   }
   if(req.method==='POST'&&url.pathname.endsWith('/discover'))return failDiscovery?{status:503,message:'discovery unavailable'}:{result:{}};
   if(cluster&&cluster.resource_id===clusters[0].resource_id&&attached&&req.method==='GET'&&url.pathname.endsWith('/topology')){
    const top=fixture.topology(cluster);const cloud={...top.instances[0],resource_id:'cloud-instance',hostname:'cloud-mysql',ip_address:'192.0.2.40'};
    return {result:{...top,instances:[...top.instances,cloud],probes:[...top.probes,{instance_id:cloud.resource_id,outcome:'reachable',discovery_observed_at:top.observed_at}],links:[...top.links,{source_instance_id:top.instances[1].resource_id,target_instance_id:cloud.resource_id}]}};
   }
   return null;
  };
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  const page=await browser.newPage({viewport:{width,height:900}});
  try{
   await page.goto(`http://127.0.0.1:${server.address().port}/#topology`);
   await page.waitForFunction(()=>document.querySelector('#cluster-select').options.length>=2);
   await page.locator('#open-cluster-management-modal').click();await page.locator('#cluster-attach-tab').click();
   await assert.doesNotReject(()=>page.locator('#cluster-attach-note').waitFor({state:'visible'}));checks++;
   assert.equal(await page.locator('#cluster-display-name').inputValue(),clusters[0].display_name);checks++;
   assert.equal(await page.locator('#cluster-engine').isDisabled(),true);checks++;
   await page.locator('[data-field="hostname"]').fill('cloud-mysql');await page.locator('[data-field="ip_address"]').fill('192.0.2.40');
   await page.locator('#submit-cluster-registration').click();await page.waitForFunction(()=>!document.querySelector('#cluster-management-modal').open);
   assert.equal(captured[0].path,`/api/v1/clusters/${clusters[0].resource_id}/database-endpoints`);checks++;
   assert.deepEqual(captured[0].body,{endpoints:[{hostname:'cloud-mysql',ip_address:'192.0.2.40',port:3306}]});checks++;
   await page.waitForFunction(()=>document.body.textContent.includes('cloud-mysql'));checks++;
   assert.ok(!control.requests.some(x=>x.method==='POST'&&x.path.startsWith('/api/v1/nodes/sync')));checks++;
   // A failed discovery retains registration and reports failure rather than claiming success.
   failDiscovery=true;
   await page.locator('#open-cluster-management-modal').click();await page.locator('#cluster-attach-tab').click();
   await page.locator('[data-field="hostname"]').fill('cloud-second');await page.locator('[data-field="ip_address"]').fill('192.0.2.41');
   await page.locator('#submit-cluster-registration').click();await page.waitForFunction(()=>document.querySelector('#cluster-management-result').textContent.includes('首次发现未完成'));
   assert.equal(await page.locator('#cluster-management-modal').evaluate(el=>el.open),true);checks++;
   const bounds=await page.locator('#submit-cluster-registration').boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=width);checks++;
   assert.equal(control.requests.filter(x=>x.method==='POST'&&x.path==='/api/v1/clusters').length,0);checks++;
   await page.locator('#cancel-cluster-management').click();
   await page.locator('#open-cluster-management-modal').click();await page.locator('#cluster-attach-tab').click();
   await page.locator('[data-field="hostname"]').fill('late-cloud');
   await page.evaluate(id=>{const select=document.querySelector('#cluster-select');select.value=id;select.dispatchEvent(new Event('change',{bubbles:true}));},clusters[1].resource_id);
   const before=captured.length;await page.locator('#submit-cluster-registration').click();
   await page.waitForFunction(()=>document.querySelector('#cluster-management-result').textContent.includes('所选集群已变化'));
   assert.equal(captured.length,before);checks++;
   await page.locator('#cancel-cluster-management').click();
   await page.locator('#open-cluster-management-modal').click();await page.locator('#cluster-attach-tab').click();
   assert.equal(await page.locator('[data-field="port"]').inputValue(),'5432');checks++;
   await page.evaluate(()=>{const select=document.querySelector('#language-select');select.value='en-US';select.dispatchEvent(new Event('change',{bubbles:true}));});
   assert.equal(await page.locator('#cluster-attach-tab').textContent(),'Connect existing instances');checks++;
   assert.doesNotMatch(await page.locator('#cluster-attach-note').textContent(),/[\u3400-\u9fff]/);checks++;
   await page.screenshot({path:`.build/instance-browser/attach-${width}.png`,fullPage:true});
  }finally{await page.close();await new Promise(resolve=>server.close(resolve));}
 }
 console.log(JSON.stringify({scope:'isolated browser fixture, no live writes',checks,viewports:[1440,390]}));
 }finally{await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
