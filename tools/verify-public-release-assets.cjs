#!/usr/bin/env node
// Verifies that no enterprise-only artifact is published on the public release
// channel, and that every published release actually carries usable offline media.
//
// Why this exists: the delivery model splits in two. Complete installation media
// go to the public channel; signed rolling-update packages are supplied to
// contracted customers only. GitHub once carried
// `clusterguard-ha-2.2-66_to_2.2-68.x86_64.cgupgrade` on release v2.2.68, so the
// public channel was handing out an enterprise artifact. It was removed on
// 2026-09-22 and this check keeps it from coming back.
//
// Usage:
//   node tools/verify-public-release-assets.cjs [--repo <owner/name>] [--include-drafts]
//                                               [--strict] [--verify-download]
//
// Rules applied to every asset name:
//   cgupgrade-suffix  `.cgupgrade` / `.cgupgrade.sha256`
//   cgpatch-suffix    `.cgpatch` / `.cgpatch.sha256` (legacy compatibility)
//   upgrade-naming    any name containing `_to_`, the `<from>_to_<to>` shape used
//                     by rolling-update packages, which catches an upgrade
//                     artifact that was renamed to hide its suffix
//
// Anything matching a rule is a failure.
//
// A release-level rule also applies: a published release must carry a complete
// offline kit (`*-offline-linux-*.tar.gz`). The public channel carries complete
// installation media only, so a release that offers no installer has no business
// being published. Release v2.2.68 was exactly this - an RPM with no kit - and
// was deleted on 2026-09-22. Drafts are exempt by default, because a draft is the
// staging area while the kit is still being uploaded; pass --include-drafts to
// apply the rule to drafts too.
//
// Matching the file name alone was never enough: a truncated upload, an empty tar
// and a kit shipped without its checksum file all satisfy a name pattern. Each
// offline kit is therefore also checked for size, upload state and a sibling
// `.sha256` companion. Missing provenance files (`RELEASE-INFO`,
// `verification.json`) are reported as warnings, because releases published
// before that convention existed never carried them; pass --strict to promote
// them to failures, which is the bar a freshly published release should meet.
//
// --verify-download goes further and compares checksums against the bytes that
// actually come back over the wire. It transfers hundreds of megabytes per kit,
// so it is opt-in.
//
// Exit code:
//   0  nothing matched
//   1  a rule matched (or, with --strict, a warning was promoted)
//   2  the release listing could not be read

const cp = require('node:child_process');
const crypto = require('node:crypto');

const flag = (name) => {
  const index = process.argv.indexOf(name);
  return index === -1 ? '' : (process.argv[index + 1] || '');
};

const repo = flag('--repo') || 'dgudgh/ClusterGuard-HA';
const includeDrafts = process.argv.includes('--include-drafts');
const strictMode = process.argv.includes('--strict');
const verifyDownload = process.argv.includes('--verify-download');

const RULES = [
  { name: 'cgupgrade-suffix', test: (n) => /\.cgupgrade(\.sha256)?$/i.test(n) },
  { name: 'cgpatch-suffix', test: (n) => /\.cgpatch(\.sha256)?$/i.test(n) },
  { name: 'upgrade-naming', test: (n) => n.includes('_to_') },
];

const OFFLINE_KIT = /-offline-linux-.*\.tar\.gz$/i;
// Every kit built so far is far above this: a genuine offline bundle carries the
// base runtime repository. Anything below is a truncated upload or a placeholder.
const MINIMUM_OFFLINE_KIT_BYTES = 10 * 1024 * 1024;
// 64 hex digits plus the file name; anything shorter cannot hold a real digest.
const MINIMUM_CHECKSUM_FILE_BYTES = 32;
const PROVENANCE_FILES = ['RELEASE-INFO', 'verification.json'];

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

