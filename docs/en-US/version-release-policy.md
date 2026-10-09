# ClusterGuard HA Version and Release Policy

> **Document role: mandatory policy for the applicable task.** Read before building, signing, releasing, or handling delivery artifacts, together with the [contract](../upgrade-validation-chain.md), [gate workflow](../zh-CN/validation-gate-workflow.md), [build checklist](../development/rules/release-checklist.md), and applicable licensing/channel rules. Dated implementation results provide context; acceptance for this run needs its own bindings and actual evidence.

> Gate workflow update (2026-10-04): use `--stage source --strict` for source development. Artifact and field acceptance require `--stage artifact` or `--stage field`, `--strict`, and `--acceptance-report FILE` with actual evidence. The default remains field; real ART/FIELD evidence is still unavailable. See the [workflow](../zh-CN/validation-gate-workflow.md).

## v2 Release Preconditions

Updates and hotfixes enforce the v2 contract: failed hotfixes use `retry`; interrupted rolling updates use `resume`. The repair recorded on 2026-10-03 used baseline `7b643f4` on `codex/2.2-postgresql`; it produced no new package or deployment and recorded ART/FIELD as **OPEN**. That historical record does not replace this delivery's source, package identity, or applicable strict acceptance.

The four build entry points run `verify-upgrade-validation-chain.cjs --contract-only` before producing/signing artifacts and require Node (`CG_NODE_BIN` may override its path). This checks contract loading and schema consistency only, not complete PRE/ART/FIELD acceptance. Run the complete gate and `--strict` before release; OPEN blocks formal release. Past results are in the [historical implementation and acceptance snapshot](upgrade-validation-chain-implementation-status.md), which is not an acceptance input for this run. Hotfix acceptance covers same-package retry, rejected resume, signed supersedes, and independent history; rolling acceptance covers resume. Never rebuild a delivered identity in place.

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/version-release-policy.md)
<!-- /LANGUAGE-SWITCH -->

Product identities use four segments. The new configuration editing/distribution feature is `3.1.2.1`; defect repairs within a feature release increment BUGFIX. The RPM format below describes installation eligibility, not the runtime product version.

## 1. RPM Version Format

ClusterGuard HA packages use the following format:

```text
MAJOR.FEATURE-RELEASE
```

For example:

```text
2.1-45
2.2-1
2.2-2
```

The corresponding Git tag uses a dot-separated format:

```text
v2.1.45
v2.2.1
v2.2.2
```

The rules are as follows:

- A major version change indicates a significant change in the control kernel, data model, or compatibility boundary.
- A feature version change indicates the start of a new product capability line, for example, PostgreSQL starting from 2.2.
- The release number strictly increments within the same feature version. Any changes affecting users, such as fixes, features, installers, configurations, dependencies, and delivery documentation, must increase the release number.

## 2. Current Version Lines

| Version Line | Status | Description |
| --- | --- | --- |
| `2.1-45` | Frozen | MySQL stable line, no new features will be added |
| `2.2-N` | Development Line | PostgreSQL capabilities and new features after 2.1 |

`v2.1.45` permanently points to the commit
`28425925a321655f38686942c6b081d235b0d288`. Subsequent fixes must not move this tag.

## 3. Immutable Release Rules

After a formal release is completed, the following objects must not be overwritten:

- Git tags;
- GitHub Releases;
- RPMs;
- Complete offline packages;
- Signed `.cgupgrade` update packages, with legacy `.cgpatch` compatibility;
- Independent PostgreSQL dependency packages;
- SHA256 files;
- Release Notes.

Hotfix `.cgpatch` filenames are part of the delivery identity. The new sealed release line starts at `3.1.1.1`; new specifications use `patch_version` and the [hotfix version and filename rules](hotfix-package-naming.md): `clusterguard-MAJOR.MINOR.PATCH.BUGFIX.<arch>.cgpatch`, with the same version signed into the manifest. New sealed package IDs equal the four-part product version; `HF-...` is retained only for historical identities; historical 2.x/HF specifications retain their frozen names. Signed files are never rebuilt in place, and a failed same-package retry keeps the original file.

