// Real page, isolated three-node observations; no updater or production writes.
const {scenarios} = require('./console-update-hotfix-recovery-acceptance.cjs');
const {runConsoleDriver} = require('./console-cdp-harness.cjs');
async function main(){
 let failures=0,count=0;
 for(const width of [1440,390]){
  const {fixture,mock}=scenarios.find(s=>s.name==='a hotfix that succeeded').make();
  const base=structuredClone(mock.snapshot[0]);
  const row=(id,from)=>{const r=structuredClone(base);r.package.patch_id=id;r.package.patch_version=id;r.package.source_version='2.2-105';r.job.patch_id=id;r.job.operation_id='operation-'+id;
   if(from){r.job.from_version=from;r.job.to_version=id;r.job.from_node_versions=['one','two','three'].map(node=>({node,version:from}));}return r;};
  const legacy=row('3.1.1.5');
  const records={uniform:[row('3.1.1.8','3.1.1.7'),legacy],legacy:[row('3.1.1.7'),legacy],mixed:[row('3.1.1.8','3.1.1.7')],missing:[row('3.1.1.8','3.1.1.7')],rollback:[row('3.1.1.8','3.1.1.8')]};
  records.mixed[0].job.from_version='';records.mixed[0].job.from_node_versions[1].version='3.1.1.8';
  records.missing[0].job.from_node_versions[1].version='';
  records.restored=structuredClone(records.rollback);records.restored[0].job.mode='execute';records.restored[0].job.status='rolled_back';records.restored[0].job.to_version='3.1.1.7';
  records.rollback[0].job.mode='rollback';records.rollback[0].job.status='rolled_back';records.rollback[0].job.to_version='';
  mock.snapshot=structuredClone(records.uniform);
  const originalHook=fixture.control.hook;fixture.control.hook=async args=>{
   if(args.url.pathname==='/api/__version/case'){mock.snapshot=structuredClone(records[args.url.searchParams.get('key')]);return {result:true};}
   return originalHook(args);
  };
  const driver=`(async()=>{
   const out=[];const check=(name,ok,detail)=>out.push({name,ok,detail});
   const byId=id=>document.getElementById(id);const sleep=ms=>new Promise(r=>setTimeout(r,ms));
   for(let i=0;i<300&&!canAdministerPlatform();i++)await sleep(25);if(!canAdministerPlatform())throw Error('admin not ready');
   const expectations={restored:['3.1.1.8 → 3.1.1.7','3.1.1.8 → 3.1.1.7'],uniform:['3.1.1.7 → 3.1.1.8','3.1.1.7 → 3.1.1.8'],legacy:['执行前版本未记录 → 3.1.1.7','Pre-operation version not recorded → 3.1.1.7'],mixed:['节点版本不一致 → 3.1.1.8','Mixed node versions → 3.1.1.8'],missing:['执行前版本未记录 → 3.1.1.8','Pre-operation version not recorded → 3.1.1.8'],rollback:['3.1.1.8 → 回退目标未记录','3.1.1.8 → Rollback target not recorded']};
   for(const [key,expected] of Object.entries(expectations)){
    await fetch('/api/__version/case?key='+key);await loadSoftwareUpdates(false);await sleep(75);
    const raw=JSON.stringify(state.softwareUpdates.packages);
    for(const [index,lang] of ['zh-CN','en-US'].entries()){
     byId('language-select').value=lang;byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));
     const cell=document.querySelector('#software-update-history tr td:nth-child(3)');
     check(key+' '+lang+' operation transition',cell.querySelector('div').textContent===expected[index],cell.textContent);
     check(key+' '+lang+' baseline separately identified',cell.querySelector('small').textContent===(index?'RPM baseline: 2.2-105':'RPM基线：2.2-105'),cell.textContent);
     check(key+' '+lang+' progress agrees with history',byId('software-update-progress-version').textContent===expected[index],byId('software-update-progress-version').textContent);
     check(key+' '+lang+' does not rewrite API evidence',JSON.stringify(state.softwareUpdates.packages)===raw,'original records preserved');
     check(key+' '+lang+' not baseline as transition',!cell.querySelector('div').textContent.includes('2.2-105 →'),'RPM eligibility is independent');
    }
    if(key==='legacy')check('does not infer from previous successful row',!document.querySelector('#software-update-history tr td:nth-child(3) div').textContent.startsWith('3.1.1.5'),'previous row is not evidence');
   }
   return out;
  })()`;
  const run=await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:900},profilePrefix:'cg-version-transition-'});
  if(!run)throw Error('browser NOT RUN');if(run.pageErrors.length)throw Error(run.pageErrors.join('\n'));
  for(const c of run.outcome){count++;console.log((c.ok?'PASS':'FAIL')+' '+width+'px '+c.name+': '+c.detail);if(!c.ok)failures++;}
  if(mock.actions.length)throw Error('version read reached action');
 }
 console.log(count+' browser assertions, '+failures+' failures; no execution.');if(failures)process.exitCode=1;
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1;});
