// Uses the real console and API fixture; every write action remains forbidden.
const {scenarios} = require('./console-update-hotfix-recovery-acceptance.cjs');
const {runConsoleDriver} = require('./console-cdp-harness.cjs');

const SUCCESS = 'all node digests and maintenance release verified';
const SUCCESS_ZH = '热修补丁完成，全部节点与控制面已验证，维护门禁已释放';
const FAILURE = 'automatic rollback incomplete; maintenance gate retained';
const FAILURE_ZH = '自动回退未完成，维护门禁仍保留，请核验节点状态';
const STAGING = 'staging the signed hotfix package';
const STAGING_ZH = '正在向控制节点传输已签名补丁';
const UNKNOWN = 'unexpected helper diagnostic <script>window.__unsafe = true</script> code=E42';

async function main() {
  let failures=0, count=0;
  for (const width of [1440,390]) {
    const {fixture,mock}=scenarios.find(s=>s.name==='a hotfix that succeeded').make();
    const base=structuredClone(mock.snapshot[0]);
    const record=(id,message,status='succeeded')=>{
      const item=structuredClone(base);item.package.patch_id=id;item.package.patch_version=id.startsWith('3.')?id:'3.1.1.3';
      item.job.patch_id=id;item.job.message=message;item.job.status=status;
      item.job.events=[{patch_id:id,status,message,updated_at:'2026-10-09T01:31:15Z'}];
      item.job.output_tail=[message];
      return item;
    };
    const records={
      success:[record('3.1.1.5',SUCCESS),record('3.1.1.4',SUCCESS_ZH),record('HF-2026-1008-04',SUCCESS),record('HF-2026-1008-02',SUCCESS)],
      failure:[record('3.1.1.5',FAILURE,'failed')],
      running:[record('3.1.1.5',STAGING,'running')],
      unknown:[record('3.1.1.5',UNKNOWN,'failed')],
    };
    records.running[0].job.progress={phase:'staging',current:1,total:3,percent:38};
    records.running[0].job.finished_at=null;
    let key='success';mock.snapshot=structuredClone(records[key]);
    const originalHook=fixture.control.hook;
    fixture.control.hook=async args=>{
      if(args.url.pathname==='/api/__message/case'){
        key=args.url.searchParams.get('key');mock.snapshot=structuredClone(records[key]);return {result:true};
      }
      return originalHook(args);
    };
    const driver=`(async()=>{
      const checks=[];const check=(name,ok,detail)=>checks.push({name,ok,detail});
      const byId=id=>document.getElementById(id);const sleep=ms=>new Promise(r=>setTimeout(r,ms));
      for(let i=0;i<300&&!canAdministerPlatform();i++)await sleep(25);
      if(!canAdministerPlatform())throw Error('admin not ready');
      document.querySelector('[data-nav="settings"]').click();
      for(let i=0;i<150&&byId('software-update-tab').hidden;i++)await sleep(25);
      byId('software-update-tab').click();
      const inspect=async(key,raw,zh)=>{
        await fetch('/api/__message/case?key='+key);await loadSoftwareUpdates(false);await sleep(75);
        const cell=document.querySelector('#software-update-history tr .history-message');
        const summary=byId('software-update-job-message');
        check(key+' history summary',cell?.querySelector('.software-update-message-summary')?.textContent===zh||cell?.textContent===zh,cell?.textContent);
        check(key+' current explanation',summary.querySelector('.software-update-message-summary')?.textContent===zh||summary.textContent===zh,summary.textContent);
        for(const id of ['software-update-events','software-update-progress-event-list']){
          check(key+' '+id+' translation',byId(id).textContent.includes(zh),byId(id).textContent);
        }
        check(key+' source job unchanged',state.softwareUpdates.packages[0].job.message===raw,state.softwareUpdates.packages[0].job.message);
        check(key+' raw output preserved',byId('software-update-output').textContent===raw,byId('software-update-output').textContent);
      };
      await inspect('success',${JSON.stringify(SUCCESS)},${JSON.stringify(SUCCESS_ZH)});
      check('all four historical rows use the same Chinese result',[...document.querySelectorAll('#software-update-history .history-message')].every(c=>c.textContent===${JSON.stringify(SUCCESS_ZH)}),[...document.querySelectorAll('#software-update-history .history-message')].map(c=>c.textContent));
      await inspect('failure',${JSON.stringify(FAILURE)},${JSON.stringify(FAILURE_ZH)});
      check('failure remains failure',byId('software-update-job-status').textContent==='升级失败',byId('software-update-job-status').textContent);
      await inspect('running',${JSON.stringify(STAGING)},${JSON.stringify(STAGING_ZH)});
      await fetch('/api/__message/case?key=unknown');await loadSoftwareUpdates(false);await sleep(75);
      const cell=document.querySelector('#software-update-history tr .history-message');
      check('unknown has honest Chinese guidance',cell.querySelector('.software-update-message-summary')?.textContent.includes('原始信息'),cell.textContent);
      const detail=cell.querySelector('details');if(detail)detail.querySelector('summary').click();
      check('unknown raw diagnosis is expandable and unchanged',detail?.open&&detail.querySelector('pre').textContent===${JSON.stringify(UNKNOWN)},detail?.textContent);
      check('raw diagnosis cannot execute HTML',!window.__unsafe&&!cell.querySelector('script'),'literal text only');
      check('unknown is not described as successful',byId('software-update-job-status').textContent==='升级失败',byId('software-update-job-status').textContent);
      if(byId('software-update-progress-dialog').open)byId('software-update-progress-dialog').close();
      await fetch('/api/__message/case?key=success');await loadSoftwareUpdates(false);
      byId('language-select').value='en-US';byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));
      check('language switch refreshes results',document.querySelector('#software-update-history .history-message').textContent===${JSON.stringify(SUCCESS)},document.querySelector('#software-update-history .history-message').textContent);
      byId('language-select').value='zh-CN';byId('language-select').dispatchEvent(new Event('change',{bubbles:true}));
      check('switching back restores Chinese',document.querySelector('#software-update-history .history-message').textContent===${JSON.stringify(SUCCESS_ZH)},document.querySelector('#software-update-history .history-message').textContent);
      return checks;
    })()`;
    const run=await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:900},profilePrefix:'cg-update-language-'});
    if(!run)throw Error('browser NOT RUN');
    if(run.pageErrors.length)throw Error(run.pageErrors.join('\n'));
    for(const c of run.outcome){count++;console.log(`${c.ok?'PASS':'FAIL'} ${width}px ${c.name}: ${JSON.stringify(c.detail)}`);if(!c.ok)failures++;}
    if(mock.actions.length)throw Error('read-only message test reached an action');
  }
  console.log(`${count} language assertions, ${failures} failures; no execution.`);
  if(failures)process.exitCode=1;
}
if(require.main===module)main().catch(e=>{console.error(e);process.exitCode=1;});
