// Acceptance: after 「上传并校验」 the 滚动升级 button must act on the package the
// operator just uploaded, and must never aim at a record this cluster cannot run.
//
// The site hit both halves of this on 2026-09-29. Manager.Snapshot orders packages
// by the stored uploaded_at descending, and HF-2026-0928-06 carries a timestamp
// written while the node clock ran ~5h39m ahead, so it stayed at packages[0] after
// cgupgrade-2.2-104-to-2.2-105-x86_64 was uploaded. The console resolved its subject
// from packages[0] alone, found a finished job there, and left the button disabled
// with a green 「校验完成」 above it: the operator had verified a package the page
// would not let them run, and nothing on screen said why.
//
// The 14:02 incident that same day is the second half. A hotfix built for 2.2-105 was
// uploaded to a cluster still running 2.2-104; the upload verified, took the only
// action slot, and its plan failed. Because a failed job stays actionable so a
// transient failure can be retried, and because the dialog has no package picker, the
// button could only ever aim at the one package this cluster can never apply - while
// cgupgrade-2.2-104-to-2.2-105-x86_64, the package that should have run, was listed
// underneath it and unreachable. The server now marks such a record incompatible and
// the console must both keep it out of the candidates and say which release line it
// belongs to.
//
// None of that is visible to a string contract. The disabled attribute, the copy
// that explains it, which package the identity grid describes, and which id the
// confirmation dialog asks the operator to type are all decided at runtime. So this
// drives the real console HTML from tools/console-ui-fixture.cjs over the DevTools
// protocol - through the console's own change listener, multipart upload, refresh
// and read-only plan request - and reads the outcome off the DOM.
//
//   node tools/console-update-pending-acceptance.cjs
//
// The fixture never reaches a database, an upgrade executor or a node: uploads and
// plans are answered locally and counted, and the run asserts that no execution
// request was ever sent. Set CHROME_BIN to pick a browser; without one the script
// reports SKIP rather than failing a machine that cannot run it.

const { createConsoleFixture } = require('./console-ui-fixture.cjs');
const { runConsoleDriver } = require('./console-cdp-harness.cjs');

const FINISHED_ID = 'HF-2026-0928-06';
const PACKAGE_ID = 'cgupgrade-2.2-104-to-2.2-105-x86_64';

// Both timestamps are copied verbatim out of the site's /var/lib/clusterguard/updates:
// the finished record carries what the ahead clock wrote, the upgrade package the
// stamp of the upload that came hours later on the wall clock.
const FINISHED_STAMP = '2026-09-29T11:01:17.712051173Z';
const PACKAGE_STAMP = '2026-09-29T05:16:35.838450034Z';

const SKEW_MARKER = '记录时间戳晚于当前时间，排序与时间不可信';

const finishedRecord = () => ({
  package: {
    patch_id: FINISHED_ID, file_name: `${FINISHED_ID}.cgpatch`, kind: 'hotfix',
    source_version: '2.2-104', target_version: '2.2-104', size_bytes: 8958572,
    signature_verified: true, rolling: false, rollback_available: true, database_mutation: false,
    uploaded_at: FINISHED_STAMP,
  },
  // What Manager.Snapshot computes on the node: uploaded_at is more than five
  // minutes ahead of this node's clock, so the row's time - and the order it
  // produced - cannot be trusted.
  clock_skew: true,
  job: {
    patch_id: FINISHED_ID, mode: 'execute', status: 'succeeded', node: '192.168.102.154',
    message: '升级包已通过签名与兼容性校验', started_at: FINISHED_STAMP, updated_at: FINISHED_STAMP,
    finished_at: FINISHED_STAMP, maintenance_active: false, automatic_failover_available: true,
    progress: { phase:'completed', total:3, current:3, percent:100 },
  },
});

const packageRecord = () => ({
  package: {
    patch_id: PACKAGE_ID, file_name: `${PACKAGE_ID}.cgupgrade`, kind: 'upgrade',
    source_version: '2.2-104', target_version: '2.2-105', size_bytes: 36704864,
    signature_verified: true, rolling: true, rollback_available: true, database_mutation: false,
    bootstrap_available: true, bootstrap_protocol: 1, uploaded_at: PACKAGE_STAMP,
  },
});

