#!/usr/bin/env node
// Verifies that every delivery under release/ still points at a commit this
// repository holds, and that the release note for that version is part of that
// commit.
//
// Why this exists: building the offline kit requires a standalone clone under
// .build/, and a release-record commit made inside that clone never returns to
// the repository. Version 2.2-102 shipped exactly that way: RELEASE-INFO named
// 2b9a449, whose objects existed only in the throwaway build clone, so the
// repository held no record at all for its newest shipped media.
//
// Usage:
//   node tools/verify-release-records.cjs [--repo <dir>] [--release-root <dir>]
//
// Verdicts per record:
//   ok           the commit is reachable from a tag or a remote-tracking ref
//   local-only   reachable only from a local branch that has not been pushed
//   orphan       the object exists but no ref reaches it
//   absent       the object is not in this repository at all
//   note-absent  the release note is not part of the recorded commit
//
// Exit code is non-zero when any record is absent or orphan. `local-only` and
// `note-absent` are reported but not fatal: legacy deliveries predate this
// check, and a version built before its note was committed is caught by the
// kit verifier, which requires a clean source tree.

const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');

const flag = (name) => {
  const index = process.argv.indexOf(name);
  return index === -1 ? '' : (process.argv[index + 1] || '');
};
const run = (args) => cp.execFileSync('git', args, { encoding: 'utf8', maxBuffer: 8e6 }).trim();
const attempt = (args) => {
  try {
    return {
      ok: true,
      out: cp.execFileSync('git', args, { encoding: 'utf8', maxBuffer: 8e6, stdio: ['ignore', 'pipe', 'ignore'] }).trim(),
    };
  } catch {
    return { ok: false, out: '' };
  }
};

const repo = path.resolve(flag('--repo') || run(['rev-parse', '--show-toplevel']));
const mainWorktree = run(['worktree', 'list', '--porcelain'])
  .split('\n').find((line) => line.startsWith('worktree ')).slice('worktree '.length);
const explicitRoot = flag('--release-root');
const releaseRoot = path.resolve(explicitRoot
  || (fs.existsSync(path.join(repo, 'release')) ? path.join(repo, 'release') : path.join(mainWorktree, 'release')));

if (!fs.existsSync(releaseRoot)) {
  console.error(`release directory not found: ${releaseRoot}`);
  process.exit(2);
}

const parseInfo = (contents) => Object.fromEntries(contents
  .split('\n')
  .map((line) => line.trim())
  .filter((line) => line && !line.startsWith('#'))
  .map((line) => {
    const separator = line.indexOf('=');
    return separator === -1 ? [line, ''] : [line.slice(0, separator), line.slice(separator + 1)];
  }));

const records = [];
for (const entry of fs.readdirSync(releaseRoot, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
  if (!entry.isDirectory()) continue;
  const infoPath = path.join(releaseRoot, entry.name, 'RELEASE-INFO');
  if (!fs.existsSync(infoPath)) continue;

  const record = parseInfo(fs.readFileSync(infoPath, 'utf8'));
  const commit = (record.commit || '').trim();
  const version = (record.version || '').trim();
  const release = (record.release || '').trim();
  const notePath = version && release ? `docs/zh-CN/release-${version}.${release}.md` : '';

  const verdicts = [];
  let refs = [];
  if (!/^[0-9a-f]{40}$/.test(commit)) {
    verdicts.push('absent');
  } else if (!attempt(['-C', repo, 'cat-file', '-e', `${commit}^{commit}`]).ok) {
    verdicts.push('absent');
  } else {
    refs = run(['-C', repo, 'for-each-ref', '--contains', commit, '--format=%(refname)']).split('\n').filter(Boolean);
    const durable = refs.some((ref) => ref.startsWith('refs/remotes/') || ref.startsWith('refs/tags/'));
    if (durable) {
      verdicts.push('ok');
    } else if (refs.some((ref) => ref.startsWith('refs/heads/'))) {
      verdicts.push('local-only');
    } else {
      verdicts.push('orphan');
    }
    if (notePath && !attempt(['-C', repo, 'cat-file', '-e', `${commit}:${notePath}`]).ok) {
      verdicts.push('note-absent');
    }
  }

  const fatal = verdicts.includes('absent') || verdicts.includes('orphan');
  records.push({
    bundle_version: record.bundle_version || entry.name,
    commit,
    refs,
    release_note: notePath || null,
    verdicts,
    fatal,
  });
}

const failures = records.filter((record) => record.fatal);
const report = {
  status: failures.length ? 'failed' : 'passed',
  release_root: releaseRoot,
  records_checked: records.length,
  failures: failures.length,
  records,
};
console.log(JSON.stringify(report, null, 2));

if (failures.length) {
  console.error('');
  for (const record of failures) {
    console.error(`FAIL ${record.bundle_version}: commit ${record.commit} ${record.verdicts.join(',')}`);
  }
  console.error('A release record that the repository cannot point at is a delivery the repository cannot account for.');
  process.exit(1);
}
