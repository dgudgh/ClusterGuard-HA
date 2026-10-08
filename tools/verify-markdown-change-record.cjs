#!/usr/bin/env node
// Checks that every Markdown change since the recorded baseline is declared in the
// Markdown change record, with the type the change actually has.
//
// This is a local source gate. It proves that the markdown a tree carries is accounted
// for; it does not prove that the generated pages were rebuilt, and it says nothing about
// any site. Both of those are stated in the record's batch notes instead.
//
// The defect this gate exists for: a change record nobody re-reads. Without a check that
// compares the record against the tree, a new page or a rewritten rule lands with no entry
// and nothing notices - the record stays plausible and wrong. So the two directions are
// both failures here: a markdown change with no entry, and an entry with no change.
//
// --repo DIR    the tree to read (default: the repository containing this tool)
// --record REL  the record file, relative to --repo or absolute
//               (default docs/development/markdown-change-record.md)
// --git DIR     the git work tree to compute coverage from (default --repo)
// --strict      a coverage check skipped for want of git becomes a failure
// --self-test   mutation verification, including a no-bite control

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const DEFAULT_RECORD = 'docs/development/markdown-change-record.md';
const RECORD_SECTION = '登记表';
const CHANGE_TYPES = ['A', 'M', 'D'];
// Phrases that say nothing about what changed. The record's own rule is that an entry must
// name the rule, number or example it moved; these are the shapes that were actually seen
// in review, so they are refused by name rather than by taste.
const EMPTY_SUMMARIES = ['优化文档', '同步更新', '更新文档', '文档修改', '修订文档', 'updated docs', 'update docs', 'misc'];
const MINIMUM_SUMMARY_LENGTH = 8;

const argument = (name, fallback) => {
  const index = process.argv.indexOf(name);
  if (index < 0) return fallback;
  const value = process.argv[index + 1];
  if (!value || value.startsWith('--')) throw new Error(`${name} requires a value`);
  return value;
};

const repoRoot = path.resolve(argument('--repo', path.resolve(__dirname, '..')));
const gitRoot = path.resolve(argument('--git', repoRoot));
const recordArgument = argument('--record', DEFAULT_RECORD);
const strict = process.argv.includes('--strict');
const selfTest = process.argv.includes('--self-test');

const resolveRecord = (value) => (path.isAbsolute(value) ? value : path.join(repoRoot, value));

const git = (args) => execFileSync('git', args, { cwd: gitRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });

const gitAvailable = () => {
  try {
    return git(['rev-parse', '--is-inside-work-tree']).trim() === 'true';
  } catch (_) {
    return false;
  }
};

// ---------------------------------------------------------------------------
// Reading the record
// ---------------------------------------------------------------------------

// The record keeps one table under the 登记表 heading and nothing else in that section, so
// an archived batch moved to its own heading later in the file is ignored rather than
// re-checked against a baseline it no longer belongs to.
const sectionLines = (source, title) => {
  const lines = source.split('\n');
  const start = lines.findIndex((line) => line.trim() === `## ${title}`);
  if (start < 0) return null;
  let end = lines.length;
  for (let index = start + 1; index < lines.length; index += 1) {
    if (lines[index].startsWith('## ')) {
      end = index;
      break;
    }
  }
  return lines.slice(start + 1, end);
};