const plannedJob = () => ({
  patch_id: PACKAGE_ID, mode: 'plan', status: 'planned', message: '只读升级计划已生成',
  started_at: PACKAGE_STAMP, updated_at: PACKAGE_STAMP, maintenance_active: false,
  automatic_failover_available: true, progress: { phase:'preparing', total:3, current:0, percent:0 },
});

const FOREIGN_ID = 'HF-2026-0929-04';
const FOREIGN_TARGET = '2.2-105+hf-2026-0929-04';

// Copied verbatim out of the retired record on the site (and from the archive this
// repository built at the time), so the fixture reproduces the 14:02 sequence rather
// than an invented one.
const FOREIGN_STAMP = '2026-09-29T06:02:27.864633761Z';
const FOREIGN_FAILED_AT = '2026-09-29T06:02:32Z';

// The server's own refusal, word for word as platformupdate.packageBaselineFault
// renders it for a 2.2-105 hotfix on a 2.2-104 cluster. The console must show this
// rather than re-deriving an answer of its own.
const FOREIGN_REASON = '热修补丁属于 2.2-105 发布线，本集群运行 2.2-104；请先完成到 2.2-105 的滚动升级再上传。';

// The 14:02 incident: a hotfix built for the release line this cluster is not on,
// uploaded successfully because a valid signature was all the channel asked for.
// Its plan then failed - and, before this fix, the failure never released the
// console's only action slot, so the package that should have run stayed
// unreachable behind it.
const foreignRecord = () => ({
  package: {
    patch_id: FOREIGN_ID, file_name: `clusterguard-ha-hotfix-${FOREIGN_ID}-2.2-105.x86_64.cgpatch`,
    kind: 'hotfix', source_version: '2.2-105', target_version: FOREIGN_TARGET, size_bytes: 8639457,
    signature_verified: true, rolling: true, rollback_available: true, database_mutation: false,
    uploaded_at: FOREIGN_STAMP,
  },
  // What Manager.Snapshot computes on the node: the server decides this once and
  // sends it with the row, so the console never has to re-derive it.
  incompatible: true,
  incompatible_reason: FOREIGN_REASON,
  job: {
    patch_id: FOREIGN_ID, mode: 'plan', status: 'failed',
    // Exactly what the job wrapper wrote on the site: a fixed sentence naming the
    // maintenance gate, which the fourth fix in this release line replaces with the
    // updater's own reason. This value is kept as it stands because it is what the
    // record actually contains, and it is not what the assertions below care about -
    // they care that the console stops aiming at this package at all.
    message: '升级任务失败或被阻断；请查看输出和事件记录，确认维护门禁状态后再续跑或回退',
    started_at: FOREIGN_STAMP, updated_at: FOREIGN_FAILED_AT, finished_at: FOREIGN_FAILED_AT,
    maintenance_active: false, automatic_failover_available: true,
    progress: { phase:'failed', total:3, current:0, percent:0 },
  },
});

// One console per run. `mock.snapshot` is re-read on every request and `afterUpload`
// decides what the list looks like once the upload has landed, which is the sequence
// that produced the disabled button.
const consoleUnderTest = ({ initial = [], upload = null, afterUpload = () => {} }) => {
  const fixture = createConsoleFixture();
  // Not .map(structuredClone): map passes the index as the second argument, which
  // structuredClone reads as its options dictionary.
  const mock = { uploads: 0, actions: [], snapshot: initial.map(item => structuredClone(item)) };
  fixture.control.hook = async ({ req, url }) => {
    if (url.pathname === '/api/v1/platform/version') {
      return { result: { version: '2.2', release: '104', rpm_architecture: 'x86_64', architecture: 'x86_64' } };
    }
    if (url.pathname === '/api/v1/platform/updates' && req.method === 'POST') {
      for await (const _ of req) { /* consume the simulated package upload */ }
      mock.uploads += 1;
      afterUpload(mock);
      return { status: 201, result: structuredClone((upload || packageRecord()).package) };
    }
    if (url.pathname === '/api/v1/platform/updates') {
      return { result: {
        available: true,
        warning: '系统升级期间无法进行自动切换，请注意关注。',
        maximum_upload_bytes: 536870912,
        packages: mock.snapshot.map(item => structuredClone(item)),
      } };
    }
    if (url.pathname.startsWith(`/api/v1/platform/updates/${PACKAGE_ID}/`)) {
      const action = url.pathname.split('/').pop();
      for await (const _ of req) { /* consume the confirmation payload */ }
      mock.actions.push(action);
      // Only the read-only plan is simulated. Anything else is refused locally, so a
      // regression that reaches the executor fails the run instead of passing quietly.
      return action === 'plan'
        ? { status: 202, result: structuredClone(plannedJob()) }
        : { status: 403, message: '本地示例预览不执行数据库或升级操作。' };
    }
    if (url.pathname === `/api/v1/platform/updates/${PACKAGE_ID}` && req.method === 'GET') {
      return { result: { package: structuredClone(packageRecord().package), job: structuredClone(plannedJob()) } };
    }
    return null;
  };
  return { fixture, mock };
};

