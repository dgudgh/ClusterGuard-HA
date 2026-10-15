# ClusterGuard HA Complete Offline Installer Parameter Reference

Target: ClusterGuard HA `3.1.3.2`, RPM `2.2-110`, and the current repository version of `scripts/install_clusterguard.sh`. This page describes source behavior only. Artifact build, upload, and field acceptance require their own stage evidence.

Related manual: “ClusterGuard HA Complete Offline Installation and Deployment Manual.” The actual entry point is `install_clusterguard.sh` at the root of the extracted offline kit.

## 1. Execution boundary

- The installer defaults to `--plan`. It parses arguments, selects and validates media, validates topology/path/credential mapping, and prints a plan without connecting to or changing remote nodes.
- Only an explicit `--execute` enters real installation. Noninteractive execution also requires `-y/--yes`; interactive execution asks the operator to type the cluster name.
- Examples below are fragments or read-only plans. They are not copy-ready field execution commands. Addresses use the documentation subnet `192.0.2.0/24`.
- A successful plan is local input validation. It is not RPM, database, Raft, replication, VIP, or field HA acceptance.
- Control nodes run the API, Console, and Raft. There must be an odd number of at least three control nodes. Data nodes have no odd-count rule, and the two sets may be separate or overlap.

## 2. Quick index

The sections below list every option accepted by the current parser, grouped by function. Short and long forms in one row are equivalent.

| Category | Options |
| --- | --- |
| Nodes and media | `-l`, `-n`, `-g`, `-r`, `--database-package-dir`, `--engine`, `--data-on-arbitrators`, `--control-only` |
| SSH and host identity | `-u`, `-P`, `-p`, `--ssh-credentials-file`, `--ssh-key`, `--ssh-port`, `--known-hosts`, `--accept-host-keys` |
| Paths and state | `-ld`, `-nd`, `--database-software-dir`, `--state-file`, `--secrets-file`, `--work-dir`, `--remote-stage` |
| Database | `--database-version`, `--database-port`, and engine-specific MySQL/PostgreSQL options |
| HA, safety, and network | `--vip`, `--interface`, `--prefix`, `--fencer`, `--fencer-assets`, `--manual-failover-only`, `--no-vip-reconcile`, `--patch-trust-key` |
| Control plane and capacity | `--cluster-name`, `--timezone`, `--api-port`, `--raft-port`, and the two minimum-space options |
| Mode | `--plan`, `--execute`, `-y`, `-h` |

In the “Used by” column, **plan + execute** means both modes parse and validate the value, while remote effects occur only under `--execute`. **Execute phase** means the plan retains or displays the input but the value is consumed during execution. **Flow control** directly selects whether execution happens.

## 3. Nodes, roles, and installation media

