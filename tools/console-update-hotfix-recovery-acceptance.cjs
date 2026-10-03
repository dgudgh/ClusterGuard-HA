// Acceptance: a hotfix must never be offered a resume, and every action must name the
// record the console describes.
//
// On 2026-09-30 at 13:30 (UTC+8) the site pressed 续跑 on a failed hotfix record while a
// newer, already-successful one sat above it in the list. Three defects lined up:
//
//   1. The console derived the button's enabled state from the newest record that could
//      still be acted on, but posted to packages[0] - the newest row overall. So the
//      screen said "resuming HF-2026-0929-04" while the request went to
//      HF-2026-0929-05's /resume. The rollback button had the same defect and a worse
//      one: it could revert a package nobody selected.
//   2. The job wrapper that turns a mode into updater arguments never looked at the
//      package kind, so a hotfix was handed --resume like any rolling upgrade - an
//      entry point that can only ever fail for a hotfix, because applying one is
//      idempotent and the supported recovery is to re-run the same patch.
//   3. The manager accepted the resume, wrote the job file, and the helper recorded the
//      refusal as the patch's own outcome - overwriting a run that had applied and
//      verified the payload on all three nodes. The console then showed 升级失败 while
//      the disk said otherwise.
//
// The console half of that cannot be checked by a string contract: whether the resume
// button is hidden, what the execute button is called, which package the identity grid
// names, and which id the confirmation asks the operator to type are all decided while
// the page runs. So this drives the real console HTML from tools/console-ui-fixture.cjs
// over the DevTools protocol and reads the outcome off the DOM, and it reads the id the
// page actually posted off the fixture's request log.
//
//   node tools/console-update-hotfix-recovery-acceptance.cjs
//
// The fixture never reaches a database, an upgrade executor or a node: the read-only
// plan is answered locally, every other action is recorded and refused. The cases that
// only read the panel assert that the plan was the only request made at all; the cases
// that submit assert that the plan and the one action they described were the only ones,
// and that the action carried the id the confirmation asked the operator to type. Set
// CHROME_BIN to pick a browser; without one the script reports NOT RUN rather than
// passing a machine that cannot run it.

const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const { runConsoleDriver } = require('./console-cdp-harness.cjs');

// The records below are copied field for field out of the site's
// /var/lib/clusterguard/updates on 2026-09-30, so the regression reproduces what the
// console was actually reading rather than an invented list. HF-2026-0929-05 was
// uploaded at 01:29Z on 09-30, HF-2026-0929-04 at 08:22Z on 09-29, so the successful
// record sorts first and the failed, still-actionable one sits below it - the exact
// ordering that made the operator's click land somewhere else.
const HOTFIX_DONE = 'HF-2026-0929-05';
const HOTFIX_FAILED = 'HF-2026-0929-04';
const ROLLING = 'cgupgrade-2.2-104-to-2.2-105-x86_64';

const HOTFIX_DONE_STAMP = '2026-09-30T01:29:19.47289955Z';
const HOTFIX_DONE_FINISHED = '2026-09-30T01:30:59Z';
const HOTFIX_FAILED_STAMP = '2026-09-29T08:22:08.240764113Z';
const HOTFIX_FAILED_STARTED = '2026-09-29T08:22:35Z';
const ROLLING_STAMP = '2026-09-29T05:16:35.838450034Z';
const ROLLING_FAILED_AT = '2026-09-29T05:20:11Z';

// What the updater wrote on 09-30 for the refused resume attempt. It is quoted here
// because it is what a pre-fix record still contains, and the console has to explain
// such a record rather than repeat it as the patch's verdict.
const RESUME_REFUSAL = '升级任务失败或被阻断：热修补丁不支持 --resume：应用本身是幂等的，直接重新执行同一个补丁即可';