Any changes to the content of files with the same name are considered a new version. Even if only the installation script or operation manual inside the package is modified, the release number must be increased because the delivery package has changed.

The following practices are prohibited:

- Deleting old tags and recreating tags with the same name;
- Replacing formal Release attachments with `--clobber`;
- Modifying RPM content but keeping the same NEVRA;
- Manually replacing scripts in the offline package without updating the summary;
- Building a formal release from an uncommitted working tree and making the Release point to another commit.

### 3.1 Publication Channel Split (from 2026-09-22)

Delivery artifacts are split into two tiers, and they go to **different channels**:

| Artifact | Channel |
| --- | --- |
| Complete offline installation media `clusterguard-ha-<version>-offline-linux-x86_64.tar.gz`, the ClusterGuard RPM, their `.sha256` files, `RELEASE-INFO`, `verification.json` | GitHub Release (public channel) |
| Signed `.cgupgrade` update packages (including legacy `.cgpatch`) and their `.sha256` files | **Contracted enterprise customers only; never uploaded to GitHub or any public channel** |

The public channel carries complete installation media only. An update package is an enterprise
artifact, and **its presence on a public channel is unauthorized distribution**: it must be
removed immediately and treated as a release incident. The converse holds too: **every release on
the public channel must carry complete offline media**. A release offering only the RPM, or only
release notes, serves no purpose and must be deleted.

- Update packages remain subject to the immutable rules above; they are simply absent from the
  public channel.
- The build script and the local `release/` directory still hold update packages. They **must not**
  be added to GitHub Release attachments.
- When removing an unauthorized attachment, **remove its `.sha256` in the same step**, otherwise
  the release keeps a digest pointing at a file that no longer exists.
- When removing a whole release, **keep the Git tag**: the tag is source provenance, not a
  distribution artifact, and it stays reachable from the remote branch, so the delivery-record gate
  is unaffected. Use `--cleanup-tag` only once nothing references the tag.
- Check command: `node tools/verify-public-release-assets.cjs`. It applies three rule groups and
  exits non-zero when any matches: (1) the `.cgupgrade` / `.cgpatch` suffix rules and the
  `<from>_to_<to>` naming rule across every release, drafts included; (2) a **published** release
  that carries no complete offline media is a violation. Drafts are exempt by default because a
  draft is the staging area while the kit is still uploading; pass `--include-drafts` to cover them
  as well; (3) the media itself must be usable: a `<name>.sha256` companion, at least 10 MiB, and
  an `uploaded` state. Matching the file name alone cannot catch an empty tar, a truncated upload,
  or a kit published without its digest, so those are checked separately. Missing `RELEASE-INFO` /
  `verification.json` is reported as a warning (earlier releases never carried them) and only
  becomes a failure with `--strict`. Run it before and after uploading; add `--verify-download` to
  check the bytes themselves against the published digest.
- Precedent one: `v2.2.68` once published `clusterguard-ha-2.2-66_to_2.2-68.x86_64.cgupgrade`
  (36,005,513 bytes). It was removed on 2026-09-22; the local copy under
  `release/2.2-68-user-e2e/` matches digest
  `402256420e44473a3c37206ab2d3b53535cda00fd717c33bc78e1dd024772565`, so nothing was lost.
- Precedent two: the same `v2.2.68` release carried only `clusterguard-ha-2.2-68.x86_64.rpm` and its
  `.sha256`, with no complete offline media, so it was **deleted in full** on 2026-09-22 (the tag
  was kept and points at `bc0546a`). Before deleting, the asset was downloaded and compared: the
  remote copy and the local `release/2.2-68-user-e2e/` copy share digest
  `9c20ca1823706c0bdeccca239c563658645cfefdd748b9e5fb2f27544a5c8a58`, so nothing was lost. After
  the deletion GitHub's Latest marker moved back to `v2.2.39`, which does carry complete media.

