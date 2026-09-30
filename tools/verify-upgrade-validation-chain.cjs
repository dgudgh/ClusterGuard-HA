#!/usr/bin/env node
// The gate for the upgrade validation chain.
//
// docs/upgrade-validation-chain.md is the mandatory contract for upgrades, hotfixes,
// rollback, building, signing and field acceptance, and its section 22 maps every rule to the
// place it is enforced. A mapping is a claim, and a claim nobody re-checks decays: this walks
// the same list and re-checks each enforcement point against the tree, so a rule that stops
// being true stops the release instead of quietly describing something that no longer holds.
//
// It also reports the obligations the contract itself records as outstanding (section 23).
// They are printed on every run, with the card that closes each one, and --strict turns them
// into failures - which is what a release gate should use until those cards land.
//
//   node tools/verify-upgrade-validation-chain.cjs [--repo <dir>] [--strict] [--self-test]
//
// Section 20 is why this exits non-zero rather than warning when the document is missing,
// unparsable, or newer than this program understands: FAIL CLOSED is the whole point of
// reading it first.
//
// --self-test proves the checks can fail. It copies the files the rules read into a temporary
// directory, mutates one thing at a time, and asserts that the rule meant to catch it does -
// plus a no-bite control that a comment-only edit is not reported as a violation. A gate that
// cannot fail is worse than no gate, because it reads as a pass.

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const SUPPORTED_CONTRACT_VERSION = 1;
const CONTRACT_DOC = 'docs/upgrade-validation-chain.md';
const CONTRACT_DOC_ZH = 'docs/zh-CN/upgrade-validation-chain.md';

// Every rule below is a claim section 22 makes. The identifiers are the rule numbers of the
// contract so a failure points at the clause to re-read, not merely at a file.
const OPEN_OBLIGATIONS = [
  { rule: '0.1.10', key: 'history.forbid_success_overwrite', card: 'UPDATE-OPERATION-HISTORY-P0' },
  { rule: '0.1.11', key: 'history.package_state_separate_from_operation_history', card: 'UPDATE-OPERATION-HISTORY-P0' },
  { rule: '0.1.11', key: 'history.append_only_operations', card: 'UPDATE-OPERATION-HISTORY-P0' },
];

const repoRoot = (() => {
  const index = process.argv.indexOf('--repo');
  return index >= 0 && process.argv[index + 1] ? path.resolve(process.argv[index + 1]) : path.resolve(__dirname, '..');
})();
const strict = process.argv.includes('--strict');
const selfTest = process.argv.includes('--self-test');

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
  const match = source.match(/```yaml\n([\s\S]*?)```/);
  if (!match) return null;
  const flat = new Map();
  let section = '';
  for (const raw of match[1].split('\n')) {
    if (!raw.trim() || raw.trim().startsWith('#')) continue;
    const indented = /^\s+\S/.test(raw);
    const separator = raw.indexOf(':');
    if (separator < 0) return null;
    const key = raw.slice(0, separator).trim();
    const value = raw.slice(separator + 1).trim();
    if (indented) {
      if (!section) return null;
      flat.set(`${section}.${key}`, value);
    } else if (value === '') {
      section = key;
    } else {
      section = '';
      flat.set(key, value);
    }
  }
  return flat;
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