const hotfixDone = () => ({
  package: {
    patch_id: HOTFIX_DONE, file_name: `clusterguard-ha-hotfix-${HOTFIX_DONE}-2.2-105.x86_64.cgpatch`,
    kind: 'hotfix', source_version: '2.2-105', target_version: `2.2-105+hf-2026-0929-05`,
    size_bytes: 96054, signature_verified: true, rolling: true, rollback_available: true,
    database_mutation: false, uploaded_at: HOTFIX_DONE_STAMP,
  },
  job: {
    patch_id: HOTFIX_DONE, mode: 'execute', status: 'succeeded', node: '192.168.102.154',
    message: 'all node digests and maintenance release verified',
    started_at: HOTFIX_DONE_STAMP, updated_at: HOTFIX_DONE_FINISHED, finished_at: HOTFIX_DONE_FINISHED,
    maintenance_active: false, automatic_failover_available: true,
    progress: { phase: 'completed', total: 3, current: 3, percent: 100 },
  },
});

const hotfixFailed = () => ({
  package: {
    patch_id: HOTFIX_FAILED, file_name: `clusterguard-ha-hotfix-${HOTFIX_FAILED}-2.2-105.x86_64.cgpatch`,
    kind: 'hotfix', source_version: '2.2-105', target_version: `2.2-105+hf-2026-0929-04`,
    size_bytes: 8692199, signature_verified: true, rolling: true, rollback_available: true,
    database_mutation: false, uploaded_at: HOTFIX_FAILED_STAMP,
  },
  job: {
    patch_id: HOTFIX_FAILED, mode: 'execute', status: 'failed', node: '192.168.102.154',
    message: 'automatic rollback incomplete; maintenance gate retained',
    started_at: HOTFIX_FAILED_STARTED, updated_at: HOTFIX_FAILED_STARTED, finished_at: null,
    maintenance_active: true, automatic_failover_available: false,
    progress: { phase: 'failed', total: 3, current: 0, percent: 0 },
  },
});

// A record left behind by the refused resume: its event chain is a complete success
// while its status says failed. The console must not read that as the patch's verdict.
const hotfixRefused = () => {
  const record = hotfixDone();
  record.job = {
    patch_id: HOTFIX_DONE, mode: 'resume', status: 'failed', node: null,
    message: RESUME_REFUSAL, started_at: '2026-09-30T05:30:29Z', updated_at: '2026-09-30T05:30:30Z',
    finished_at: '2026-09-30T05:30:30Z', maintenance_active: false, automatic_failover_available: true,
    progress: { phase: 'failed', total: 3, current: 0, percent: 0 },
  };
  return record;
};

const rollingFailed = () => ({
  package: {
    patch_id: ROLLING, file_name: `${ROLLING}.cgupgrade`, kind: 'upgrade',
    source_version: '2.2-104', target_version: '2.2-105', size_bytes: 36704864,
    signature_verified: true, rolling: true, rollback_available: true, database_mutation: false,
    bootstrap_available: true, bootstrap_protocol: 1, uploaded_at: ROLLING_STAMP,
  },
  job: {
    patch_id: ROLLING, mode: 'execute', status: 'failed', node: '192.168.102.152',
    message: '升级任务失败或被阻断；请查看输出和事件记录，确认维护门禁状态后再续跑或回退',
    started_at: ROLLING_STAMP, updated_at: ROLLING_FAILED_AT, finished_at: ROLLING_FAILED_AT,
    maintenance_active: false, automatic_failover_available: true,
    progress: { phase: 'failed', total: 3, current: 1, percent: 33 },
  },
});

const plannedJob = patchID => ({
  patch_id: patchID, mode: 'plan', status: 'planned', message: '只读升级计划已生成',
  started_at: HOTFIX_FAILED_STARTED, updated_at: HOTFIX_FAILED_STARTED, finished_at: null,
  maintenance_active: false, automatic_failover_available: true,
  progress: { phase: 'preparing', total: 3, current: 0, percent: 0 },
});