| Option and aliases | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `-l LIST`, `--arbitrator-nodes LIST`, `--controller-nodes LIST` | Required | Comma-separated control nodes, trimmed and deduplicated. At least three and an odd count are required. This is the Raft voter set; data-node count does not replace quorum. | `-l 192.0.2.11,192.0.2.12,192.0.2.13` | Plan + execute |
| `-n LIST`, `--data-nodes LIST` | Required for a database deployment that is not control-only | Database and Agent nodes, deduplicated, with no odd-count rule. VIP automatic failover requires at least two. | `-n 192.0.2.21,192.0.2.22` | Plan + execute |
| `--data-on-arbitrators` | Off | Appends every control node to the data-node set. Duplicate addresses are not repeated; overlapping nodes become mixed-role nodes. | `--data-on-arbitrators` | Plan + execute |
| `-g FILE`, `--application-rpm FILE` | Optional | ClusterGuard RPM. If omitted, exactly one `clusterguard-ha-*.rpm` must exist in kit `packages/`; zero or multiple matches fail. An adjacent `.sha256`, when present, must match. | `-g ./packages/clusterguard-ha-2.2-110.x86_64.rpm` | Plan + execute |
| `-r FILE`, `--database-package FILE`, `--mysql-package FILE`, `--postgresql-source-package FILE` | Required for database deployment unless auto-discovery resolves one | Explicit MySQL/UPSQL binary tar or PostgreSQL binary/official-source tar. Aliases do not force the engine. Media needs one safe top-level directory and the required binaries/source files. | `-r /secure/media/mysql-8.0.44-linux-x86_64.tar.xz` | Plan + execute |
| `--database-package-dir DIR` | Empty; may start from `CG_DATABASE_PACKAGE_DIR` | Used only without `-r` and restricts discovery to this directory. Otherwise the kit `packages/database/` is searched first; if it has any match, `/opt` is not searched. `/opt` is the fallback. Multiple matches fail closed. | `--database-package-dir /secure/database-media` | Plan + execute |
| `--engine mysql\|postgresql\|none` | `mysql` with data nodes; `none` without data nodes or in control-only mode | Selects the database flow. `none` disables database work and VIP reconciliation. MySQL/PG require data nodes. | `--engine postgresql` | Plan + execute |
| `--control-only`, `--skip-mysql` | Off | Installs only the control plane. It cannot be combined with data nodes or database media, and engine must be empty or `none`. `--skip-mysql` is a legacy name and also skips PostgreSQL. | `--control-only` | Plan + execute |

## 4. SSH, passwords, and host keys

Credential precedence follows the selected mode. A per-node list rejects a shared password, per-node file, and private key. A per-node file rejects a per-node list and private key. The source accepts a shared password together with a per-node file, in which case the file supplies the actual node values. It also accepts a shared password with a private key, in which case password transport mode is selected while the key is still passed to SSH. Do not depend on mixed inputs; production commands should select one explicit authentication method. Passwords are not written to deployment state or shown in the plan. Command-line secrets can appear in shell history and process listings; prefer a private key, a `0600` credential file, or the hidden prompt.

| Option and aliases | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `-u USER`, `--ssh-user USER` | `root` | Remote SSH user, validated as a regular Linux username. The current installer never invokes `sudo`. Real installation writes system paths, installs software, and operates systemd, so the remote session must have UID 0; use `-u root`. An ordinary non-root account is not supported by this entry point even with NOPASSWD sudo. | `-u root` | Plan + execute |
| `-P PASS`, `--ssh-password PASS` | Starts from `CG_SSH_PASSWORD`; hidden prompt if still empty at execution | One shared password for all nodes. A simultaneous per-node file supplies actual node values; a simultaneous key still leaves password transport selected. Never put a real password in documentation, task records, or public command lines. | `--ssh-password '<secure-input>'` | Execute phase |
| `-p LIST`, `--ssh-passwords LIST` | Empty | Maps passwords to the **deduplicated `all_nodes` order**: control nodes in order, then data nodes not already present. Count must match exactly and values cannot be empty. Mutually exclusive with `-P`, credential file, and key. Passwords containing commas cannot use this form. | `-p '<node1-pass>,<node2-pass>,<node3-pass>'` | Plan validates mapping; execute consumes it |
| `--ssh-credentials-file FILE` | Empty | Lines use `node=password`; blank and `#` lines are ignored. Mode must be exactly `0600`; every deduplicated node must be covered, with no duplicate keys. Values may contain `=`. Mutually exclusive with per-node list and private key. | `--ssh-credentials-file /secure/site/ssh-passwords.env` | Plan validates; execute consumes it |
| `--ssh-key FILE` | Empty | SSH private key. This is a private key for SSH and is unrelated to the patch-signing public key. Mutually exclusive with per-node password list/file. | `--ssh-key /secure/site/installer_ed25519` | Plan + execute |
| `--ssh-port NUMBER` | `22` | Installation SSH port, 1..65535. A database deployment currently rejects non-22 because the Agent inventory uses port 22; control-only may change it. | `--ssh-port 22` | Plan + execute |
| `--known-hosts FILE` | Empty | Uses a reviewed known_hosts file in place. It must exist. All SSH/SCP keeps strict host-key checking. | `--known-hosts /secure/site/known_hosts` | Execute phase |
| `--accept-host-keys` | Off | On first execution, collects keys into the work directory with `ssh-keyscan` and prints fingerprints. It explicitly accepts the keys observed then; it does not disable later strict checking. An existing work-directory file is reused. | `--accept-host-keys` | Execute phase |

