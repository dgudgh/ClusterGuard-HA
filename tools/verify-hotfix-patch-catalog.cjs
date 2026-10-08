#!/usr/bin/env node
/**
 * verify-hotfix-patch-catalog.cjs
 *
 * Regression gate for "every bug fix is covered by a patch".
 *
 * Before this gate existed a fix could be committed, tested and documented while
 * the only way for a running site to receive it was to wait for the next full
 * release: .cgupgrade carries whole RPMs and is applied by the rolling upgrade
 * executor, so it cannot express "one bug fix". Site 192.168.102.152-154 was
 * stuck on four fixes that had no deliverable at all — the reconcile flap that
 * fenced the authorized primary every five seconds, the two update-chain
 * prerequisites, and the console teardown after a host power-off.
 *
 * A patch is one per site visit, not one per commit. Two fixes that both rebuild
 * /usr/local/bin/clusterguard must not ship as two patches: applying the older
 * one last silently undoes the newer fix. The gate therefore also checks that
 * the payload of a patch really covers every fix it declares, and that each
 * release line's artifact directory is a single, unambiguous application entry
 * point for that baseline.
 *
 * The gate ties four things together so none of them can drift alone:
 *
 *   - hotfixes/*.json declares the fix commits a patch covers, the tree it was
 *     built from, and the bilingual description of every fix;
 *   - scripts/build-hotfix-patch.sh turns that declaration into a signed
 *     .cgpatch carrying rebuilt binaries, touched systemd units, the source
 *     diff, and an apply/rollback pair;
 *   - scripts/render-hotfix-catalog.cjs renders docs/hotfix-patches.md and
 *     docs/zh-CN/hotfix-patches.md from the artifacts that really exist;
 *   - every fix commit between the release baseline and HEAD must be covered,
 *     so committing a fix without building its patch fails the gate.
 *
 * Run: node tools/verify-hotfix-patch-catalog.cjs [--repo <path>]
 *                                                [--artifact-root <path>]
 *                                                [--public-key <file>]
 *
 * --repo is the tree that carries the declarations, the ledger and the rendered
 * catalogues; --artifact-root is the tree that carries release/. They are usually
 * the same, and they are separate for one real case: release/ is not committed, so
 * a linked worktree has the ledger but none of the artifacts it describes. Running
 * from a worktree without --artifact-root reports every single ledger entry as
 * "已登记的产物不在磁盘上" - a wall of failures that says nothing about the
 * artifacts and hides the checks that would otherwise have run.
 *
 * Pass --public-key (or set CG_HOTFIX_TRUSTED_PUBLIC_KEY) to also verify the
 * signature on every artifact. Without it that one check is skipped, and the
 * verdict says so instead of claiming a pass.
 */
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { execFileSync } = require('child_process');

const arg = (flag, fallback) => {
  const index = process.argv.indexOf(flag);
  return index !== -1 && process.argv[index + 1] ? process.argv[index + 1] : fallback;
};

const repo = path.resolve(arg('--repo', path.resolve(__dirname, '..')));
const artifactRoot = path.resolve(arg('--artifact-root', repo));
const publicKey = arg('--public-key', process.env.CG_HOTFIX_TRUSTED_PUBLIC_KEY || '');
// One release line per patch: a 2.2-103 site and a 2.2-104 site must never be
// pointed at the same directory, or an operator could apply a patch built for
// the wrong baseline (that silently downgrades binaries). Each spec therefore
// owns its own delivery directory, derived from the version it was built for.
const artifactDirFor = (body) => `release/${body.rpm_version}-${body.rpm_release}-hotfixes`;

const failures = [];
const skips = [];
const check = (name, ok, detail) => {
  if (ok) {
    console.log(`ok   ${name}`);
  } else {
    failures.push(name);
    console.error(`FAIL ${name}${detail ? ` — ${detail}` : ''}`);
  }
};
// A check that could not run is not a check that passed. Signature verification is
// the only one in this gate that needs something the repository does not carry: a
// trusted public key. Leaving the key optional is deliberate, so that a
// contributor without signing material can still gate the ledger, the coverage
// and the payload contents. But that convenience used to be indistinguishable
// from success - a keyless run printed 41 ok lines and exited 0 while proving
// nothing about whether these artifacts are the ones this project signed, and the
// only trace was one line in the middle of a long report. A skip is part of the
// verdict now, not a detail next to it.
const skip = (name, why) => {
  skips.push({ name, why });
  console.log(`skip ${name} — ${why}`);
};

const sha256File = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim();

// `hotfixes/` holds two kinds of document: one declaration per patch, and the
// publication ledger next to them. Both are JSON in the same directory, so the
// loader has to tell them apart explicitly. Reading the ledger as a declaration
// is not a near miss that the field checks would catch - the ledger has no
// base_commit, so the completeness check reports it and the gate then dies on
// `git log undefined..HEAD` with a stack trace, burying the verdict under it.
// A declaration is recognised by its id; every other JSON in this directory is
// named as unrecognised rather than silently skipped, so a mistyped filename
// cannot quietly drop a patch out of the gate's view. A file that does not even
// parse is reported the same way - these files are hand-edited, and a stray
// comma must read as "HF-2026-0929-05.json is broken", not as a stack trace
// from JSON.parse that looks like the gate itself fell over.
const specDirectory = path.join(repo, 'hotfixes');
const companionDocuments = new Set(['hotfix-publications.json']);
const documents = [];
const unparsableDocuments = [];
if (fs.existsSync(specDirectory)) {
  for (const name of fs.readdirSync(specDirectory).filter((entry) => entry.endsWith('.json')).sort()) {
    const file = path.join(specDirectory, name);
    try {
      documents.push({ name, path: file, body: JSON.parse(fs.readFileSync(file, 'utf8')) });
    } catch (error) {
      unparsableDocuments.push(`${name}: ${String(error.message).split('\n')[0]}`);
    }
  }
}
const specs = documents.filter((document) => !companionDocuments.has(document.name));
const unrecognised = specs
  .filter((document) => !/^HF-\d{4}-\d{4}-\d{2}$/.test(document.body.id || ''))
  .map((document) => document.name);
for (let index = specs.length - 1; index >= 0; index -= 1) {
  if (unrecognised.includes(specs[index].name)) specs.splice(index, 1);
}