// `mock.actions` keeps the id each write was aimed at, which is the whole point: the
// assertions below are about the patch_id the page posted, not about which button the
// operator thought they pressed.
const consoleUnderTest = (initial, { maintenanceActive = false } = {}) => {
  const fixture = createConsoleFixture();
  const mock = { actions: [], snapshot: initial.map(item => structuredClone(item)), report: null };
  const findRecord = patchID => mock.snapshot.find(item => item.package.patch_id === patchID);
  const traceMarks = process.env.CG_ACCEPTANCE_TRACE === '1';
  fixture.control.hook = async ({ req, url }) => {
    if (url.pathname === '/api/__acceptance-mark') {
      if (traceMarks) console.log(`  phase: ${url.searchParams.get('label')}`);
      return { result: true };
    }
    if (url.pathname === REPORT_PATH) {
      let body = '';
      for await (const chunk of req) body += chunk;
      try { mock.report = JSON.parse(body || '{}'); } catch (_) { mock.report = null; }
      if (traceMarks) console.log('  driver report received');
      return { result: true };
    }
    if (url.pathname === '/api/v1/platform/version') {
      return { result: { version: '2.2', release: '105', rpm_architecture: 'x86_64', architecture: 'x86_64' } };
    }
    if (url.pathname === '/api/v1/control-plane/status') {
      return { result: {
        mode: 'raft', role: 'leader', ready: true, quorum_confirmed: true,
        voter_count: 3, local_controller_id: 'node-1', leader_id: 'node-1',
        update_maintenance_active: maintenanceActive, uptime_seconds: 72600,
      } };
    }
    if (url.pathname === '/api/v1/platform/updates' && req.method === 'GET') {
      return { result: {
        available: true,
        warning: '系统升级期间无法进行自动切换，请注意关注。',
        maximum_upload_bytes: 536870912,
        packages: mock.snapshot.map(item => structuredClone(item)),
      } };
    }
    if (url.pathname.startsWith('/api/v1/platform/updates/')) {
      const parts = url.pathname.split('/');
      const patchID = decodeURIComponent(parts[5] || '');
      const action = parts[6] || '';
      for await (const _ of req) { /* consume the confirmation payload */ }
      if (!action && req.method === 'GET') {
        const record = findRecord(patchID) || {};
        return { result: { package: structuredClone(record.package || {}), job: structuredClone(record.job || null) } };
      }
      mock.actions.push({ patchID, action });
      if (action === 'plan') {
        const record = findRecord(patchID);
        if (record) record.job = structuredClone(plannedJob(patchID));
        return { status: 202, result: structuredClone(plannedJob(patchID)) };
      }
      // Only the read-only plan is simulated. Anything else is refused locally, so a
      // regression that reaches the executor fails the run instead of passing quietly -
      // while the id it aimed at has already been recorded.
      return { status: 403, message: '本地示例预览不执行数据库或升级操作。' };
    }
    return null;
  };
  return { fixture, mock };
};

// Both drivers run inside one page evaluation, so everything they need travels in as an
// argument - including this shared preamble, which has no inner template literals.
//
// The drivers report their result over the fixture rather than only returning it. Returning
// it is the natural shape, and it is how the three gate-only cases work, but Chrome did not
// deliver the reply for the cases that click through the confirmation: every phase was
// reached, the driver resolved, and the evaluate never answered - which the harness can only
// report as a 120s watchdog, with no detail. Posting the same object means a driver that
// finished is read back even when the protocol reply is lost, and a driver that stalled is
// still caught, by its own budget and by the phases it reported on the way.
const DRIVER_BUDGET_MS = 45000;
const HARNESS_BUDGET_MS = 120000;
const REPORT_PATH = '/api/__acceptance-report';

