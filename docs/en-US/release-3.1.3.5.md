# ClusterGuard HA 3.1.3.5 Complete Offline Installation Candidate

## Identity and scope

Product 3.1.3.5, RPM baseline 2.2-113, sole directory release/3.1.3.5/. This fixes Helper exit226/NAMESPACE during first startup after MySQL and controller installation, when the shared runtime directory is absent. Previous 3.1.3.4 artifacts remain unchanged. Source and documentation use the sole codex/2.2-postgresql branch.

This is a local manual-installation candidate. No GitHub Release is created; production signature ART and complete three-node FIELD remain OPEN.

## Fixes

- systemd resolves ReadWritePaths before ExecStartPre. Only volatile /run/clusterguard may initially be absent; the normal pre-command creates it as root:clusterguard,0755. Persistent update/private/configuration/state/log directories remain mandatory.
- The installer also prepares the shared parent before starting Helper. Startup failures report systemd state and existence/permissions of three relevant directories while retaining the error.
- The main process retains PrivateTmp, ProtectHome, NoNewPrivileges and Unix peer-credential checks. No shared RuntimeDirectory ownership, privileged main-process prefix or SELinux disablement is introduced. Stopping Helper preserves neighboring files.
- Existing MySQL failed-installation proof, whole-instance preservation, locking, mount protection, approved production cnf and /data rules remain.

## Resume the current manual installation

The user log completed three MySQL installations, two replica synchronizations and controller startup, then failed while configuring Helper. Current .152 MySQL is active with read_only/super_read_only0, and site phase is registered. **Do not move database directories, clear site state or regenerate keys.**

Run the original command from the new kit, retaining the original state, secrets and work directory. The installer checks management markers on every data node; consistent registered state skips initialization and first replica synchronization. Inconsistent markers reject cluster-wide replacement. Do not rewrite state to bypass it. A complete current resume command accompanies the artifact in MANUAL-TEST.md.

Database software defaults to /usr/local/mysql and the data root to /data; ordinary approved production parameters are retained. Supply the complete vendor archive and checksum via-r or documented /opt discovery. The full tar is not a console cgpatch.

## Evidence boundaries

Independent named units and paths on real systemd239 reproduce old226 and validate the fixed unit using the actual Helper binary: first startup, healthz, neighboring-file preservation on stop, restart after removing only the test runtime, unknown-UID rejection, socket660/private700, main-process namespace and NoNewPrivs. A missing persistent private directory still fails226. The real Helper, databases and site configuration were not changed on the user's behalf.

Source, regression, media and diagnostic cleanup results are recorded in the [task](../development/update-helper-namespace-2026-10-15.md). Isolated Helper lifecycle tests do not establish three-node installation, replication/VIP/recovery or production signature acceptance. See the [installation manual](offline-rpm-install.md) and [complete CLI reference](offline-installer-parameters.md).

## Build receipt

Clean source2024567, zero untracked files, pushed to GitHub mainline. Complete tar:33,850,253 bytes; SHA256:809abbf457f851134471de8ad3689d9f66da66c91154c4bb12fc06da29dd4c85. All15 structure checks and three actual installer/runtime-unit/RPM-unit byte comparisons passed; previous3.1.3.4 tar is unchanged. SOURCE42 checks,114 mutations,40 Go packages and109 Chrome assertions passed. Resume command and manual checklist accompany the artifact. Signature ART/FIELD remain OPEN; this receipt was appended after the immutable build.
