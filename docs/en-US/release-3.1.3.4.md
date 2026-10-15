# ClusterGuard HA 3.1.3.4 Complete Offline Installation Candidate

## Identity and purpose

Product **3.1.3.4**, RPM installation/compatibility baseline **2.2-112**. This candidate is for the user's manual full-installation and failure-resume testing. Its only artifact directory is `release/3.1.3.4/`; previous 3.1.3.2/3.1.3.3 bytes remain unchanged. Source and documentation use the sole branch `codex/2.2-postgresql`.

**No GitHub Release is published; signature ART and real three-node FIELD remain OPEN.** Source, isolated native checks and media structure checks are recorded separately. Production delivery acceptance is incomplete until the manual three-node installation passes.

## Fixes

- The existence of `data/mysql/` no longer establishes completed initialization. Failed native initialization may create it; the previous diagnostic incorrectly called managed residue unmanaged.
- An atomic installation journal binds node, port, paths, vendor archive, approved configuration, directory identity and failed data/log evidence. Only proven empty-directory preparation or an observed nonzero native initialization result can resume. Unknown, completed or changed evidence protects the original database.
- Resume preserves the entire failed instance in a unique sibling backup, including data, configuration, logs and phase journal. A legacy `.installing` marker no longer permits deleting partial managed data. Software ownership and service quiescence checks remain enforced.
- A stable parent `flock` serializes same-port installation. The RPM explicitly requires system util-linux; MySQL data-node preflight reports missing tooling early.
- Ambiguous legacy residue is rejected before clock, RPM or firewall mutations, with the original log path and preservation guidance.

The strict runtime validation, disposable initialization-only semi-sync compatibility and password-redacted diagnostics from 3.1.3.3 remain. Approved production primary/replica cnf parameters are retained, apart from deployment paths, unique identity and the confirmed read-only startup protection.

## Existing legacy failed installation

Versions 3.1.3.2/3.1.3.3 have no reliable phase journal; do not backfill a new proof. An operator must confirm no mysqld process or target-port listener, a fresh failed initialization and no business data, confirm no instance or child-path mounts (including bind mounts), then preserve the entire instance by renaming it. Ancestor mounts such as `/data` are allowed. Retain the original state, secrets and work directory and reuse the original command with this kit. Do not delete data, markers or site state, or set `rebuild=true`.

See [installation manual §12.10](offline-rpm-install.md#1210-failed-initialization-resume-and-manual-delivery-acceptance) for the manual acceptance checklist and [installer options](offline-installer-parameters.md) for every CLI parameter. The user retains control of the real three-node installation.

## Media and evidence

Artifacts are `clusterguard-ha-3.1.3.4-offline-linux-x86_64.tar.gz`, its checksum, RPM, `RELEASE-INFO`, `SHA256SUMS` and `verification.json`. The tar includes the installer, five programs, systemd units, helper scripts, production templates, static jq, offline base runtime dependencies, public key, licenses and bilingual installer option references.

Vendor database archives are not embedded by default. Supply the approved complete vendor archive and checksum using `-r FILE` or the documented `/opt` discovery. Software defaults to `/usr/local/mysql` and the data root to `/data`. Custom directories must satisfy target permissions and SELinux policy; disabling SELinux is not acceptance.

Current results, raw evidence and baseline are recorded in the [development task](../development/mysql-install-resume-2026-10-15.md). Isolated native full-helper checks use a separate port/directory and temporary systemd bridge to test real MySQL startup and accounts. They do not establish three-node replication, VIP, switchover or disaster-recovery acceptance. Media structure verification does not replace production signature ART; unsigned RPM and current acceptance-model limitations remain recorded.