const PAGE_HELPERS = `
  const BUDGET_MS = ${DRIVER_BUDGET_MS};
  const trace = [];
  // The harness also collects runtime exceptions, but only while it holds a live session.
  // Since the result now travels over the fixture, the failures travel with it: an
  // uncaught error or a rejected promise is one of the things this acceptance is about.
  const pageErrors = [];
  window.addEventListener('error', event => pageErrors.push(String((event && (event.message || event.error)) || 'error')));
  window.addEventListener('unhandledrejection', event => pageErrors.push('unhandled rejection: ' + String((event && event.reason && event.reason.message) || (event && event.reason) || 'unknown')));
  const mark = label => {
    trace.push(label + '@' + (Date.now() % 1000000));
    fetch('/api/__acceptance-mark?label=' + encodeURIComponent(label)).catch(() => {});
  };
  const report = payload => fetch(${JSON.stringify(REPORT_PATH)}, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
  }).catch(() => {});
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const until = async (probe, budget) => {
    const started = Date.now();
    while (Date.now() - started < budget) {
      let ready = false;
      try { ready = probe(); } catch (_) {}
      if (ready) return true;
      await sleep(50);
    }
    return false;
  };
  const byId = id => document.getElementById(id);
  const text = id => (byId(id) ? byId(id).textContent.trim() : '<missing ' + id + '>');
  const settingsVisible = () => {
    const view = document.querySelector('[data-view="settings"]');
    return !!view && !view.hidden;
  };
  const diagnose = () => [
    'view=' + settingsVisible(),
    'section=' + state.settingsSection,
    'admin=' + canAdministerPlatform(),
    'available=' + !!(state.softwareUpdates && state.softwareUpdates.available),
    'tabHidden=' + (byId('software-update-tab') || {}).hidden,
    'panelHidden=' + (byId('software-update-panel') || {}).hidden,
    'openerDisabled=' + (byId('open-software-update-dialog') || {}).disabled,
    'dialogOpen=' + (byId('software-update-dialog') ? byId('software-update-dialog').open : 'missing'),
  ].join(' ');
  const openUpgradeDialog = async () => {
    if (!await until(() => !!document.querySelector('[data-nav="settings"]'), 20000)) {
      throw new Error('the console never finished booting; ' + diagnose());
    }
    document.querySelector('[data-nav="settings"]').click();
    if (!await until(settingsVisible, 20000)) {
      throw new Error('the settings view never became visible; ' + diagnose());
    }
    const deadline = Date.now() + 10000;
    for (;;) {
      const tab = byId('software-update-tab');
      if (tab && !tab.hidden) tab.click();
      if (byId('software-update-panel') && byId('software-update-panel').hidden === false) break;
      if (Date.now() > deadline) throw new Error('the version-update panel never became visible; ' + diagnose());
      await sleep(100);
    }
    const opened = Date.now() + 15000;
    for (;;) {
      const opener = byId('open-software-update-dialog');
      if (opener && !opener.disabled) opener.click();
      if (byId('software-update-dialog') && byId('software-update-dialog').open) break;
      if (Date.now() > opened) throw new Error('the upgrade dialog never opened; ' + diagnose());
      await sleep(100);
    }
  };
  const readButtons = () => ({
    execute: { text: text('execute-software-update'), disabled: byId('execute-software-update').disabled },
    resume: { hidden: byId('resume-software-update').hidden, disabled: byId('resume-software-update').disabled },
    rollback: { hidden: byId('rollback-software-update').hidden, disabled: byId('rollback-software-update').disabled },
    identity: text('software-update-patch-id'),
    history: [...document.querySelectorAll('#software-update-history tr')].map(row => ({
      patchID: (row.children[1] || {}).textContent ? row.children[1].textContent.trim() : '',
      mode: (row.children[3] || {}).textContent ? row.children[3].textContent.trim() : '',
      message: (row.children[5] || {}).textContent ? row.children[5].textContent.trim() : '',
    })),
  });
  const subject = () => {
    const pending = (state.softwareUpdates && state.softwareUpdates.packages || []).filter(softwareUpdateActionable)[0];
    return pending || (state.softwareUpdates && state.softwareUpdates.packages || [])[0] || null;
  };
  const typeConfirmationAndSubmit = async () => {
    const phrase = text('software-update-confirmation-phrase');
    mark('confirm:phrase');
    const input = byId('software-update-confirmation-input');
    if (!input) throw new Error('the confirmation has no input to type the package id into');
    input.value = phrase;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    mark('confirm:typed');
    await sleep(150);
    // The click is the last thing the driver does, and it does not wait for the page to
    // settle afterwards. A click dispatches its request synchronously, so the fixture has
    // recorded what the page aimed at before this returns - and what it aimed at is the whole
    // question. Waiting here added one more protocol round trip after an interaction, and
    // that round trip is the one Chrome stopped answering.
    byId('confirm-software-update-action').click();
    mark('confirm:clicked');
    return phrase;
  };
`;

