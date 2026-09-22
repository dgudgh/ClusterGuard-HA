#!/usr/bin/env node
// Guard the license declaration of the ClusterGuard HA delivery line.
//
// Why this exists: on 2026-09-22 this repository had exactly one place that named
// a license - `license: Proprietary` in packaging/rpm/nfpm.yaml - and no LICENSE
// file at all, so GitHub reported `license: null` and a reader had no way to tell
// what the terms were. The project is now `AGPL-3.0-only`. That kind of drift is
// invisible: nothing breaks, the build still succeeds, and the first person to
// notice is a customer's lawyer.
//
// The specific trap this catches: a license lives in more places than the LICENSE
// file. Editing one of them and not the others produces a repository that states
// two different things at once. Every location is therefore asserted together.
//
// Usage:
//   node tools/verify-license-consistency.cjs [--repo <path>]
//
// Exit codes: 0 all checks pass, 1 at least one check failed, 2 cannot run.

const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');
const crypto = require('node:crypto');

const flag = (name) => {
  const index = process.argv.indexOf(name);
  return index === -1 ? '' : (process.argv[index + 1] || '');
};
const git = (args) => cp.execFileSync('git', args, { encoding: 'utf8', maxBuffer: 8e6 }).trim();

const repo = path.resolve(flag('--repo') || git(['rev-parse', '--show-toplevel']));

// The authoritative license of this project. Changing it means changing this
// constant as well - which is the point: the change becomes deliberate.
const EXPECTED = {
  spdx: 'AGPL-3.0-only',
  name: 'GNU Affero General Public License, version 3',
  sha256: '8486a10c4393cee1c25392769ddd3b2d6c242d6ec7928e1414efff7dfb2f07ef',
  phrases: ['GNU AFFERO GENERAL PUBLIC LICENSE', 'Version 3, 19 November 2007'],
};

// MPL-2.0 is the only copyleft among the dependencies, and it is what rules out
// GPL-2.0-only for this project. Keep the list explicit rather than inferred.
const MPL_MODULES = [
  'github.com/hashicorp/raft',
  'github.com/hashicorp/raft-boltdb/v2',
  'github.com/hashicorp/golang-lru',
  'github.com/hashicorp/go-immutable-radix',
];

const READ = (relative) => {
  const absolute = path.join(repo, relative);
  return fs.existsSync(absolute) ? fs.readFileSync(absolute, 'utf8') : null;
};

const failures = [];
let checksRun = 0;

const check = (id, condition, detail) => {
  checksRun += 1;
  if (!condition) failures.push({ id, detail });
};

// ---------------------------------------------------------------- LICENSE ----

const licenseText = READ('LICENSE');
check('license-present', licenseText !== null, 'LICENSE is missing from the repository root');

if (licenseText !== null) {
  const digest = crypto.createHash('sha256').update(licenseText, 'utf8').digest('hex');
  check('license-digest', digest === EXPECTED.sha256,
    `LICENSE sha256 is ${digest}, expected the verbatim official text ${EXPECTED.sha256}`);

  for (const phrase of EXPECTED.phrases) {
    check(`license-phrase:${phrase.slice(0, 24)}`, licenseText.includes(phrase),
      `LICENSE does not contain ${JSON.stringify(phrase)}`);
  }
}

// A git-ignored LICENSE would be absent from every clone and every release.
let ignored = false;
try {
  cp.execFileSync('git', ['check-ignore', '-q', 'LICENSE'], { cwd: repo, stdio: 'ignore' });
  ignored = true;
} catch {
  ignored = false;
}
check('license-not-ignored', !ignored, 'LICENSE is git-ignored, so it would never be distributed');

// ------------------------------------------------- packaging declaration -----

const nfpm = READ('packaging/rpm/nfpm.yaml');
check('rpm-metadata-present', nfpm !== null, 'packaging/rpm/nfpm.yaml is missing');

if (nfpm !== null) {
  const declared = (nfpm.match(/^license:[ \t]*(.+?)[ \t]*$/m) || [])[1] || '';
  check('rpm-license-field', declared === EXPECTED.spdx,
    `packaging/rpm/nfpm.yaml declares license: ${declared || '(none)'}, expected ${EXPECTED.spdx}`);

  const requiredContents = [
    'docs/LICENSE',
    '/usr/share/doc/clusterguard-ha/LICENSE',
    'docs/THIRD-PARTY-NOTICES.md',
    '/usr/share/doc/clusterguard-ha/THIRD-PARTY-NOTICES.md',
    'docs/MPL-2.0.txt',
    '/usr/share/doc/clusterguard-ha/MPL-2.0.txt',
  ];
  for (const token of requiredContents) {
    check(`rpm-contents:${token}`, nfpm.includes(token),
      `packaging/rpm/nfpm.yaml contents does not mention ${token}, so the RPM would ship without it`);
  }
}