// The listing returns 100 entries per page. Reading one page silently hides every
// release past the newest hundred, so walk until a short page comes back.
function listReleases() {
  const collected = [];
  for (let page = 1; page <= 100; page += 1) {
    const result = gh(['api', `repos/${repo}/releases?per_page=100&page=${page}`]);
    if (!result.ok) {
      return { error: `cannot list releases for ${repo}: ${result.out}` };
    }
    let batch;
    try {
      batch = JSON.parse(result.out);
    } catch (error) {
      return { error: `cannot parse the release listing: ${error.message}` };
    }
    if (!Array.isArray(batch)) {
      return { error: `unexpected release listing shape on page ${page}` };
    }
    collected.push(...batch);
    if (batch.length < 100) {
      return { releases: collected, pages: page };
    }
  }
  return { error: 'release listing did not terminate within 100 pages' };
}

async function downloadAsset(asset) {
  const token = (process.env.GITHUB_TOKEN || '').trim() || gh(['auth', 'token']).out;
  if (!token) {
    return { error: 'no GitHub token available (set GITHUB_TOKEN or run gh auth login)' };
  }
  try {
    const response = await fetch(asset.url, {
      redirect: 'follow',
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: 'application/octet-stream',
        'User-Agent': 'clusterguard-public-release-gate',
        'X-GitHub-Api-Version': '2022-11-28',
      },
    });
    if (!response.ok) {
      return { error: `download failed with HTTP ${response.status}` };
    }
    const body = Buffer.from(await response.arrayBuffer());
    return { digest: crypto.createHash('sha256').update(body).digest('hex'), bytes: body.length, body };
  } catch (error) {
    return { error: `download failed: ${error.message}` };
  }
}