// Reads the three action gates for whatever the list currently holds. No click, no
// request: this is the half of the incident that is visible before anyone acts.
const GATES_DRIVER = `(async () => {${PAGE_HELPERS}
  const work = (async () => {
    await openUpgradeDialog();
    mark('dialog-open');
    const read = readButtons();
    mark('gates-read');
    read.subject = (() => {
      const item = subject();
      return item && item.package ? item.package.patch_id : null;
    })();
    read.state = {
      validation: byId('software-update-validation').dataset.state,
      targetLabel: text('software-update-target-label'),
      targetValue: text('software-update-latest-target'),
    };
    return read;
  })().catch(error => ({ __error: String((error && error.message) || error) }));
  const outcome = await Promise.race([work, new Promise(resolve => setTimeout(() => resolve('__budget__'), BUDGET_MS))]);
  const result = { failures: [], trace, pageErrors };
  if (outcome === '__budget__') {
    result.budgetExceeded = true;
    result.failures.push('the panel never settled; last phase ' + (trace[trace.length - 1] || 'none'));
  } else if (outcome.__error) {
    result.error = outcome.__error;
    result.failures.push('driver aborted: ' + outcome.__error);
  } else {
    Object.assign(result, outcome);
  }
  report(result);
  return result;
})()`;

// Presses an action button, waits for the confirmation the console opens by itself,
// types the id it asks for, and submits. The id the page posts is read off the fixture.
const SUBMIT_DRIVER = buttonID => `(async () => {${PAGE_HELPERS}
  const work = (async () => {
    const read = {};
    await openUpgradeDialog();
    mark('dialog-open');
    Object.assign(read, readButtons());
    read.subject = (() => {
      const item = subject();
      return item && item.package ? item.package.patch_id : null;
    })();
    mark('gates-read');
    byId(${JSON.stringify(buttonID)}).click();
    mark('action-clicked');
    read.opened = await until(() => byId('software-update-confirmation-dialog').open, 30000);
    mark(read.opened ? 'confirmation-opened' : 'confirmation-missing');
    read.title = text('software-update-confirmation-title');
    read.subtitle = text('software-update-confirmation-subtitle');
    read.confirmLabel = text('confirm-software-update-action');
    read.phrase = text('software-update-confirmation-phrase');
    if (!read.opened) return read;
    read.submitted = await typeConfirmationAndSubmit();
    mark('confirmation-submitted');
    return read;
  })().catch(error => ({ __error: String((error && error.message) || error) }));
  const outcome = await Promise.race([work, new Promise(resolve => setTimeout(() => resolve('__budget__'), BUDGET_MS))]);
  const result = { failures: [], trace, pageErrors };
  if (outcome === '__budget__') {
    result.budgetExceeded = true;
    result.failures.push('the action never completed; last phase reached: ' + (trace[trace.length - 1] || 'none'));
  } else if (outcome.__error) {
    result.error = outcome.__error;
    result.failures.push('driver aborted: ' + outcome.__error);
  } else {
    Object.assign(result, outcome);
    if (!result.opened) result.failures.push('the confirmation never opened');
  }
  report(result);
  return result;
})()`;

