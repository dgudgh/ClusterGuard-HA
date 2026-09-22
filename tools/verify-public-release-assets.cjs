#!/usr/bin/env node
// Verifies that no enterprise-only artifact is published on the public release
// channel.
//
// Why this exists: the delivery model splits in two. Complete installation media
// go to the public channel; signed rolling-update packages are supplied to
// contracted customers only. GitHub once carried
// `clusterguard-ha-2.2-66_to_2.2-68.x86_64.cgupgrade` on release v2.2.68, so the
// public channel was handing out an enterprise artifact. It was removed on
// 2026-09-22 and this check keeps it from coming back.
//
// Usage:
//   node tools/verify-public-release-assets.cjs [--repo <owner/name>]
//
// Rules applied to every asset name:
//   cgupgrade-suffix  `.cgupgrade` / `.cgupgrade.sha256`
//   cgpatch-suffix    `.cgpatch` / `.cgpatch.sha256` (legacy compatibility)
//   upgrade-naming    any name containing `_to_`, the `<from>_to_<to>` shape used
//                     by rolling-update packages, which catches an upgrade
//                     artifact that was renamed to hide its suffix
//
// Anything matching a rule is a failure. A release carrying no complete offline
// kit is reported as a notice instead: legacy releases predate the rule, and the
// public channel is not required to carry an installer for every historical
// version.
//
// Exit code is non-zero when any rule matches. This script talks to GitHub and
// needs an authenticated `gh`.

const cp = require('node:child_process');

const flag = (name) => {
  const index = process.argv.indexOf(name);
  return index === -1 ? '' : (process.argv[index + 1] || '');
};

const repo = flag('--repo') || 'dgudgh/ClusterGuard-HA';

const RULES = [
  { name: 'cgupgrade-suffix', test: (n) => /\.cgupgrade(\.sha256)?$/i.test(n) },
  { name: 'cgpatch-suffix', test: (n) => /\.cgpatch(\.sha256)?$/i.test(n) },
  { name: 'upgrade-naming', test: (n) => n.includes('_to_') },
];

const OFFLINE_KIT = /-offline-linux-.*\.tar\.gz$/i;

const gh = (args) => {
  try {
    return {
      ok: true,
      out: cp.execFileSync('gh', args, { encoding: 'utf8', maxBuffer: 32e6, stdio: ['ignore', 'pipe', 'pipe'] }).trim(),
    };
  } catch (error) {
    return { ok: false, out: String(error.stderr || error.message || '').trim() };
  }
};

const result = gh(['api', `repos/${repo}/releases?per_page=100`]);
if (!result.ok) {
  console.error(`cannot list releases for ${repo}: ${result.out}`);
  console.error('this check needs an authenticated gh with read access to the repository');
  process.exit(2);
}

let releases;
try {
  releases = JSON.parse(result.out);
} catch (error) {
  console.error(`cannot parse the release listing: ${error.message}`);
  process.exit(2);
}

const failures = [];
const notices = [];
let assetsChecked = 0;

for (const release of releases) {
  const tag = release.tag_name;
  const assets = Array.isArray(release.assets) ? release.assets : [];
  assetsChecked += assets.length;

  for (const asset of assets) {
    const matched = RULES.filter((rule) => rule.test(asset.name)).map((rule) => rule.name);
    if (matched.length > 0) {
      failures.push({
        tag,
        asset: asset.name,
        size: asset.size,
        state: asset.state,
        matched_rules: matched,
      });
    }
  }

  const names = assets.map((asset) => asset.name);
  if (!names.some((name) => OFFLINE_KIT.test(name))) {
    notices.push({
      tag,
      draft: Boolean(release.draft),
      prerelease: Boolean(release.prerelease),
      assets: names,
      note: 'no complete offline kit on this release',
    });
  }
}

const report = {
  status: failures.length === 0 ? 'passed' : 'failed',
  repo,
  releases_checked: releases.length,
  assets_checked: assetsChecked,
  failures,
  notices,
};

console.log(JSON.stringify(report, null, 2));

if (failures.length > 0) {
  console.error('');
  console.error(`public channel carries ${failures.length} enterprise-only artifact(s):`);
  for (const failure of failures) {
    console.error(`  ${failure.tag}  ${failure.asset}  (${failure.matched_rules.join(', ')})`);
  }
  console.error('remove them: gh release delete-asset <tag> <asset> --repo ' + repo);
  process.exit(1);
}

console.log('');
console.log(`public channel is clean: ${releases.length} release(s), ${assetsChecked} asset(s), 0 enterprise-only artifact(s)`);
for (const notice of notices) {
  console.log(`notice: ${notice.tag} carries no complete offline kit (assets: ${notice.assets.join(', ') || 'none'})`);
}