async function main() {
  const listed = listReleases();
  if (listed.error) {
    console.error(listed.error);
    console.error('this check needs an authenticated gh with read access to the repository');
    process.exit(2);
  }

  const releases = listed.releases;
  const failures = [];
  const missingOfflineKit = [];
  const incompleteMedia = [];
  const warnings = [];
  let assetsChecked = 0;

  const note = (entry) => {
    // Warnings describe historical choices that must not silently become the
    // norm, so they only turn fatal when the caller asks for it with --strict.
    if (entry.kind === 'warning' && !strictMode) {
      warnings.push(entry);
      return;
    }
    delete entry.kind;
    if (strictMode) {
      entry.promoted = true;
    }
    failures.push(entry);
  };

  for (const release of releases) {
    const tag = release.tag_name;
    const draft = Boolean(release.draft);
    const assets = Array.isArray(release.assets) ? release.assets : [];
    assetsChecked += assets.length;
    const names = new Set(assets.map((asset) => asset.name));

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

    const kits = assets.filter((asset) => OFFLINE_KIT.test(asset.name));
    if (kits.length === 0 && (!draft || includeDrafts)) {
      missingOfflineKit.push({
        tag,
        draft,
        prerelease: Boolean(release.prerelease),
        assets: [...names],
        matched_rules: ['missing-offline-kit'],
      });
    }

    for (const kit of kits) {
      const companionName = `${kit.name}.sha256`;
      const companion = assets.find((asset) => asset.name === companionName);
      if (!companion) {
        incompleteMedia.push({
          tag,
          asset: kit.name,
          size: kit.size,
          matched_rules: ['missing-checksum-companion'],
          detail: `no ${companionName} beside the offline kit`,
        });
      } else if (companion.size < MINIMUM_CHECKSUM_FILE_BYTES) {
        incompleteMedia.push({
          tag,
          asset: companionName,
          size: companion.size,
          matched_rules: ['checksum-companion-too-small'],
          detail: `expected at least ${MINIMUM_CHECKSUM_FILE_BYTES} bytes of digest text`,
        });
      }
      if (kit.size < MINIMUM_OFFLINE_KIT_BYTES) {
        incompleteMedia.push({
          tag,
          asset: kit.name,
          size: kit.size,
          matched_rules: ['offline-kit-too-small'],
          detail: `expected at least ${MINIMUM_OFFLINE_KIT_BYTES} bytes`,
        });
      }
      if (kit.state && kit.state !== 'uploaded') {
        incompleteMedia.push({
          tag,
          asset: kit.name,
          size: kit.size,
          state: kit.state,
          matched_rules: ['offline-kit-not-uploaded'],
          detail: `upload state is ${kit.state}`,
        });
      }
      if (companion && verifyDownload) {
        // Names and sizes can both be right while the bytes are wrong.
        const kitDownload = await downloadAsset(kit);
        const companionDownload = await downloadAsset(companion);
        if (kitDownload.error || companionDownload.error) {
          incompleteMedia.push({
            tag,
            asset: kit.name,
            matched_rules: ['download-verification-failed'],
            detail: kitDownload.error || companionDownload.error,
          });
        } else {
          const recorded = companionDownload.body.toString('utf8').trim().split(/\s+/)[0];
          if (recorded.toLowerCase() !== kitDownload.digest) {
            incompleteMedia.push({
              tag,
              asset: kit.name,
              size: kit.size,
              matched_rules: ['checksum-mismatch'],
              detail: `published digest ${recorded} but ${kitDownload.bytes} downloaded bytes hash to ${kitDownload.digest}`,
            });
          }
        }
      }
    }

    if (kits.length > 0 && (!draft || includeDrafts)) {
      for (const expected of PROVENANCE_FILES) {
        if (!names.has(expected)) {
          note({
            tag,
            kind: 'warning',
            asset: expected,
            matched_rules: ['missing-provenance-file'],
            detail: `${expected} is not published alongside the offline kit`,
          });
        }
      }
    }
  }

  const failing = failures.length + missingOfflineKit.length + incompleteMedia.length;

  const report = {
    status: failing === 0 ? (warnings.length > 0 ? 'passed-with-warnings' : 'passed') : 'failed',
    repo,
    releases_checked: releases.length,
    listing_pages: listed.pages,
    drafts_included: includeDrafts,
    strict: strictMode,
    downloads_verified: verifyDownload,
    assets_checked: assetsChecked,
    failures,
    missing_offline_kit: missingOfflineKit,
    incomplete_media: incompleteMedia,
    warnings,
  };

  console.log(JSON.stringify(report, null, 2));

  if (failing > 0) {
    console.error('');
    const promoted = failures.filter((failure) => failure.promoted);
    const forbidden = failures.filter((failure) => !failure.promoted);
    if (forbidden.length > 0) {
      console.error(`public channel carries ${forbidden.length} enterprise-only artifact(s):`);
      for (const failure of forbidden) {
        console.error(`  ${failure.tag}  ${failure.asset}  (${failure.matched_rules.join(', ')})`);
      }
      console.error('remove them: gh release delete-asset <tag> <asset> --repo ' + repo);
    }
    if (promoted.length > 0) {
      console.error(`${promoted.length} strict-mode violation(s) that the default run only warns about:`);
      for (const failure of promoted) {
        console.error(`  ${failure.tag}  ${failure.asset}  (${failure.matched_rules.join(', ')})`);
      }
    }
    if (missingOfflineKit.length > 0) {
      console.error(`public channel carries ${missingOfflineKit.length} release(s) without a complete offline kit:`);
      for (const entry of missingOfflineKit) {
        console.error(`  ${entry.tag}  (assets: ${entry.assets.join(', ') || 'none'})`);
      }
      console.error('a published release must carry complete installation media; remove the release:');
      console.error('  gh release delete <tag> --repo ' + repo + ' --yes   # keep the tag for provenance, or add --cleanup-tag');
    }
    if (incompleteMedia.length > 0) {
      console.error(`${incompleteMedia.length} offline kit asset(s) are not usable:`);
      for (const entry of incompleteMedia) {
        console.error(`  ${entry.tag}  ${entry.asset}  (${entry.matched_rules.join(', ')}) ${entry.detail || ''}`);
      }
      console.error('re-upload the media together with its <name>.sha256 companion, or remove the broken asset.');
    }
    process.exit(1);
  }

  console.log('');
  if (warnings.length > 0) {
    console.log(`${warnings.length} warning(s) - rerun with --strict to treat these as failures:`);
    for (const entry of warnings) {
      console.log(`  ${entry.tag}  ${entry.asset}  ${entry.detail}`);
    }
    console.log('');
  }
  console.log(`public channel is clean: ${releases.length} release(s), ${assetsChecked} asset(s), 0 enterprise-only artifact(s), every release carries a complete offline kit`);
}

main().catch((error) => {
  console.error(`verify-public-release-assets failed unexpectedly: ${error && error.stack ? error.stack : error}`);
  process.exit(2);
});