// ------------------------------------------------------ build wiring ---------

const rpmBuild = READ('scripts/build-clusterguard-rpm.sh');
check('rpm-build-present', rpmBuild !== null, 'scripts/build-clusterguard-rpm.sh is missing');
if (rpmBuild !== null) {
  check('rpm-build-installs-license',
    /repository\}\/LICENSE/.test(rpmBuild) && /root\}\/docs\/LICENSE/.test(rpmBuild),
    'scripts/build-clusterguard-rpm.sh does not copy LICENSE into the packaging stage');
  check('rpm-build-installs-notices',
    /repository\}\/THIRD-PARTY-NOTICES\.md/.test(rpmBuild),
    'scripts/build-clusterguard-rpm.sh does not copy THIRD-PARTY-NOTICES.md into the packaging stage');
}

const kitBuild = READ('scripts/build-clusterguard-offline-kit.sh');
check('kit-build-present', kitBuild !== null, 'scripts/build-clusterguard-offline-kit.sh is missing');
if (kitBuild !== null) {
  check('kit-build-installs-license',
    /kit\}\/LICENSE/.test(kitBuild) && /output\}\/LICENSE/.test(kitBuild),
    'scripts/build-clusterguard-offline-kit.sh does not put LICENSE in the kit and its output directory');
  check('kit-build-installs-notices',
    /kit\}\/THIRD-PARTY-NOTICES\.md/.test(kitBuild),
    'scripts/build-clusterguard-offline-kit.sh does not put THIRD-PARTY-NOTICES.md in the kit');
}

// ------------------------------------------------- third-party notices -------

const notices = READ('THIRD-PARTY-NOTICES.md');
check('notices-present', notices !== null, 'THIRD-PARTY-NOTICES.md is missing from the repository root');

const mpl = READ('docs/licenses/MPL-2.0.txt');
check('mpl-copy-present', mpl !== null, 'docs/licenses/MPL-2.0.txt is missing');
if (mpl !== null) {
  check('mpl-copy-content', /Mozilla Public License/i.test(mpl),
    'docs/licenses/MPL-2.0.txt does not look like the Mozilla Public License');
}

const goMod = READ('go.mod');
check('go-mod-present', goMod !== null, 'go.mod is missing');

if (goMod !== null && notices !== null) {
  const modules = [];
  for (const line of goMod.split('\n')) {
    const single = line.match(/^require[ \t]+([^ \t]+)[ \t]+(v[^ \t]+)/);
    if (single) modules.push({ module: single[1], version: single[2] });
    const block = line.match(/^[ \t]+([^ \t()]+)[ \t]+(v[^ \t]+)/);
    if (block && !line.trim().startsWith('//')) modules.push({ module: block[1], version: block[2] });
  }

  check('go-mod-modules-found', modules.length > 0, 'no modules could be parsed out of go.mod');

  const noticeLines = notices.split('\n');
  for (const { module, version } of modules) {
    const line = noticeLines.find((candidate) => candidate.includes(module));
    check(`notices-covers:${module}`,
      Boolean(line) && line.includes(version),
      `THIRD-PARTY-NOTICES.md has no row for ${module} @ ${version}, so a shipped binary would carry an unlisted dependency`);
    if (MPL_MODULES.includes(module) && line) {
      check(`notices-marks-mpl:${module}`, line.includes('MPL-2.0'),
        `THIRD-PARTY-NOTICES.md does not mark ${module} as MPL-2.0`);
    }
  }

  for (const module of MPL_MODULES) {
    check(`mpl-module-declared:${module}`, modules.some((entry) => entry.module === module),
      `${module} is listed as MPL-2.0 but is no longer a dependency; update the compatibility conclusion`);
  }
}

// --------------------------------------------- conflicting declarations ------

const SKIP_DIRS = new Set(['.git', '.build', 'node_modules', '.worktrees', 'release', '.venv', 'dist']);
const SCAN_EXTENSIONS = new Set(['.md', '.yaml', '.yml', '.json', '.sh', '.go', '.cjs', '.mjs']);

