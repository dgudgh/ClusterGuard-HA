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
 * the payload of a patch really covers every fix it declares, and that the
 * artifact directory is a single, unambiguous application entry point.
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
const artifactDir = 'release/2.2-103-hotfixes';

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

// --- Gate 2: every declaration has a signed artifact -----------------------
const artifactDirectory = path.join(artifactRoot, artifactDir);
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
const unsignedOrInvalid = [];
const sidecarMismatch = [];
const resolved = [];

for (const spec of specs) {
  const body = spec.body;
  const archive = path.join(artifactDirectory, archiveName(body));
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
  const apply = fs.readFileSync(path.join(root, 'apply.sh'), 'utf8');
  if (!/sha256sum -c SHA256SUMS/.test(apply)) badScripts.push(`${body.id}:apply.sh 未校验 SHA256SUMS`);
  if (/^\s*systemctl restart /m.test(apply)) badScripts.push(`${body.id}:apply.sh 自动重启服务`);
  for (const entry of manifest.files) {
    if (entry.install_path && !apply.includes(entry.install_path)) {
      badScripts.push(`${body.id}:apply.sh 未安装 ${entry.install_path}`);
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
if (publicKey) {
  check('every artifact signature verifies against the trusted public key', unsignedOrInvalid.length === 0, unsignedOrInvalid.join(', '));
} else {
  console.log('skip signature verification — pass --public-key or set CG_HOTFIX_TRUSTED_PUBLIC_KEY');
}

// --- Gate 2b: the payload really covers every declared fix ------------------
// A patch that declares a fix but does not ship the artifact that fix needs is
// worse than no patch: the site believes it is fixed. Derive the requirement
// from each fix commit on its own, exactly like the builder does.
const payloadRequirements = (commit) => {
  const changed = git('diff', '--name-only', `${commit}^..${commit}`).split('\n').filter(Boolean);
  const needed = [];
  if (changed.some((file) => /^(internal\/agent|cmd\/clusterguard-agent)\//.test(file))) needed.push('payload/bin/clusterguard-agent');
  if (changed.some((file) => /^(internal\/api|cmd\/clusterguard)\//.test(file))) needed.push('payload/bin/clusterguard');
  for (const file of changed.filter((file) => /^packaging\/systemd\/.*\.service$/.test(file))) {
    needed.push(`payload/systemd/${file.replace(/^packaging\/systemd\//, '')}`);
  }
  for (const file of changed.filter((file) => /^scripts\/clusterguard-[a-z0-9-]+\.sh$/.test(file))) {
    needed.push(`payload/libexec/${file.replace(/^scripts\//, '')}`);
  }
  if (changed.includes('scripts/install_clusterguard.sh')) needed.push('payload/installer/install_clusterguard.sh');
  return needed;
};

const underdelivered = [];
for (const item of resolved) {
  const shipped = new Set(item.manifest.files.map((file) => file.artifact));
  for (const commit of item.manifest.fix_commits || []) {
    for (const artifact of payloadRequirements(commit)) {
      if (!shipped.has(artifact)) underdelivered.push(`${item.body.id}:${commit.slice(0, 7)} 需要 ${artifact}`);
    }
  }
}
check('every declared fix is really present in the payload', underdelivered.length === 0, underdelivered.join('; '));

// A patch may be built from a ported tree, but that tree must still be "the
// release baseline plus the declared fixes". Fixes made on a development line
// often share a file with an unreleased feature, and shipping that feature
// inside a hotfix is how a site gets an unvalidated change.
const payloadPathPattern = /^(internal\/agent|cmd\/clusterguard-agent|internal\/api|cmd\/clusterguard)\/|^packaging\/systemd\/.*\.service$|^scripts\/install_clusterguard\.sh$|^scripts\/clusterguard-[a-z0-9-]+\.sh$/;
const productionFiles = (range) => git('diff', '--name-only', range).split('\n').filter(Boolean)
  .filter((file) => payloadPathPattern.test(file));

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

const archiveCount = fs.existsSync(artifactDirectory)
  ? fs.readdirSync(artifactDirectory).filter((name) => name.endsWith('.cgpatch')).length
  : 0;
check('the artifact directory is one unambiguous application entry point per declaration',
  archiveCount === specs.length, `${archiveCount} .cgpatch file(s) for ${specs.length} declaration(s)`);

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
    const touchesPayload = git('diff', '--name-only', `${hash}^..${hash}`).split('\n').filter(Boolean)
      .some((file) => payloadPathPattern.test(file));
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
