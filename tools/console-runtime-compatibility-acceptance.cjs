// Actual console and browser clicks, isolated HTTP409 fixture; never a node write.
const assert=require('node:assert/strict');
const {scenarios,SUBMIT_DRIVER}=require('./console-update-hotfix-recovery-acceptance.cjs');
const {runConsoleDriver}=require('./console-cdp-harness.cjs');
const RAW='runtime compatibility floor prohibits this update target: original target is unknown';
const ZH='已保存的策略需要兼容保护，目标程序能力不足或无法验证，已阻止升级或回退。';
async function main(){
 let total=0;
 for(const test of [{width:1440,marked:true},{width:390,marked:true},{width:1440,marked:false},{width:1440,marked:true,committed:true}]){
  const {width,marked,committed=false}=test;const definite=marked&&!committed;
  const {fixture,mock}=scenarios.find(x=>x.name==='a hotfix that succeeded').make();
  const before=JSON.stringify(mock.snapshot),original=fixture.control.hook;
  fixture.control.hook=async args=>{
   if(args.req.method==='POST' && args.url.pathname.endsWith('/rollback')){
    let body='';for await(const chunk of args.req)body+=chunk;
    mock.actions.push({patchID:args.url.pathname.split('/')[5],action:'rollback',body:JSON.parse(body)});
    return {status:409,message:RAW,result:marked?{runtime_compatibility_rejected:true}:{},committed};
   }
   return original(args);
  };
  const driver=SUBMIT_DRIVER('rollback-software-update').replace("mark('confirmation-submitted');",`
    mark('confirmation-submitted');
    read.settled=await until(()=>!state.softwareUpdateRunning && text('software-update-confirmation-state').includes(${JSON.stringify(ZH)}),5000);
    read.zh=text('software-update-confirmation-state');
    read.uncertain=!!state.softwareUpdateSubmission;
    read.raw=state.softwareUpdateConfirmationMessage;
    byId('language-select').value='en-US';byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));await sleep(100);
    read.en=text('software-update-confirmation-state');
    byId('language-select').value='zh-CN';byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));await sleep(100);
    read.back=text('software-update-confirmation-state');
  `);
  const run=await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:900},profilePrefix:'cg-floor-'});
  assert(run,'browser NOT RUN');assert.deepEqual(run.pageErrors,[]);const page=run.outcome;
  const checks={
   'no page errors':!page.pageErrors?.length,
   'confirmation opened':page.opened===true,
   'refusal settled':page.settled===true,
   ...(definite?{
    'Chinese refusal':page.zh===ZH,
    'English refusal':page.en.includes('incompatible or unverified')&&!/[\u3400-\u9fff]/.test(page.en),
    'switch back':page.back===ZH,
    'definite refusal, not uncertain':page.uncertain===false,
    'raw diagnosis retained':page.raw===RAW,
   }:{'unmarked or committed conflict remains uncertain':page.uncertain===true}),
   'one explicit rollback request':mock.actions.filter(x=>x.action==='rollback').length===1,
   'typed confirmation bound to request':mock.actions.find(x=>x.action==='rollback')?.body.confirmation===page.phrase,
   'persisted fixture job unchanged':JSON.stringify(mock.snapshot)===before,
  };
  for(const [name,ok] of Object.entries(checks)){total++;console.log(`${ok?'PASS':'FAIL'} ${width}px marked=${marked} committed=${committed} ${name}`);assert(ok,`${name}: ${JSON.stringify(page)}`)}
 }
 console.log(`${total} assertions passed; isolated browser/API only, no execution`);
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1});