const scenarios = [
  {
    name: 'installed hotfix survives a later failed plan',
    make: () => {
      const item = hotfixDone();
      item.job = {...item.job, mode:'plan', status:'failed', deployment_state:'installed', message:'helper unavailable'};
      return consoleUnderTest([item]);
    },
    driver:GATES_DRIVER,
  },

  {
    // A: a hotfix that succeeded. There is nothing to continue and nothing to re-run.
    name: 'a hotfix that succeeded',
    make: () => consoleUnderTest([hotfixDone()]),
    driver: GATES_DRIVER,
  },
  {
    // B: a hotfix that failed. The recovery the updater supports is re-running the same
    // patch, so that is what the console must offer - and never a resume.
    name: 'a hotfix that failed',
    make: () => consoleUnderTest([hotfixFailed()]),
    driver: GATES_DRIVER,
  },
  {
    // C: a rolling upgrade that failed. A node-by-node upgrade that stopped part-way
    // leaves nodes on two versions, so resume is the only way forward and stays.
    name: 'a rolling upgrade that failed',
    make: () => consoleUnderTest([rollingFailed()]),
    driver: GATES_DRIVER,
  },
  {
    // B': the record the leader still holds for HF-2026-0929-05: the event chain is a
    // complete success, and the status is a refusal that was written over it. It reads as
    // failed, so it stays actionable - and a hotfix is recovered by re-running it. What
    // must never come back is 续跑, which is the only thing this over written record
    // could otherwise invite.
    name: 'a hotfix whose resume was refused',
    make: () => consoleUnderTest([hotfixRefused()]),
    driver: GATES_DRIVER,
  },
  {
    // B+: the same failed hotfix, with the operator actually re-running it. The id the
    // page posts has to be the id it showed.
    name: 're-running a failed hotfix',
    make: () => consoleUnderTest([hotfixFailed()]),
    driver: SUBMIT_DRIVER('execute-software-update'),
    awaitAction: true,
    expectAction: 'retry',
  },
  {
    // A later success does not prove it superseded this failed package. Without an
    // explicit relationship, the older failed row remains the subject.
    name: 'unrelated newer success does not retire failed hotfix',
    make: () => consoleUnderTest([hotfixDone(), hotfixFailed()]),
    driver: GATES_DRIVER,
  },
  {
    // D: the 13:30 list. The successful record sorts first, the failed, still-actionable
    // one below it. The active gate keeps that failed record actionable, so the action
    // belongs to the record the panel describes.
    name: 'acting below a newer successful record',
    make: () => consoleUnderTest([hotfixDone(), hotfixFailed()], { maintenanceActive: true }),
    driver: SUBMIT_DRIVER('execute-software-update'),
    awaitAction: true,
    expectAction: 'retry',
  },
  {
    // E: the rollback half of the same list. A rollback aimed at packages[0] would
    // revert a package nobody selected.
    name: 'rolling back below a newer successful record',
    make: () => consoleUnderTest([hotfixDone(), hotfixFailed()], { maintenanceActive: true }),
    driver: SUBMIT_DRIVER('rollback-software-update'),
    awaitAction: true,
    expectAction: 'rollback',
  },
];

const failures = [];
const record = (name, passed, detail = '') => {
  console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
  if (!passed) failures.push(name);
};

const actionIDs = mock => mock.actions.filter(entry => entry.action !== 'plan').map(entry => entry.patchID);

