# ClusterGuard HA 3.1.3.3 Complete Offline Installation Candidate

## Identity and status

Product **3.1.3.3**, RPM installation and compatibility baseline **2.2-111**. This BUGFIX release repairs the 3.1.3.2 initialization failure with real MySQL 8.0.44 semi-sync plugin options. Existing artifacts are not overwritten.

This complete installation candidate is local at `release/3.1.3.3/` only. Source and documentation are committed on `codex/2.2-postgresql`; the kit's `RELEASE-INFO` records its exact clean build commit. **No GitHub Release is published for this candidate. ART/FIELD remain OPEN.**

## Installation fix

MySQL initialization ignores plugin-load, leaving strict semi-sync variables unregistered. Previously the helper initialized directly with the production runtime configuration, exited 1 and wrote the actual error to the instance error.log while the terminal only reported a failed remote step.

The helper still validates the unchanged runtime configuration strictly, then creates a 0600 disposable initialization copy. Only semi-sync plugin options in that copy receive a loose prefix. Values, InnoDB file layout, case handling and all other parameters stay unchanged. The copy is removed on success and failure; systemd continues to use the original my.cnf. Production originals and runtime configuration stay strict, without automatic tuning.

Configuration validation and initialization errors now report the original status, log path and final 80 native log lines. Actual password values are redacted; payloads are never printed.

### Resuming an already failed installation

Preserve deployment state, secrets, work-dir and the original instance error log. If failed initialization already created data/mysql/, existing data protection still rejects replacement. Do not remove data, ownership markers or site state to bypass that protection. Only after verifying a fresh failed installation with no running process or business data, quarantine the entire failed instance directory before retrying the corrected media with the original site files. Existing real databases require controlled recovery or rebuild. This version does not automatically move or remove site data.

## Cumulative changes

- MySQL 8.0 installation uses the approved production primary/replica cnf originals. Other parameters, comments, order and duplicate entries are preserved. Only deployment paths, ports, node identity and the approved read-only startup guards change. Both originals are shipped.
- Software defaults to `/usr/local/mysql`; the data root defaults to `/data`, giving `/data/mysql/<port>` per MySQL instance. Configure them independently with `--database-software-dir` and `-nd/--database-data`. Select site templates with `--mysql-master-cnf` and `--mysql-slave-cnf`. Duplicate generated node identities are rejected before execution.
- Every node starts read-only. The explicitly confirmed first-install bootstrap configures replication, then sets `read_only` and `super_read_only` to `OFF` on the first primary while replicas remain read-only. This bootstrap step precedes controller initialization; runtime Agent and writer lease enforcement subsequently protect write ownership.
- When two replicas only lose semi-sync ACKs, the current qualified primary can retain its VIP by renewing its existing, unexpired writer lease for the same owner. Semi-sync remains degraded. Controller quorum, Agent observation quorum, proof of primary writability, sole VIP ownership, maintenance gates and fencing remain mandatory. Missing evidence still revokes authority; this path never authorizes a new primary.
- Includes committed disaster-recovery fixes for physical binlog loss, actual log history, guarded reconstruction and native error retention, plus runtime-parameter layout and bilingual message fixes. Cloning retains authorization, fencing and read-only guards.
- Adds the [complete offline installer command reference](offline-installer-parameters.md): every option, alias, default, required condition, effect, conflict and safe input method. Scope is installation commands only, not a platform or cnf parameter encyclopedia. Both languages are shipped in the kit's `docs/` directory.

## Media

- `clusterguard-ha-3.1.3.3-offline-linux-x86_64.tar.gz` and its `.sha256` sidecar.
- `clusterguard-ha-2.2-111.x86_64.rpm` and its `.sha256` sidecar.
- `RELEASE-INFO`, `SHA256SUMS`, structural verification report `verification.json` and installation guides.

The complete tar contains the installer, five programs, systemd units, installation/configuration/upgrade/recovery scripts, production templates, static Linux jq, Rocky Linux 8 x86_64 base runtime dependencies and repository metadata, the public trust key, and AGPL-3.0-only/third-party licenses.

**Database vendor archives are not embedded by default.** Supply an approved full vendor archive matching the target system, architecture and version, select it explicitly with `-r FILE` or use the documented `/opt` discovery, and provide its `.sha256` sidecar. PostgreSQL source compilation needs matching independent offline build dependencies; base runtime dependencies do not replace them. No new field support is claimed for other OS/database combinations.

After extraction, verify checksums and review help and a read-only plan. See the [offline installation manual](offline-rpm-install.md). The complete tar is installation media, not a `.cgupgrade` or `.cgpatch` upload for the Console.

## Verification boundaries

Real MySQL 8.0.44 tests in isolated directories reproduced the old primary-template initialization failure. Fixed primary and replica templates initialized and passed strict runtime validation; an ordinary unknown option still failed. Runtime my.cnf and 34 InnoDB parameter lines were unchanged. These are isolated native checks, not completion of installation on the site 3306 instance.

The development task records SOURCE checks; `verification.json` records actual complete-media structure and integrity checks. Real Chrome audits use isolated fixture APIs and do not connect to site databases.

The current RPM builder does not produce a production-signed RPM. Structural verification does not replace mandatory signature ART acceptance, and this version does not claim full ART approval. No three-node installation, real systemd restart, replication/VIP failure, switchover or disaster-recovery field acceptance was executed for this candidate. Verify target OS RPM trust, media and site configuration and schedule the corresponding acceptance before updating.
