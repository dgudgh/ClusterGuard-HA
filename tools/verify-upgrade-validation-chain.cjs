#!/usr/bin/env node
// Checks the supported v2 section 21 contract, executable routing and rollback
// generation, console subject identity, immutable delivery ledger and lock rules.
// This is a local source gate, not proof of artifact or production acceptance.
// Remaining ART/FIELD evidence is tracked in
// docs/zh-CN/upgrade-validation-chain-implementation-status.md and is OPEN;
// --strict refuses the selected stage until its evidence exists (default: field).
// --stage source permits source development without asserting ART/FIELD completion.
// --acceptance-report FILE supplies version-bound artifact/site evidence inventories.
// --contract-only checks loading/schema compatibility before builders write bytes.
// --self-test runs mutations in an isolated copy and includes no-bite controls.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const vm = require('node:vm');
const evidence = require('./upgrade-acceptance-evidence.cjs');

const SUPPORTED_CONTRACT_VERSION = 2;
const CONTRACT_DOC = 'docs/upgrade-validation-chain.md';
const CONTRACT_DOC_ZH = 'docs/zh-CN/upgrade-validation-chain.md';

// Every rule below is a claim section 22 makes. The identifiers are the rule numbers of the
// contract so a failure points at the clause to re-read, not merely at a file.


const repoRoot = (() => {
  const index = process.argv.indexOf('--repo');
  return index >= 0 && process.argv[index + 1] ? path.resolve(process.argv[index + 1]) : path.resolve(__dirname, '..');
})();
const strict = process.argv.includes('--strict');
const selfTest = process.argv.includes('--self-test');
const contractOnly = process.argv.includes('--contract-only');
const argument = name => {
  const index=process.argv.indexOf(name);
  if(index<0) return null;
  if(!process.argv[index+1] || process.argv[index+1].startsWith('--')) throw Error(`${name} requires a value`);
  return process.argv[index+1];
};
let stage, reportFile;
try {
  stage=argument('--stage') || 'field';
  reportFile=argument('--acceptance-report');
  if(!['source','artifact','field'].includes(stage)) throw Error('unknown stage: '+stage);
  if(contractOnly && (reportFile || process.argv.includes('--stage'))) throw Error('--contract-only cannot assert an acceptance stage');
  if(selfTest && (reportFile || process.argv.includes('--stage'))) throw Error('--self-test cannot assert an acceptance stage');
  if(stage==='source' && reportFile) throw Error('source stage does not consume artifact/field evidence');
} catch(error) { console.error(error.message); process.exit(1); }


// The tree the checks read. The self-test points this at the mutated copy; a mutation is only
// meaningful if the checks are reading the tree the mutation was written to.
let checkRoot = repoRoot;

const read = rel => {
  const absolute = path.join(checkRoot, rel);
  try { return fs.readFileSync(absolute, 'utf8'); } catch (_) { return null; }
};

// Parses the one machine-readable block out of the document. Deliberately a small parser for
// the shapes the contract uses - `key: value`, and `key:` followed by one indented level -
// rather than a YAML dependency: the block is the contract's own interface, and a gate that
// needs a package installed to read the rules is a gate that will not be run.
const parseContract = source => {
  if (typeof source !== 'string') return null;
  const marker = '# 21. Machine-Readable Contract';
  if (source.split(marker).length !== 2) return null;
  const section = source.split(marker)[1].split('\n# 22.')[0];
  const blocks = [...section.matchAll(/```yaml\n([\s\S]*?)```/g)];
  if (blocks.length !== 1) return null;
  const schema = JSON.parse(read('internal/updatecontract/schema.json') || 'null');
  if (!schema) return null;
  const values = new Map(), groups = new Set();
  let group = '';
  for (const line of blocks[0][1].split('\n')) {
    if (!line.trim()) continue;
    const match = line.match(/^( *)([a-z_][a-z_0-9]*): *(.*)$/);
    if (!match) return null;
    const [, indent, key, value] = match;
    if (!indent) {
      if (value || groups.has(key) || !Object.keys(schema).some(field => field.startsWith(key+'.'))) return null;
      groups.add(key); group = key;
    } else {
      const field = group+'.'+key;
      if (indent !== '  ' || !group || values.has(field) || schema[field] !== value) return null;
      values.set(field,value);
    }
  }
  return values.size === Object.keys(schema).length ? values : null;
};

// The job wrapper's routing is a pure function for exactly this reason: it can be executed.
// Asserting on its text would pass for a function whose arms were swapped, so the flags it
// actually emits for each (mode, kind) are read off a real run.
const shellFunction = (source, name) => {
  const lines = source.split('\n');
  const start = lines.findIndex(line => line.startsWith(`${name}() {`));
  if (start < 0) return null;
  const end = lines.indexOf('}', start);
  if (end < 0) return null;
  return lines.slice(start, end + 1).join('\n');
};

const runRouting = (mode, kind) => {
  const source = read('scripts/clusterguard-update-job.sh');
  if (!source) return null;
  const fn = shellFunction(source, 'update_mode_arguments');
  if (!fn) return null;
  const script = `${fn}\nupdate_mode_arguments ${JSON.stringify(mode)} ${JSON.stringify(kind)} HF-TEST-01 2>/dev/null\n`;
  try {
    return execFileSync('/bin/bash', ['-c', script], { encoding: 'utf8', timeout: 20000 })
      .split('\n').map(line => line.trim()).filter(Boolean);
  } catch (_) { return null; }
};

const generatedRollback = () => {
  const builder = read('scripts/build-hotfix-patch.sh');
  const renderer = builder?.split("cat >\"${render_js}\" <<'RENDER_JS'\n")[1]?.split('\nRENDER_JS')[0];
  if (!renderer) return null;
  const directory = fs.mkdtempSync(path.join(os.tmpdir(),'cg-rollback-gate-'));
  try {
    fs.writeFileSync(path.join(directory,'render.cjs'),renderer);
    fs.writeFileSync(path.join(directory,'manifest.json'),JSON.stringify({hotfix_id:'HF-GATE',build_commit:'1234567890abcdef',fix_commits:[],source:{version:'2.2',release:'105'},files:[{artifact:'payload/example',install_path:'/tmp/example',mode:'0755',owner:'root',group:'root'}],verification:[]}));
    execFileSync(process.execPath,[path.join(directory,'render.cjs'),path.join(directory,'manifest.json'),'',path.join(directory,'apply.sh'),path.join(directory,'rollback.sh')],{timeout:20000});
    return fs.readFileSync(path.join(directory,'rollback.sh'),'utf8');
  } finally { fs.rmSync(directory,{recursive:true,force:true}); }
};