// A click dispatches its request synchronously, but the fixture answers it asynchronously, so
// the log is read once a write has landed rather than the instant the driver returns.
const waitForAction = async (mock, budgetMs = 15000) => {
  const started = Date.now();
  while (Date.now() - started < budgetMs) {
    if (mock.actions.some(entry => entry.action !== 'plan')) return true;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  return false;
};

const main = async () => {
  const only = process.argv.find(argument => argument.startsWith('--only='));
  const selected = only ? scenarios.filter(scenario => scenario.name.includes(only.slice('--only='.length))) : scenarios;
  for (const scenario of selected) {
    console.log(`\n=== ${scenario.name} ===`);
    const { fixture, mock } = scenario.make();
    let run = null;
    let protocolNote = '';
    try {
      run = await runConsoleDriver({
        fixture, driver: scenario.driver,
        hash: '#settings', profilePrefix: 'cg-update-recovery-', timeoutMs: HARNESS_BUDGET_MS,
      });
    } catch (error) {
      // The cases that click through the confirmation reached every phase and then Chrome
      // never delivered the evaluate reply. The result was reported over the fixture, so it
      // is read from there; what the reply would also have carried is noted, not assumed.
      protocolNote = `the protocol reply was not delivered (${error.message}); the driver's reported result is used`;
    }
    if (!run && !mock.report) {
      const detail = protocolNote || 'no usable browser, so this acceptance did not run.';
      console.log(`\nNOT RUN: ${detail}`);
      record(`${scenario.name}: the driver reported a result`, false, detail);
      continue;
    }
    const page = mock.report || (run && run.outcome) || {};
    const pageErrors = [...new Set([...((run && run.pageErrors) || []), ...(page.pageErrors || [])])];
    if (scenario.awaitAction) await waitForAction(mock);
    if (protocolNote) console.log(`  note: ${protocolNote}`);
    for (const line of pageErrors) console.log(`  page error: ${line}`);
    console.log(`  trace=${(page.trace || []).join(' -> ')}`);
    console.log(`  subject=${page.subject} execute=${JSON.stringify(page.execute)} resume=${JSON.stringify(page.resume)} rollback=${JSON.stringify(page.rollback)}`);
    console.log(`  actions=${JSON.stringify(mock.actions)}`);
    if (page.phrase) console.log(`  confirmation=${JSON.stringify({ title: page.title, phrase: page.phrase })}`);
    for (const line of page.failures || []) console.log(`  driver: ${line}`);

    record(`${scenario.name}: the console raised no runtime exception`,
      pageErrors.length === 0, pageErrors.join(' | '));
    record(`${scenario.name}: the driver completed`, !page.error, page.error || '');
    record(`${scenario.name}: the driver stayed inside its budget`, !page.budgetExceeded,
      (page.failures || []).join(' | '));

    // The gate-only cases stop at the panel, so the plan is the only request they may
    // make. The cases that click through the confirmation do reach the executor - that is
    // what they are for - and what they must not reach is anything besides the plan and
    // the single action the confirmation described.
    if (scenario.awaitAction) {
      const writes = mock.actions.filter(entry => entry.action !== 'plan');
      const aimed = actionIDs(mock);
      record(`${scenario.name}: only the described ${scenario.expectAction} reached the executor`,
        writes.length === 1 && writes[0].action === scenario.expectAction && aimed[0] === page.phrase,
        JSON.stringify(mock.actions));
    } else {
      record(`${scenario.name}: nothing reached the upgrade executor`,
        mock.actions.every(entry => entry.action === 'plan'),
        actionIDs(mock).join(', ') || '(no write actions)');
    }


    if (scenario.name === 'installed hotfix survives a later failed plan') {
      record(`${scenario.name}: no retry of an installed deployment`,page.execute?.disabled === true,JSON.stringify(page.execute));
      record(`${scenario.name}: no resume of an installed hotfix`,page.resume?.hidden === true,JSON.stringify(page.resume));
      record(`${scenario.name}: identity remains explicit`,page.identity === HOTFIX_DONE,page.identity);
    }
    if (scenario.name === 'a hotfix that succeeded') {
      record(`${scenario.name}: 续跑 is not offered`, page.resume && page.resume.hidden === true,
        `hidden=${page.resume && page.resume.hidden}`);
      record(`${scenario.name}: the execute button is not a re-run either`,
        page.execute && page.execute.text === '滚动升级' && page.execute.disabled === true,
        `${page.execute && page.execute.text} disabled=${page.execute && page.execute.disabled}`);
      record(`${scenario.name}: the panel describes the succeeded hotfix`,
        page.identity === HOTFIX_DONE, page.identity);
    }

    if (scenario.name === 'a hotfix that failed') {
      record(`${scenario.name}: 续跑 is not offered`, page.resume && page.resume.hidden === true,
        `hidden=${page.resume && page.resume.hidden}`);
      record(`${scenario.name}: the execute button becomes 重新执行`,
        page.execute && page.execute.text === '重新执行',
        `${page.execute && page.execute.text}`);
      record(`${scenario.name}: the re-run can actually be pressed`,
        page.execute && page.execute.disabled === false,
        `disabled=${page.execute && page.execute.disabled}`);
      record(`${scenario.name}: the panel describes the failed hotfix`,
        page.identity === HOTFIX_FAILED, page.identity);
      record(`${scenario.name}: rollback is offered as the alternative`,
        page.rollback && page.rollback.hidden === false,
        `hidden=${page.rollback && page.rollback.hidden}`);
    }

    if (scenario.name === 'unrelated newer success does not retire failed hotfix') {
      record(`${scenario.name}: the failed attempt remains in history`,
        page.history && page.history.some(row => row.patchID === HOTFIX_FAILED),
        (page.history || []).map(row => row.patchID).join(', '));
      record(`${scenario.name}: both hotfix history rows name the hotfix action`,
        page.history && [HOTFIX_DONE, HOTFIX_FAILED].every(patchID =>
          page.history.some(row => row.patchID === patchID && row.mode === '热修应用')),
        (page.history || []).map(row => `${row.patchID}:${row.mode}`).join(', '));
      record(`${scenario.name}: the failed patch remains the subject`,
        page.identity === HOTFIX_FAILED && page.subject === HOTFIX_FAILED,
        `${page.identity} / ${page.subject}`);
      record(`${scenario.name}: the failed patch may be retried only by explicit action`,
        page.execute && page.execute.text === '重新执行' && page.resume && page.resume.hidden === true,
        JSON.stringify({ execute:page.execute, resume:page.resume }));
    }

    if (scenario.name === 'a hotfix whose resume was refused') {
      // The refused attempt did not apply anything, but its record is what the leader
      // shows, so the panel has to explain it as a failed run of this patch rather than
      // as a patch that can be continued.
      record(`${scenario.name}: 续跑 is not offered`, page.resume && page.resume.hidden === true,
        `hidden=${page.resume && page.resume.hidden}`);
      record(`${scenario.name}: the recovery offered instead is 重新执行`,
        page.execute && page.execute.text === '重新执行' && page.execute.disabled === false,
        `${page.execute && page.execute.text} disabled=${page.execute && page.execute.disabled}`);
      record(`${scenario.name}: the panel still describes that patch`,
        page.identity === HOTFIX_DONE, page.identity);
    }

    if (scenario.name === 'a rolling upgrade that failed') {
      record(`${scenario.name}: history keeps the rolling action name`,
        page.history && page.history.some(row => row.patchID === ROLLING && row.mode === '滚动升级'),
        (page.history || []).map(row => `${row.patchID}:${row.mode}`).join(', '));
      record(`${scenario.name}: 续跑 is offered`, page.resume && page.resume.hidden === false,
        `hidden=${page.resume && page.resume.hidden}`);
      record(`${scenario.name}: 续跑 can be pressed`,
        page.resume && page.resume.disabled === false, `disabled=${page.resume && page.resume.disabled}`);
      record(`${scenario.name}: the execute button stays a rolling upgrade`,
        page.execute && page.execute.text === '滚动升级', `${page.execute && page.execute.text}`);
    }

    if (scenario.name === 're-running a failed hotfix') {
      record(`${scenario.name}: the confirmation names the re-run, not a rolling upgrade`,
        page.opened === true && page.title === '确认重新执行',
        `${page.title} opened=${page.opened}`);
      record(`${scenario.name}: the confirmation asks for the patched id`,
        page.phrase === HOTFIX_FAILED, page.phrase);
      record(`${scenario.name}: the page posted the patch it described`,
        actionIDs(mock).length === 1 && actionIDs(mock)[0] === HOTFIX_FAILED,
        JSON.stringify(mock.actions));
    }

    if (scenario.name === 'acting below a newer successful record') {
      record(`${scenario.name}: the successful record still sorts first`,
        page.history && page.history[0] && page.history[0].patchID === HOTFIX_DONE,
        (page.history || []).map(row => row.patchID).join(', '));
      record(`${scenario.name}: the panel describes the failed record, not the newest row`,
        page.identity === HOTFIX_FAILED, page.identity);
      record(`${scenario.name}: the action was aimed at that record and nothing else`,
        actionIDs(mock).length === 1 && actionIDs(mock)[0] === HOTFIX_FAILED,
        JSON.stringify(actionIDs(mock)));
      record(`${scenario.name}: the newer successful record was never acted on`,
        !actionIDs(mock).includes(HOTFIX_DONE), JSON.stringify(actionIDs(mock)));
    }

    if (scenario.name === 'rolling back below a newer successful record') {
      record(`${scenario.name}: the panel describes the failed record, not the newest row`,
        page.identity === HOTFIX_FAILED, page.identity);
      record(`${scenario.name}: the confirmation asks for the failed record's id`,
        page.phrase === HOTFIX_FAILED, page.phrase);
      record(`${scenario.name}: the rollback was aimed at that record and nothing else`,
        actionIDs(mock).length === 1 && actionIDs(mock)[0] === HOTFIX_FAILED,
        JSON.stringify(actionIDs(mock)));
      record(`${scenario.name}: the newer successful record was never reverted`,
        !actionIDs(mock).includes(HOTFIX_DONE), JSON.stringify(actionIDs(mock)));
    }
  }

  console.log('');
  if (failures.length) {
    console.log(`${failures.length} check(s) failed:`);
    for (const name of failures) console.log(`  - ${name}`);
    process.exitCode = 1;
    return;
  }
  console.log('All update hotfix-recovery checks passed.');
};

if (require.main === module) {
  main().catch(error => {
    console.error(error);
    process.exitCode = 1;
  });
}

module.exports = { GATES_DRIVER, SUBMIT_DRIVER, scenarios };