// Both drivers run inside one page evaluation, so everything they need travels in as
// an argument - including this shared preamble, which has no inner template literals.
const PAGE_HELPERS = `
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
  // Reads the console's own bookkeeping, so a failure says which layer refused
  // instead of only that something never became visible.
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
    // The navigation starts an asynchronous settings load that re-selects the stored
    // section, so a single tab click can be undone by it. Click until the panel the
    // click is supposed to open is the one on screen.
    const deadline = Date.now() + 10000;
    for (;;) {
      const tab = byId('software-update-tab');
      if (tab && !tab.hidden) tab.click();
      if (byId('software-update-panel') && byId('software-update-panel').hidden === false) break;
      if (Date.now() > deadline) throw new Error('the version-update panel never became visible; ' + diagnose());
      await sleep(100);
    }
    // The opener starts disabled until the first snapshot says the channel is available,
    // so a single click can land on a disabled button and be swallowed.
    const opened = Date.now() + 15000;
    for (;;) {
      const opener = byId('open-software-update-dialog');
      if (opener && !opener.disabled) opener.click();
      if (byId('software-update-dialog') && byId('software-update-dialog').open) break;
      if (Date.now() > opened) throw new Error('the upgrade dialog never opened; ' + diagnose());
      await sleep(100);
    }
  };
`;

const UPLOAD_DRIVER = ({ file }) => `(async () => {${PAGE_HELPERS}
  const result = { steps: [], failures: [] };

  try {
    await openUpgradeDialog();
    result.before = {
      validation: byId('software-update-validation').dataset.state,
      disabled: byId('execute-software-update').disabled,
      patchID: text('software-update-patch-id'),
      rows: document.querySelectorAll('#software-update-history tr').length,
    };

    // Upload through the console's own handler: the picker's change listener, the
    // multipart POST, then the refresh that re-reads the package list.
    const input = byId('software-update-package-file');
    const transfer = new DataTransfer();
    transfer.items.add(new File([new Uint8Array(2048)], ${JSON.stringify(file)}, { type:'application/octet-stream' }));
    input.files = transfer.files;
    input.dispatchEvent(new Event('change', { bubbles:true }));
    byId('upload-software-update').click();
    if (!await until(() => !state.softwareUpdateRunning && state.softwareUpdateValidationState !== 'validating', 20000)) {
      throw new Error('the upload never finished validating');
    }

    result.validation = {
      state: byId('software-update-validation').dataset.state,
      title: text('software-update-validation-title'),
      detail: text('software-update-validation-detail'),
    };
    result.button = { disabled: byId('execute-software-update').disabled };
    result.target = { label: text('software-update-target-label'), value: text('software-update-latest-target') };
    result.identity = {
      patchID: text('software-update-patch-id'),
      targetVersion: text('software-update-target-version'),
      rolling: text('software-update-rolling'),
    };
    result.pageKind = text('software-update-package-kind');
    result.history = [...document.querySelectorAll('#software-update-history tr')].map(row => ({
      status: (row.children[0] || {}).textContent ? row.children[0].textContent.trim() : '',
      patchID: (row.children[1] || {}).textContent ? row.children[1].textContent.trim() : '',
      message: (row.children[5] || {}).textContent ? row.children[5].textContent.trim() : '',
    }));
    return result;
  } catch (error) {
    result.error = String((error && error.message) || error);
    result.steps.push('BAD  driver aborted -> ' + result.error);
    result.failures.push('driver aborted: ' + result.error);
    return result;
  }
})()`;