// The console's whole update decision - "is this payload on the disk", "is this record still
// actionable", "which record may a rollback name" - is a pure block of functions, so it is
// executed here against the shapes the site actually serves. Reading the source would pass
// for a block whose branches were swapped; the 2026-10-08 record is a shape no invented
// fixture would have produced, so it is one of the cases below.
const softwareUpdateDecision = (packages = []) => {
  const source = read('internal/api/console.html');
  if (!source) return null;
  const start = source.indexOf('const softwareUpdateCompletedAttempt =');
  const end = source.indexOf('const softwareUpdateIsHotfix =', start);
  if (start < 0 || end < 0) return null;
  const harness = 'const state={softwareUpdates:{packages:' + JSON.stringify(packages) + '}};'
    + 'const softwareUpdateDateText=()=>\'\';\n'
    + source.slice(start, end)
    + '\n;({payloadApplied:softwareUpdatePayloadApplied,actionable:softwareUpdateActionable,'
    + 'rollbackTarget:softwareUpdateRollbackTarget,outcome:softwareUpdateOutcome})';
  try { return vm.runInNewContext(harness, {}, { timeout: 1000 }); } catch (_) { return null; }
};

// A verified run of an older attempt, overwritten by an attempt that was refused before it
// reached a node: status says failed, the last completed event says the files landed.
const OVERWRITTEN_VERIFIED_RUN = { status:'failed', events:[{status:'running'},{status:'succeeded',updated_at:'2026-10-08T16:58:30Z'}] };
const GENUINE_FAILURE_RUN = { status:'failed', events:[{status:'running'},{status:'failed',updated_at:'2026-10-08T16:58:30Z'}] };

// The guard's second source is a shell function, so it is run rather than matched: the
// difference between a guard that reads operation records and one that only reads deployment
// records is invisible in a diff but decides whether the guard executes at all.
const payloadAppliedVerdict = statusFile => {
  const source = read('scripts/clusterguard-upgrade.sh');
  if (!source) return null;
  const fn = shellFunction(source, 'payload_applied');
  if (!fn) return null;
  const script = fn + '\npayload_applied ' + JSON.stringify(statusFile) + '\n';
  try {
    execFileSync('/bin/bash', ['-c', 'command -v jq >/dev/null'], { timeout: 20000 });
  } catch (_) { return null; }
  try {
    execFileSync('/bin/bash', ['-c', script], { encoding: 'utf8', timeout: 20000 });
    return 0;
  } catch (error) { return typeof error.status === 'number' ? error.status : -1; }
};

const writeOperationTree = (root, name, status, events) => {
  const directory = path.join(root, name);
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, 'status.json'), JSON.stringify({ status }));
  if (events) {
    fs.writeFileSync(path.join(directory, 'events.jsonl'), events.map(event => JSON.stringify(event)).join('\n') + '\n');
  }
  return path.join(directory, 'status.json');
};