check('every JSON document in hotfixes/ parses', unparsableDocuments.length === 0, unparsableDocuments.join('; '));
check('hotfix declarations exist', specs.length > 0, 'hotfixes/*.json is empty');
check('every JSON in hotfixes/ is a patch declaration or the publication ledger', unrecognised.length === 0,
  `${unrecognised.join(', ')} 既没有 HF-YYYY-MMDD-NN 形式的 id，也不是已知的伴随文件`);

// --- Gate 1: every declaration is complete and points at real commits ------
const incomplete = [];
const badCommits = [];
const resolveCommit = (reference) => {
  const resolved = git('rev-parse', `${reference}^{commit}`);
  return resolved;
};
for (const spec of specs) {
  const body = spec.body;
  for (const key of ['id', 'severity', 'base_commit', 'rpm_version', 'rpm_release']) {
    if (typeof body[key] !== 'string' || body[key].length === 0) incomplete.push(`${spec.name}:${key}`);
  }
  for (const block of ['title']) {
    for (const locale of ['zh', 'en']) {
      if (!body[block] || typeof body[block][locale] !== 'string' || body[block][locale].length === 0) {
        incomplete.push(`${spec.name}:${block}.${locale}`);
      }
    }
  }
  if (!Array.isArray(body.fix_commits) || body.fix_commits.length === 0) {
    incomplete.push(`${spec.name}:fix_commits`);
  }
  if (!Array.isArray(body.fixes) || body.fixes.length === 0) {
    incomplete.push(`${spec.name}:fixes`);
  }
  for (const [index, item] of (body.fixes || []).entries()) {
    for (const block of ['title', 'symptom', 'root_cause', 'fix']) {
      for (const locale of ['zh', 'en']) {
        if (!item[block] || typeof item[block][locale] !== 'string' || item[block][locale].length === 0) {
          incomplete.push(`${spec.name}:fixes[${index}].${block}.${locale}`);
        }
      }
    }
    if (typeof item.commit !== 'string' || item.commit.length === 0) incomplete.push(`${spec.name}:fixes[${index}].commit`);
    if (typeof item.severity !== 'string' || item.severity.length === 0) incomplete.push(`${spec.name}:fixes[${index}].severity`);
  }
  // The id format is what told the loader above that this file is a declaration
  // at all, so it cannot still be wrong here; asserting it twice would leave a
  // sentence no mutation can ever turn red.
  const declaredCommits = new Set((body.fix_commits || []).map((commit) => {
    try {
      return resolveCommit(commit);
    } catch (error) {
      return null;
    }
  }));
  for (const item of body.fixes || []) {
    let resolved = null;
    try {
      resolved = resolveCommit(item.commit);
    } catch (error) {
      resolved = null;
    }
    if (resolved && !declaredCommits.has(resolved)) incomplete.push(`${spec.name}:fixes[].commit 未出现在 fix_commits 中 (${item.commit})`);
  }
  try {
    for (const commit of body.fix_commits || []) {
      const resolved = resolveCommit(commit);
      git('merge-base', '--is-ancestor', body.base_commit, resolved);
    }
    if (body.build_commit) {
      const resolved = resolveCommit(body.build_commit);
      git('merge-base', '--is-ancestor', body.base_commit, resolved);
    }
  } catch (error) {
    badCommits.push(`${spec.name}:${body.base_commit}`);
  }
  if (body.patch_version !== undefined) {
    const match = typeof body.patch_version === 'string'
      ? body.patch_version.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/)
      : null;
    if (!match) {
      incomplete.push(`${spec.name}:patch_version 格式必须为 MAJOR.CAPABILITY.INTERNAL.BUGFIX`);
    } else {
      if (`${match[1]}.${match[2]}` !== body.rpm_version) {
        incomplete.push(`${spec.name}:patch_version 前两段必须等于 rpm_version`);
      }
      if (match[3] !== body.rpm_release) {
        incomplete.push(`${spec.name}:patch_version 内部功能段必须等于 rpm_release`);
      }
      if (Number(match[4]) < 1) incomplete.push(`${spec.name}:patch_version BUGFIX 必须从 1 开始`);
    }
  }
}
check('every declaration carries the id, severity, commits and bilingual text', incomplete.length === 0, incomplete.join(', '));
check('every declaration points at real fix commits whose baseline is an ancestor', badCommits.length === 0, badCommits.join(', '));

// A signed manifest is append-only: a wrong claim written into it can only be
// corrected by shipping another artifact, which is how one sentence turned into
// three extra revisions of a patch nobody had deployed. The `reason` field is
// therefore held to the single thing that cannot drift - why this identity
// replaces the previous one - and this check makes that structural rather than
// aspirational: no paths (they move; the ledger's own location changed the same
// day the field was first used) and no numbers (they get measured wrong; a byte
// count that held for one of the two binaries did not hold for the other).
const driftyReasons = [];
for (const spec of specs) {
  if ((spec.body.revision || 0) === 0) continue;
  const identity = spec.body.supersedes_artifact;
  if (!identity || typeof identity.reason !== 'string' || identity.reason.trim().length === 0) {
    driftyReasons.push(`${spec.name}: 修订理由为空`);
    continue;
  }
  for (const [label, pattern] of [['路径', /[/\\]/], ['量化事实', /\d/]]) {
    if (pattern.test(identity.reason)) driftyReasons.push(`${spec.name}: 修订理由包含会漂移的${label}`);
  }
  for (const key of ['file', 'sha256']) {
    if (typeof identity[key] !== 'string' || identity[key].length === 0) {
      driftyReasons.push(`${spec.name}: supersedes_artifact.${key} 缺失`);
    }
  }
}
check('a revision reason only says why the identity was replaced: no path, no number',
  driftyReasons.length === 0, driftyReasons.join('; '));

// The reverse of the check above. fix_commits is what the signed manifest
// promises the site, fixes[] is the only place an operator can read what each
// of those commits actually changed; the two were only ever compared in one
// direction, so a commit could sit in fix_commits with no entry at all. The
// commit table is rendered from fix_commits, so its short sha would still show
// up in both catalogues and every other check would stay green while the patch
// shipped a change nobody had described.
const undocumentedFixes = [];
for (const spec of specs) {
  const described = new Set((spec.body.fixes || []).map((item) => {
    try {
      return resolveCommit(item.commit);
    } catch (error) {
      return null;
    }
  }));
  for (const commit of spec.body.fix_commits || []) {
    let resolved = null;
    try {
      resolved = resolveCommit(commit);
    } catch (error) {
      continue;
    }
    if (!described.has(resolved)) {
      undocumentedFixes.push(`${spec.body.id}:${commit.slice(0, 7)} 在 fix_commits 中但没有对应的 fixes[] 条目`);
    }
  }
}
check('every declared fix commit is described by a bilingual fix entry',
  undocumentedFixes.length === 0, undocumentedFixes.join('; '));