Per-node credential file shape, with placeholders only:

```text
192.0.2.11=<password-for-11>
192.0.2.12=<password-for-12>
192.0.2.13=<password-for-13>
```

## 5. Data, software, state, and work paths

| Option and aliases | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `-ld DIR`, `--controller-data DIR` | `/var/lib/clusterguard` | Root for control metadata, Raft, and bootstrap admin password. It must be a safe absolute path other than `/`. It is not the database data root. | `-ld /data/clusterguard` | Plan + execute |
| `-nd DIR`, `--database-data DIR`, `--mysql-data DIR` | `/data` | Database data root. MySQL instance is `<DIR>/mysql/<port>`; PG data is `<DIR>/postgresql/<port>/data`. `--mysql-data` also affects PostgreSQL because it is only an alias. | `-nd /data` | Plan + execute |
| `--database-software-dir DIR`, `--mysql-software-dir DIR` | MySQL `/usr/local/mysql` | Complete MySQL software directory, independent of `-nd`. It must be a safe absolute path and cannot contain/overlap instance state. MySQL only. It is not the tar path. | `--database-software-dir /apps/mysql8` | Plan + execute |
| `--state-file FILE` | `$PWD/clusterguard-deployment-state.json` | Holds fixed cluster/node/instance UUIDs, phase, and identity binding. Recovery/rerun must reuse it. An existing file with a different cluster name, engine, port, or remote-root policy fails. Created as `0600`. | `--state-file /secure/site/deployment-state.json` | Plan displays; execute reads/writes |
| `--secrets-file FILE` | `$PWD/clusterguard-deployment-secrets.env` | Site secrets and managed database credentials. First execution creates it as `0600`; rerun requires all 13 keys. Losing it cannot safely “resume” an existing identity by inventing a new file. | `--secrets-file /secure/site/deployment-secrets.env` | Execute reads/writes |
| `--work-dir DIR` | `$PWD/clusterguard-site` | Local certificate source, SSH keys, node configs, temporary API files, and known_hosts; execution uses `umask 077`. Preserve and back it up with state/secrets for recovery. | `--work-dir /secure/site/work` | Execute reads/writes |
| `--remote-stage DIR` | `/var/lib/clusterguard/bootstrap` | Remote staging directory on every node; must be absolute and contain no newline. | `--remote-stage /var/lib/clusterguard/bootstrap` | Execute phase |

The state file owns identity and phase, the secrets file owns secrets, and the work directory owns certificates and rendered configuration. They are not interchangeable caches. Changing any of them on a rerun can create identity, certificate, or credential mismatch.

## 6. Common database options

| Option and aliases | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `--database-version VER`, `--postgresql-version VER` | Initially empty; PG is defaulted to `16` only after media selection | An explicit value participates in media filename matching and is passed to PG build/install; PG accepts dotted digits only. When omitted, auto-discovery does not filter on `16`; the PG value becomes `16` only after media has been selected. Multiple versions in a search directory require an explicit value. Production commands should always state the version. | `--database-version 16.4` | Plan + execute |
| `--database-port N`, `--mysql-port N`, `--postgresql-port N` | MySQL `3306`; PG `5432`; none `0` | Port 1..65535. All three spellings are equivalent and do not select an engine. Port participates in paths, unit names, identity, and MySQL `server_id`. | `--database-port 3306` | Plan + execute |
| `--dependencies DIR` | Auto-selects kit `dependencies/` when it contains RPMs | Local RPM repository for base runtime dependencies. PG source build borrows it only when `COLLECTION-INFO` says `postgresql_source_build=true`; normally keep PG build dependencies separate. | `--dependencies ./dependencies` | Plan + execute |
| `--controller-min-free-gb N` | `2` | Minimum free GiB on every control node filesystem containing `-ld`; positive integer. | `--controller-min-free-gb 4` | Plan + execution preflight |
| `--database-min-free-gb N` | `10` | Minimum free GiB on every data-node filesystem containing `-nd`; positive integer. | `--database-min-free-gb 100` | Plan + execution preflight |