// What the operator presses next has to name the package they chose, not the record
// sitting on top of the list.
const PLAN_DRIVER = patchID => `(async () => {${PAGE_HELPERS}
  const result = { steps: [], failures: [] };
  try {
    await openUpgradeDialog();
    result.buttonDisabled = byId('execute-software-update').disabled;
    byId('execute-software-update').click();
    result.opened = await until(() => byId('software-update-confirmation-dialog').open, 20000);
    result.phrase = byId('software-update-confirmation-phrase').textContent.trim();
    result.subtitle = byId('software-update-confirmation-subtitle').textContent.trim();
    byId('cancel-software-update-confirmation').click();
    result.dismissed = await until(() => !byId('software-update-confirmation-dialog').open, 8000);
    if (!result.opened) result.failures.push('the confirmation never opened');
    if (result.phrase !== ${JSON.stringify(patchID)}) result.failures.push('the confirmation named ' + result.phrase);
    if (!result.dismissed) result.failures.push('the confirmation could not be dismissed');
    return result;
  } catch (error) {
    result.error = String((error && error.message) || error);
    result.steps.push('BAD  driver aborted -> ' + result.error);
    result.failures.push('driver aborted: ' + result.error);
    return result;
  }
})()`;


const FILE_NAME = `${PACKAGE_ID}.cgupgrade`;

const scenarios = [
  {
    kind: 'fresh-upload',
    name: 'a fresh upload below a future-stamped finished record',
    // Before the upload only the finished record exists; the upload is what adds the
    // package - exactly the 13:16 sequence.
    make: () => consoleUnderTest({
      initial: [finishedRecord()],
      afterUpload: mock => { mock.snapshot = [finishedRecord(), packageRecord()]; },
    }),
    // Re-runs against the state the operator is left in, to press 滚动升级.
    makeAfterUpload: () => consoleUnderTest({ initial: [finishedRecord(), packageRecord()] }),
  },
  {
    kind: 'no-actionable',
    name: 'an upload the list has no actionable record for',
    // The server answers the upload with the record it already holds - what a second
    // upload of a finished package looks like from the console's side - and the list
    // keeps returning only that finished record.
    make: () => consoleUnderTest({ initial: [finishedRecord()], upload: finishedRecord() }),
  },
  {
    kind: 'ordinary',
    name: 'the ordinary upload with no stale record in the way',
    make: () => consoleUnderTest({
      initial: [],
      afterUpload: mock => { mock.snapshot = [packageRecord()]; },
    }),
  },
  {
    kind: 'foreign-above',
    name: 'a foreign-baseline package sorting above the upgrade that should run',
    // The 14:02 list exactly: the 2.2-105 hotfix was uploaded at 06:02Z, the rolling
    // package at 05:16Z, so the record the console can never run sorts first. Before
    // this fix that was the only target the dialog had.
    make: () => consoleUnderTest({ initial: [foreignRecord(), packageRecord()] }),
    makeAfterUpload: () => consoleUnderTest({ initial: [foreignRecord(), packageRecord()] }),
  },
  {
    kind: 'foreign-only',
    name: 'nothing but a foreign-baseline package in the list',
    // No actionable record at all, and the one on top belongs to another release
    // line. The panel must say so instead of calling it a finished record.
    make: () => consoleUnderTest({ initial: [foreignRecord()] }),
  },
];

const failures = [];
const record = (name, passed, detail = '') => {
  console.log(`${passed ? 'PASS' : 'FAIL'}  ${name}${detail ? ` - ${detail}` : ''}`);
  if (!passed) failures.push(name);
};

