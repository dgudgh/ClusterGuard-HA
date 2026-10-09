// Real browser interactions, isolated API replies; never writes node files.
const {chromium}=require('playwright');
const {createConsoleFixture}=require('./console-ui-fixture.cjs');
const fs=require('node:fs');const path=require('node:path');
async function main(){
 const baseline=process.env.CONSOLE_BASELINE==='1';const out=path.resolve(process.env.CONSOLE_TEST_OUTPUT||'.build/configuration-collapse-evidence');fs.mkdirSync(out,{recursive:true});
 const browser=await chromium.launch({channel:'chrome',headless:true});const checks=[];
 const check=(width,name,ok,detail)=>{checks.push({width,name,ok,detail});console.log((ok?'PASS':'FAIL')+' '+width+' '+name+': '+JSON.stringify(detail));};
 try{for(const width of [1440,390]){
  const fixture=createConsoleFixture(process.env.CONSOLE_HTML_PATH);let failRead=false;
  const literal='<img src=x onerror="window.__unsafeConfiguration=true">';
  const view={path:'/etc/clusterguard/clusterguard.json',file_present:true,reload_supported:false,reload_note:'参数变更需要重启节点；重新读取只刷新显示。',warnings:['sample warning'],sections:[
   {key:'runtime',label:'控制面运行参数',note:'HTTP/TLS/元数据路径属于节点本地引导配置，改动必须重启该节点。',values:[{key:'http_address',value:'0.0.0.0:3000',source:'file',restart_required:true},{key:'control_token_env',value:'环境变量 CG_CONTROL_TOKEN',source:'file',restart_required:true,credential_ref:true,note:literal}]},
   {key:'consensus',label:'Raft 共识与成员',note:'每台控制器各有一份，三台之间 address 必须互不相同。',values:[{key:'local_id',value:'sample-node-1',source:'file',restart_required:true},{key:'peers',value:'sample-node-2@192.0.2.2:10009;'.repeat(8),source:'file',restart_required:true}]}
  ]};
  fixture.control.hook=async({url,req})=>{
   if(url.pathname==='/api/v1/control-plane/configuration/node')return {result:{fields:[],values:{},ready:false}};
   if(url.pathname==='/api/v1/control-plane/configuration/distribution')return {result:{members:[],tasks:[]}};
   if(url.pathname==='/api/v1/control-plane/configuration')return failRead?{status:503,message:'configuration unavailable'}:{result:view};
   if(req.method!=='GET')throw Error('unexpected mutation '+req.method+' '+url.pathname);
   return null;
  };
  await new Promise(resolve=>fixture.server.listen(0,'127.0.0.1',resolve));const origin='http://127.0.0.1:'+fixture.server.address().port;
  const page=await browser.newPage({viewport:{width,height:900}});const errors=[];page.on('pageerror',e=>errors.push(e.message));
  try{
   await page.goto(origin+'/#settings');await page.locator('#settings-configuration-tab').click();await page.locator('#configuration-source-summary').getByText('配置文件已读取',{exact:true}).waitFor();await page.waitForFunction(()=>!state.configurationLoading);
   const group=key=>page.locator('details.configuration-section[data-configuration-section="'+key+'"]');
   check(width,'all groups initially collapsed',await page.locator('details.configuration-section').count()===2 && await page.locator('details.configuration-section[open]').count()===0,await page.locator('details.configuration-section').count());
   check(width,'tables initially hidden',await page.locator('.configuration-table').evaluateAll(nodes=>nodes.every(n=>!n.checkVisibility())),await page.locator('.configuration-table').count());
   if(baseline)continue;
   await page.screenshot({path:path.join(out,'configuration-collapsed-'+width+'.png'),fullPage:true});
   await group('runtime').locator('summary').click();
   check(width,'click opens only selected group',await group('runtime').getAttribute('open')!==null&&await group('consensus').getAttribute('open')===null,'runtime only');
   check(width,'original parameter and restart marker remain visible',await group('runtime').getByText('0.0.0.0:3000',{exact:true}).isVisible()&&await group('runtime').getByText('需重启',{exact:true}).first().isVisible(),'effective source unchanged');
   check(width,'credential reference and literal diagnostic preserved',await group('runtime').getByText('凭据引用',{exact:false}).isVisible()&&!await group('runtime').locator('img').count()&&!await page.evaluate(()=>window.__unsafeConfiguration),literal);
   await group('consensus').locator('summary').focus();await page.keyboard.press('Enter');
   check(width,'keyboard Enter opens group',await group('consensus').getAttribute('open')!==null,'native summary');
   await page.keyboard.press('Space');
   check(width,'keyboard Space closes group',await group('consensus').getAttribute('open')===null,'native summary');
   await page.locator('#reload-configuration').click();await page.waitForFunction(()=>!state.configurationLoading);
   check(width,'reload retains independent open state',await group('runtime').getAttribute('open')!==null&&await group('consensus').getAttribute('open')===null,'no forced expansion');
   await page.locator('#settings-status-tab').click();
   await page.locator('#language-select').selectOption('en-US');
   await page.locator('#settings-configuration-tab').click();await page.waitForFunction(()=>!state.configurationLoading);
   check(width,'language rendering retains open state',await group('runtime').getAttribute('open')!==null&&await group('consensus').getAttribute('open')===null,'rendered again');
   check(width,'parameter count follows selected language',await group('runtime').locator('.configuration-section-count').textContent()==='2 parameters','English count');
   await page.locator('#settings-status-tab').click();await page.locator('#language-select').selectOption('zh-CN');
   await page.locator('#settings-configuration-tab').click();await page.waitForFunction(()=>!state.configurationLoading);
   failRead=true;await page.locator('#reload-configuration').click();await page.waitForFunction(()=>!state.configurationLoading);
   check(width,'read failure preserves expanded evidence with an error',await group('runtime').getAttribute('open')!==null&&await page.locator('#configuration-sections').getByText('运行参数暂时不可用：configuration unavailable',{exact:true}).isVisible(),'not fake new success');
   failRead=false;await page.locator('#reload-configuration').click();await page.waitForFunction(()=>!state.configurationLoading);
   check(width,'whole page never overflows horizontally',await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1),width);
   await group('consensus').locator('summary').click();
   const scroll=group('consensus').locator('.configuration-table-scroll');
   check(width,'expanded table scroll stays inside the group',await scroll.evaluate(n=>n.scrollWidth>=n.clientWidth&&getComputedStyle(n).overflowX==='auto'),'table readable on narrow viewport');
   await page.screenshot({path:path.join(out,'configuration-'+width+'.png'),fullPage:true});
   await page.reload();await page.locator('#settings-configuration-tab').click();await page.locator('#configuration-source-summary').getByText('配置文件已读取',{exact:true}).waitFor();await page.waitForFunction(()=>!state.configurationLoading);
   check(width,'full page reload restores collapsed default',await page.locator('details.configuration-section[open]').count()===0,'session-only view preference');
   await group('runtime').locator('summary').click();await page.evaluate(()=>clearSessionData());
   check(width,'session cleanup clears expansion cache',await page.evaluate(()=>state.configurationExpandedSections.size===0),'no cross-account preference');
   check(width,'session cleanup removes previous parameter groups',await page.locator('#configuration-sections details').count()===0,'no old toggle can restore state');
   check(width,'no browser exceptions',errors.length===0,errors);
   check(width,'all configuration actions remain read only',fixture.control.mutations===0,fixture.control.mutations);
  }finally{await page.close();fixture.server.closeAllConnections();await new Promise(resolve=>fixture.server.close(resolve));}
 }}finally{await browser.close();}
 const failures=checks.filter(c=>!c.ok);fs.writeFileSync(path.join(out,baseline?'baseline.json':'acceptance.json'),JSON.stringify({baseline,checks,failures},null,2));console.log(checks.length+' assertions, '+failures.length+' failures');if(failures.length)process.exitCode=1;
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1;});
