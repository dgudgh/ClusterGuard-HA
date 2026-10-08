const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawnSync } = require('node:child_process');

test('local archives are excluded while real declarations still fail the CLI gate', () => {
  const repo = path.resolve(__dirname, '..');
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-license-scope-'));
  try {
    for (const file of execFileSync('git', ['ls-files', '-z'], { cwd: repo, encoding: 'utf8' }).split('\0').filter(Boolean)) {
      const source = path.join(repo, file);
      if (!fs.existsSync(source)) continue;
      fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
      fs.copyFileSync(source, path.join(root, file));
    }
    const write = file => {
      fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
      fs.writeFileSync(path.join(root, file), ['SPDX', '-License-Identifier: MIT\n'].join(''));
    };
    const run = () => spawnSync(process.execPath, [path.join(__dirname, 'verify-license-consistency.cjs'), '--repo', root], { encoding: 'utf8' });
    assert.equal(run().status, 0, 'the copied repository must pass before introducing fixtures');
    for (const file of ['.workbuddy/memory/MEMORY.md', '.workbuddy/archive/memory/old.md', '.workbuddy/reference/releases.md']) write(file);
    assert.equal(run().status, 0, 'all root-local agent data stays outside product declarations');
    for (const file of ['internal/license-scope-fixture.go', 'docs/license-scope-fixture.md', 'internal/.workbuddy/license-scope-fixture.md']) {
      write(file);
      const result = run();
      assert.equal(result.status, 1, file + ' must still be rejected');
      assert.match(result.stdout, /no-conflicting-declarations/);
      assert.ok(result.stdout.includes(file), 'the error must name the actual conflicting declaration');
      fs.unlinkSync(path.join(root, file));
    }
    assert.equal(run().status, 0, 'removing the actual declarations restores the passing gate');
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