const CHECKS = [
  {
    rule:'22 / FIELD-012', title:'console language preference covers settings and keeps raw values intact',
    run:()=>{
      const page=read('internal/api/console.html')||'';
      const start=page.indexOf('    const uiCatalog = '),end=page.indexOf('    const staticUILanguageBindings =',start);
      if(start<0||end<0)return 'missing authored UI catalogue';
      const context={document:{documentElement:{lang:'en-US'}},state:{}};
      const result=vm.runInNewContext(page.slice(start,end)+`\n[ui('账户与安全'),ui('显示偏好'),ui('配置文件：{0}','管理员')];`,context);
      if(result[0]!=='Account and security'||result[1]!=='Display preferences'||result[2]!=='Configuration file: 管理员')return 'English setting or verbatim interpolation broken';
      const change=page.match(/const changeConsoleLanguage = language => \{([\s\S]*?)\n    \};/);
      if(!change||!change[1].includes('renderAccountIdentity(); renderControlPlaneStatus(); renderConfiguration();')||!change[1].includes('renderSoftwareUpdates(); renderSoftwareUpdateConfirmation();'))return 'language change omits a dynamic settings renderer';
      if(!change[1].includes('setSettingsSection(state.settingsSection, false, false)'))return 'locale change can reload settings';
      if(!(read('tools/console-language-acceptance.cjs')||'').includes('user-provided display name untouched'))return 'missing full-page browser regression';
      return true;
    },
  },

  {
    rule: '22 / FIELD-012',
    title: 'update history and events localize messages without rewriting evidence',
    run: () => {
      const page=read('internal/api/console.html') || '';
      const start=page.indexOf('    const softwareUpdateMessageText = message => {');
      if(start<0) return 'missing shared message formatter';
      const source=page.slice(start,page.indexOf('    const renderSoftwareUpdateMessage =',start));
      const result=vm.runInNewContext(`${source}\nsoftwareUpdateMessageText('all node digests and maintenance release verified');`,{state:{language:'zh-CN'}});
      if(result!=='热修补丁完成，全部节点与控制面已验证，维护门禁已释放')return 'verified success lost its Chinese presentation';
      if(!page.includes('renderSoftwareUpdateMessage(messageCell, job && job.message'))return 'history bypasses shared formatter';
      if(!page.includes("renderSoftwareUpdateMessage(byId('software-update-job-message'), job && job.message"))return 'current job bypasses shared formatter';
      if((page.match(/renderSoftwareUpdateMessage\(message, event\.message/g)||[]).length!==2)return 'inline or progress events bypass shared formatter';
      if(!page.includes("text('pre', '', raw)"))return 'unknown diagnosis is no longer retained as literal text';
      if(!(read('tools/console-update-message-language-acceptance.cjs')||'').includes('unknown raw diagnosis is expandable and unchanged'))return 'missing real-browser evidence preservation regression';
      return true;
    },
  },
  {
    rule: '22 / FIELD-012',
    title: 'hotfix staging retains node progress and its console stage',
    run: () => {
      const manager = read('internal/platformupdate/manager.go') || '';
      const start = manager.indexOf('func progressPercent(');
      const body = manager.slice(start, manager.indexOf('\n}\n', start));
      if (!body.includes('case "staging", "updating":')) return 'staging no longer shares the node-step progress calculation';
      const page = read('internal/api/console.html') || '';
      if (!page.includes("['staging', 'updating', 'rollback'].includes(phase) ? 1 : 0")) return 'staging jumps back to preparation in the console';
      if (!page.includes("staging:ui('正在向控制节点传输已签名补丁')")) return 'staging lost its operator label';
      if (!(read('internal/platformupdate/progress_staging_test.go') || '').includes('func TestHotfixStagingProgressAcrossThreeNodes(')) return 'missing persisted three-node progress regression';
      if (!(read('tools/console-update-staging-progress-acceptance.cjs') || '').includes('new operation resets rather than inherits 100%')) return 'missing actual-browser progress/reset regression';
      return true;
    },
  },
  {
    rule:'INV-001 / INV-009',title:'version identity and signed supersession reach the current subject',
    run:()=>{
      const source=read('internal/api/console.html');
      const body=source.match(/const softwareUpdateActionable = item => \{([\s\S]*?)\n    \};/);
      if(!body) return 'actionable resolver missing';
      const actionable=vm.runInNewContext(`item=>{${body[1]}}`,{softwareUpdatePayloadApplied:()=>false});
      if(actionable({superseded_by:'3.1.1.4',job:{status:'failed'}})!==false || actionable({job:{status:'failed'}})!==true) return 'signed retirement lost or unrelated history hidden';
      const manager=read('internal/platformupdate/manager.go');
      if(!manager.includes('successor.Package.Supersedes') || !manager.includes('!manager.successorInstalled(successor.Package.PatchID)') || !manager.includes('!successor.Package.SignatureVerified')) return 'retirement no longer requires signed installed successor';
      const builder=read('scripts/build-hotfix-patch.sh');
      if(!builder.includes('[[ "${hotfix_id}" == "${patch_version}" ]]')) return 'new product ID does not equal signed version';
      return true;
    },
  },

  {
    rule:'11', title:'runtime and history use their own verified product versions',
    run: () => {
      const page=read('internal/api/console.html');
      const display=page?.match(/const platformVersionText = \(\) => \{([\s\S]*?)\n    \};/);
      const target=page?.match(/const softwareUpdateTargetVersion = record => ([^;]+);/);
      if(!display || !target) return 'version display functions missing';
      for(const [product,want] of [['3.1.1.1','3.1.1.1'],['','2.2-105']]) {
        const ctx={state:{platformVersion:{product_version:product,version:'2.2',release:'105'}}};
        if(vm.runInNewContext(`(()=>{${display[1]}})()`,ctx)!==want) return 'runtime version did not come from its own build';
      }
      const version=vm.runInNewContext(`record => ${target[1]}`);
      if(version({patch_version:'3.1.1.3',target_version:'2.2-105+hf'})!=='3.1.1.3' || version({target_version:'2.2-105+hf'})!=='2.2-105+hf') return 'signed target or legacy fallback lost';
      if(!read('scripts/build-hotfix-patch.sh')?.includes('internal/buildinfo.ProductVersion=${patch_version}')) return 'build does not bind product version';
      if(!read('internal/platformupdate/inspector.go')?.includes('patchVersion := values["patch_version"]')) return 'signed inspect version is discarded';
      if(!read('internal/platformupdate/version.go')?.includes('hex.EncodeToString(hash.Sum(nil)) != softwarePackage.SHA256') || !read('internal/platformupdate/version.go')?.includes('!inspected.SignatureVerified')) return 'legacy history enrichment is not verified';
      return true;
    },
  },

  {
    rule:'INV-004',
    title:'deployment result survives a later failed operation',
    run:() => {
      const source = read('internal/api/console.html');
      const start=source.indexOf('const softwareUpdateCompletedAttempt =');
      const end=source.indexOf('const softwareUpdateOutcomeNote =',start);
      if (start < 0 || end < 0) return 'missing outcome evaluator';
      const fn = vm.runInNewContext(source.slice(start,end)+'; softwareUpdateOutcome',{},{timeout:1000});
      if (fn({deployment_state:'installed',status:'failed'}) !== 'applied_attempt_failed') return 'failed attempt overwrites installed result';
      if (fn({deployment_state:'rolled_back',status:'failed',events:[{status:'succeeded'}]}) !== 'failed') return 'stale event overrides authoritative rollback';
      return true;
    },
  },

  {
    rule: '20',
    title: 'the mandatory document is present and parses',
    run: () => {
      const source = read(CONTRACT_DOC);
      if (!source) return `missing ${CONTRACT_DOC}`;
      const contract = parseContract(source);
      if (!contract) return `${CONTRACT_DOC} has no parsable machine-readable block`;
      const version = Number(contract.get('contract.version'));
      if (!Number.isInteger(version)) return 'VALIDATION_CONTRACT_VERSION is not an integer';
      if (version > SUPPORTED_CONTRACT_VERSION) {
        return `the contract is version ${version} and this program supports ${SUPPORTED_CONTRACT_VERSION}: FAIL CLOSED`;
      }
      if (contract.get('contract.mandatory') !== 'true') return 'mandatory is not true';
      if (contract.get('contract.fail_closed') !== 'true') return 'fail_closed is not true';
      return true;
    },
  },
  {
    rule: '20',
    title: 'the Chinese copy exists and states the same contract',
    run: () => {
      const english = read(CONTRACT_DOC);
      const chinese = read(CONTRACT_DOC_ZH);
      if (!chinese) return `missing ${CONTRACT_DOC_ZH}`;
      const left = parseContract(english);
      const right = parseContract(chinese);
      if (!left || !right) return 'one of the two documents has no parsable block';
      if (left.size !== right.size) return `key count differs: ${left.size} vs ${right.size}`;
      for (const [key, value] of left) {
        if (!right.has(key)) return `the Chinese copy is missing ${key}`;
        if (right.get(key) !== value) return `${key} differs: ${value} vs ${right.get(key)}`;
      }
      return true;
    },
  },
  {
    rule: '21',
    title: 'runtime consumers load the same supported contract',
    run: () => {
      if (read('internal/updatecontract/contract.md') !== read(CONTRACT_DOC)) return 'embedded contract differs';
      for (const file of ['internal/platformupdate/manager.go','internal/platformupdate/helper.go']) {
        if (!read(file)?.includes('updatecontract.Validate()')) return file+' does not load the contract';
      }
      return true;
    },
  },
  {
    rule: '3 / 4',
    title: "a hotfix resume is rejected, retry is explicit, and only a rolling upgrade resumes",
    run: () => {
      const hotfix = runRouting('resume', 'hotfix');
      if (hotfix !== null) return 'resume+hotfix was not rejected';
      const retry = runRouting('retry', 'hotfix');
      if (!retry?.includes('--retry') || !retry.includes('--execute') || retry.includes('--resume')) return 'retry+hotfix lost explicit retry identity';
      const upgrade = runRouting('resume', 'upgrade');
      if (!upgrade) return 'update_mode_arguments could not be executed for an upgrade';
      if (!upgrade.includes('--resume')) return `resume+upgrade lost --resume: ${upgrade.join(' ')}`;
      const plan = runRouting('plan', 'hotfix');
      if (!plan || !plan.includes('--plan') || plan.includes('--execute')) return 'plan must stay read-only for both kinds';
      return true;
    },
  },
  {
    rule: '3 / 4',
    title: 'the back end refuses a hotfix resume before anything is written',
    run: () => {
      const manager = read('internal/platformupdate/manager.go');
      if (!manager) return 'missing internal/platformupdate/manager.go';
      const guard = manager.match(/if\s+mode\s*==\s*ModeResume\s*&&[\s\S]{0,120}?PackageKindHotfix[\s\S]{0,200}?ErrResumeUnsupported/);
      if (!guard) return 'the manager no longer refuses a hotfix resume with ErrResumeUnsupported';
      const api = read('internal/api/updates.go');
      if (!api) return 'missing internal/api/updates.go';
      const lines = api.split('\n');
      const at = lines.findIndex(line => line.includes('ErrResumeUnsupported'));
      if (at < 0) return 'the API no longer maps ErrResumeUnsupported at all';
      // The mapping is a `case` listing several errors and then the writeError call, so the
      // status is on the line after the one that names this error - not on the same line.
      const following = lines.slice(at, at + 3).join('\n');
      if (!/writeError\(writer, http\.StatusConflict/.test(following)) {
        return 'ErrResumeUnsupported is no longer answered with a conflict status';
      }
      return true;
    },
  },
  {
    rule: 'INV-005',
    title: 'the maintenance lock is adopted only by the patch that left it',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      if (!source.includes("grep -Fqx '${patch_id}'")) return "current_update_lock_on_host no longer matches on its own patch id";
      return true;
    },
  },
  {
    rule: 'INV-005',
    title: 'a foreign maintenance lock is refused by name',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      const fn = shellFunction(source, 'detect_foreign_update_lock');
      if (!fn) return 'detect_foreign_update_lock is gone';
      if (!fn.includes('die "CG_FOREIGN_UPDATE_LOCK: 维护门禁当前由 ${previous} 持有')) return 'detect_foreign_update_lock no longer refuses the foreign holder';
      if (!/found\s*==\s*\$\{#controllers\[@\]\}/.test(fn)) return 'the foreign check no longer requires every controller to agree';
      const helper = shellFunction(source, 'foreign_update_lock_on_host');
      if (!helper) return 'foreign_update_lock_on_host is gone';
      if (!helper.includes("!= '${patch_id}'")) return 'the foreign helper no longer compares against its own patch id';
      const hotfix = shellFunction(source, 'run_hotfix_update');
      if (!hotfix) return 'run_hotfix_update is gone';
      if (!hotfix.includes('detect_foreign_update_lock')) return 'the hotfix flow no longer consults the foreign check';
      if (!hotfix.includes('if ! ${current_patch_maintenance_active}; then')) {
        return 'the foreign check is no longer conditional on not holding the gate itself';
      }
      return true;
    },
  },
  {
    rule: '4 / 5',
    title: 'the console resolves one subject and describes the action it takes',
    run: () => {
      const page = read('internal/api/console.html');
      if (!page) return 'missing internal/api/console.html';
      const required = [
        ['single subject resolver', 'const softwareUpdateSubject = () => pendingSoftwareUpdate() || latestSoftwareUpdate();'],
        ['confirmation defaulting to the subject', 'patchID = softwareUpdateSubject()?.package?.patch_id'],
        ['rollback and resume using the subject', 'byId(\'resume-software-update\').addEventListener(\'click\', () => openSoftwareUpdateConfirmation(\'resume\'))'],
        ['the retry verdict travelling into the confirmation', "openSoftwareUpdateConfirmation(retry ? 'retry' : 'execute', patchID, retry)"],
        ['the plan step acting on the actionable record', 'const target = pendingSoftwareUpdate();'],
      ];
      for (const [label, needle] of required) {
        if (!page.includes(needle)) return `${label}: ${needle} is gone`;
      }
      if (!page.includes("retry === null ? softwareUpdateIsRetry(softwareUpdateSubject()) : retry")) {
        return 'the confirmation no longer takes the verdict from the action that was chosen';
      }
      return true;
    },
  },
  {
    rule: 'INV-001 / INV-002',
    title: 'no action falls back to an implicit record, and an unknown id fails',
    run: () => {
      const page = read('internal/api/console.html');
      if (!page) return 'missing internal/api/console.html';
      for (const call of page.match(/startSoftwareUpdate\([^)]*\)/g) || []) {
        if (call.includes('latestSoftwareUpdate()')) return `startSoftwareUpdate falls back to the newest row: ${call}`;
      }
      for (const call of page.match(/openSoftwareUpdateConfirmation\([^)]*\)/g) || []) {
        if (call.includes('latestSoftwareUpdate()')) return `the confirmation falls back to the newest row: ${call}`;
      }
      if (!page.includes('const item = (state.softwareUpdates.packages || []).find(candidate => candidate.package.patch_id === patchID);')) {
        return 'startSoftwareUpdate no longer resolves the record by the id it was given';
      }
      if (!page.includes('if (!item) return false;')) return 'startSoftwareUpdate no longer fails for an id that is not in the list';
      return true;
    },
  },
  {
    rule: 'RB-002',
    title: 'the generated rollback never copies over a running binary',
    run: () => {
      const generated = generatedRollback();
      if (!generated) return 'rollback generator unavailable';
      const builder = generated.split('\n').filter(line => !line.trimStart().startsWith('#')).join('\n');
      if (!builder.includes('restore_tmp="$(mktemp')) return 'the generated rollback no longer stages into a temporary file';
      if (!builder.includes('mv -f "${restore_tmp}" "${destination}"')) return 'the generated rollback no longer replaces the file atomically';
      // The failure mode is a copy whose target is the live file. A copy whose *source* is the
      // destination - apply.sh keeping a `.bak` beside the file it replaces - is fine, so the
      // pattern has to be about the last argument, not about the word cp.
      if (/\bcp\b[^\n]*"\$\{destination\}"\s*\)?\s*$/m.test(builder)) {
        return 'a generated step copies over the destination instead of renaming onto it';
      }
      return true;
    },
  },
  {
    rule: 'RB-001',
    title: 'a backup list is bound to the hotfix that wrote it',
    run: () => {
      const builder = read('scripts/build-hotfix-patch.sh');
      if (!builder) return 'missing scripts/build-hotfix-patch.sh';
      if (!builder.includes('backup-${manifest.hotfix_id}-')) {
        return 'the apply step no longer names the backup list after its hotfix id';
      }
      if (!builder.includes('"\\${backup_dir}"/backup-${manifest.hotfix_id}-*.txt')) {
        return 'the rollback no longer globs only its own hotfix id';
      }
      if (/ls -1[^\n]*backup-\*\.txt/.test(builder)) {
        return 'the rollback selects the newest backup regardless of which hotfix wrote it';
      }
      return true;
    },
  },
  {
    rule: 'INV-003 / 2',
    title: 'the ledger keeps every published identity and its revision chain intact',
    run: () => {
      const ledger = read('hotfixes/hotfix-publications.json');
      if (!ledger) return 'missing hotfixes/hotfix-publications.json';
      let parsed;
      try { parsed = JSON.parse(ledger); } catch (error) { return `the ledger does not parse: ${error.message}`; }
      const entries = parsed.publications || parsed;
      if (!Array.isArray(entries) || !entries.length) return 'the ledger holds no publications';
      const bySHA = new Map(entries.map(entry => [entry.sha256, entry]));
      const currentPerDirectory = new Map();
      for (const entry of entries) {
        const revision = entry.revision || 0;
        if (revision === 0) {
          if (entry.supersedes_sha256) return `${entry.file} is revision 0 but claims to replace something`;
        } else {
          if (!entry.file.includes(`-r${revision}-`)) return `${entry.file} is revision ${revision} but its name does not carry it`;
          const previous = bySHA.get(entry.supersedes_sha256);
          if (!previous) return `${entry.file} replaces a digest that is not in the ledger`;
          if ((previous.revision || 0) !== revision - 1) return `${entry.file} must replace the previous revision only`;
          if (previous.superseded_by !== entry.file) return `${entry.file} replaces ${previous.file}, which does not point back`;
        }
        if (entry.status === 'current') {
          const directory = entry.file.split('/').slice(0, 2).join('/');
          currentPerDirectory.set(directory, (currentPerDirectory.get(directory) || 0) + 1);
        }
        if (!/^[0-9a-f]{64}$/.test(entry.sha256 || '')) return `${entry.file} has no sha256`;
      }
      for (const [directory, count] of currentPerDirectory) {
        if (count !== 1) return `${directory} has ${count} current artifacts; a delivery directory holds exactly one`;
      }
      return true;
    },
  },
  {
    rule: 'BC-002',
    title: 'the catalogue gate scans the build range per commit for unaccounted production changes',
    run: () => {
      const gate = read('tools/verify-hotfix-patch-catalog.cjs');
      if (!gate) return 'missing tools/verify-hotfix-patch-catalog.cjs';
      if (!gate.includes('hotfix-component-map.cjs')) return 'the catalogue gate no longer derives components from the import graph';
      if (!gate.includes('componentResolver.isProductionPath')) return 'the catalogue gate no longer asks whether a path is a production path';
      if (!gate.includes('改动了生产路径但未被声明')) return 'the "unaccounted production change" failure is gone';
      if (!/\$\{item\.manifest\.base_commit\}\.\.\$\{item\.manifest\.build_commit\}/.test(gate)) {
        return 'the gate no longer scans the declared base..build range';
      }
      return true;
    },
  },
  {
    rule: '7',
    title: 'a leader change is not a failure, and an unknown leader is not a change',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      const resolve = shellFunction(source, 'resolve_leader_host');
      if (!resolve) return 'resolve_leader_host is gone: a patch that restarts a node would pin the pre-run leader again';
      const idle = shellFunction(source, 'verify_cluster_idle');
      if (!idle) return 'verify_cluster_idle is gone';
      const unknown = idle.indexOf('leader_unknown');
      const changed = idle.indexOf('violations=leader_changed');
      if (unknown < 0) return 'an unobserved leader is no longer reported as leader_unknown';
      if (changed < 0) return 'a genuinely different leader is no longer reported as leader_changed';
      if (unknown > changed) return 'the unknown case is checked after the changed case, so an unobserved leader is still called a change';
      if (!/-z "\$\{observed_leader\}"/.test(idle)) return 'the unknown branch no longer keys off an empty observation';
      return true;
    },
  },
  {
    rule: '12 / 16 / 17',
    title: 'the artifact chain and the console regression exist',
    run: () => {
      for (const file of ['scripts/build-hotfix-patch.sh', 'tools/verify-hotfix-patch-catalog.cjs', 'tools/verify-upgrade-validation-chain.cjs']) {
        if (!read(file)) return `missing ${file}`;
      }
      const acceptance = read('tools/console-update-hotfix-recovery-acceptance.cjs');
      if (!acceptance) return 'missing the console regression that section 17 requires';
      // §16's five mandatory cases, plus the two shapes the site produced: the operation that
      // overwrote an applied package's record (REG-003) and the rollback that would have mixed
      // two patches. A scenario renamed here is a scenario that stopped being run.
      for (const scenario of [
        'a hotfix that succeeded',
        'a hotfix that failed',
        'a rolling upgrade that failed',
        'a hotfix whose resume was refused',
        'acting below a newer successful record',
        'an applied record is not retired by a timestamp, it is retired by its payload',
        'rolling back a record whose files have been replaced',
      ]) {
        if (!acceptance.includes(`name: '${scenario}'`)) return `the console regression no longer covers: ${scenario}`;
      }
      return true;
    },
  },
  {
    rule: '18',
    title: 'the release stage vocabulary is stated, so a stage cannot be overstated',
    run: () => {
      const source = read(CONTRACT_DOC);
      const stages = ['built', 'signed', 'validated', 'released_local', 'pushed', 'tagged', 'uploaded_field', 'installed_field', 'verified_field'];
      for (const stage of stages) {
        if (!source.includes(stage)) return `the contract no longer lists the release stage "${stage}"`;
      }
      if (!source.includes('禁止把：')) return 'missing release stage discipline';
      return true;
    },
  },
  {
    rule: '6 / INV-009',
    title: 'the supersede guard reads a source that exists on the field',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      const guard = shellFunction(source, 'assert_not_superseded');
      if (!guard) return 'assert_not_superseded is gone';
      // The refusal is only worth reading if the guard reaches it on a tree that has no
      // deployment record, which is every tree an update runner predating deployment.json
      // leaves behind.
      if (!/for status_file in [^;]*status\.json[^;]*;/.test(guard)) {
        return 'the guard no longer enumerates operation records, so on a field tree it does not run at all';
      }
      if (!guard.includes('payload_applied "${status_file}" || continue')) {
        return 'operation records are enumerated but never judged, so an applied predecessor cannot be a successor';
      }
      if (!guard.includes('[[ "${operation}" == "hotfix" ]] || continue')) {
        return 'the operation record is no longer restricted to hotfixes, so a rolling package could block every action';
      }
      if (!guard.includes('die "CG_PACKAGE_IDENTITY_MISMATCH: ${patch_id} 已被签名且已安装的 ${successor} 替代"')) {
        return 'the guard no longer refuses a superseded predecessor by name';
      }
      const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-payload-applied-'));
      try {
        const verified = writeOperationTree(root, 'verified', 'failed', OVERWRITTEN_VERIFIED_RUN.events);
        const refused = writeOperationTree(root, 'refused', 'failed', GENUINE_FAILURE_RUN.events);
        const applied = writeOperationTree(root, 'applied', 'succeeded', null);
        const silent = writeOperationTree(root, 'silent', 'failed', null);
        const asked = [
          ['a verified run whose record was overwritten', verified, 0],
          ['a run that genuinely failed', refused, 1],
          ['a run the runner recorded as succeeded', applied, 0],
          ['a record with no event chain at all', silent, 1],
        ];
        for (const [label, statusFile, expected] of asked) {
          const verdict = payloadAppliedVerdict(statusFile);
          if (verdict === null) return 'payload_applied could not be executed (jq or the function is missing)';
          if (verdict !== expected) return `${label} was judged ${verdict}, expected ${expected}`;
        }
        return true;
      } finally { fs.rmSync(root, { recursive: true, force: true }); }
    },
  },
  {
    rule: 'INV-004 / 21',
    title: 'a verified run still counts as applied after a refused attempt overwrote its record',
    run: () => {
      const decision = softwareUpdateDecision();
      if (!decision) return 'the console decision block could not be executed';
      const cases = [
        ['the 2026-10-08 record: failed status, verified event chain, no deployment record',
          { package:{patch_id:'HF-OLD',kind:'hotfix'}, job: OVERWRITTEN_VERIFIED_RUN }, true],
        ['a run the runner recorded as succeeded',
          { package:{patch_id:'HF-OK',kind:'hotfix'}, job:{status:'succeeded'} }, true],
        ['a run that genuinely failed',
          { package:{patch_id:'HF-BAD',kind:'hotfix'}, job: GENUINE_FAILURE_RUN }, false],
        ['a rolled-back deployment record outranking a stale success event',
          { package:{patch_id:'HF-RB',kind:'hotfix'}, job:{status:'failed',deployment_state:'rolled_back',events:[{status:'succeeded'}]} }, false],
        ['a record with no operation at all',
          { package:{patch_id:'HF-NEW',kind:'hotfix'}, job:null }, false],
      ];
      for (const [label, item, expected] of cases) {
        if (decision.payloadApplied(item) !== expected) {
          return `${label} was judged payloadApplied=${decision.payloadApplied(item)}, expected ${expected}`;
        }
      }
      return true;
    },
  },
  {
    rule: '5 / UI-005',
    title: 'rollback names only the record whose payload is on the disk',
    run: () => {
      const decision = softwareUpdateDecision();
      if (!decision) return 'the console decision block could not be executed';
      // A record whose payload is on the disk has nothing left to run. Leaving it actionable
      // is what pinned the panel to a replaced patch on 2026-10-08: it read failed, so it
      // stayed actionable for ever, owned the subject, and was the only record a rollback
      // could aim at - while the newer patch that replaced it was already on every node.
      const refused = { package:{patch_id:'HF-OLD',kind:'hotfix',rollback_available:true}, job: OVERWRITTEN_VERIFIED_RUN };
      const stillFailing = { package:{patch_id:'HF-BAD',kind:'hotfix',rollback_available:true}, job: GENUINE_FAILURE_RUN };
      if (decision.actionable(refused)) return 'a record whose payload is already applied is still actionable';
      if (!decision.actionable(stillFailing)) return 'a record that genuinely failed is no longer actionable';
      // "Newest row" and "newest applied record" are the same thing only by accident. The
      // button has to name the second, or it reverts a patch the operator never selected.
      const appliedNewer = { package:{patch_id:'HF-NEW',kind:'hotfix',rollback_available:true}, job:{status:'succeeded',finished_at:'2026-10-08T16:58:30Z'} };
      const failedOlder = { package:{patch_id:'HF-BAD2',kind:'hotfix',rollback_available:true}, job:{status:'failed',finished_at:'2026-09-30T05:30:30Z',events:[{status:'failed'}]} };
      const targeted = softwareUpdateDecision([failedOlder, appliedNewer]);
      if (!targeted) return 'the console decision block could not be executed with a package list';
      const target = targeted.rollbackTarget();
      if (!target) return 'no rollback target was resolved for a site with one applied patch';
      if (target.package.patch_id !== 'HF-NEW') {
        return `a rollback named ${target.package.patch_id}; it may only name the newest record whose payload is on the disk`;
      }
      // The button is bound to that target and refused when the panel describes another
      // record. The browser regression, not this gate, proves the refusal reaches the DOM.
      const page = read('internal/api/console.html');
      for (const [label, needle] of [
        ['the button follows the rollback target, not the subject', 'const rollbackApplies = !!rollbackTarget && rollbackTarget === subject;'],
        ['the button is hidden for every other record', "byId('rollback-software-update').hidden = !rollbackApplies;"],
        ['clicking it refuses a record whose files were replaced', 'if (!target || target !== softwareUpdateSubject()) {'],
      ]) {
        if (!page.includes(needle)) return `${label}: ${needle} is gone`;
      }
      const acceptance = read('tools/console-update-hotfix-recovery-acceptance.cjs');
      if (!acceptance) return 'missing the console regression that section 17 requires';
      for (const scenario of [
        'rolling back a record whose files have been replaced',
        'an applied record is not retired by a timestamp, it is retired by its payload',
        'rolling back the newest applied record names it',
      ]) {
        if (!acceptance.includes(`name: '${scenario}'`)) return `the console regression no longer covers: ${scenario}`;
      }
      return true;
    },
  },
  {
    rule: 'INV-009 / 6',
    title: 'the back end establishes supersession from the operation record too',
    run: () => {
      const source = read('internal/platformupdate/supersedes.go');
      if (!source) return 'missing internal/platformupdate/supersedes.go';
      const start = source.indexOf('func (manager *Manager) successorInstalled(');
      if (start < 0) return 'successorInstalled is gone';
      const body = source.slice(start, source.indexOf('\n}\n', start));
      if (!body.includes('manager.Deployment(patchID)')) return 'the deployment record is no longer consulted';
      if (!body.includes('payloadInstalled(job)')) {
        return 'the operation record is no longer consulted, so a patch applied without a deployment record is not a successor';
      }
      if (!body.includes('PackageKindHotfix')) return 'the second source is no longer restricted to hotfixes';
      // A binding without a test is a claim. These two names are the runs that establish the
      // verdict on the field shape: no deployment record at all, and a success overwritten
      // by an attempt the updater refused on its first line.
      const tests = read('internal/platformupdate/supersedes_test.go');
      if (!tests) return 'missing internal/platformupdate/supersedes_test.go';
      for (const name of [
        'TestAppliedSuccessorBlocksPredecessorWithoutAnyDeploymentRecord',
        'TestRejectedAttemptOverVerifiedSuccessCountsAsApplied',
      ]) {
        if (!tests.includes(`func ${name}(`)) return `the run that proves the second source is gone: ${name}`;
      }
      return true;
    },
  },
];