const main = async () => {
  for (const scenario of scenarios) {
    console.log(`\n=== ${scenario.name} ===`);
    const { fixture, mock } = scenario.make();
    const run = await runConsoleDriver({
      fixture, driver: UPLOAD_DRIVER({ file: FILE_NAME }),
      hash: '#settings', profilePrefix: 'cg-update-pending-', timeoutMs: 120000,
    });
    if (!run) {
      console.log('\nSKIPPED: no usable browser, so this acceptance did not run.');
      return;
    }
    const page = run.outcome || {};
    for (const line of page.steps || []) console.log(`  ${line}`);
    for (const line of run.pageErrors || []) console.log(`  page error: ${line}`);
    console.log(`  before: ${JSON.stringify(page.before)}`);

    record(`${scenario.name}: the console raised no runtime exception`,
      (run.pageErrors || []).length === 0, (run.pageErrors || []).join(' | '));
    record(`${scenario.name}: exactly one package was uploaded`, mock.uploads === 1, `${mock.uploads} upload(s)`);
    record(`${scenario.name}: nothing reached the upgrade executor`,
      mock.actions.every(action => action === 'plan'), mock.actions.join(', ') || '(no actions)');
    record(`${scenario.name}: the driver completed`, !page.error, page.error || '');

    // A record the cluster can never run must not be the row the operator is sent to.
    // Both lines have to appear in the marker: naming the package's line without the
    // running one leaves the operator without the step that comes first.
    const namesBothLines = value => (value || '').includes('2.2-105') && (value || '').includes('2.2-104');

    if (scenario.kind === 'fresh-upload') {
      record(`${scenario.name}: 滚动升级 is enabled with the finished record on top`,
        page.button && page.button.disabled === false, `disabled=${page.button && page.button.disabled}`);
      record(`${scenario.name}: the row above the fresh upload is flagged as future-stamped`,
        page.history && page.history[0] && page.history[0].patchID === FINISHED_ID
          && (page.history[0].message || '').includes(SKEW_MARKER),
        `${(page.history || [])[0] && page.history[0].patchID} :: ${(page.history || [])[0] && page.history[0].message}`);
      record(`${scenario.name}: the fresh upload is not flagged`,
        page.history && page.history[1] && !(page.history[1].message || '').includes(SKEW_MARKER),
        `${(page.history || [])[1] && page.history[1].patchID} :: ${(page.history || [])[1] && page.history[1].message}`);
      record(`${scenario.name}: the identity grid describes the uploaded package`,
        page.identity && page.identity.patchID === PACKAGE_ID && page.identity.targetVersion === '2.2-105',
        `${(page.identity || {}).patchID} -> ${(page.identity || {}).targetVersion}`);
      record(`${scenario.name}: the summary calls the uploaded package the pending target`,
        page.target && page.target.label === '待升级目标版本' && page.target.value === '2.2-105 · 已上传',
        `${(page.target || {}).label} :: ${(page.target || {}).value}`);
    } else if (scenario.kind === 'no-actionable') {
      record(`${scenario.name}: the panel says the upload verified but nothing can run`,
        page.validation && page.validation.state === 'verified-blocked'
          && page.validation.title === '校验完成，但当前没有可执行的升级包',
        `${(page.validation || {}).state} :: ${(page.validation || {}).title}`);
      record(`${scenario.name}: the panel names the record occupying the list`,
        ((page.validation || {}).detail || '').includes(FINISHED_ID)
          && ((page.validation || {}).detail || '').includes('不能重复发起'),
        (page.validation || {}).detail);
      record(`${scenario.name}: 滚动升级 stays disabled instead of acting on a finished record`,
        page.button && page.button.disabled === true, `disabled=${page.button && page.button.disabled}`);
      record(`${scenario.name}: the summary calls the finished record the last completed version`,
        page.target && page.target.label === '最近完成版本' && page.target.value === '2.2-104 · 升级成功',
        `${(page.target || {}).label} :: ${(page.target || {}).value}`);
    } else if (scenario.kind === 'foreign-above') {
      record(`${scenario.name}: the inapplicable record is listed first`,
        page.history && page.history[0] && page.history[0].patchID === FOREIGN_ID,
        `${(page.history || [])[0] && page.history[0].patchID} :: ${(page.history || [])[0] && page.history[0].message}`);
      record(`${scenario.name}: its row says which release line it belongs to and which one this is`,
        namesBothLines((page.history || [])[0] && page.history[0].message),
        (page.history || [])[0] && page.history[0].message);
      record(`${scenario.name}: the package that can actually run carries no such marker`,
        page.history && page.history[1] && !namesBothLines(page.history[1].message),
        `${(page.history || [])[1] && page.history[1].patchID} :: ${(page.history || [])[1] && page.history[1].message}`);
      record(`${scenario.name}: 滚动升级 is enabled despite the foreign record on top`,
        page.button && page.button.disabled === false, `disabled=${page.button && page.button.disabled}`);
      record(`${scenario.name}: the identity grid describes the package that can run, not the one on top`,
        page.identity && page.identity.patchID === PACKAGE_ID && page.identity.targetVersion === '2.2-105',
        `${(page.identity || {}).patchID} -> ${(page.identity || {}).targetVersion}`);
      record(`${scenario.name}: the summary calls the package that can run the pending target`,
        page.target && page.target.label === '待升级目标版本' && page.target.value === '2.2-105 · 已上传',
        `${(page.target || {}).label} :: ${(page.target || {}).value}`);
    } else if (scenario.kind === 'foreign-only') {
      record(`${scenario.name}: the panel says the upload verified but nothing can run`,
        page.validation && page.validation.state === 'verified-blocked'
          && page.validation.title === '校验完成，但当前没有可执行的升级包',
        `${(page.validation || {}).state} :: ${(page.validation || {}).title}`);
      record(`${scenario.name}: the panel blames the release line, not a record that finished`,
        ((page.validation || {}).detail || '').includes(FOREIGN_ID)
          && namesBothLines((page.validation || {}).detail)
          && !((page.validation || {}).detail || '').includes('已经执行完成'),
        (page.validation || {}).detail);
      record(`${scenario.name}: 滚动升级 stays disabled rather than aiming at an inapplicable package`,
        page.button && page.button.disabled === true, `disabled=${page.button && page.button.disabled}`);
      record(`${scenario.name}: the summary does not promise this is the pending target`,
        page.target && page.target.label === '最近处理版本'
          && (page.target.value || '').startsWith(FOREIGN_TARGET),
        `${(page.target || {}).label} :: ${(page.target || {}).value}`);
    } else {
      record(`${scenario.name}: the panel reports a plain verified upload`,
        page.validation && page.validation.state === 'verified' && page.validation.title === '校验完成'
          && ((page.validation || {}).detail || '').includes(PACKAGE_ID),
        `${(page.validation || {}).state} :: ${(page.validation || {}).title} :: ${(page.validation || {}).detail}`);
      record(`${scenario.name}: 滚动升级 is enabled`, page.button && page.button.disabled === false,
        `disabled=${page.button && page.button.disabled}`);
      record(`${scenario.name}: the ordinary path carries no skew marker`,
        (page.history || []).every(row => !(row.message || '').includes(SKEW_MARKER)),
        (page.history || []).map(row => row.message).join(' | '));
      record(`${scenario.name}: exactly the uploaded package is listed`, (page.history || []).length === 1,
        `${(page.history || []).length} row(s)`);
    }

    // Second half of the primary scenario: the button the operator now presses has to
    // name the package they uploaded.
    if (scenario.makeAfterUpload) {
      const follow = scenario.makeAfterUpload();
      const planned = await runConsoleDriver({
        fixture: follow.fixture, driver: PLAN_DRIVER(PACKAGE_ID), hash: '#settings',
        profilePrefix: 'cg-update-pending-', timeoutMs: 120000,
      });
      if (!planned) { console.log('\nSKIPPED: no usable browser, so this acceptance did not run.'); return; }
      const step = planned.outcome || {};
      for (const line of step.steps || []) console.log(`  ${line}`);
      for (const line of planned.pageErrors || []) console.log(`  page error: ${line}`);
      record(`${scenario.name}: 滚动升级 opens the confirmation for the uploaded package`,
        step.buttonDisabled === false && step.opened === true && step.phrase === PACKAGE_ID,
        `disabled=${step.buttonDisabled} opened=${step.opened} phrase=${step.phrase}`);
      record(`${scenario.name}: the confirmation is the rolling-upgrade one and is dismissible`,
        (step.subtitle || '').includes('跟随节点') && step.dismissed === true,
        `${step.subtitle} | dismissed=${step.dismissed}`);
      record(`${scenario.name}: the operator's click sent one read-only plan and nothing more`,
        follow.mock.actions.join(',') === 'plan', follow.mock.actions.join(', ') || '(no actions)');
    }
  }

  if (failures.length) {
    console.error(`\n${failures.length} check(s) failed: ${failures.join(', ')}`);
    process.exitCode = 1;
  } else {
    console.log('\nAll update-pending checks passed.');
  }
};

main().catch(error => {
  console.error(`acceptance failed: ${error.message}`);
  process.exitCode = 1;
});