## 4. Branch Rules

- Keep only `codex/2.2-postgresql` as the mainline. Commit all features and repairs there with traceable history; keep historical tags immutable.
- The working tree must be clean before building a formal package; the commits in `RELEASE-INFO` must be consistent with the release tag.

Do not rewrite changes for 2.2 PostgreSQL back to `v2.1.45`, nor deliver 2.2 code using 2.1 filenames.

## 5. PostgreSQL 2.2 Delivery Rules

The main offline package for 2.2 contains only a minimal set of signed dependencies required for ClusterGuard and database operation. PostgreSQL official source code compilation dependencies use the following strategy:

1. When `--engine postgresql` is explicitly specified, the default is to resolve from the software source configured on the build node over the network.
2. Compilation dependencies are installed into a temporary DNF installroot, without modifying the production host's RPM database.
3. If the network connection fails, the process must explicitly stop and prompt the user to upload an independent PostgreSQL dependency package.
4. In purely offline environments, `--postgresql-dependencies` is used to specify the decompressed independent dependency repository.
5. The main package and dependency packages are separately verified for SHA256, RPM signature, architecture, system major version, and repository metadata.

Typical 2.2 dependency add-on package name:

```text
clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64.tar.gz
```

This dependency add-on package must not be repackaged into the main offline package, so MySQL-only deployments do not carry the PostgreSQL build toolchain.

## 6. Formal Release Gate

Each formal version must complete at least:

```bash
go test ./...
go vet ./...
find scripts -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
git diff --check
```

And verify:

1. Git working tree is clean, and the tag points to the current commit.
2. RPM name, version, and architecture are correct.
3. Both layers of SHA256 inside and outside the offline package pass.
4. RPM signature, dependencies, systemd unit, and configuration permissions are correct.
5. The installer's read-only plan and actual installation both pass in a clean three-node environment.
6. Corresponding database versions complete switching, failure, old master recovery, restart, network partition, and concurrency testing.
7. Security gate, Leader forwarding, Raft majority, VIP uniqueness, audit, and report all pass.
8. Release Notes clearly specify the supported scope, known boundaries, and upgrade methods.
9. The patch contains target and rollback RPMs, and passes signature, compatibility, rolling-order, resume, and automatic-rollback tests.

If any gate fails, only an internal candidate package can be generated, and formal tags or GitHub Releases cannot be created.

`release_channel` in `RELEASE-INFO` is decided automatically by the build script
(`scripts/build-clusterguard-offline-kit.sh`) from whether the **source tree is clean**: clean
writes `stable`, otherwise `candidate`. It does **not** mean the gates above have passed. A
`release_channel=stable` archive can therefore still be an internal candidate package; formal
release eligibility depends only on whether every gate in this section passes, and
`release_channel` never substitutes for site acceptance.

## 7. Release Steps

Formal release is executed in the following order:

1. Gather and submit all changes for this version.
2. Execute full testing and on-site acceptance.
3. Build RPM, main offline package, signed update package, and necessary independent dependency packages from a clean commit (the update package stays local; see §3.1).
4. Verify the summary, signature, package content, and installation process.
5. Create an annotated tag, for example, `v2.2.1`.
6. Push the commit and tag.
7. Create a GitHub Release and upload read-only attachments — **the complete installation media, their `.sha256` files, `RELEASE-INFO` and `verification.json` only**. Signed `.cgupgrade` and legacy `.cgpatch` update packages **must not be uploaded** (see §3.1). Run `node tools/verify-public-release-assets.cjs` before and after uploading: it checks for unauthorized update packages and for any **published release without complete offline media**. A release missing its media must not stay on the public channel.
8. Redownload the attachments from GitHub and perform a summary and installation smoke test again.

After the release is completed, only new versions are allowed to fix issues.

See the [Version Update and Rollback Guide](update-and-patch.md) for field update, resume,
and rollback commands. The release private key must never enter the repository,
RPM, offline kit, or a customer server.