Every database tar must be readable, contain no absolute or `..` traversal entries, and have one top-level directory. MySQL requires `mysqld`, `mysql`, and `mysqldump`. A PG binary requires `initdb`, `postgres`, `psql`, `pg_basebackup`, `pg_rewind`, and `pg_controldata`; otherwise the official source-tree shape is tested. An adjacent `.sha256`, when present, is checked. Absence of a sidecar does not prove vendor-signature verification.

## 7. MySQL options and configuration preservation

| Option | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `--mysql-master-cnf FILE` | Kit `templates/mysql/my-master.cnf`, with development-tree fallback to `configs/mysql/` | Production template for the first data node. Must be a regular non-symlink file. A custom template changes only the source template; renderer still changes paths, port, `server_id`, and read-only startup guards while preserving all other values, comments, order, and duplicates. | `--mysql-master-cnf /secure/config/my-master.cnf` | Plan + execute |
| `--mysql-slave-cnf FILE` | Kit `templates/mysql/my-slave.cnf`, with development-tree fallback to `configs/mysql/` | Template for the second and later data nodes; same rules as above. “slave” is a legacy option name. Runtime role remains controlled by topology and writer lease. | `--mysql-slave-cnf /secure/config/my-slave.cnf` | Plan + execute |
| `--mysql-root-password PASS` | Starts from `CG_NODE_MYSQL_ROOT_PASSWORD`; random on first execution if still empty | Local MySQL root password, 8..128 characters from letters, digits, and `@%+=:,._-`. Command-line use exposes history/process data; prefer protected environment injection or generated value. | `--mysql-root-password '<generated-secret>'` | Plan validates; execute writes secrets |
| `--mysql-root-remote-host HOST` | Starts from `CG_NODE_MYSQL_ROOT_REMOTE_HOST`; empty means do not create/change remote root | Explicitly creates `root@HOST`; MySQL only. `%` is widest scope and must follow site policy. The value is bound into state and must match on rerun. | `--mysql-root-remote-host '192.0.2.%'` | Plan + execute |

MySQL software defaults to `/usr/local/mysql`, while the instance defaults to `/data/mysql/<port>`; they are independent. Custom primary/replica templates change only the template source. The installer preserves other production parameters and renders the deployment paths, port, node identity, and read-only startup guards. Every node starts with `read_only=ON` and `super_read_only=ON`; only after controlled replication setup and role authorization does the primary switch them to `OFF`, while replicas remain read-only. Duplicate generated node identities fail before execution.

## 8. PostgreSQL options and separate build dependencies

| Option | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `--postgresql-password PASS` | Starts from `CG_POSTGRESQL_ADMIN_PASSWORD`; random on first execution if empty | `postgres` admin password; same character policy as MySQL root. | `--postgresql-password '<generated-secret>'` | Plan validates; execute consumes it |
| `--postgresql-allowed-cidr CIDR` | Inferred for one IPv4 `/24`; required for DNS or multiple subnets | PG management and replication network; IPv4 CIDR only. | `--postgresql-allowed-cidr 192.0.2.0/24` | Plan + execute |
| `--postgresql-build-node HOST` | First data node for an official source package | Compiles once on this data node, then distributes one SHA256-verified binary. Must belong to data nodes. Invalid for PG binary media and other engines. | `--postgresql-build-node 192.0.2.21` | Plan + execute |
| `--postgresql-build-jobs N` | Starts from `CG_POSTGRESQL_BUILD_JOBS`; empty lets helper detect, capped at 16 | Explicit values 1..128; official PG source media only. Explicit and automatic caps intentionally differ. | `--postgresql-build-jobs 8` | Plan + execute |
| `--postgresql-dependencies DIR` | Empty; without it, source build uses the build node's online repository | **Separate PG source-build dependency repository**. At execution it must contain `repodata/repomd.xml`. It is distinct from base runtime `--dependencies`. | `--postgresql-dependencies /secure/pg-build-dependencies` | Plan displays; execute archives/transfers |

