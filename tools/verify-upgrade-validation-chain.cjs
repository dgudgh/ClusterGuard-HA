#!/usr/bin/env node
// Checks the supported v2 section 21 contract, executable routing and rollback
// generation, console subject identity, immutable delivery ledger and lock rules.
// This is a local source gate, not proof of artifact or production acceptance.
// Remaining ART/FIELD evidence is tracked in
// docs/zh-CN/upgrade-validation-chain-implementation-status.md and is OPEN;
// --strict refuses release until that evidence exists.
// --contract-only checks loading/schema compatibility before builders write bytes.
// --self-test runs mutations in an isolated copy and includes no-bite controls.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const vm = require('node:vm');

const SUPPORTED_CONTRACT_VERSION = 2;
const CONTRACT_DOC = 'docs/upgrade-validation-chain.md';
const CONTRACT_DOC_ZH = 'docs/zh-CN/upgrade-validation-chain.md';

// Every rule below is a claim section 22 makes. The identifiers are the rule numbers of the
// contract so a failure points at the clause to re-read, not merely at a file.
const OPEN_OBLIGATIONS = [
  { rule: '11 / 15', key: 'artifact_and_field_acceptance', status:'full ART/FIELD evidence is not completed', card: 'UPDATE-V2-FIELD-ACCEPTANCE' },
];

const repoRoot = (() => {
  const index = process.argv.indexOf('--repo');
  return index >= 0 && process.argv[index + 1] ? path.resolve(process.argv[index + 1]) : path.resolve(__dirname, '..');
})();
const strict = process.argv.includes('--strict');
const selfTest = process.argv.includes('--self-test');
const contractOnly = process.argv.includes('--contract-only');

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

const CHECKS = [
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
      for (const scenario of [
        'a hotfix that succeeded',
        'a hotfix that failed',
        'a rolling upgrade that failed',
        'acting below a newer successful record',
        'rolling back below a newer successful record',
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

const reportOpen = () => {
  const source = read(CONTRACT_DOC) || '';
  const open = contractOnly ? [] : OPEN_OBLIGATIONS;
  for (const obligation of open) {
    console.log(`OPEN  §${obligation.rule} ${obligation.key} — ${obligation.status || "not implemented"}, card ${obligation.card}`);
  }
  return open;
};

// The files the rules read. The self-test copies exactly these, so a mutation cannot be
// caught by reading something the gate does not actually consult.
const READ_FILES = [
  CONTRACT_DOC, CONTRACT_DOC_ZH, 'internal/updatecontract/contract.md', 'internal/updatecontract/schema.json', 'internal/platformupdate/helper.go',
  'scripts/clusterguard-update-job.sh', 'scripts/clusterguard-upgrade.sh', 'scripts/build-hotfix-patch.sh',
  'internal/platformupdate/manager.go', 'internal/api/updates.go', 'internal/api/console.html',
  'hotfixes/hotfix-publications.json', 'tools/verify-hotfix-patch-catalog.cjs',
  'tools/console-update-hotfix-recovery-acceptance.cjs', 'tools/verify-upgrade-validation-chain.cjs',
];

const MUTATIONS = [
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
    name: 'a published identity loses its revision marker',
    file: 'hotfixes/hotfix-publications.json',
    find: 'clusterguard-ha-hotfix-HF-2026-0930-01-r1-2.2-105.x86_64.cgpatch',
    replace: 'clusterguard-ha-hotfix-HF-2026-0930-01-2.2-105.x86_64.cgpatch',
    rule: 'the ledger keeps every published identity',
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

if (selfTest) {
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
  const open = reportOpen();
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
    console.log('  That is a pass for the rules whose enforcement point still holds, and a failure for the rest.');
    process.exitCode = 1;
  } else {
    console.log(`${contractOnly ? 3 : CHECKS.length} checks passed, ${open.length} obligation(s) open, 0 failed.`);
  }
}