// --- Gate 2: every declaration has a signed artifact -----------------------
// A revision is a *different* artifact, never a rewrite of the published one.
// Legacy declarations carry revision in the filename; versioned declarations
// carry the new Bug-fix segment in the filename and keep revision in the manifest.
const archiveName = (body) =>
  body.patch_version
    ? `clusterguard-${body.patch_version}.x86_64.cgpatch`
    : `clusterguard-ha-hotfix-${body.id}${body.revision ? `-r${body.revision}` : ''}`
      + `-${body.rpm_version}-${body.rpm_release}.x86_64.cgpatch`;

const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-hotfix-gate-'));
const cleanup = () => fs.rmSync(stage, { recursive: true, force: true });
process.on('exit', cleanup);

const unpack = (archive) => {
  const target = path.join(stage, path.basename(archive).replace(/\.cgpatch$/, ''));
  fs.mkdirSync(target, { recursive: true });
  execFileSync('tar', ['-xzf', archive, '-C', target]);
  return path.join(target, 'clusterguard-hotfix');
};

const missingArtifacts = [];
const mismatched = [];
const incompleteArchives = [];
const checksumMismatch = [];
// Problems are attributed to the artifact they were found in, not collected
// globally: a published artifact can never be repaired in place, so a defect
// found in one that has already been handed over is frozen - declared in the
// publication ledger and allowed to stay - while the artifact a release line
// currently offers has to be clean.
const problemsByArtifact = new Map();
const note = (artifact, message) => {
  if (!problemsByArtifact.has(artifact)) problemsByArtifact.set(artifact, []);
  problemsByArtifact.get(artifact).push(message);
};
const cleanArtifacts = [];

// --- Publication ledger ------------------------------------------------------
// A signed .cgpatch is the only record of what a site ran. Rebuilding one in
// place - same filename, same version, different bytes - destroys exactly that
// record: the site reports "2.2-105", the repository also has a "2.2-105", and
// the digests differ, so neither side can say what the nodes are running. So
// publishing appends an identity here and nothing already published is ever
// rewritten; a correction is a *new* artifact. Historical declarations use
// `-r1`, `-r2`, ...; versioned declarations advance the Bug-fix segment and keep
// the revision chain in the manifest. `status: "frozen"` marks an identity that is kept only
// as evidence - its bytes are never upgraded to today's rules - and it has to
// declare the defects it still carries, so the reason it is not uploadable is
// written down rather than merely implied.
// The ledger lives in the source tree, next to the declarations it complements,
// not in the media tree: `release/` has never been tracked by git, and a
// provenance record that exists on one machine only is not a record. Entries are
// keyed by the artifact's path relative to the artifact root, so the file
// travels with the branch while the artifacts stay where they are built.
const ledgerPath = path.join(repo, 'hotfixes/hotfix-publications.json');
let ledger = null;
let ledgerError = null;
if (fs.existsSync(ledgerPath)) {
  try {
    ledger = JSON.parse(fs.readFileSync(ledgerPath, 'utf8'));
  } catch (error) {
    // The ledger is hand-edited too; a syntax error in it must be reported as a
    // ledger problem, not thrown as a stack trace that hides every other check.
    ledgerError = String(error.message).split('\n')[0];
  }
}
const publications = new Map();
for (const entry of (ledger && ledger.publications) || []) publications.set(entry.file, entry);
const relativeToRoot = (file) => path.relative(artifactRoot, file).split(path.sep).join('/');
const undocumentedRestarts = [];
const unboundTooling = [];
const unsignedOrInvalid = [];
const sidecarMismatch = [];
const resolved = [];

