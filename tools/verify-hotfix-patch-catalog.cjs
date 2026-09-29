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
const check = (name, ok, detail) => {
  if (ok) {
    console.log(`ok   ${name}`);
  } else {
    failures.push(name);
    console.error(`FAIL ${name}${detail ? ` — ${detail}` : ''}`);
  }
};

const sha256File = (file) => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8' }).trim();

const specDirectory = path.join(repo, 'hotfixes');
const specs = fs.existsSync(specDirectory)
  ? fs.readdirSync(specDirectory).filter((name) => name.endsWith('.json')).sort()
    .map((name) => ({ name, path: path.join(specDirectory, name), body: JSON.parse(fs.readFileSync(path.join(specDirectory, name), 'utf8')) }))
  : [];

check('hotfix declarations exist', specs.length > 0, 'hotfixes/*.json is empty');

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
  if (!/^HF-\d{4}-\d{4}-\d{2}$/.test(body.id || '')) incomplete.push(`${spec.name}:id-format`);
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
}
check('every declaration carries the id, severity, commits and bilingual text', incomplete.length === 0, incomplete.join(', '));
check('every declaration points at real fix commits whose baseline is an ancestor', badCommits.length === 0, badCommits.join(', '));

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
const archiveName = (body) =>
  `clusterguard-ha-hotfix-${body.id}-${body.rpm_version}-${body.rpm_release}.x86_64.cgpatch`;

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
const badScripts = [];
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
  const root = unpack(archive);
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
      manifest.kind !== 'hotfix') {
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
      badScripts.push(`${body.id}:${script}`);
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
  if (!/sha256sum -c SHA256SUMS/.test(applyCode)) badScripts.push(`${body.id}:apply.sh 未校验 SHA256SUMS`);
  if (/^\s*systemctl restart /m.test(applyCode)) badScripts.push(`${body.id}:apply.sh 自动重启服务`);
  for (const entry of manifest.files) {
    if (entry.install_path && !applyCode.includes(entry.install_path)) {
      badScripts.push(`${body.id}:apply.sh 未安装 ${entry.install_path}`);
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
    badScripts.push(`${body.id}:rollback.sh 未用改名换 inode 恢复文件（就地写运行中的二进制会 ETXTBSY）`);
  }
  if (/cp -p "\$\{backup\}" "\$\{destination\}"/.test(rollbackCode)) {
    badScripts.push(`${body.id}:rollback.sh 直接覆盖目标文件（运行中的二进制会 ETXTBSY）`);
  }
  // rollback.sh may only use this patch's own backup manifest. Taking whichever
  // backup-*.txt happens to be newest restores an unrelated set of files, and
  // restore_backup() reads "not in the manifest" as "did not exist before the
  // patch" - so for a destination the wrong manifest fails to mention, it deletes
  // the live file instead of restoring it.
  if (!applyCode.includes(`/backup-${manifest.hotfix_id}-`)) {
    badScripts.push(`${body.id}:apply.sh 未把备份清单绑定到本补丁的 hotfix id`);
  }
  if (!rollbackCode.includes(`/backup-${manifest.hotfix_id}-`)) {
    badScripts.push(`${body.id}:rollback.sh 未把备份清单绑定到本补丁的 hotfix id`);
  }
  if (/backup-\*\.txt/.test(rollbackCode)) {
    badScripts.push(`${body.id}:rollback.sh 接受任意备份清单，可能恢复别的补丁的文件或误删文件`);
  }
  // On the command-line path there is no daemon to consult the manifest, so the
  // units that have to restart can only reach the operator through the script's
  // own output. A replaced binary whose unit is never restarted leaves the old
  // process running: the patch applies cleanly and nothing changes.
  for (const unit of new Set(manifest.files.map((file) => file.restart_unit).filter(Boolean))) {
    if (!applyCode.includes(unit)) badScripts.push(`${body.id}:apply.sh 未提示重启 ${unit}`);
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
  resolved.push({ body, archive, sha: actualSha, manifest });
}

check('every declaration has its .cgpatch artifact', missingArtifacts.length === 0, missingArtifacts.join(', '));
check('every artifact has a matching .sha256 sidecar', sidecarMismatch.length === 0, sidecarMismatch.join(', '));
check('every artifact manifest agrees with its declaration', mismatched.length === 0, mismatched.join('; '));
check('every artifact carries manifest, signature, checksums, scripts, README and source diff', incompleteArchives.length === 0, incompleteArchives.join('; '));
check('every artifact checksum file matches its payload', checksumMismatch.length === 0, checksumMismatch.join(', '));
check('apply.sh and rollback.sh are valid, verify checksums, install every path and never restart by themselves', badScripts.length === 0, badScripts.join('; '));
check('README names every unit the payload replaces in both its Apply and its Rollback block', undocumentedRestarts.length === 0, undocumentedRestarts.join('; '));
check('the signed manifest anchors apply.sh and rollback.sh and SHA256SUMS covers them', unboundTooling.length === 0, unboundTooling.join('; '));
if (publicKey) {
  check('every artifact signature verifies against the trusted public key', unsignedOrInvalid.length === 0, unsignedOrInvalid.join(', '));
} else {
  console.log('skip signature verification — pass --public-key or set CG_HOTFIX_TRUSTED_PUBLIC_KEY');
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
const badDirCounts = [];
for (const [dir, declaredCount] of declaredDirs) {
  const directory = path.join(artifactRoot, dir);
  const count = fs.existsSync(directory)
    ? fs.readdirSync(directory).filter((name) => name.endsWith('.cgpatch')).length
    : 0;
  if (count !== declaredCount) badDirCounts.push(`${dir}: ${count} .cgpatch for ${declaredCount} declaration(s)`);
}
check('every release line holds one unambiguous application entry point per declaration',
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
const declared = new Set();
for (const spec of specs) {
  for (const commit of spec.body.fix_commits || []) declared.add(git('rev-parse', `${commit}^{commit}`));
}
const baselines = [...new Set(specs.map((spec) => spec.body.base_commit))];
const undeclaredFixes = [];
for (const baseline of baselines) {
  const commits = git('log', '--format=%H%x09%s', `${baseline}..HEAD`).split('\n').filter(Boolean);
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
}
if (fs.existsSync(renderer)) {
  check('the renderer reads manifests from the artifacts instead of trusting a list',
    /tar/.test(fs.readFileSync(renderer, 'utf8')) && /HOTFIX-MANIFEST\.json/.test(fs.readFileSync(renderer, 'utf8')));
}

if (failures.length) {
  console.error(`\n${failures.length} check(s) failed.`);
  process.exit(1);
}
console.log('\nall hotfix patch catalogue checks passed.');