The source build node also needs at least 6 GiB of temporary space. `--postgresql-dependencies` does not turn a binary package into a source build. `--postgresql-build-node/jobs` fail for non-source media.

## 9. Control plane, network, timezone, and HA safety

| Option | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `--cluster-name NAME` | `clusterguard-control-plane` or `<engine>-ha-<port>` | 3..63 allowed characters, beginning with alphanumeric. It is bound into state and used for execution confirmation. | `--cluster-name mysql-ha-3306` | Plan + execute |
| `--vip ADDRESS` | Empty | Optional IPv4 writer endpoint. MySQL/PG + VIP without manual-only enables both database automatic failover and Agent majority fencing; at least two data nodes. A VIP alone is not safety authorization. | `--vip 192.0.2.100` | Plan + execute |
| `--interface NAME` | `ens160` | VIP interface; letters, digits, and `_.:-` are accepted. | `--interface ens160` | Plan + execute |
| `--prefix N` | `24` | VIP CIDR prefix, 1..32. It is validated even when no VIP is configured but binds no address then. | `--prefix 24` | Plan + execute |
| `--no-vip-reconcile` | Absent, so post-install convergence is enabled | Prevents enabling the Agent reconcile timer and waiting for the initial VIP lease at the end of installation. It **does not disable database automatic-failover configuration**. Use `--manual-failover-only` to prohibit automatic switching. | `--no-vip-reconcile` | Execute phase |
| `--manual-failover-only` | Off | For MySQL/PG with VIP, explicitly disables automatic failover and retains controlled manual switching. It cannot be combined with fencer or fencer assets. Without VIP, it changes no other default. | `--manual-failover-only` | Plan + execute |
| `--fencer FILE` | Empty | Optional external fencing enhancement. Must be a local executable regular non-symlink file; allowed only for MySQL/PG with VIP and installed at `/usr/local/libexec/clusterguard-fencer`. It supplements, and never replaces, Raft quorum, Agent quorum fencing, writer lease, or maintenance gates. | `--fencer /secure/fencing/site-fencer` | Plan + execute |
| `--fencer-assets DIR` | Empty; requires `--fencer` | Site configuration directory. It cannot be a symlink or contain symlinks/subdirectories; top-level regular filenames are restricted. Installed under `/etc/clusterguard/fencing/`. Do not place unprotected private-key examples there. | `--fencer-assets /secure/fencing/site-config` | Plan + execute |
| `--api-port N` | `3000` | Console/API port, 1..65535, used for control access and Leader targeting. | `--api-port 3000` | Plan + execute |
| `--raft-port N` | `10009` | Control-node Raft port, 1..65535. Changing it does not alter the odd voter count or majority requirement. | `--raft-port 10009` | Plan + execute |
| `--timezone ZONE` | If empty, read operator-host `timedatectl`, then try its `/etc/localtime` zone | When valid zoneinfo is found, pins one display timezone on all nodes. An invalid explicit value or unresolved default only warns and sends no timezone, leaving each node's display zone unchanged. It does not replace NTP/skew checks. | `--timezone Asia/Shanghai` | Plan + execute |
| `--patch-trust-key FILE` | Starts from `CG_PATCH_TRUST_KEY`, then kit public-key discovery; read-only update UI if still empty | Must be an OpenSSL-readable regular **public key**, never a signing private key. Execution deploys trust and enables the controlled update Helper. It does not bypass signature, plan, maintenance lock, or rolling gates. | `--patch-trust-key ./trust/patch-signing-public.pem` | Plan + execute |