for (const spec of specs) {
  const body = spec.body;
  const archive = path.join(artifactRoot, artifactDirFor(body), archiveName(body));
  if (!fs.existsSync(archive)) {
    missingArtifacts.push(`${body.id} -> ${archiveName(body)}`);
    continue;
  }
  const actualSha = sha256File(archive);
  const sidecar = `${archive}.sha256`;
  if (!fs.existsSync(sidecar) || !fs.readFileSync(sidecar, 'utf8').includes(actualSha)) {
    sidecarMismatch.push(body.id);
  }
  // A corrupt artifact used to blow up here as an unhandled exception, which
  // reads as a broken gate rather than as a broken package - and a stack trace
  // is exactly the kind of output that gets skimmed past. Report it as what it
  // is and keep going, so one bad file does not hide the rest.
  let root;
  try {
    root = unpack(archive);
  } catch (error) {
    incompleteArchives.push(`${body.id}: 产物无法解包（${String(error.message).split('\n')[0]}）`);
    continue;
  }
  const manifest = JSON.parse(fs.readFileSync(path.join(root, 'HOTFIX-MANIFEST.json'), 'utf8'));
  const declaredCommits = (body.fix_commits || []).map((commit) => git('rev-parse', `${commit}^{commit}`)).sort();
  const manifestCommits = [...(manifest.fix_commits || [])].sort();
  const declaredBuild = body.build_commit
    ? git('rev-parse', `${body.build_commit}^{commit}`)
    : declaredCommits[declaredCommits.length - 1];
  if (manifest.hotfix_id !== body.id ||
      declaredCommits.join(',') !== manifestCommits.join(',') ||
      manifest.build_commit !== declaredBuild ||
      manifest.source.version !== body.rpm_version || manifest.source.release !== body.rpm_release ||
      manifest.kind !== 'hotfix' ||
      (manifest.patch_version || null) !== (body.patch_version || null) ||
      (manifest.revision || 0) !== (body.revision || 0) ||
      JSON.stringify(manifest.supersedes_artifact || null) !== JSON.stringify(body.supersedes_artifact || null)) {
    mismatched.push(`${body.id}: manifest disagrees with hotfixes/${spec.name}`);
  }
  const requiredFiles = ['HOTFIX-MANIFEST.json', 'HOTFIX-MANIFEST.sig', 'SHA256SUMS', 'apply.sh', 'rollback.sh', 'README.md', manifest.source_patch];
  const absent = requiredFiles.filter((name) => !fs.existsSync(path.join(root, name)));
  if (absent.length) incompleteArchives.push(`${body.id}: ${absent.join(', ')}`);

  const sums = fs.readFileSync(path.join(root, 'SHA256SUMS'), 'utf8').trim().split('\n')
    .map((line) => line.split(/\s+/)).filter((parts) => parts.length >= 2);
  for (const [expected, artifact] of sums) {
    if (sha256File(path.join(root, artifact)) !== expected) checksumMismatch.push(`${body.id}:${artifact}`);
  }
  for (const script of ['apply.sh', 'rollback.sh']) {
    try {
      execFileSync('bash', ['-n', path.join(root, script)]);
    } catch (error) {
      note(archive, `${body.id}:${script}`);
    }
  }
  // Only executable lines count. These generated scripts document the rules they
  // follow, and the comment explaining why rollback.sh may only use this patch's
  // own manifest necessarily names the backup-*.txt glob it forbids. Testing the
  // whole file lets prose satisfy a requirement or trip a prohibition: the first
  // version of the glob ban fired on its own explanation in all four packages.
  const code = (script) => script.split('\n').filter((line) => !/^\s*#/.test(line)).join('\n');
  const apply = fs.readFileSync(path.join(root, 'apply.sh'), 'utf8');
  const applyCode = code(apply);
  if (!/sha256sum -c SHA256SUMS/.test(applyCode)) note(archive, `${body.id}:apply.sh 未校验 SHA256SUMS`);
  if (/^\s*systemctl restart /m.test(applyCode)) note(archive, `${body.id}:apply.sh 自动重启服务`);
  for (const entry of manifest.files) {
    if (entry.install_path && !applyCode.includes(entry.install_path)) {
      note(archive, `${body.id}:apply.sh 未安装 ${entry.install_path}`);
    }
  }
  // The files a hotfix replaces include running binaries, so restoring one by
  // writing over the destination in place fails with ETXTBSY on every node. That
  // is how the automatic rollback of HF-2026-0929-04 failed on all three of them
  // and left the maintenance gate stuck. The restored file has to be staged
  // beside its destination and renamed over it, which swaps the inode and leaves
  // any running process untouched.
  const rollbackScript = fs.readFileSync(path.join(root, 'rollback.sh'), 'utf8');
  const rollbackCode = code(rollbackScript);
  if (!/mv -f "\$\{restore_tmp\}"/.test(rollbackCode)) {
    note(archive, `${body.id}:rollback.sh 未用改名换 inode 恢复文件（就地写运行中的二进制会 ETXTBSY）`);
  }
  if (/cp -p "\$\{backup\}" "\$\{destination\}"/.test(rollbackCode)) {
    note(archive, `${body.id}:rollback.sh 直接覆盖目标文件（运行中的二进制会 ETXTBSY）`);
  }
  // rollback.sh may only use this patch's own backup manifest. Taking whichever
  // backup-*.txt happens to be newest restores an unrelated set of files, and
  // restore_backup() reads "not in the manifest" as "did not exist before the
  // patch" - so for a destination the wrong manifest fails to mention, it deletes
  // the live file instead of restoring it.
  if (!applyCode.includes(`/backup-${manifest.hotfix_id}-`)) {
    note(archive, `${body.id}:apply.sh 未把备份清单绑定到本补丁的 hotfix id`);
  }
  if (!rollbackCode.includes(`/backup-${manifest.hotfix_id}-`)) {
    note(archive, `${body.id}:rollback.sh 未把备份清单绑定到本补丁的 hotfix id`);
  }
  if (/backup-\*\.txt/.test(rollbackCode)) {
    note(archive, `${body.id}:rollback.sh 接受任意备份清单，可能恢复别的补丁的文件或误删文件`);
  }
  // On the command-line path there is no daemon to consult the manifest, so the
  // units that have to restart can only reach the operator through the script's
  // own output. A replaced binary whose unit is never restarted leaves the old
  // process running: the patch applies cleanly and nothing changes.
  for (const unit of new Set(manifest.files.map((file) => file.restart_unit).filter(Boolean))) {
    if (!applyCode.includes(unit)) note(archive, `${body.id}:apply.sh 未提示重启 ${unit}`);
  }
  // README.md is the other half of that same instruction, and the one an operator
  // actually reads before running apply.sh. It used to print a single hard-coded
  // unit while the payload replaced three: following it left
  // clusterguard-update-helper and clusterguard-agent-reconcile running their old
  // binaries, which is the same "applies cleanly, changes nothing" shape the
  // apply.sh fix removed. Checking only apply.sh would leave the blind spot open.
  //
  // The check is per section, not on the whole file: the Apply and the Rollback
  // blocks each have to name every unit, because an operator following one of them
  // never reads the other. A whole-file check passes as soon as the string appears
  // anywhere, so deleting the line from Apply while Rollback keeps it stayed green.
  const readme = fs.readFileSync(path.join(root, 'README.md'), 'utf8');
  const sections = {};
  for (const part of readme.split(/^## /m)) {
    const newline = part.indexOf('\n');
    if (newline === -1) continue;
    sections[part.slice(0, newline).trim()] = part.slice(newline + 1);
  }
  const applySection = sections['应用 / Apply'] || '';
  const rollbackSection = sections['回滚 / Rollback'] || '';
  if (!applySection || !rollbackSection) {
    undocumentedRestarts.push(`${body.id}:README 缺少「应用 / Apply」或「回滚 / Rollback」节`);
  }
  for (const unit of new Set(manifest.files.map((file) => file.restart_unit).filter(Boolean))) {
    if (!applySection.includes(`systemctl restart ${unit}`)) undocumentedRestarts.push(`${body.id}:README 应用节未提示重启 ${unit}`);
    if (!rollbackSection.includes(`systemctl restart ${unit}`)) undocumentedRestarts.push(`${body.id}:README 回滚节未提示重启 ${unit}`);
  }
  // apply.sh and rollback.sh run as root on every node and decide what is written
  // where, so the signed manifest has to anchor them. Before 2026-09-29 they were
  // generated after the manifest was signed and covered by nothing: a swapped
  // apply.sh satisfied every payload digest while installing something else.
  if (manifest.schema_version !== 3) unboundTooling.push(`${body.id}:schema_version 必须为 3`);
  for (const [name, declared] of [
    ['apply.sh', manifest.tooling && manifest.tooling.apply && manifest.tooling.apply.sha256],
    ['rollback.sh', manifest.tooling && manifest.tooling.rollback && manifest.tooling.rollback.sha256],
  ]) {
    const actual = sha256File(path.join(root, name));
    if (declared !== actual) unboundTooling.push(`${body.id}:${name} 的摘要未写入签名清单`);
    if (!sums.some(([expected, artifact]) => artifact === name && expected === actual)) {
      unboundTooling.push(`${body.id}:SHA256SUMS 未覆盖 ${name}`);
    }
  }
  if (publicKey) {
    try {
      execFileSync('openssl', ['dgst', '-sha256', '-verify', publicKey,
        '-signature', path.join(root, 'HOTFIX-MANIFEST.sig'), path.join(root, 'HOTFIX-MANIFEST.json')],
        { stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (error) {
      unsignedOrInvalid.push(body.id);
    }
  }
  resolved.push({ body, archive, sha: actualSha, manifest, publication: publications.get(relativeToRoot(archive)) });
}

check('every declaration has its .cgpatch artifact', missingArtifacts.length === 0, missingArtifacts.join(', '));
check('every artifact has a matching .sha256 sidecar', sidecarMismatch.length === 0, sidecarMismatch.join(', '));
check('every artifact manifest agrees with its declaration', mismatched.length === 0, mismatched.join('; '));
check('every artifact carries manifest, signature, checksums, scripts, README and source diff', incompleteArchives.length === 0, incompleteArchives.join('; '));
check('every artifact checksum file matches its payload', checksumMismatch.length === 0, checksumMismatch.join(', '));
// A frozen identity is kept as evidence of what a site ran, so its bytes stay as
// they are even when today's rules would have generated them differently. That
// exemption is not a free pass: every problem found in a frozen artifact has to
// be declared, and every declared defect has to still be found. The two
// directions matter equally - an undeclared defect hides a known-bad package,
// and a stale declaration would let a defect be repaired silently under cover of
// a note that says it is still there.
const liveProblems = [];
const undeclaredDefects = [];
const staleDefects = [];
for (const item of resolved) {
  const found = problemsByArtifact.get(item.archive) || [];
  const declared = (item.publication && item.publication.known_defects) || [];
  if (item.publication && item.publication.status === 'frozen') {
    for (const problem of found) {
      if (!declared.includes(problem)) undeclaredDefects.push(`${item.body.id}: 冻结产物有未声明的缺陷 — ${problem}`);
    }
    for (const defect of declared) {
      if (!found.includes(defect)) staleDefects.push(`${item.body.id}: 已声明的缺陷不再存在 — ${defect}`);
    }
  } else {
    liveProblems.push(...found);
  }
}
check('apply.sh and rollback.sh are valid, verify checksums, install every path and never restart by themselves',
  liveProblems.length === 0, liveProblems.join('; '));
check('a frozen publication declares exactly the defects it still carries',
  undeclaredDefects.length === 0 && staleDefects.length === 0,
  [...undeclaredDefects, ...staleDefects].join('; '));
check('README names every unit the payload replaces in both its Apply and its Rollback block', undocumentedRestarts.length === 0, undocumentedRestarts.join('; '));
check('the signed manifest anchors apply.sh and rollback.sh and SHA256SUMS covers them', unboundTooling.length === 0, unboundTooling.join('; '));
if (publicKey) {
  check('every artifact signature verifies against the trusted public key', unsignedOrInvalid.length === 0, unsignedOrInvalid.join(', '));
} else {
  skip('every artifact signature verifies against the trusted public key',
    'no trusted public key given: pass --public-key or set CG_HOTFIX_TRUSTED_PUBLIC_KEY');
}

// --- Gate 2b: the payload really covers every declared fix ------------------
// A patch that declares a fix but does not ship the artifact that fix needs is
// worse than no patch: the site believes it is fixed. Derive the requirement
// from each fix commit on its own, exactly like the builder does, and express it
// as the *site path* the fix has to reach rather than as an artifact name —
// packaging/rpm/nfpm.yaml is what decides where a file lives.
const mapping = require(path.join(__dirname, '..', 'scripts', 'hotfix-payload-map.cjs'));
// Which artifact carries a change is a fact about the import graph, so the same
// module the builder uses answers it here too. Hard-coded path prefixes used to
// answer it, and a fix in a shared package — internal/coordination, where a VIP
// ownership lease survived a backwards clock step and disabled automatic
// failover — matched neither prefix: the requirement came out empty, the patch
// shipped no binary, and this gate reported the fix as delivered.
const components = require(path.join(__dirname, '..', 'scripts', 'hotfix-component-map.cjs'));
const nfpmText = fs.readFileSync(path.join(__dirname, '..', 'packaging', 'rpm', 'nfpm.yaml'), 'utf8');
const nfpmDestinations = mapping.parseNfpmDestinations(nfpmText);
const componentResolver = components.resolver(repo, { nfpmText });

const requiredDestinations = (commit) => {
  const changed = git('diff', '--name-only', `${commit}^..${commit}`).split('\n').filter(Boolean);
  const binaries = componentResolver.binariesForFiles(changed);
  const units = [];
  const scripts = [];
  for (const file of changed.filter((entry) => /^packaging\/systemd\/.*\.(service|timer)$/.test(entry))) {
    units.push(file.replace(/^packaging\/systemd\//, ''));
  }
  for (const file of changed.filter((entry) => /^scripts\/clusterguard-[a-z0-9-]+\.sh$/.test(entry))) {
    scripts.push(file.replace(/^scripts\//, ''));
  }
  const resolved = mapping.resolvePayloadItems({ binaries, units, scripts }, nfpmText);
  return {
    paths: resolved.entries.map((entry) => entry.install_path),
    unmapped: resolved.missing.concat(resolved.problems),
    installerOnly: changed.includes('scripts/install_clusterguard.sh'),
  };
};

const underdelivered = [];
const unmappedRequirements = [];
for (const item of resolved) {
  const shipped = new Set(item.manifest.files.map((file) => file.install_path).filter(Boolean));
  const kinds = new Set(item.manifest.files.map((file) => file.kind));
  for (const commit of item.manifest.fix_commits || []) {
    const { paths, unmapped, installerOnly } = requiredDestinations(commit);
    for (const problem of unmapped) unmappedRequirements.push(`${item.body.id}:${commit.slice(0, 7)} ${problem}`);
    for (const destination of paths) {
      if (!shipped.has(destination)) underdelivered.push(`${item.body.id}:${commit.slice(0, 7)} 需要 ${destination}`);
    }
    if (installerOnly && !kinds.has('installer')) {
      underdelivered.push(`${item.body.id}:${commit.slice(0, 7)} 需要 installer payload`);
    }
  }
}
check('every declared fix is really present in the payload', underdelivered.length === 0, underdelivered.join('; '));
check('every runtime script a fix touches is declared by the packaging manifest',
  unmappedRequirements.length === 0, unmappedRequirements.join('; '));

// --- Gate 2c: payload files must install where the product looks for them ----
// The site path, mode and ownership of every shipped file are decided by the RPM
// packaging. Deriving them from the source name shipped the 2026-09-28 console
// fix to /usr/local/libexec/clusterguard-upgrade.sh while the product executes
// /usr/local/sbin/clusterguard-upgrade: the patch applied cleanly, the operator
// verified the wrong file, and the behaviour never changed.
const destinationDrift = [];
const sourceKeyFor = (entry) => {
  const base = path.basename(entry.artifact);
  if (entry.kind === 'binary') return `bin/${base}`;
  if (entry.kind === 'systemd_unit') return `packaging/${base}`;
  if (entry.kind === 'runtime_script') return `scripts/${base}`;
  return null; // installer-only payload has no site path by design
};
for (const item of resolved) {
  for (const entry of item.manifest.files) {
    const key = sourceKeyFor(entry);
    if (!key) continue;
    const declared = nfpmDestinations.get(key);
    if (!declared || !declared.dst) {
      destinationDrift.push(`${item.body.id}:${key} 未在打包清单声明`);
      continue;
    }
    if (entry.install_path !== declared.dst) {
      destinationDrift.push(`${item.body.id}:${key} 落点 ${entry.install_path} ≠ 打包清单 ${declared.dst}`);
    }
    const declaredMode = mapping.normalizeMode(declared.mode);
    if (declaredMode && entry.mode !== declaredMode) {
      destinationDrift.push(`${item.body.id}:${key} 模式 ${entry.mode} ≠ 打包清单 ${declaredMode}`);
    }
    const owner = entry.owner || 'root';
    const group = entry.group || 'root';
    if (owner !== (declared.owner || 'root') || group !== (declared.group || 'root')) {
      destinationDrift.push(`${item.body.id}:${key} 属主 ${owner}:${group} ≠ 打包清单 ${declared.owner || 'root'}:${declared.group || 'root'}`);
    }
  }
}
check('every payload file installs where the packaging manifest says it lives',
  destinationDrift.length === 0, destinationDrift.join('; '));

// A patch may be built from a ported tree, but that tree must still be "the
// release baseline plus the declared fixes". Fixes made on a development line
// often share a file with an unreleased feature, and shipping that feature
// inside a hotfix is how a site gets an unvalidated change.
//
// "Production" is decided by the import graph, not by a prefix list, so a change
// in a shared package cannot slip past this check by not looking like one of the
// two binaries.
const productionFiles = (range) => git('diff', '--name-only', range).split('\n').filter(Boolean)
  .filter((file) => componentResolver.isProductionPath(file));

const contaminated = [];
for (const item of resolved) {
  const declared = new Set();
  for (const commit of item.manifest.fix_commits || []) {
    for (const file of productionFiles(`${commit}^..${commit}`)) declared.add(file);
  }
  const extra = productionFiles(`${item.manifest.base_commit}..${item.manifest.build_commit}`)
    .filter((file) => !declared.has(file));
  if (extra.length) contaminated.push(`${item.body.id}: ${extra.join(', ')}`);
}
check('the build tree carries no production change beyond the declared fixes', contaminated.length === 0, contaminated.join('; '));

// A declared fix commit must actually be in the tree the patch was built from.
// The 2.2-103 line is a ported branch, so its five fixes exist there as
// cherry-picks with different hashes; declaring the originals made the signed
// manifest and the published catalogue name commits the archive does not
// contain at all. Ancestry is the only thing that tells the two apart, so both
// directions are checked: nothing declared that is absent from the tree, and
// nothing in the tree left undeclared.
const foreignFixes = [];
for (const item of resolved) {
  for (const commit of item.manifest.fix_commits || []) {
    try {
      git('merge-base', '--is-ancestor', commit, item.manifest.build_commit);
    } catch (error) {
      foreignFixes.push(`${item.body.id}:${commit.slice(0, 7)} 不在构建树 ${item.manifest.build_commit.slice(0, 7)} 中`);
    }
  }
}
check('every declared fix commit is an ancestor of the build tree', foreignFixes.length === 0, foreignFixes.join('; '));

// The file-level scan above cannot see an undeclared commit that edits a file
// another declared fix already touched — exactly the case for console.html,
// which three of these fixes share. Compare commits, not files: any commit in
// the build range that reaches a production path must be declared, otherwise
// the tree embeds a change the manifest denies shipping.
const unauthorisedFixes = [];
for (const item of resolved) {
  // A private delivery can carry an explicit proof of retained, already-fielded
  // port history. This does not exempt any new runtime change or change the base.
  if (item.manifest.baseline_history?.length) {
    try {
      if (JSON.stringify(item.manifest.baseline_history) !== JSON.stringify(item.body.baseline_history)) throw Error('signed baseline history differs from spec');
      const result = require('./hotfix-build-accounting.cjs').account(repo, item.manifest.base_commit,
        item.manifest.build_commit, item.manifest.fix_commits, item.manifest.baseline_history);
      for (const commit of result.undeclared) unauthorisedFixes.push(`${item.body.id}:${commit.slice(0,7)} 改动了生产路径但未被声明`);
    } catch (error) {
      unauthorisedFixes.push(`${item.body.id}:${error.message}`);
    }
    continue;
  }
  const declaredCommits = new Set(item.manifest.fix_commits || []);
  const ranged = git('log', '--format=%H', `${item.manifest.base_commit}..${item.manifest.build_commit}`)
    .split('\n').filter(Boolean);
  for (const commit of ranged) {
    if (declaredCommits.has(commit)) continue;
    if (productionFiles(`${commit}^..${commit}`).length > 0) {
      unauthorisedFixes.push(`${item.body.id}:${commit.slice(0, 7)} 改动了生产路径但未被声明`);
    }
  }
}
check('every production change in the build tree is an accounted-for declared fix',
  unauthorisedFixes.length === 0, unauthorisedFixes.join('; '));

const declaredDirs = new Map();
for (const spec of specs) {
  const dir = artifactDirFor(spec.body);
  declaredDirs.set(dir, (declaredDirs.get(dir) || 0) + 1);
}
// A delivery directory is what an operator is pointed at, so it must offer one
// unambiguous thing to apply. Several .cgpatch files sitting in it is normal and
// intended: a superseded identity keeps its bytes, because those bytes are the
// evidence of what some site once ran, and deleting them to tidy a directory
// would destroy exactly the record this ledger exists to keep. What would be
// ambiguous is two *current* identities in the same directory - the operator's
// choice would then depend on a directory listing rather than on the ledger. So
// the count that matters comes from the ledger, not from the filesystem.
const badDirCounts = [];
const currentByLine = new Map();
for (const [dir] of declaredDirs) {
  const offered = [...publications.entries()]
    .filter(([file, entry]) => entry.status === 'current' && path.posix.dirname(file) === dir)
    .map(([file]) => file);
  currentByLine.set(dir, offered);
  if (offered.length > 1) {
    badDirCounts.push(`${dir}: ${offered.length} 个 current 产物（${offered.join(', ')}）`);
  }
}
check('every release line offers at most one current artifact in its delivery directory',
  badDirCounts.length === 0, badDirCounts.join('; '));

// A .cgpatch sitting in a release line that no declaration claims is a trap:
// the renderer would still list it, and an operator could apply a patch the
// gate never validated. Scan every release/*-hotfixes directory for strays.
const strayArchives = [];
const releaseRoot = path.join(artifactRoot, 'release');
if (fs.existsSync(releaseRoot)) {
  for (const entry of fs.readdirSync(releaseRoot)) {
    const candidate = path.join(releaseRoot, entry);
    if (!fs.statSync(candidate).isDirectory() || !entry.endsWith('-hotfixes')) continue;
    if (declaredDirs.has(`release/${entry}`)) continue;
    for (const name of fs.readdirSync(candidate)) {
      if (name.endsWith('.cgpatch')) strayArchives.push(`release/${entry}/${name}`);
    }
  }
}
check('no stray .cgpatch hides in an undeclared release line', strayArchives.length === 0, strayArchives.join(', '));

// --- Publication identity is append-only ------------------------------------
const deliveryArchives = [];
for (const dir of declaredDirs.keys()) {
  const directory = path.join(artifactRoot, dir);
  if (!fs.existsSync(directory)) continue;
  for (const name of fs.readdirSync(directory)) {
    if (name.endsWith('.cgpatch')) deliveryArchives.push(`${dir}/${name}`);
  }
}
const unregistered = deliveryArchives.filter((file) => !publications.has(file));
check('the publication ledger parses', ledgerError === null, `${ledgerPath} — ${ledgerError || ''}`);
check('every artifact a release line offers is registered in the publication ledger',
  unregistered.length === 0, unregistered.join(', '));

const rewritten = [];
const misplaced = [];
for (const [file, entry] of publications) {
  const absolute = path.join(artifactRoot, file);
  const present = fs.existsSync(absolute);
  if (entry.bytes_retained === false) {
    // The digest is still the record of what a site ran, so the entry stays even
    // when the bytes are gone; what must not happen is a *different* file being
    // passed off as it.
    if (present) misplaced.push(`${file}: 登记为字节未保留，但文件仍在磁盘上`);
    continue;
  }
  if (!present) {
    misplaced.push(`${file}: 已登记的产物不在磁盘上`);
    continue;
  }
  const actual = sha256File(absolute);
  if (actual !== entry.sha256 || fs.statSync(absolute).size !== entry.size) {
    rewritten.push(`${file}: 磁盘 ${actual.slice(0, 12)}…/${fs.statSync(absolute).size} ≠ 已发布 ${entry.sha256.slice(0, 12)}…/${entry.size}`);
  }
}
check('no published artifact is rewritten in place, and none that lost its bytes is still present',
  rewritten.length === 0 && misplaced.length === 0, [...rewritten, ...misplaced].join('; '));

const brokenChain = [];
for (const [file, entry] of publications) {
  const revision = entry.revision || 0;
  if (revision === 0) {
    if (entry.supersedes_sha256) brokenChain.push(`${file}: 首次发布不应声明替代对象`);
    continue;
  }
  if (!file.includes(`-r${revision}-`)) {
    brokenChain.push(`${file}: 修订身份必须写进文件名（-r${revision}）`);
  }
  if (!entry.supersedes_sha256) {
    brokenChain.push(`${file}: 修订必须声明它替代的身份`);
    continue;
  }
  const previous = [...publications.entries()].find(([, other]) => other.sha256 === entry.supersedes_sha256);
  if (!previous) {
    brokenChain.push(`${file}: 替代的摘要 ${String(entry.supersedes_sha256).slice(0, 12)}… 不在台账里`);
    continue;
  }
  if ((previous[1].revision || 0) !== revision - 1) {
    brokenChain.push(`${file}: 只能替代上一修订 r${revision - 1}`);
  }
  if (previous[1].superseded_by !== file) {
    brokenChain.push(`${file}: 被替代的 ${previous[0]} 没有回指本身份`);
  }
}
check('a revision names the identity it supersedes, and that identity points back',
  brokenChain.length === 0, brokenChain.join('; '));

const notCurrent = [];
for (const item of resolved) {
  const entry = item.publication;
  if (!entry) {
    notCurrent.push(`${item.body.id}: 产物的身份未登记`);
    continue;
  }
  if (!['current', 'frozen', 'superseded'].includes(entry.status)) {
    notCurrent.push(`${item.body.id}: 未知的产物状态 ${entry.status}`);
    continue;
  }
  // The three statuses mean different things to an operator reading the catalogue
  // and the difference decides what this check may demand:
  //   current    - this is the artifact the line offers; correct target.
  //   frozen     - a retired line whose bytes are kept as evidence (HF-2026-0929-03
  //                and -04 were applied at a site and are not rebuildable). A
  //                declaration pointing at one is a historical record, not a
  //                pointer an operator should follow blindly, so it is legitimate
  //                even when a newer patch exists on the same line.
  //   superseded - a newer revision of *this same patch* exists. A declaration
  //                pointing here would send the operator to a retired revision,
  //                which is the one combination that is never correct.
  if (entry.status === 'superseded') {
    const offered = currentByLine.get(artifactDirFor(item.body)) || [];
    notCurrent.push(`${item.body.id}: 声明指向已被取代的产物 ${entry.file}（本线当前入口：${offered.join(', ') || '无'}）`);
  }
}
check('no declaration points at a superseded artifact: the catalogue must name the newest revision',
  notCurrent.length === 0, notCurrent.join('; '));

// --- Gate 3: the bilingual catalogue is rendered and truthful --------------
const catalogueEnglish = path.join(repo, 'docs/hotfix-patches.md');
const catalogueChinese = path.join(repo, 'docs/zh-CN/hotfix-patches.md');
check('the hotfix catalogue exists in both languages',
  fs.existsSync(catalogueEnglish) && fs.existsSync(catalogueChinese));

if (fs.existsSync(catalogueEnglish) && fs.existsSync(catalogueChinese)) {
  const english = fs.readFileSync(catalogueEnglish, 'utf8');
  const chinese = fs.readFileSync(catalogueChinese, 'utf8');
  const absentIds = [];
  const staleSha = [];
  const absentCommits = [];
  for (const item of resolved) {
    for (const [label, text] of [['en', english], ['zh', chinese]]) {
      if (!text.includes(item.body.id)) absentIds.push(`${label}:${item.body.id}`);
      if (!text.includes(item.sha)) staleSha.push(`${label}:${item.body.id}`);
      for (const commit of item.manifest.fix_commits || []) {
        if (!text.includes(commit.slice(0, 7))) absentCommits.push(`${label}:${commit.slice(0, 7)}`);
      }
    }
  }
  check('both catalogues list every hotfix id', absentIds.length === 0, absentIds.join(', '));
  check('both catalogues quote the sha256 of the artifact that is on disk', staleSha.length === 0, staleSha.join(', '));
  check('both catalogues list every fix commit the patch covers', absentCommits.length === 0, absentCommits.join(', '));
  check('the catalogue is generated, not hand-written',
    /Generated by `scripts\/render-hotfix-catalog\.cjs`/.test(english) &&
    /由 `scripts\/render-hotfix-catalog\.cjs`/.test(chinese));
}

// --- Gate 4: no fix commit can escape without a patch ----------------------
// Every lookup here is best-effort. A declaration whose baseline or fix commit
// does not resolve is already reported by name in the two checks above; letting
// `git log <bad ref>..HEAD` throw instead would replace that verdict with a
// stack trace, and the coverage question this section asks would go unanswered.
const declared = new Set();
for (const spec of specs) {
  for (const commit of spec.body.fix_commits || []) {
    try {
      declared.add(git('rev-parse', `${commit}^{commit}`));
    } catch (error) {
      // Unresolvable fix commit; the declaration checks already failed for it.
    }
  }
}
const baselines = [...new Set(specs
  .map((spec) => spec.body.base_commit)
  .filter((baseline) => typeof baseline === 'string' && baseline.length > 0))];
const undeclaredFixes = [];
for (const baseline of baselines) {
  let commits;
  try {
    commits = git('log', '--format=%H%x09%s', `${baseline}..HEAD`).split('\n').filter(Boolean);
  } catch (error) {
    // Unresolvable baseline; the declaration checks already failed for it.
    continue;
  }
  for (const line of commits) {
    const [hash, subject] = line.split('\t');
    if (!/^fix(\([^)]+\))?:/.test(subject)) continue;
    // Only a fix that touches a production path needs a site patch. A fix to
    // this gate, the builder, the renderer or the documentation ships with the
    // source tree; demanding a patch for it would make the gate unsatisfiable
    // (the builder dies when a fix has no deliverable artifact).
    const touchesPayload = productionFiles(`${hash}^..${hash}`).length > 0;
    if (!touchesPayload) continue;
    if (!declared.has(hash)) undeclaredFixes.push(`${hash.slice(0, 7)} ${subject}`);
  }
}
check('every bug fix commit since the release baseline is covered by a patch', undeclaredFixes.length === 0, undeclaredFixes.join('; '));

// --- Gate 5: the builder and renderer are wired into the repository --------
const builder = path.join(repo, 'scripts/build-hotfix-patch.sh');
const renderer = path.join(repo, 'scripts/render-hotfix-catalog.cjs');
check('the patch builder and catalogue renderer are present', fs.existsSync(builder) && fs.existsSync(renderer));
if (fs.existsSync(builder)) {
  const source = fs.readFileSync(builder, 'utf8');
  check('the builder refuses to build when the signing key does not match the trusted public key',
    /补丁签名私钥与预期受信公钥不匹配/.test(source));
  check('the builder validates the generated scripts before packaging',
    /bash -n "\$\{root\}\/apply\.sh"/.test(source) && /bash -n "\$\{root\}\/rollback\.sh"/.test(source));
  check('the builder binds a new patch_version to the versioned archive name and manifest',
    /archive_name="clusterguard-\$\{patch_version\}\.\$\{rpm_arch\}\.cgpatch"/.test(source) &&
    /patch_version: spec\.patch_version \|\| null/.test(source));
  // The ledger check above can only see a rewrite after it happened. This one is
  // the prevention: the builder must stop before touching a path that already
  // holds an artifact, and send the operator to a new identity instead.
  check('the builder refuses to rebuild a published artifact in place',
    /产物已存在，拒绝原地覆盖/.test(source) && /revision/.test(source) && /supersedes_artifact/.test(source));
}
if (fs.existsSync(renderer)) {
  const rendererSource = fs.readFileSync(renderer, 'utf8');
  check('the renderer reads manifests from the artifacts instead of trusting a list',
    /tar/.test(rendererSource) && /HOTFIX-MANIFEST\.json/.test(rendererSource));
  // A delivery directory holds several identities of the same patch, so "the
  // newest file" is a guess and every filename is not an answer. The catalogue
  // tells an operator which one to upload, and that answer has to come from the
  // ledger - the same source the gate itself trusts. Otherwise the document and
  // the gate could disagree about which artifact is current, and only the gate
  // would be right.
  check('the renderer takes the current identity from the ledger, not from the newest filename',
    /hotfix-publications\.json/.test(rendererSource) && /status === "current"/.test(rendererSource));
}

if (failures.length) {
  console.error(`\n${failures.length} check(s) failed.`);
  process.exit(1);
}
if (skips.length) {
  console.log(`\nno check failed, but ${skips.length} did not run:`);
  for (const item of skips) {
    console.log(`  - ${item.name} — ${item.why}`);
  }
  console.log('that is not a pass: nothing here proves these are the artifacts this project signed.');
} else {
  console.log('\nall hotfix patch catalogue checks passed.');
}
