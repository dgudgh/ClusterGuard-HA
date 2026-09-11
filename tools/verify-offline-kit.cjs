const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const cp = require('node:child_process');
const crypto = require('node:crypto');
const assert = require('node:assert/strict');

// Verify a locally built kit without running its installer or contacting any host.
const [archiveArg, sourceArg, outputArg, expectedVersion, expectedRelease, expectedDatabaseCount] = process.argv.slice(2);
assert.ok(
  archiveArg && sourceArg && outputArg && expectedVersion && expectedRelease && expectedDatabaseCount,
  'usage: node tools/verify-offline-kit.cjs ARCHIVE CLEAN_SOURCE REPORT VERSION RELEASE DATABASE_PACKAGE_COUNT',
);
assert.match(expectedVersion, /^[0-9][0-9A-Za-z._+~-]*$/);
assert.match(expectedRelease, /^[0-9][0-9A-Za-z._+~-]*$/);
assert.match(expectedDatabaseCount, /^[0-9]+$/);
const expectedDatabasePackageCount = Number(expectedDatabaseCount);
const archive = path.resolve(archiveArg), source = path.resolve(sourceArg);
const reportPath = path.resolve(outputArg);
const run = (command, args, options = {}) => cp.execFileSync(command, args, {encoding:'utf8', maxBuffer:32e6, ...options});
const sha = file => crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'cg-kit-verify-'));
const checks = [];
const check = (name, fn) => { fn(); checks.push(name); };
const info = file => Object.fromEntries(fs.readFileSync(file,'utf8').trim().split('\n').map(line => {
  const i = line.indexOf('='); return [line.slice(0,i),line.slice(i+1)];
}));
const files = root => fs.readdirSync(root,{withFileTypes:true}).flatMap(entry => {
  const file = path.join(root,entry.name);
  assert.ok(!entry.isSymbolicLink(), `unexpected symbolic link: ${file}`);
  return entry.isDirectory() ? files(file) : [file];
});
const verifySums = file => {
  const root = path.dirname(file);
  for (const line of fs.readFileSync(file,'utf8').trim().split('\n')) {
    const match = /^([a-f0-9]{64}) [ *](.+)$/.exec(line);
    assert.ok(match, `invalid checksum line in ${file}`);
    const target = path.resolve(root,match[2]);
    assert.ok(target.startsWith(root + path.sep), 'checksum path escapes directory');
    assert.equal(sha(target),match[1], `checksum mismatch: ${match[2]}`);
  }
};
const extract = (file, destination, rpm = false) => {
  fs.mkdirSync(destination,{recursive:true});
  const names = run('tar',['-tf',file]).trim().split('\n');
  assert.ok(names.every(name => (!path.isAbsolute(name) || (rpm && /^\/(etc|usr)\//.test(name))) && !name.split('/').includes('..')), 'unsafe archive path');
  const kinds = run('tar',['-tvf',file]).trim().split('\n').map(line => line[0]);
  assert.ok(kinds.every(kind => kind === '-' || kind === 'd'), 'archive contains non-regular entry');
  run('tar',['-xf',file,'-C',destination]);
};
const expectedCommit = run('git',['-C',source,'rev-parse','HEAD']).trim();
const expectedHTML = fs.readFileSync(path.join(source,'internal/api/console.html'));
check('clean committed build source', () => assert.equal(run('git',['-C',source,'status','--porcelain']).trim(),''));
check('archive SHA256', () => verifySums(archive + '.sha256'));
extract(archive,path.join(scratch,'kit'));
const roots = fs.readdirSync(path.join(scratch,'kit'));
assert.equal(roots.length,1);
const kit = path.join(scratch,'kit',roots[0]);
const metadata = info(path.join(kit,'RELEASE-INFO'));
check('release identity and clean source', () => {
  assert.equal(metadata.version,expectedVersion); assert.equal(metadata.release,expectedRelease);
  assert.equal(metadata.commit,expectedCommit); assert.equal(metadata.source_tree_dirty,'false');
  assert.equal(metadata.source_untracked_count,'0'); assert.equal(metadata.architecture,'x86_64');
  assert.equal(Number(metadata.database_package_count),expectedDatabasePackageCount);
});
check('all kit checksum manifests', () => files(kit).filter(file => path.basename(file) === 'SHA256SUMS' || file.endsWith('.sha256')).forEach(verifySums));
check('patch trust public key when bundled', () => {
  const keyPath = path.join(kit,'trust/patch-signing-public.pem');
  if (!fs.existsSync(keyPath)) return;
  const key = crypto.createPublicKey(fs.readFileSync(keyPath));
  assert.equal(key.type, 'public');
});
check('runtime dependencies', () => {
  const deps = fs.readdirSync(path.join(kit,'dependencies'));
  for (const name of ['libaio','ncurses-compat-libs','numactl-libs']) assert.ok(deps.some(file => file.startsWith(name + '-') && file.endsWith('.rpm')));
  assert.ok(fs.existsSync(path.join(kit,'dependencies/repodata/repomd.xml')));
});
const databaseDirectory = path.join(kit,'packages/database');
const databasePackages = fs.readdirSync(databaseDirectory).filter(name =>
  !name.endsWith('.sha256') && name !== 'README.txt' && fs.statSync(path.join(databaseDirectory,name)).isFile());
check('database package contents', () => {
  assert.equal(databasePackages.length, expectedDatabasePackageCount);
  if (expectedDatabasePackageCount > 0) {
    assert.ok(databasePackages.some(name => /mysql/i.test(name)), 'MySQL package is missing');
    assert.ok(databasePackages.some(name => /postgresql/i.test(name)), 'PostgreSQL package is missing');
  }
});
const runtimeArchiveName = `clusterguard-ha-${expectedVersion}-${expectedRelease}-linux-amd64.tar.gz`;
const runtimeArchive = path.join(kit,`packages/${runtimeArchiveName}`);
extract(runtimeArchive,path.join(scratch,'runtime'));
const runtime = path.join(scratch,`runtime/clusterguard-ha-${expectedVersion}-${expectedRelease}-linux-amd64`);
check('runtime checksum manifest', () => verifySums(path.join(runtime,'SHA256SUMS')));
const rpm = path.join(kit,`packages/clusterguard-ha-${expectedVersion}-${expectedRelease}.x86_64.rpm`);
extract(rpm,path.join(scratch,'rpm'),true);
const rpmRoot = path.join(scratch,'rpm');
check('RPM build identity', () => {
  const build = info(path.join(rpmRoot,'usr/share/doc/clusterguard-ha/BUILD-INFO'));
  assert.equal(build.version,expectedVersion); assert.equal(build.release,expectedRelease); assert.equal(build.commit,expectedCommit);
});
check('all payload ELF binaries and compiled source revision', () => {
  for (const file of [...files(runtime),...files(rpmRoot),...files(path.join(kit,'tools'))]) {
    const prefix = fs.readFileSync(file).subarray(0,4);
    if (prefix.equals(Buffer.from([0x7f,0x45,0x4c,0x46]))) {
      assert.match(run('file',['-b',file]),/x86-64|x86_64/);
      if (path.basename(file).startsWith('clusterguard') || path.basename(file) === 'cgctl') {
        const settings = run('go',['version','-m',file]);
        assert.ok(settings.includes('GOOS=linux') && settings.includes('GOARCH=amd64'));
        // Cross-compiled binaries may not carry Go VCS metadata. When it is
        // present, it must still prove the exact clean source revision.
        if (settings.includes('vcs.revision=')) {
          assert.ok(settings.includes('vcs.revision=' + expectedCommit), `wrong compiled revision: ${file}`);
          assert.ok(settings.includes('vcs.modified=false'), `dirty compiled revision: ${file}`);
        }
      }
    }
  }
});
check('both controller payloads embed the exact tested HTML', () => {
  for (const file of [path.join(runtime,'bin/clusterguard'),path.join(rpmRoot,'usr/local/bin/clusterguard')]) {
    assert.ok(fs.readFileSync(file).includes(expectedHTML), `embedded console mismatch: ${file}`);
  }
});
check('shell syntax, JSON config and high-confidence secret scan', () => {
  for (const file of [...files(kit),...files(runtime),...files(rpmRoot)]) {
    if (file.endsWith('.sh')) run('bash',['-n',file]);
    if (file.endsWith('.json') || file.endsWith('.json.example')) JSON.parse(fs.readFileSync(file,'utf8'));
    if (fs.statSync(file).size > 2e6) continue;
    const contents = fs.readFileSync(file,'utf8');
    assert.ok(!/-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----\r?\n[A-Za-z0-9+/=\r\n]{60,}/.test(contents), `private key: ${file}`);
    assert.ok(!/\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})\b/.test(contents), `token: ${file}`);
  }
});
check('installer help runs without deployment', () => {
  const help = run('bash',[path.join(kit,'install_clusterguard.sh'),'--help']);
  assert.ok(help.includes('--execute') && /plan|计划/.test(help));
});
const report = {status:'passed', archive:path.basename(archive), sha256:sha(archive), commit:expectedCommit,
  console_sha256:sha(path.join(source,'internal/api/console.html')), metadata, checks,
  deployed:false, field_acceptance:false,
  database_vendor_packages_included:databasePackages.length > 0 && databasePackages.some(name => /mysql/i.test(name)) && databasePackages.some(name => /postgresql/i.test(name)),
  limits:['No target Linux installation was executed.', 'No live MySQL or PostgreSQL recovery, VIP or rolling upgrade was executed.', 'RPM dependency signature trust must be verified on the target OS.']};
fs.mkdirSync(path.dirname(reportPath),{recursive:true});
fs.writeFileSync(reportPath,JSON.stringify(report,null,2) + '\n');
console.log(JSON.stringify(report));