const HEADER = /^\|\s*日期\s*\|\s*文件\s*\|\s*变更\s*\|\s*说明\s*\|\s*$/;
const SEPARATOR = /^\|\s*:?-{2,}:?\s*\|\s*:?-{2,}:?\s*\|\s*:?-{2,}:?\s*\|\s*:?-{2,}:?\s*\|\s*$/;
const ROW = /^\|\s*(\d{4}-\d{2}-\d{2})\s*\|\s*`([^`]+)`\s*\|\s*([AMD])\s*\|\s*([^|]+?)\s*\|\s*$/;
const BASELINE = /^recorded-through:\s*([0-9a-fA-F]{7,40})\s*$/;

const parseRecord = (source) => {
  const declared = [];
  const result = { baseline: null, baselineCount: 0, header: false, separator: false, rows: declared, section: false };
  if (typeof source !== 'string') return result;
  for (const line of source.split('\n')) {
    const match = BASELINE.exec(line.trim());
    if (match) {
      result.baselineCount += 1;
      result.baseline = match[1];
    }
  }
  const lines = sectionLines(source, RECORD_SECTION);
  if (!lines) return result;
  result.section = true;
  let index = 0;
  while (index < lines.length && !HEADER.test(lines[index])) index += 1;
  if (index >= lines.length) return result;
  result.header = true;
  index += 1;
  if (index < lines.length && SEPARATOR.test(lines[index])) {
    result.separator = true;
    index += 1;
  }
  for (; index < lines.length; index += 1) {
    const match = ROW.exec(lines[index]);
    if (!match) {
      if (lines[index].trim()) break;
      continue;
    }
    const [, date, file, type, summary] = match;
    declared.push({ date, file, type, summary: summary.trim(), line: index + 1 });
  }
  return result;
};

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

const checks = [];
const skip = [];
const check = (title, ok, detail) => checks.push({ title, ok: Boolean(ok), detail: detail || '' });

const markdownPathFault = (file) => {
  if (!file) return 'empty path';
  if (path.isAbsolute(file)) return 'absolute path';
  if (file.startsWith('./')) return 'leading ./';
  if (file.split('/').includes('..')) return 'contains ..';
  if (!file.endsWith('.md')) return 'not a .md file';
  if (/\s/.test(file)) return 'contains whitespace';
  return null;
};

const runChecks = (recordFile) => {
  checks.length = 0;
  skip.length = 0;

  let source = null;
  try {
    source = fs.readFileSync(recordFile, 'utf8');
  } catch (_) {
    source = null;
  }
  check('the change record is readable', source !== null, recordFile);
  const record = parseRecord(source);

  check('the record declares exactly one baseline commit',
    record.baseline !== null && record.baselineCount === 1,
    `found ${record.baselineCount} recorded-through line(s)`);
  check('the record has one 登记表 with the four required columns',
    record.section && record.header && record.separator,
    `section=${record.section} header=${record.header} separator=${record.separator}`);

  const baseline = record.baseline;

  const malformed = record.rows.map((row) => markdownPathFault(row.file)).filter(Boolean);
  check('every declared path is a repo-relative markdown file', malformed.length === 0, malformed.slice(0, 5).join(', '));

  const vague = record.rows.filter((row) => {
    const summary = row.summary.toLowerCase().replace(/[。.\s]/g, '');
    return summary.length < MINIMUM_SUMMARY_LENGTH || EMPTY_SUMMARIES.some((phrase) => summary === phrase.replace(/\s/g, ''));
  });
  check('every declared change says what changed', vague.length === 0,
    vague.slice(0, 5).map((row) => `${row.file}: ${row.summary}`).join(' | '));

  const seen = new Set();
  const duplicated = [];
  for (const row of record.rows) {
    const key = `${row.date}\u0000${row.file}`;
    if (seen.has(key)) duplicated.push(`${row.date} ${row.file}`);
    seen.add(key);
  }
  check('no file is declared twice on the same date', duplicated.length === 0, duplicated.join(', '));

  const existence = [];
  const byFile = new Map();
  for (const row of record.rows) {
    if (!byFile.has(row.file)) byFile.set(row.file, []);
    byFile.get(row.file).push(row);
  }
  for (const [file, rows] of byFile) {
    const types = new Set(rows.map((row) => row.type));
    if (types.has('A') && types.has('D')) {
      existence.push(`${file}: declared both A and D`);
      continue;
    }
    const exists = fs.existsSync(path.join(repoRoot, file));
    if (types.has('A') && !exists) existence.push(`${file}: declared A but not on disk`);
    if (types.has('M') && !exists) existence.push(`${file}: declared M but not on disk`);
    if (types.has('D') && exists) existence.push(`${file}: declared D but still on disk`);
  }
  check('a declared add exists, a declared modify exists, a declared delete does not', existence.length === 0,
    existence.slice(0, 5).join('; '));

  // A skipped check is listed by name, one line per check, so "this run did not cover it"
  // can never be read as "this run passed it".
  const BASELINE_CHECKS = ['the baseline commit exists in the git tree', 'the baseline commit is an ancestor of HEAD'];
  const COVERAGE_CHECKS = ['every markdown change since the baseline is declared with the same type', 'no declaration survives the file it describes'];

  if (!gitAvailable()) {
    skip.push(...BASELINE_CHECKS, ...COVERAGE_CHECKS);
  } else {
    let resolved = null;
    try {
      resolved = git(['rev-parse', '--verify', `${baseline}^{commit}`]).trim();
    } catch (_) {
      resolved = null;
    }
    check('the baseline commit exists in the git tree', resolved !== null, `recorded-through: ${baseline}`);
    if (resolved) {
      let ancestor = true;
      try {
        git(['merge-base', '--is-ancestor', resolved, 'HEAD']);
      } catch (_) {
        ancestor = false;
      }
      check('the baseline commit is an ancestor of HEAD', ancestor, `recorded-through: ${baseline}`);
    } else {
      skip.push(BASELINE_CHECKS[1]);
    }

    let changed = new Map();
    try {
      const output = git(['diff', '--name-status', baseline, '--', '*.md']);
      for (const line of output.split('\n')) {
        if (!line.trim()) continue;
        const fields = line.split('\t');
        const status = fields[0][0];
        const file = fields[fields.length - 1];
        // A delete then re-add nets to the same path; the newest state wins so the type the
        // record has to carry is the one the tree actually has.
        if (status === 'R' || status === 'C') continue;
        changed.set(file, status);
      }
    } catch (error) {
      changed = null;
    }

    if (changed === null) {
      skip.push(...COVERAGE_CHECKS);
    } else {
      const missing = [];
      const wrongType = [];
      const declaredType = new Map();
      for (const row of record.rows) {
        if (!declaredType.has(row.file)) declaredType.set(row.file, new Set());
        declaredType.get(row.file).add(row.type);
      }
      for (const [file, status] of changed) {
        if (!declaredType.has(file)) {
          missing.push(`${status} ${file}`);
          continue;
        }
        const declared = declaredType.get(file);
        // The record must carry exactly the status the tree reports. In particular a path
        // that exists at the baseline can never be declared as newly added: that is a claim
        // the tree contradicts, and tolerating it here is how a record starts describing a
        // history that did not happen.
        if (!declared.has(status)) {
          wrongType.push(`${file}: tree says ${status}, record says ${[...declared].join('/')}`);
        }
      }
      check('every markdown change since the baseline is declared with the same type',
        missing.length === 0 && wrongType.length === 0,
        [...missing.slice(0, 5), ...wrongType.slice(0, 5)].join('; '));

      const stale = record.rows
        .map((row) => row.file)
        .filter((file, index, all) => all.indexOf(file) === index)
        .filter((file) => !changed.has(file));
      check('no declaration survives the file it describes', stale.length === 0, stale.slice(0, 5).join(', '));
    }
  }

  return { record, baseline };
};

// ---------------------------------------------------------------------------
// Self-test
// ---------------------------------------------------------------------------

// Each mutation is written into an isolated copy of the record and the checks are re-run
// against that copy. A mutation whose text is not present is reported as SKIPPED rather
// than as a pass: a mutation that never landed proves nothing, and reading it as "the gate
// caught it" is the exact mistake this harness exists to prevent.
const MUTATIONS = [
  {
    name: 'a declared change is dropped from the table',
    rule: 'every markdown change since the baseline is declared with the same type',
    apply: (source) => source.replace(/^\| 2026-10-08 \| `docs\/zh-CN\/version-release-policy\.md` \|[^\n]*\n/m, ''),
  },
  {
    name: 'a declared add is rewritten as a modify',
    rule: 'every markdown change since the baseline is declared with the same type',
    apply: (source) => source.replace('| `docs/development/markdown-change-record.md` | A |', '| `docs/development/markdown-change-record.md` | M |'),
  },
  {
    name: 'a file that existed at the baseline is declared newly added',
    rule: 'every markdown change since the baseline is declared with the same type',
    apply: (source) => source.replace(/^(\| 2026-10-08 \| `docs\/README\.md` \| )M( \|)/m, '$1A$2'),
  },
  {
    name: 'an unchanged file is declared anyway',
    rule: 'no declaration survives the file it describes',
    apply: (source) => source.replace(/^(\| 2026-10-08 \| `docs\/README\.md` \| M \|)/m, '| 2026-10-08 | `docs/en-US/README.md` | M | 无变化占位条目。 |\n$1'),
  },
  {
    name: 'a file still on disk is declared deleted',
    rule: 'a declared add exists, a declared modify exists, a declared delete does not',
    apply: (source) => source.replace('| `docs/hotfix-patches.md` | M |', '| `docs/hotfix-patches.md` | D |'),
  },
  {
    name: 'the baseline line is removed',
    rule: 'the record declares exactly one baseline commit',
    apply: (source) => source.replace(/^recorded-through:.*\n/m, ''),
  },
  {
    name: 'the table loses its 说明 column',
    rule: 'the record has one 登记表 with the four required columns',
    apply: (source) => source.replace('| 日期 | 文件 | 变更 | 说明 |', '| 日期 | 文件 | 变更 |'),
  },
  {
    name: 'the baseline points at a commit that does not exist',
    rule: 'the baseline commit exists in the git tree',
    apply: (source) => source.replace(/^recorded-through:.*$/m, 'recorded-through: 0000000'),
  },
  {
    name: 'a declared path escapes the repository',
    rule: 'every declared path is a repo-relative markdown file',
    apply: (source) => source.replace('| `docs/README.md` | M |', '| `/etc/clusterguard/README.md` | M |'),
  },
];

const CONTROL = {
  name: 'prose outside the table changes and the gate still passes',
  apply: (source) => `${source}\n<!-- 控制：只改表格与基线之外的文字，门禁必须仍然通过。 -->\n`,
};

// The baseline must be an ancestor of HEAD. That case cannot be built from a fixed hash: it
// needs a commit that exists and is not an ancestor of HEAD, and a repository may not contain
// one. It is looked up at run time and reported as not exercised when absent, so the line is
// always printed and never silently counted as caught.
const findDivergentCommit = () => {
  let refs = [];
  try {
    refs = git(['for-each-ref', '--format=%(objectname)']).split('\n').map((line) => line.trim()).filter(Boolean);
  } catch (_) {
    return null;
  }
  for (const sha of refs) {
    try {
      // A ref can name an object that is not present (a pruned checkpoint or a stale stash).
      // Requiring the object to resolve keeps the judge from mutating the baseline to a hash
      // that fails for the wrong reason, which would report the wrong rule as the detector.
      git(['cat-file', '-e', `${sha}^{commit}`]);
    } catch (_) {
      continue;
    }
    try {
      git(['merge-base', '--is-ancestor', sha, 'HEAD']);
    } catch (_) {
      return sha;
    }
  }
  return null;
};

const runSelfTest = () => {
  const original = fs.readFileSync(resolveRecord(recordArgument), 'utf8');
  const base = runChecks(resolveRecord(recordArgument));
  if (!base.record.baseline || checks.some((entry) => !entry.ok)) {
    console.error('self-test: the record does not pass as written, so no mutation can be judged');
    for (const entry of checks) if (!entry.ok) console.error(`  FAIL ${entry.title}: ${entry.detail}`);
    return 1;
  }

  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-md-record-'));
  let failures = 0;
  let skipped = 0;
  let unexercised = 0;
  const run = (source) => {
    const file = path.join(directory, `record-${Math.random().toString(36).slice(2)}.md`);
    fs.writeFileSync(file, source);
    runChecks(file);
    return checks.filter((entry) => !entry.ok);
  };
  const judge = (name, rule, mutated) => {
    const failed = run(mutated);
    const byRule = failed.some((entry) => entry.title === rule);
    if (failed.length > 0 && byRule) {
      console.log(`self-test: caught ${name} (${rule})`);
      return;
    }
    failures += 1;
    console.error(`self-test: NOT caught ${name} — expected "${rule}" to fail; failing checks: ${failed.map((entry) => entry.title).join(', ') || 'none'}`);
  };

  for (const mutation of MUTATIONS) {
    const mutated = mutation.apply(original);
    if (mutated === original) {
      console.log(`self-test: SKIPPED ${mutation.name} — the text to mutate is not in the record`);
      skipped += 1;
      continue;
    }
    judge(mutation.name, mutation.rule, mutated);
  }

  // The ancestor rule is judged against a commit looked up at run time.
  const divergent = gitAvailable() ? findDivergentCommit() : null;
  if (divergent) {
    judge('the baseline points at a commit that is not an ancestor of HEAD', 'the baseline commit is an ancestor of HEAD',
      original.replace(/^recorded-through:.*$/m, `recorded-through: ${divergent}`));
  } else {
    console.log('self-test: not exercised — the baseline is not a divergent commit (no non-ancestor commit is available here)');
    unexercised += 1;
  }

  const control = CONTROL.apply(original);
  if (control === original) {
    console.log('self-test: SKIPPED the no-bite control — the text to mutate is not in the record');
    skipped += 1;
  } else {
    const failed = run(control);
    if (failed.length === 0) {
      console.log(`self-test: control passed (${CONTROL.name})`);
    } else {
      failures += 1;
      console.error(`self-test: control turned the gate red — ${CONTROL.name}: ${failed.map((entry) => entry.title).join(', ')}`);
    }
  }

  console.log(`self-test: ${MUTATIONS.length + 1 - skipped} mutation(s) judged, ${failures} not caught, ${skipped} skipped, ${unexercised} not exercised here`);
  return failures === 0 && skipped === 0 ? 0 : 1;
};

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

if (selfTest) process.exit(runSelfTest());

const { record } = runChecks(resolveRecord(recordArgument));

const failed = checks.filter((entry) => !entry.ok);
for (const entry of checks) {
  if (!entry.ok) console.log(`FAIL ${entry.title}: ${entry.detail}`);
  else console.log(`ok   ${entry.title}`);
}
for (const entry of skip) console.log(`skip ${entry}`);

const listing = (() => {
  if (!gitAvailable()) return [];
  try {
    return git(['ls-files', '--others', '--exclude-standard', '--', '*.md'])
      .split('\n')
      .map((line) => line.trim())
      .filter(Boolean);
  } catch (_) {
    return [];
  }
})();
if (listing.length) {
  console.log(`\n${listing.length} untracked markdown file(s) are outside this gate until they are added to the index:`);
  for (const file of listing.slice(0, 8)) console.log(`  - ${file}`);
  if (listing.length > 8) console.log(`  ... and ${listing.length - 8} more`);
}

console.log(`\n${checks.length - failed.length} check(s) passed, ${failed.length} failed, ${skip.length} skipped.`);
if (failed.length) process.exitCode = 1;
if (skip.length && strict) {
  console.error('strict: a skipped coverage check is a failure');
  process.exitCode = 1;
}