const runChecks = () => {
  const failures = [];
  for (const check of (contractOnly ? CHECKS.filter(check => ['20','21'].includes(check.rule)) : CHECKS)) {
    let outcome;
    try { outcome = check.run(); } catch (error) { outcome = `threw: ${error.message}`; }
    if (outcome === true) {
      console.log(`ok    §${check.rule} ${check.title}`);
    } else {
      console.log(`FAIL  §${check.rule} ${check.title}\n      ${outcome}`);
      failures.push(`${check.title}: ${outcome}`);
    }
  }
  return failures;
};

const reportOpen = failures => {
  if (contractOnly) return [];
  console.log(`Stage: ${stage} (source checks are always required)`);
  if(stage==='source') {
    console.log('DEFERRED ART/FIELD: outside source-stage completion; release and site acceptance are not asserted.');
    return [];
  }
  const key=stage==='artifact'?'artifact_acceptance':'artifact_and_field_acceptance';
  if(reportFile) {
    try {
      const report=evidence.validateReport(checkRoot,path.resolve(reportFile),stage);
      console.log(`EVIDENCE ${stage} inventory verified for ${report.package_id}; recorded by ${report.recorded_by}`);
      if(stage==='artifact') console.log('DEFERRED FIELD: artifact-stage completion does not assert site acceptance.');
      return [];
    } catch(error) {
      failures.push('invalid acceptance report: '+error.message);
      console.log('FAIL acceptance evidence: '+error.message);
    }
  }
  const open=[{rule:'11 / 15',key,status:`${stage==='artifact'?'ART':'ART/FIELD'} evidence is not completed`,card:'UPDATE-V2-FIELD-ACCEPTANCE'}];
  for(const obligation of open) console.log(`OPEN  §${obligation.rule} ${obligation.key} — ${obligation.status}, card ${obligation.card}`);
  return open;
};