The fixed safety relationship is: Raft majority confirms control authority; Agent quorum fencing makes a former node without majority authorization revoke VIP locally and stay read-only; writer lease controls write role; an optional external fencer adds site-specific isolation proof. `--no-vip-reconcile` controls activation of periodic VIP convergence only and is not a substitute for disabling automatic failover.

## 10. Execution mode and help

| Option and aliases | Default / requirement | Meaning, validation, effect, and limit | Safe example | Used by |
| --- | --- | --- | --- | --- |
| `--plan` | Default | Sets `execute=false`. If combined with `--execute`, the last occurrence wins. It still reads local tar listings, sidecars, cnf files, and credential mapping. | `./install_clusterguard.sh ... --plan` | Flow control |
| `--execute` | Explicitly required for real installation | After local validation and plan output, checks execution tools, confirms, connects, and changes remote nodes. | Review the same arguments with `--plan`; an authorized operator appends it separately | Flow control |
| `-y`, `--yes` | Off | Skips typing the cluster name; required for noninteractive `--execute`. It skips no safety, media, or topology validation. | `--execute --yes` (authorized automation only) | Flow control |
| `-h`, `--help` | Optional | Prints built-in help and exits 0. Built-in help omits some accepted compatibility aliases; use this page and the parser for the complete set. | `./install_clusterguard.sh --help` | No installation |

## 11. Public environment variables

A corresponding CLI option normally overrides the environment value read during initialization. Never expose secrets in public logs. Wait/retry variables below are accepted operational tuning inputs; they do not relax health criteria.

| Environment variable | Default | Effect and limit | CLI / phase |
| --- | --- | --- | --- |
| `CG_DATABASE_PACKAGE_DIR` | Empty | Sole directory for database auto-discovery. | Overridden by `--database-package-dir` |
| `CG_POSTGRESQL_BUILD_JOBS` | Empty | PG source parallelism; explicit value 1..128. | Overridden by `--postgresql-build-jobs` |
| `CG_SSH_PASSWORD` | Empty | Shared SSH password; needed at execution only when no other authentication exists. | Overridden by `-P` |
| `CG_NODE_MYSQL_ROOT_PASSWORD` | Empty | Initial MySQL root value when creating secrets; reruns use the secrets file. | Initial value overridden by `--mysql-root-password` |
| `CG_NODE_MYSQL_ROOT_REMOTE_HOST` | Empty | Explicit remote-root Host policy. | Overridden by `--mysql-root-remote-host` |
| `CG_POSTGRESQL_ADMIN_PASSWORD` | Empty | Initial PG admin value when creating secrets; reruns use secrets. | Initial value overridden by `--postgresql-password` |
| `CG_POSTGRESQL_ALLOWED_CIDR` | Empty | PG management/replication IPv4 CIDR. | Overridden by `--postgresql-allowed-cidr` |
| `CG_PATCH_TRUST_KEY` | Empty | Patch-signing public-key path. | Overridden by `--patch-trust-key`, followed by kit discovery |
| `CG_JQ_BINARY` | `jq` in PATH, then kit static jq | Selects an executable jq; invalid path fails. | No CLI |
| `CG_INSTALL_API_RETRY_ATTEMPTS` | `4` | API Leader-target/GET retry attempts, 1..10. A non-GET transport failure is not blindly retried. | Execute phase |
| `CG_INSTALL_VIP_LEASE_TIMEOUT_SECONDS` | `300` | Initial VIP lease wait, 30..600 seconds. | Execution acceptance |
| `CG_INSTALL_VERIFY_TIMEOUT_SECONDS` | `180` | Total stable-topology health wait, 30..900 seconds. | Execution acceptance |
| `CG_INSTALL_VERIFY_POLL_SECONDS` | `2` | Topology polling interval, 1..30 seconds. | Execution acceptance |
| `CG_INSTALL_VERIFY_STABLE_OBSERVATIONS` | `3` | Consecutive healthy observations, 2..10. | Execution acceptance |