// This script is the checker, not a declaration: the pattern definitions and the
// report object below quote the very syntax being searched for, so scanning it
// reports phantom hits. Matched by basename rather than by resolved path so the
// gate stays excluded even when it runs against a tree copied somewhere else
// (--repo <path>), where __filename would not be inside the scanned root.
const SELF_NAME = 'verify-license-consistency.cjs';

const walk = (directory, out = []) => {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) continue;
      // docs/html is generated from the markdown already scanned above.
      if (entry.name === 'html' && path.basename(directory) === 'docs') continue;
      walk(path.join(directory, entry.name), out);
    } else if (SCAN_EXTENSIONS.has(path.extname(entry.name))) {
      out.push(path.join(directory, entry.name));
    }
  }
  return out;
};

// Only declaration-shaped occurrences count. Prose that discusses other licenses
// (this repository's own licensing pages do) must not be reported.
const DECLARATION_PATTERNS = [
  { shape: 'yaml license field', regex: /^[ \t]*license[ \t]*:[ \t]*(\S.*?)[ \t]*$/ },
  { shape: 'json license field', regex: /"license"[ \t]*:[ \t]*"([^"]*)"/ },
  { shape: 'spdx header', regex: /SPDX-License-Identifier:[ \t]*(\S+)/ },
];

const conflicting = [];
for (const file of walk(repo)) {
  if (path.basename(file) === SELF_NAME) continue;
  const relative = path.relative(repo, file);
  const contents = fs.readFileSync(file, 'utf8');
  contents.split('\n').forEach((line, index) => {
    for (const { shape, regex } of DECLARATION_PATTERNS) {
      const match = line.match(regex);
      if (!match) continue;
      const value = match[1].trim();
      if (value === EXPECTED.spdx) continue;
      conflicting.push(`${relative}:${index + 1} (${shape}) declares ${value}`);
    }
  });
}

check('no-conflicting-declarations', conflicting.length === 0,
  `found a license declaration that contradicts ${EXPECTED.spdx}:\n    ${conflicting.join('\n    ')}`);

// -------------------------------------------------- documentation parity -----

const STATEMENT_FILES = [
  'README.md',
  'README.zh-CN.md',
  'docs/README.md',
  'docs/zh-CN/README.md',
  'docs/en-US/README.md',
  'docs/zh-CN/licensing.md',
  'docs/en-US/licensing.md',
  'AGENTS.md',
];

for (const relative of STATEMENT_FILES) {
  const contents = READ(relative);
  check(`states-license:${relative}`, contents !== null && contents.includes(EXPECTED.spdx),
    contents === null
      ? `${relative} is missing`
      : `${relative} does not mention ${EXPECTED.spdx}`);
}

// A reader who is told the license in one language must be told the same thing,
// with the same paths and commands, in the other.
const PAIRED_TOKENS = [
  'AGPL-3.0-only',
  'LICENSE',
  'THIRD-PARTY-NOTICES.md',
  'docs/licenses/MPL-2.0.txt',
  '/usr/share/doc/clusterguard-ha/LICENSE',
  '/usr/share/doc/clusterguard-ha/MPL-2.0.txt',
  'node tools/verify-license-consistency.cjs',
  'packaging/rpm/nfpm.yaml',
  'scripts/build-clusterguard-rpm.sh',
  'scripts/build-clusterguard-offline-kit.sh',
  'BUILD-INFO',
  'build-info.json',
];

const zhLicensing = READ('docs/zh-CN/licensing.md');
const enLicensing = READ('docs/en-US/licensing.md');
if (zhLicensing !== null && enLicensing !== null) {
  for (const token of PAIRED_TOKENS) {
    check(`bilingual-token:${token}`,
      zhLicensing.includes(token) && enLicensing.includes(token),
      `licensing pages disagree on ${JSON.stringify(token)}: zh=${zhLicensing.includes(token)} en=${enLicensing.includes(token)}`);
  }
}

// ------------------------------------------------------------------ report ---

const report = {
  status: failures.length === 0 ? 'passed' : 'failed',
  license: EXPECTED.spdx,
  repo,
  checks_run: checksRun,
  failures,
};

console.log(JSON.stringify(report, null, 2));

if (failures.length > 0) {
  console.error('');
  console.error(`${failures.length} of ${checksRun} license checks failed:`);
  for (const failure of failures) {
    console.error(`  ${failure.id}`);
    console.error(`    ${failure.detail}`);
  }
  process.exit(1);
}

console.log('');
console.log(`license is coherent: ${checksRun} checks passed, ${EXPECTED.spdx} declared in every location`);