// The files the rules read. The self-test copies exactly these, so a mutation cannot be
// caught by reading something the gate does not actually consult.
const READ_FILES = [
  'tools/console-language-acceptance.cjs',
  'tools/console-update-message-language-acceptance.cjs',
  'internal/platformupdate/progress_staging_test.go', 'tools/console-update-staging-progress-acceptance.cjs',
  'internal/platformupdate/version.go', 'internal/platformupdate/inspector.go',
  CONTRACT_DOC, CONTRACT_DOC_ZH, 'internal/updatecontract/contract.md', 'internal/updatecontract/schema.json', 'internal/platformupdate/helper.go',
  'internal/platformupdate/supersedes.go', 'internal/platformupdate/supersedes_test.go',
  'scripts/clusterguard-update-job.sh', 'scripts/clusterguard-upgrade.sh', 'scripts/build-hotfix-patch.sh',
  'internal/platformupdate/manager.go', 'internal/api/updates.go', 'internal/api/console.html',
  'hotfixes/hotfix-publications.json', 'tools/verify-hotfix-patch-catalog.cjs',
  'tools/console-update-hotfix-recovery-acceptance.cjs', 'tools/verify-upgrade-validation-chain.cjs',
];

const MUTATIONS = [
  {name:'English settings retain Chinese account heading',file:'internal/api/console.html',find:'"账户与安全": "Account and security"',replace:'"账户与安全": "账户与安全"',rule:'console language preference covers settings and keeps raw values intact'},
  {name:'language switch omits controller status',file:'internal/api/console.html',find:'renderAccountIdentity(); renderControlPlaneStatus(); renderConfiguration();',replace:'renderAccountIdentity(); renderConfiguration();',rule:'console language preference covers settings and keeps raw values intact'},

  {name:'history prints raw English again',file:'internal/api/console.html',find:'renderSoftwareUpdateMessage(messageCell, job && job.message',replace:'renderSoftwareUpdateMessage(messageCell, null',rule:'update history and events localize messages without rewriting evidence'},
  {name:'verified hotfix result loses Chinese translation',file:'internal/api/console.html',find:"'all node digests and maintenance release verified':'热修补丁完成，全部节点与控制面已验证，维护门禁已释放'",replace:"'all node digests and maintenance release verified':'all node digests and maintenance release verified'",rule:'update history and events localize messages without rewriting evidence'},
  {name:'hotfix staging becomes zero again',file:'internal/platformupdate/manager.go',find:'case "staging", "updating":',replace:'case "updating":',rule:'hotfix staging retains node progress and its console stage'},
  {name:'hotfix staging jumps back to console preparation',file:'internal/api/console.html',find:"['staging', 'updating', 'rollback'].includes(phase) ? 1 : 0",replace:"['updating', 'rollback'].includes(phase) ? 1 : 0",rule:'hotfix staging retains node progress and its console stage'},
  {name:'retired historical failure occupies current summary again',file:'internal/api/console.html',find:'item.incompatible || item.superseded_by',replace:'item.incompatible || false',rule:'version identity and signed supersession reach the current subject'},

  { name:'filename migration leaves runtime behind', file:'scripts/build-hotfix-patch.sh', find:'internal/buildinfo.ProductVersion=${patch_version}', replace:'internal/buildinfo.Release=${patch_version}', rule:'runtime and history use their own verified product versions' },
  { name:'UI ignores sealed runtime version', file:'internal/api/console.html', find:'if (version.product_version) return version.product_version;', replace:'if (false) return version.product_version;', rule:'runtime and history use their own verified product versions' },

  {
    name:'a failed attempt overwrites installed deployment',file:'internal/api/console.html',
    find:"job?.deployment_state === 'installed' && status === 'failed'",
    replace:"false && status === 'failed'",
    rule:'deployment result survives a later failed operation',
  },

  {
    name:'atomic restore moved into a comment',
    file:'scripts/build-hotfix-patch.sh',
    find:"rollback.push('    mv -f \"${restore_tmp}\" \"${destination}\"');",
    replace:"//rollback.push('    mv -f \"${restore_tmp}\" \"${destination}\"');",
    rule:'the generated rollback never copies over a running binary',
  },

  ...[
    ['unknown field', '  version: 2', '  version: 2\n  unknown: true'],
    ['duplicate field', '  version: 2', '  version: 2\n  version: 2'],
    ['unsupported version', '  version: 2', '  version: 3'],
    ['invalid nesting', '  version: 2', '    version: 2'],
  ].map(([name,find,replace]) => ({name,file:CONTRACT_DOC,find,replace,rule:'the mandatory document is present and parses'})),

  {
    name: 'resume+hotfix goes back to --resume',
    file: 'scripts/clusterguard-update-job.sh',
    find: '[[ "${requested_kind}" != hotfix ]] ||',
    replace: 'true ||',
    rule: 'a hotfix resume is rejected',
  },
  {
    name: 'the resume arm is reached for every kind',
    file: 'scripts/clusterguard-update-job.sh',
    find: '[[ "${requested_kind}" != hotfix ]] ||',
    replace: '[[ "${requested_kind}" == hotfix ]] ||',
    rule: 'a hotfix resume is rejected',
  },
  {
    name: 'the foreign lock check stops refusing',
    file: 'scripts/clusterguard-upgrade.sh',
    find: '    die "CG_FOREIGN_UPDATE_LOCK: 维护门禁当前由 ${previous} 持有',
    replace: '    log "CG_FOREIGN_UPDATE_LOCK: 维护门禁当前由 ${previous} 持有',
    rule: 'a foreign maintenance lock is refused by name',
  },
  {
    name: 'the confirmation re-derives the verdict instead of reusing it',
    file: 'internal/api/console.html',
    find: "openSoftwareUpdateConfirmation(retry ? 'retry' : 'execute', patchID, retry)",
    replace: "openSoftwareUpdateConfirmation('execute', patchID)",
    rule: 'the console resolves one subject and describes the action it takes',
  },
  {
    name: 'the rollback goes back to copying over the destination',
    file: 'scripts/build-hotfix-patch.sh',
    find: "rollback.push('    mv -f \"${restore_tmp}\" \"${destination}\"');",
    replace: "rollback.push('    cp -p \"${backup}\" \"${destination}\"');",
    rule: 'the generated rollback never copies over a running binary',
  },
  {
    name: 'the backup list loses its hotfix id',
    file: 'scripts/build-hotfix-patch.sh',
    find: 'backup-${manifest.hotfix_id}-',
    replace: 'backup-',
    // Both the name the apply step writes and the glob the rollback reads carry the id;
    // dropping it from one of them leaves the other intact, and this rule is about both.
    replaceAll: true,
    rule: 'a backup list is bound to the hotfix that wrote it',
  },
  {
    name: 'an unobserved leader is called a change again',
    file: 'scripts/clusterguard-upgrade.sh',
    find: 'violations=leader_unknown',
    replace: 'violations=leader_changed',
    rule: 'a leader change is not a failure, and an unknown leader is not a change',
  },
  {
    name: 'the ledger revises an identity in place instead of superseding it',
    file: 'hotfixes/hotfix-publications.json',
    find: 'clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch',
    replace: 'clusterguard-ha-hotfix-HF-2026-0930-01-2.2-105.x86_64.cgpatch',
    rule: 'the ledger keeps every published identity',
  },
  {
    // The defect this check exists for: with only the deployment record as a source, the
    // guard does not fail closed on a field tree - it never runs.
    name: 'the supersede guard goes back to reading only deployment records',
    file: 'scripts/clusterguard-upgrade.sh',
    find: '  for status_file in "${private_root}"/jobs/*/status.json "${private_root}"/history/*/status.json "${update_root}"/*/status.json; do',
    replace: '  for status_file in "${private_root}"/jobs/*/absent.json "${private_root}"/history/*/absent.json "${update_root}"/*/absent.json; do',
    rule: 'the supersede guard reads a source that exists on the field',
  },
  {
    name: 'a rolling package is admitted as a blocking successor',
    file: 'scripts/clusterguard-upgrade.sh',
    find: '    [[ "${operation}" == "hotfix" ]] || continue',
    replace: '    [[ "${operation}" == "hotfix" ]] || true',
    rule: 'the supersede guard reads a source that exists on the field',
  },
  {
    name: 'a refused attempt stops counting as a verified run',
    file: 'scripts/clusterguard-upgrade.sh',
    find: '  [[ "${status}" == "failed" ]] || return 1',
    replace: '  return 1',
    rule: 'the supersede guard reads a source that exists on the field',
  },
  {
    name: 'an overwritten verified run stops counting as applied',
    file: 'internal/api/console.html',
    find: "      return ['succeeded', 'applied_attempt_failed'].includes(softwareUpdateOutcome(job));",
    replace: "      return softwareUpdateOutcome(job) === 'succeeded';",
    rule: 'a verified run still counts as applied after a refused attempt overwrote its record',
  },
  {
    name: 'an applied record stays actionable again',
    file: 'internal/api/console.html',
    find: '      if (softwareUpdatePayloadApplied(item)) return false;',
    replace: '      if (false) return false;',
    rule: 'rollback names only the record whose payload is on the disk',
  },
  {
    name: 'a rollback goes back to naming the newest row rather than the newest applied one',
    file: 'internal/api/console.html',
    find: '      .filter(softwareUpdatePayloadApplied)\n',
    replace: '      .filter(() => false)\n',
    rule: 'rollback names only the record whose payload is on the disk',
  },
  {
    name: 'the back end goes back to the deployment record alone',
    file: 'internal/platformupdate/supersedes.go',
    find: '	return found && payloadInstalled(job)',
    replace: '	return false',
    rule: 'the back end establishes supersession from the operation record too',
  },
];

