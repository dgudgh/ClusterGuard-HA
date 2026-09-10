const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const cp = require('node:child_process');
const assert = require('node:assert/strict');
const baseline = process.argv.includes('--baseline');
const output = path.resolve(process.env.BUNDLE_TEST_OUTPUT || `.build/offline-kit-99/${baseline ? 'before' : 'after'}`);
fs.mkdirSync(output, { recursive:true });
const run = (command, args, options = {}) => cp.execFileSync(command, args, { encoding:'utf8', maxBuffer:8e6, ...options });
const realGo = run('bash', ['-c', 'command -v go']).trim();
const targetOS = run('go', ['env', 'GOOS']).trim();
const targetArch = run('go', ['env', 'GOARCH']).trim();
const tests = baseline ? [{ label:'2.2-99', args:[] }] : [
  { label:'2.2-99', args:[] },
  { label:'release-candidate', args:['--rpm-version','2.2','--rpm-release','99'] }
];
const results = [];
for (const item of tests) {
  const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-bundle-version-'));
  const wrapperDir = path.join(stage, 'compiler');
  fs.mkdirSync(wrapperDir);
  const invocationLog = path.join(stage, 'compiler.jsonl');
  // Record the real compiler invocation; trimpath builds do not expose ldflags via go version -m.
  fs.writeFileSync(path.join(wrapperDir, 'go'), `#!${process.execPath}\n` +
    `const fs = require('node:fs'); const cp = require('node:child_process');\n` +
    `fs.appendFileSync(${JSON.stringify(invocationLog)}, JSON.stringify(process.argv.slice(2)) + '\\n');\n` +
    `const r = cp.spawnSync(${JSON.stringify(realGo)}, process.argv.slice(2), {stdio:'inherit'});\n` +
    `process.exit(r.status === null ? 1 : r.status);\n`, {mode:0o755});
  run('bash', ['scripts/build-clusterguard-bundle.sh', '--version',item.label, '--goos',targetOS, '--goarch',targetArch, '--output',stage, ...item.args],
    {env:{...process.env, PATH:wrapperDir + path.delimiter + process.env.PATH}});
  const invocations = fs.readFileSync(invocationLog, 'utf8').trim().split('\n').map(JSON.parse);
  const name = `clusterguard-ha-${item.label}-${targetOS}-${targetArch}`;
  run('tar', ['-xzf',path.join(stage,name + '.tar.gz'), '-C',stage]);
  for (const binary of ['clusterguard','cgctl','clusterguard-agent']) {
    const executable = path.join(stage,name,'bin',binary);
    const settings = run('go',['version','-m',executable]);
    const info = binary === 'clusterguard' ? JSON.parse(run(executable,['--version-json'])) : null;
    const invocation = invocations.find(args => args.includes('build') && args.includes(`./cmd/${binary}`));
    const flags = invocation?.[invocation.indexOf('-ldflags') + 1] || '';
    const passed = flags.includes('buildinfo.Version=2.2') && flags.includes('buildinfo.Release=99')
      && settings.includes(`path\tclusterguard.io/ha/cmd/${binary}`)
      && settings.includes(`GOOS=${targetOS}`) && settings.includes(`GOARCH=${targetArch}`)
      && (!info || (info.version === '2.2' && info.release === '99' && info.commit !== 'unknown' && info.built_at !== 'unknown'));
    results.push({ label:item.label, binary, info, compiler_flags:flags, passed });
  }
}
const failures = results.filter(item => !item.passed).length;
const report = { status:baseline ? 'baseline-recorded' : failures ? 'failed' : 'passed', scope:'native local binaries; no install or network operations', results, failures };
fs.writeFileSync(path.join(output,'result.json'), JSON.stringify(report,null,2) + '\n');
console.log(JSON.stringify(report));
if (baseline) assert.equal(report.failures,3);
else assert.equal(report.failures,0);
