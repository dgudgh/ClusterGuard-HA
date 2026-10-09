// Real Chrome + persisted-job Manager projection. No executor or production node.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const { runConsoleDriver } = require('./console-cdp-harness.cjs');

async function main() {
  let records;
  const supplied = process.argv.indexOf('--progress-fixture');
  if (supplied >= 0) records = JSON.parse(fs.readFileSync(process.argv[supplied + 1]));
  else {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-staging-progress-'));
    try {
      const file = path.join(directory, 'progress.json');
      execFileSync(process.env.GO_BIN || 'go', ['test', './internal/platformupdate', '-run', '^TestHotfixStagingProgressAcrossThreeNodes$', '-count=1'], {
        cwd:path.resolve(__dirname, '..'), env:{...process.env, CG_STAGING_PROGRESS_FIXTURE:file}, stdio:'pipe',
      });
      records = JSON.parse(fs.readFileSync(file));
    } finally { fs.rmSync(directory, {recursive:true, force:true}); }
  }
  const expected = [8,15,15,38,38,38,61,61,61,85,92,100];
  if (records.length !== expected.length) throw Error('incomplete Manager progression');
  let failures = 0, checks = 0;
  for (const width of [1440,390]) {
    const fixture = createConsoleFixture();
    let index = 0;
    const retry = structuredClone(records[0]);
    retry.job.operation_id = 'new-retry-operation';
    retry.job.mode = 'retry'; retry.job.status = 'queued';
    retry.job.started_at = retry.job.updated_at = '2026-10-09T00:10:00Z';
    retry.job.events = [];
    retry.job.progress = {phase:'queued', current:0, total:3, percent:3};
    fixture.control.hook = async ({url,req}) => {
      if (url.pathname === '/api/__progress/step') {
        index = Number(url.searchParams.get('index'));
        return {result:{index}};
      }
      const item = records[index] || retry;
      if (url.pathname === '/api/v1/platform/updates') return {result:{available:true,packages:[item]}};
      if (url.pathname === '/api/v1/control-plane/status') return {result:{mode:'raft',role:'leader',ready:true,quorum_confirmed:true,voter_count:3,update_maintenance_active:item.job.status !== 'succeeded'}};
      if (req.method !== 'GET') throw Error('unexpected mutation ' + url.pathname);
      return null;
    };
    const driver = `(async () => {
      const results = [];
      const expected = ${JSON.stringify(expected)};
      const records = ${JSON.stringify(records.map(r=>({phase:r.job.progress.phase,current:r.job.progress.current,node:r.job.node})))};
      const byId = id => document.getElementById(id);
      const sleep = ms => new Promise(resolve => setTimeout(resolve,ms));
      for (let n=0; n<200 && !canAdministerPlatform(); n++) await sleep(25);
      if (!canAdministerPlatform()) throw Error('admin never loaded');
      const check = (name,ok,detail) => results.push({name,ok,detail});
      for (let i=0;i<expected.length;i++) {
        await fetch('/api/__progress/step?index='+i);
        await loadSoftwareUpdates(false);
        // Ancillary control-plane read can render again; check its settled result too.
        await sleep(75);
        const actual = byId('software-update-progress-percent').textContent.trim();
        check('step '+i+' '+records[i].phase+' percent',actual===expected[i]+'%',actual);
        check('step '+i+' node count',byId('software-update-progress-nodes').textContent.trim()===records[i].current+' / 3',byId('software-update-progress-nodes').textContent);
        if (records[i].phase==='staging') {
          check('staging '+i+' label',byId('software-update-progress-label').textContent.includes('传输'),byId('software-update-progress-label').textContent);
          check('staging '+i+' remains node stage',document.querySelector('[data-update-progress-stage="nodes"]').classList.contains('active'),document.querySelector('.software-update-stage.active')?.dataset.updateProgressStage);
          check('staging '+i+' accessible percentage',byId('software-update-progress-track').getAttribute('aria-valuenow')===String(expected[i]),byId('software-update-progress-track').outerHTML);
        }
      }
      await fetch('/api/__progress/step?index='+expected.length);
      await loadSoftwareUpdates(false); await sleep(75);
      check('new operation resets rather than inherits 100%',byId('software-update-progress-percent').textContent.trim()==='3%',byId('software-update-progress-percent').textContent);
      check('new operation returns to preparation',document.querySelector('[data-update-progress-stage="prepare"]').classList.contains('active'),'new retry');
      return results;
    })()`;
    const run = await runConsoleDriver({fixture,driver,hash:'#settings',viewport:{width,height:900},profilePrefix:'cg-progress-'});
    if (!run) throw Error('browser NOT RUN');
    for (const item of run.outcome) {
      checks++;
      console.log(`${item.ok?'PASS':'FAIL'} ${width}px ${item.name}: ${item.detail}`);
      if (!item.ok) failures++;
    }
    if (run.pageErrors.length) throw Error(run.pageErrors.join('\n'));
  }
  console.log(`${checks} browser assertions, ${failures} failures; actual Manager projection, no execution.`);
  if (failures) process.exitCode = 1;
}
if (require.main === module) main().catch(error=>{console.error(error);process.exitCode=1;});
