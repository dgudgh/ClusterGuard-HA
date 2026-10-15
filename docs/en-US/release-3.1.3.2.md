# ClusterGuard HA 3.1.3.2 Complete Offline Installation Candidate

## Identity and status

Product **3.1.3.2**, RPM installation and compatibility baseline **2.2-110**. Approved production templates and an independent software-directory option add functionality, advancing the PATCH segment to 3 and restarting BUGFIX at 1. The first internal 3.1.3.1 candidate was rejected for incorrect bundled documentation; this candidate increments BUGFIX to 2 without replacing old bytes.

This complete installation candidate is local at `release/3.1.3.2/` only. Source and documentation are committed on `codex/2.2-postgresql`; the kit's `RELEASE-INFO` records its exact clean build commit. **No GitHub Release is published for this candidate. ART/FIELD remain OPEN.**

## Changes

- MySQL 8.0 installation uses the approved production primary/replica cnf originals. Other parameters, comments, order and duplicate entries are preserved. Only deployment paths, ports, node identity and the approved read-only startup guards change. Both originals are shipped.
- Software defaults to `/usr/local/mysql`; the data root defaults to `/data`, giving `/data/mysql/<port>` per MySQL instance. Configure them independently with `--database-software-dir` and `-nd/--database-data`. Select site templates with `--mysql-master-cnf` and `--mysql-slave-cnf`. Duplicate generated node identities are rejected before execution.
- Every node starts read-only. The explicitly confirmed first-install bootstrap configures replication, then sets `read_only` and `super_read_only` to `OFF` on the first primary while replicas remain read-only. This bootstrap step precedes controller initialization; runtime Agent and writer lease enforcement subsequently protect write ownership.
- When two replicas only lose semi-sync ACKs, the current qualified primary can retain its VIP by renewing its existing, unexpired writer lease for the same owner. Semi-sync remains degraded. Controller quorum, Agent observation quorum, proof of primary writability, sole VIP ownership, maintenance gates and fencing remain mandatory. Missing evidence still revokes authority; this path never authorizes a new primary.
- Includes committed disaster-recovery fixes for physical binlog loss, actual log history, guarded reconstruction and native error retention, plus runtime-parameter layout and bilingual message fixes. Cloning retains authorization, fencing and read-only guards.
- Adds the [complete offline installer command reference](offline-installer-parameters.md): every option, alias, default, required condition, effect, conflict and safe input method. Scope is installation commands only, not a platform or cnf parameter encyclopedia. Both languages are shipped in the kit's `docs/` directory.

## Media

- `clusterguard-ha-3.1.3.2-offline-linux-x86_64.tar.gz` and its `.sha256` sidecar.
- `clusterguard-ha-2.2-110.x86_64.rpm` and its `.sha256` sidecar.
- `RELEASE-INFO`, `SHA256SUMS`, structural verification report `verification.json` and installation guides.

The complete tar contains the installer, five programs, systemd units, installation/configuration/upgrade/recovery scripts, production templates, static Linux jq, Rocky Linux 8 x86_64 base runtime dependencies and repository metadata, the public trust key, and AGPL-3.0-only/third-party licenses.

**Database vendor archives are not embedded by default.** Supply an approved full vendor archive matching the target system, architecture and version, select it explicitly with `-r FILE` or use the documented `/opt` discovery, and provide its `.sha256` sidecar. PostgreSQL source compilation needs matching independent offline build dependencies; base runtime dependencies do not replace them. No new field support is claimed for other OS/database combinations.

After extraction, verify checksums and review help and a read-only plan. See the [offline installation manual](offline-rpm-install.md). The complete tar is installation media, not a `.cgupgrade` or `.cgpatch` upload for the Console.

## Verification boundaries

The development task records SOURCE checks; `verification.json` records actual complete-media structure and integrity checks. Real Chrome audits use isolated fixture APIs and do not connect to site databases.

The current RPM builder does not produce a production-signed RPM. Structural verification does not replace mandatory signature ART acceptance, and this version does not claim full ART approval. No three-node installation, real systemd restart, replication/VIP failure, switchover or disaster-recovery field acceptance was executed for this candidate. Verify target OS RPM trust, media and site configuration and schedule the corresponding acceptance before updating.