const CHECKS = [
  {
    rule: '20',
    title: 'the mandatory document is present and parses',
    run: () => {
      const source = read(CONTRACT_DOC);
      if (!source) return `missing ${CONTRACT_DOC}`;
      const contract = parseContract(source);
      if (!contract) return `${CONTRACT_DOC} has no parsable machine-readable block`;
      const version = Number(contract.get('VALIDATION_CONTRACT_VERSION'));
      if (!Number.isInteger(version)) return 'VALIDATION_CONTRACT_VERSION is not an integer';
      if (version > SUPPORTED_CONTRACT_VERSION) {
        return `the contract is version ${version} and this program supports ${SUPPORTED_CONTRACT_VERSION}: FAIL CLOSED`;
      }
      if (contract.get('mandatory') !== 'true') return 'mandatory is not true';
      if (contract.get('fail_closed') !== 'true') return 'fail_closed is not true';
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
    rule: '23',
    title: 'every outstanding obligation is recorded, carded and still declared',
    run: () => {
      const source = read(CONTRACT_DOC);
      const contract = parseContract(source) || new Map();
      for (const obligation of OPEN_OBLIGATIONS) {
        if (contract.get(obligation.key) !== 'true') return `${obligation.key} is not declared true in the contract`;
        const at = source.indexOf(obligation.key);
        if (at < 0) return `${obligation.key} is not listed among the open obligations`;
        const after = source.slice(at, at + 600);
        if (!after.includes(`card: ${obligation.card}`)) return `${obligation.key} names no card (expected ${obligation.card})`;
      }
      return true;
    },
  },
  {
    rule: '0.1.3',
    title: "a hotfix resume is routed to a re-execution, and only a rolling upgrade resumes",
    run: () => {
      const hotfix = runRouting('resume', 'hotfix');
      if (!hotfix) return 'update_mode_arguments could not be executed';
      if (hotfix.includes('--resume')) return `resume+hotfix still emits --resume: ${hotfix.join(' ')}`;
      if (!hotfix.includes('--execute')) return `resume+hotfix does not re-execute: ${hotfix.join(' ')}`;
      const upgrade = runRouting('resume', 'upgrade');
      if (!upgrade) return 'update_mode_arguments could not be executed for an upgrade';
      if (!upgrade.includes('--resume')) return `resume+upgrade lost --resume: ${upgrade.join(' ')}`;
      const plan = runRouting('plan', 'hotfix');
      if (!plan || !plan.includes('--plan') || plan.includes('--execute')) return 'plan must stay read-only for both kinds';
      return true;
    },
  },
  {
    rule: '0.1.3',
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
    rule: '0.1.6',
    title: 'the maintenance lock is adopted only by the patch that left it',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      if (!source.includes("grep -Fqx '${patch_id}'")) return "current_update_lock_on_host no longer matches on its own patch id";
      return true;
    },
  },
  {
    rule: '0.1.7',
    title: 'a foreign maintenance lock is refused by name',
    run: () => {
      const source = read('scripts/clusterguard-upgrade.sh');
      if (!source) return 'missing scripts/clusterguard-upgrade.sh';
      const fn = shellFunction(source, 'detect_foreign_update_lock');
      if (!fn) return 'detect_foreign_update_lock is gone';
      if (!fn.includes('die ')) return 'detect_foreign_update_lock no longer refuses the run';
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
    rule: '0.1.2 / 5.1 / 5.2',
    title: 'the console resolves one subject and describes the action it takes',
    run: () => {
      const page = read('internal/api/console.html');
      if (!page) return 'missing internal/api/console.html';
      const required = [
        ['single subject resolver', 'const softwareUpdateSubject = () => pendingSoftwareUpdate() || latestSoftwareUpdate();'],
        ['confirmation defaulting to the subject', 'patchID = softwareUpdateSubject()?.package?.patch_id'],
        ['rollback and resume using the subject', 'byId(\'resume-software-update\').addEventListener(\'click\', () => openSoftwareUpdateConfirmation(\'resume\'))'],
        ['the retry verdict travelling into the confirmation', "openSoftwareUpdateConfirmation('execute', patchID, softwareUpdateIsRetry(target))"],
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
    rule: '0.1.17 / 0.1.18',
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
    rule: '8.1 / 8.2',
    title: 'the generated rollback never copies over a running binary',
    run: () => {
      const builder = read('scripts/build-hotfix-patch.sh');
      if (!builder) return 'missing scripts/build-hotfix-patch.sh';
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
    rule: '9',
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
    rule: '0.1.8 / 0.1.9 / 2.2',
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
    rule: '10.2',
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
    rule: '7.1 / 7.2',
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
    rule: '0.1.15 / 13',
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
    rule: '19',
    title: 'the release stage vocabulary is stated, so a stage cannot be overstated',
    run: () => {
      const source = read(CONTRACT_DOC);
      const stages = ['built', 'signed', 'validated', 'released locally', 'pushed', 'tagged', 'uploaded to field', 'installed', 'verified in field'];
      for (const stage of stages) {
        if (!source.includes(stage)) return `the contract no longer lists the release stage "${stage}"`;
      }
      if (!/Every report must state the true stage/.test(source)) return 'the release discipline clause lost its requirement to state the true stage';
      return true;
    },
  },
];

const runChecks = () => {
  const failures = [];
  for (const check of CHECKS) {
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
  const open = OPEN_OBLIGATIONS.filter(obligation => source.includes(obligation.key));
  for (const obligation of open) {
    console.log(`OPEN  §${obligation.rule} ${obligation.key} — not implemented, card ${obligation.card}`);
  }
  return open;
};

// The files the rules read. The self-test copies exactly these, so a mutation cannot be
// caught by reading something the gate does not actually consult.
const READ_FILES = [
  CONTRACT_DOC, CONTRACT_DOC_ZH,
  'scripts/clusterguard-update-job.sh', 'scripts/clusterguard-upgrade.sh', 'scripts/build-hotfix-patch.sh',
  'internal/platformupdate/manager.go', 'internal/api/updates.go', 'internal/api/console.html',
  'hotfixes/hotfix-publications.json', 'tools/verify-hotfix-patch-catalog.cjs',
  'tools/console-update-hotfix-recovery-acceptance.cjs', 'tools/verify-upgrade-validation-chain.cjs',
];

const MUTATIONS = [
  {
    name: 'resume+hotfix goes back to --resume',
    file: 'scripts/clusterguard-update-job.sh',
    find: 'if [[ "${requested_kind}" == hotfix ]]; then',
    replace: 'if false; then',
    rule: 'a hotfix resume is routed to a re-execution',
  },
  {
    name: 'the resume arm is reached for every kind',
    file: 'scripts/clusterguard-update-job.sh',
    find: 'if [[ "${requested_kind}" == hotfix ]]; then',
    replace: 'if [[ "${requested_kind}" != hotfix ]]; then',
    rule: 'a hotfix resume is routed to a re-execution',
  },
  {
    name: 'the foreign lock check stops refusing',
    file: 'scripts/clusterguard-upgrade.sh',
    find: '    die "维护门禁当前由 ${previous} 持有',
    replace: '    log "维护门禁当前由 ${previous} 持有',
    rule: 'a foreign maintenance lock is refused by name',
  },
  {
    name: 'the confirmation re-derives the verdict instead of reusing it',
    file: 'internal/api/console.html',
    find: "openSoftwareUpdateConfirmation('execute', patchID, softwareUpdateIsRetry(target))",
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
      failures.push(...open.map(obligation => `${obligation.key} is not implemented`));
    }
  }
  if (failures.length) {
    console.log(`${CHECKS.length} checks, ${failures.length} failed:`);
    for (const failure of failures) console.log(`  - ${failure}`);
    console.log('  That is a pass for the rules whose enforcement point still holds, and a failure for the rest.');
    process.exitCode = 1;
  } else {
    console.log(`${CHECKS.length} checks passed, ${open.length} obligation(s) open, 0 failed.`);
  }
}