const copyForSelfTest = () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-validation-chain-'));
  for (const file of READ_FILES) {
    const source = read(file);
    if (source === null) continue;
    fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    fs.writeFileSync(path.join(root, file), source);
  }
  return root;
};

const runSelfTest = () => {
  const root = copyForSelfTest();
  const problems = [];
  const failuresFor = () => {
    const saved = [];
    const original = console.log;
    checkRoot = root;
    console.log = line => { if (line.startsWith('FAIL')) saved.push(line); };
    try { runChecks(); } finally { console.log = original; checkRoot = repoRoot; }
    return saved.join('\n');
  };

  // S0: with nothing changed the checks must all pass, or "it caught the mutation" would be
  // indistinguishable from "it fails on everything".
  const baseline = failuresFor();
  console.log(`\nself-test: no-bite control (unmutated copy) -> ${baseline ? `FAILED\n${baseline}` : 'all checks pass'}`);
  if (baseline) problems.push('the unmutated copy does not pass, so every mutation result would be meaningless');

  for (const mutation of MUTATIONS) {
    const absolute = path.join(root, mutation.file);
    const before = fs.readFileSync(absolute, 'utf8');
    if (!before.includes(mutation.find)) {
      console.log(`self-test: SKIPPED ${mutation.name} — the text to mutate is not in ${mutation.file}`);
      problems.push(`${mutation.name}: the mutation did not apply`);
      continue;
    }
    fs.writeFileSync(absolute, mutation.replaceAll
      ? before.split(mutation.find).join(mutation.replace)
      : before.replace(mutation.find, mutation.replace === undefined ? `${mutation.find} # mut` : mutation.replace));
    const caught = failuresFor();
    fs.writeFileSync(absolute, before);
    const ok = caught.includes(mutation.rule);
    console.log(`self-test: ${ok ? 'caught' : 'MISSED'} ${mutation.name}`);
    if (!ok) problems.push(`${mutation.name} was not caught by "${mutation.rule}"`);
  }

  // A comment-only change must not be reported: the checks have to distinguish a statement
  // from prose about it, or documentation would trip the gate.
  const commentTarget = path.join(root, 'scripts/clusterguard-update-job.sh');
  const withComment = fs.readFileSync(commentTarget, 'utf8').replace(
    'update_mode_arguments() {',
    '# a hotfix resume must never emit --resume; a rolling upgrade must\nupdate_mode_arguments() {');
  fs.writeFileSync(commentTarget, withComment);
  const commentFailures = failuresFor();
  fs.writeFileSync(commentTarget, fs.readFileSync(commentTarget, 'utf8'));
  console.log(`self-test: comment-only control -> ${commentFailures ? 'FALSE POSITIVE' : 'no violation reported'}`);
  if (commentFailures) problems.push('a comment-only change was reported as a violation');

  fs.rmSync(root, { recursive: true, force: true });
  return problems;
};

