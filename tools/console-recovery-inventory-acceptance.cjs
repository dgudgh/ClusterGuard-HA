// Actual console and Chrome, isolated API replies; no field database writes.
const {chromium}=require('playwright'), assert=require('node:assert/strict'), fs=require('node:fs'), path=require('node:path');
const {createConsoleFixture}=require('./console-ui-fixture.cjs');
const out=path.resolve(process.env.CONSOLE_TEST_OUTPUT||'.build/recovery-inventory-browser');
(async()=>{
 fs.mkdirSync(out,{recursive:true});const checks=[],fixture=createConsoleFixture(),{server,control,clusters}=fixture;
 let reason='candidate evaluation has no current primary',role='admin',badDetail=false,blocked=false,sequence=0;
 const executions=[],tasks=new Map();
 control.hook=async({req,url,cluster})=>{
  if(url.pathname==='/api/v1/auth/me')return {result:{user:{username:'fixture',role}}};
  if(req.method==='POST'&&cluster&&url.pathname.endsWith('/discover'))return {result:{}};
  if(cluster&&url.pathname===`/api/v1/clusters/${cluster.resource_id}`){const result=fixture.detail(cluster);if(badDetail)result.cluster={...cluster,resource_id:'wrong-cluster'};return {result};}
  if(cluster&&url.pathname.endsWith('/topology')){const result=fixture.topology(cluster);result.instances.forEach((i,n)=>i.role=reason.includes('multiple')&&n<2?'primary':'unknown');return {result};}
  if(cluster&&url.pathname.endsWith('/candidates'))return {status:409,message:reason};
  if(cluster&&url.pathname.endsWith('/recovery/status'))return {result:{available:true,tasks:tasks.has(cluster.resource_id)?[tasks.get(cluster.resource_id)]:[],recovery:null}};
  if(cluster&&url.pathname.endsWith('/recovery/plan')){
   const task={resource_id:`fresh-plan-${++sequence}`,cluster_id:cluster.resource_id,metadata_revision:1,stage:blocked?'blocked':'planned',members:fixture.detail(cluster).instances,events:[]};tasks.set(cluster.resource_id,task);
   return {result:{task,ready:!blocked,message:blocked?'repository conflict: recovery plan immutable inventory changed; replan before confirmation; started recovery protections require review':''}};
  }
  if(cluster&&url.pathname.endsWith('/recovery/execute')){let body='';for await(const chunk of req)body+=chunk;executions.push(JSON.parse(body));return {status:403,message:'fixture never executes recovery'};}
 };
 await new Promise(r=>server.listen(0,'127.0.0.1',r));const browser=await chromium.launch({headless:true,channel:'chrome'});
 const check=(name,value)=>{assert.ok(value,name);checks.push(name);};
 const loaded=page=>page.waitForFunction(()=>document.querySelector('#cluster-load-notice').dataset.state==='partial');
 try{
  for(const width of [1280,390])for(const language of ['zh-CN','en-US']){
   const page=await browser.newPage({viewport:{width,height:900}}),errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.goto(`http://127.0.0.1:${server.address().port}/#settings`);await loaded(page);
   await page.locator('#language-select').selectOption(language);await page.locator('[data-nav="operations"]').click();
   for(const cluster of clusters)for(const cause of ['candidate evaluation has no current primary','candidate evaluation found multiple current primaries']){
    reason=cause;blocked=false;await page.locator('#cluster-select').selectOption(cluster.resource_id);await page.locator('#refresh-cluster').click();await loaded(page);await page.locator('#operations-disaster-tab').click();
    const prefix=`${width}/${language}/${cluster.engine}/${cause.includes('multiple')?'multiple':'none'}`,notice=await page.locator('#cluster-load-notice').textContent();
    check(prefix+' independent preflight guidance',notice.includes(language==='zh-CN'?'灾难恢复使用独立预检':'Disaster recovery has an independent preflight'));
    check(prefix+' requires explicit unlock',await page.locator('#switch-lock').isEnabled()&&await page.locator('#open-disaster-recovery').isDisabled());
    await page.locator('#switch-lock').click();check(prefix+' ordinary operations remain blocked',await page.locator('#open-disaster-recovery').isEnabled()&&await page.locator('#execute-switchover').isDisabled()&&await page.locator('#execute-rejoin').isDisabled());
    await page.locator('#open-disaster-recovery').click();await page.locator('#disaster-confirmation').waitFor({state:'visible'});
    await page.locator('#disaster-confirm-name').fill(cluster.display_name);await page.locator('#disaster-confirm-impact').check();check(prefix+' exact confirmation enables new plan',await page.locator('#disaster-execute').isEnabled());
    const previous=await page.evaluate(()=>state.disaster.task.resource_id);await page.locator('#disaster-replan').click();await page.waitForFunction(()=>!state.disaster.busy);
    check(prefix+' new plan invalidates previous confirmation',await page.evaluate(id=>state.disaster.task.resource_id!==id&&document.getElementById('disaster-confirm-name').value===''&&!document.getElementById('disaster-confirm-impact').checked,previous)&&await page.locator('#disaster-execute').isDisabled());
    const before=executions.length;await page.evaluate(()=>{document.getElementById('disaster-execute').disabled=false;document.getElementById('disaster-execute').click();});check(prefix+' forced stale confirmation rejected',executions.length===before);
    blocked=true;await page.locator('#disaster-replan').click();await page.waitForFunction(()=>!state.disaster.busy);check(prefix+' protected mismatch localized and blocked',(await page.locator('#disaster-alert').textContent()).includes(language==='zh-CN'?'节点库存已变化':'inventory changed')&&await page.locator('#disaster-confirmation').isHidden());
    await page.screenshot({path:path.join(out,`${width}-${language}-${cluster.engine}-${cause.includes('multiple')?'multiple':'none'}.png`)});
    await page.locator('#cancel-disaster').click();check(prefix+' close relocks',await page.locator('#open-disaster-recovery').isDisabled());
   }
   badDetail=true;await page.locator('#refresh-cluster').click();await page.waitForFunction(()=>document.querySelector('#cluster-load-notice').dataset.state==='error');check(`${width}/${language} untrusted detail cannot unlock`,await page.locator('#switch-lock').isDisabled()&&await page.locator('#open-disaster-recovery').isDisabled());badDetail=false;
   for(const denied of ['viewer','operator']){role=denied;await page.reload();await loaded(page);await page.locator('#operations-disaster-tab').click();check(`${width}/${language}/${denied} cannot unlock`,await page.locator('#switch-lock').isDisabled()&&await page.locator('#open-disaster-recovery').isDisabled());}
   role='admin';check(`${width}/${language} no browser exceptions`,errors.length===0);await page.close();
  }
  check('no execute or unexpected mutations',executions.length===0&&control.mutations===0);const result={status:'passed',scope:'isolated real Chrome and console; no field writes',assertions:checks.length,checks,fixture_plans:sequence,executions:executions.length};fs.writeFileSync(path.join(out,'result.json'),JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify(result));
 }finally{await browser.close();await new Promise(r=>server.close(r));}
})().catch(e=>{console.error(e);process.exitCode=1;});