if (process.argv.includes('--print-evidence-binding')) {
  if (strict || selfTest || contractOnly || reportFile || process.argv.includes('--stage')) {
    console.error('--print-evidence-binding only prints identity; it cannot run an acceptance stage');
    process.exitCode=1;
  } else {
    try { console.log(JSON.stringify({schema_version:1,source_sha256:evidence.sourceDigest(repoRoot),contract_sha256:evidence.digest(fs.readFileSync(path.join(repoRoot,CONTRACT_DOC)))},null,2)); }
    catch(error) { console.error(error.message);process.exitCode=1; }
  }
} else if (selfTest) {
  console.log(`self-testing ${path.join(__dirname, path.basename(__filename))} against a copy of ${repoRoot}\n`);
  const problems = runSelfTest();
  console.log('');
  if (problems.length) {
    console.log(`${problems.length} mutation problem(s):`);
    for (const problem of problems) console.log(`  - ${problem}`);
    process.exitCode = 1;
  } else {
    console.log(`All ${MUTATIONS.length} mutations were caught and both controls behaved.`);
  }
} else {
  console.log(`Upgrade validation chain — contract ${read(CONTRACT_DOC) ? '' : '(missing) '}at ${CONTRACT_DOC}\n`);
  const failures = runChecks();
  const open = reportOpen(failures);
  console.log('');
  if (open.length) {
    console.log(`${open.length} obligation(s) are open and are not counted as passing checks.`);
    if (strict) {
      console.log('--strict is in effect, so they fail this run.');
      failures.push(...open.map(obligation => `${obligation.key}: ${obligation.status || "not implemented"}`));
    }
  }
  if (failures.length) {
    console.log(`${CHECKS.length} checks, ${failures.length} failed:`);
    for (const failure of failures) console.log(`  - ${failure}`);
    console.log('  This stage did not pass.');
    process.exitCode = 1;
  } else {
    console.log(`${contractOnly ? 3 : CHECKS.length} checks passed, ${open.length} obligation(s) open, 0 failed.`);
  }
}
