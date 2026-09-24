# ClusterGuard HA Offline Installation and Deployment Manual

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/offline-rpm-install.md)
<!-- /LANGUAGE-SWITCH -->

Applicable releases: ClusterGuard HA 2.1-45 (final MySQL-only release) and 2.2.x x86_64
Supported systems: RHEL, Rocky Linux, AlmaLinux, Oracle Linux 8/9, systemd, x86_64
Deployment Method: One operations machine installs multiple control nodes and database nodes remotely via SSH

Version boundary: `2.1-45` is the final release of the MySQL-only 2.1 line; PostgreSQL installation and switching start with
`2.2-1`. Do not use 2.1 packages to execute the PostgreSQL section of this manual. The final 2.1 release is documented in the
[2.1-45 Release Notes](release-2.1.45.md), and the immutable release rules are documented in the
[Version and Release Policy](version-release-policy.md).

This document only describes the current official multi-node installer `install_clusterguard.sh`. It is the recommended production installation method: uniformly generating node identities, certificates, Raft configurations, database configurations, replication topologies, Agent, and VIP convergence configurations.

Do not mix this manual with old versions of Orchestrator, single-machine RPM manual configurations.

`clusterguard-configure` is the node-level configuration program called by the installer on remote nodes. During the initial deployment, it should not be manually invoked by the operations personnel one by one; it is only used when the platform node lifecycle, controlled recovery process, or engineering support explicitly requires it.

## 1. Deliverables and Installation Methods

The official delivery directory is the build output directory `release/<bundle-version>/`
(the listing below uses the 2.1-45 media version):

```text
release/2.1-45/
  RELEASE-INFO
  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
  clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
  clusterguard-ha-2.1-45.x86_64.rpm
  clusterguard-ha-2.1-45.x86_64.rpm.sha256
  SHA256SUMS
  docs/                                   # bundled Chinese manuals
```

Unpacking the `*.tar.gz` yields the **media directory**, which shares the archive name but has
different contents: the RPM and node runtime appear again under `packages/`.

```text
clusterguard-ha-2.1-45-offline-linux-x86_64/
  install_clusterguard.sh                 # production installation entry point
  packages/                               # ClusterGuard RPM and node runtime tar.gz
  packages/database/                      # MySQL / PostgreSQL media and README.txt
  dependencies/                           # offline RPM repository (with repodata/)
  docs/                                   # bundled manuals
  tools/                                  # jq, clock mesh, PostgreSQL source build tools
  examples/                               # fencing and docker-swarm examples
  RELEASE-INFO
  SHA256SUMS
```

Production deployment should use the complete offline package, not manually install RPM individually:

```bash
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.1-45-offline-linux-x86_64
sha256sum -c SHA256SUMS
```

Before installation, you can optionally check the RPM signature and content, but do not replace the official RPM with files of the same name after unpacking:

```bash
rpm -K packages/clusterguard-ha-2.1-45.x86_64.rpm
rpm -qpl packages/clusterguard-ha-2.1-45.x86_64.rpm
```

The offline package includes ClusterGuard HA, the installer, static `jq`, basic runtime dependencies required for MySQL and the control plane, sample configurations, and Chinese manuals. The larger PostgreSQL source code compilation dependency closure is delivered separately; the main offline package does not include MySQL, UPSQL, PostgreSQL, Oracle, or SQL Server vendor patches and licenses.

## 2. Roles and Deployment Topology

ClusterGuard HA has three node roles:

| Role | Function | Quantity Rules |
| --- | --- | --- |
| Control Node | Runs API, Web Console, Raft, audit, and workflow | Must be an odd number of at least 3 |
| Data Node | Runs MySQL or PostgreSQL and restricted Agent | No requirement for odd numbers |
| Hybrid Node | Simultaneously a control node and a data node | The control node set must still be an odd number |

Common deployment modes:

| Mode | `-l` Control Nodes | `-n` Data Nodes | Use Cases |
| --- | --- | --- | --- |
| Three-node hybrid | Three identical hosts | Three identical hosts | Small production and testing environments |
| Control and database separation | Three arbitration hosts | Two or more database hosts | Recommended production isolation mode |
| Only control plane | Three arbitration hosts | Not transmitted | Deploy the control plane first, then connect to the database from the console later |

The correspondence of the three hosts in the example:

| IP | Hostname | Fixed Node Name |
| --- | --- | --- |
| `192.168.102.152` | `orch-mysql01` | `cg-node-0001` |
| `192.168.102.153` | `orch-mysql02` | `cg-node-0002` |
| `192.168.102.154` | `orch-mysql03` | `cg-node-0003` |

The installer will record an immutable UUID. When modifying the hostname, IP, or port later, it should be handled through the metadata correction process in the Console, not by reinstalling after deleting the local deployment status.

## 3. Pre-Deployment Checks

### 3.1 Operations Machine Requirements

The operations machine should be able to SSH into all target nodes and have:

```text
bash, ssh, scp, ssh-keyscan, ssh-keygen, openssl, curl, tar
```

The operations machine needs to save deployment status, site secrets, and certificate working directories. These are essential materials for subsequent idempotent execution, expansion, node recovery, and administrator takeover, and must be placed in a protected and backed-up location.

### 3.2 Target Node Requirements

- Use the same Linux distribution main version and x86_64 architecture.
- Each node uses a unique and stable hostname.
- Control nodes must reserve at least 2 GiB of available space; the database data directory must reserve at least 10 GiB, and production environments should increase based on capacity planning.
- Clock synchronization is required between all nodes. The installer will check the NTP status; in production environments, unsynchronized states must be fixed first.
- Root should be able to log in via SSH, or a dedicated operations account with sudo privileges should be provided.
- Network and security policies must allow the following connections.

| Port | Direction | Purpose |
| --- | --- | --- |
| 22/TCP | Operations machine to all nodes | Installation, synchronization, and recovery |
| 3000/TCP | Between administrator and control nodes | Console, API, Leader forwarding |
| 10009/TCP | Control nodes communicate with each other | Raft consensus |
| 3306/TCP | MySQL data nodes communicate with each other and with the control plane | MySQL replication and management |
| 5432/TCP | PostgreSQL data nodes communicate with each other and with the control plane | PostgreSQL replication and management |

The installer will automatically configure the ports it manages within the enabled and running firewalld. External firewalls, security groups, ACLs, and switch networks still need to be opened by the operations team in advance. The installer will call the restricted offline `dnf install` or `yum localinstall` to install verified local RPMs, and operations personnel do not need to manually execute RPM installation on each node.

### 3.3 Hostname and VIP

First, confirm the hostname is correct on the target node, for example:

```bash
hostnamectl set-hostname orch-mysql01
hostnamectl --static
```

The VIP must be an address not used by other devices in the current Layer 2 network. Do not manually configure the VIP on any network interface in advance; after installation, ClusterGuard Agent will bind it uniquely based on the primary database role, Raft majority, and lease.

The default MySQL HA deployment with VIP enables automatic database-level failover, not relying on VMware virtual machine names or VMX paths. The control plane probes the MySQL port and SQL health every second; after four current failed-primary observations span at least three seconds, the Raft Leader may evaluate a candidate-primary transition. The old-primary Agent retains only a short majority authorization. When that authorization expires it revokes the VIP, records a persistent isolation intent, and keeps MySQL read-only. The control plane preserves a separate 15-second authorization-expiry fence and rechecks Raft majority, failure evidence, and the exact transition lease before allowing promotion. A blocked attempt uses a separate 30-second retry backoff.

These intervals serve different purposes and do not add up to a guaranteed application RTO. Production clients must use bounded connection timeouts and retries, and the site acceptance test must measure from the writer endpoint.

Therefore, the installation command by default does not require `--fencer`. This is not directly treating "3306 not reachable" as the old primary having powered off, but using short-term majority authorization and node-local fail-closed mechanisms to prevent nodes that have lost the majority from continuing to hold write entry points. If the operating system, Agent timer, and management network may also fail simultaneously, production environments are still recommended to add BMC, PDU, cloud API, or virtualization platform as a second layer of external isolation enhancement. Explicitly adding `--manual-failover-only` can disable automatic recovery, retaining only manual controlled switching.

#### Optional: VMware Workstation SSH External Isolation Enhancement

The offline medium's `examples/fencing/` provides an optional VMware Workstation SSH isolator. It does not participate in the identity of database nodes, master-slave topology, or default switching determination, but acts as a stronger proof of old primary power-off when the site explicitly passes in `--fencer`. It logs into the Windows VMware host through an independent management network, verifies the fixed IP to VMX whitelist using `vmrun list`; only `fence` actions will execute `vmrun stop ... hard`, and `status` actions are always read-only.

The following conditions must be met first:

1. The VMware host must enable OpenSSH Server and be reachable from the management network of the three control nodes.
2. Use SSH key login. Prohibit writing Windows passwords into isolation scripts, environment variables, or configuration files.
3. The SSH account must be able to see and manage the target VMware Workstation virtual machine; it is usually the same Windows account that starts these virtual machines, or a dedicated VMware management account configured by the enterprise.
4. `vmware_known_hosts` must be pre-fixed with the host SSH public key; prohibit `StrictHostKeyChecking=no`.
5. Each database instance IP must map exactly one absolute `.vmx` path in `vmware-targets.tsv`.

Prepare the file in the secure directory:

```bash
install -d -m 0700 /secure/clusterguard/fencing/site-config
cp examples/fencing/vmware-workstation.conf.example \
  /secure/clusterguard/fencing/site-config/vmware-workstation.conf
cp examples/fencing/vmware-targets.tsv.example \
  /secure/clusterguard/fencing/site-config/vmware-targets.tsv
install -m 0600 /secure/source/vmware_ed25519 \
  /secure/clusterguard/fencing/site-config/vmware_ed25519
ssh-keyscan -H 192.168.102.68 > \
  /secure/clusterguard/fencing/site-config/vmware_known_hosts
chmod 0600 /secure/clusterguard/fencing/site-config/*
```

Edit `vmware-workstation.conf`, where the runtime path must remain:

```text
identity_file=/etc/clusterguard/fencing/vmware_ed25519
known_hosts_file=/etc/clusterguard/fencing/vmware_known_hosts
targets_file=/etc/clusterguard/fencing/vmware-targets.tsv
```

Then add the following to the original installation command:

```bash
--fencer ./examples/fencing/vmware-workstation-ssh-fencer \
--fencer-assets /secure/clusterguard/fencing/site-config
```

The installer will install the isolator to `/usr/local/libexec/clusterguard-fencer`, and install the site files as `0640 root:clusterguard` to `/etc/clusterguard/fencing/`. Before going live, each mapping must be verified with `status`, and then verified once in an isolation test environment with a real shutdown; it cannot be accepted solely based on SSH connectivity.

## 4. Prepare Database Media and Offline Dependencies

The official offline package can embed approved database media directly through repeatable `--database-package` during construction. Without `-r`, the installer first searches the current offline kit's `packages/database/`; it searches `/opt` only when the kit has no matching package. Use `--database-package-dir /secure/database-media` to search only the specified enterprise media directory.

The installer requires the media to be readable `tar`, `tar.gz`, `tgz`, `tar.xz`, `tar.bz2`, or `tbz2`, and the compressed package must have only one secure top-level directory. It uses `--engine` and `--database-version` to match filenames; for example, MySQL 8.0.44 will match packages with filenames that simultaneously include `mysql` or `upsql` and `8.0.44`. Multiple candidates in the selected directory still require an explicit `-r`. Older published installers also require `-r` when both the kit and `/opt` contain a match.

MySQL 8.0 example:

```bash
sha256sum /opt/mysql-8.0.44-linux-glibc2.17-x86_64.tar.xz
```

MySQL 8.4 only needs to replace the filename, `--database-version`, and cluster name. UPSQL uses its enterprise-approved compatible MySQL tar package and deploys it under `--engine mysql`.

The installer will read the physical memory on each MySQL data node and automatically generate the parameters for that node. `innodb_buffer_pool_size` defaults to 70% of the physical memory and rounds up to the nearest integer multiple of 4 GiB; if the rounded value exceeds 80% of the physical memory, it will take the maximum 4 GiB multiple that does not exceed 80%, without setting a fixed capacity upper limit. For example, 256 GiB of memory will be configured as 180 GiB. `max_connections` defaults to 1000. Caching, temporary tables, and redo still follow memory tiers. Single connection buffering remains controlled, avoiding uncontrollable additional memory amplification when 1000 connections are concurrent.

MySQL data nodes require at least 5 GiB of physical memory; if the capacity is below this, the installer will block before initializing the data directory as it cannot simultaneously satisfy 4 GiB alignment and 80% upper limit.

MySQL by default listens on all IPv4 addresses of the database port, and the ClusterGuard management account can directly connect via TCP. To be compatible with traditional drivers, MySQL 8.0 uses `default_authentication_plugin=mysql_native_password`; MySQL 8.4 uses its supported `mysql_native_password=ON` and explicitly sets the platform account plugin. By default, no remote `root` account will be created or modified, and remote control uses independent discovery, execution, and replication accounts.

If remote root is indeed required on-site, the parameter must be explicitly added. `%` indicates allowing any source, and production environments are more recommended to restrict to the management network segment:

```bash
# Explicitly allow root login from any source
--mysql-root-remote-host '%'

# More restrictive management subnet example
--mysql-root-remote-host '192.168.102.%'
```

The installation plan will explicitly show whether remote MySQL root access is disabled by default or explicitly enabled for `root@...`. This policy is written to the deployment state and control-node lifecycle configuration, and remains consistent when MySQL nodes are later added or rebuilt from the Console. Omitting the parameter does not delete an existing remote root account or reset its password or privileges.

If the MySQL binary depends on libraries not provided by the target operating system, the same distribution, main version, architecture, and signed RPMs must be placed in `dependencies/`. You can use the offline package's tools on a connected same version mirror machine to collect:

```bash
tools/收集RHEL离线依赖.sh --help
```

Do not use `--nogpgcheck` against a networked repository or against RPMs that have not been verified individually. You may disable `gpgcheck` for a repository that points at the **local media directory** only after verifying every RPM in `dependencies/` with `rpm --checksig` and checking `SHA256SUMS` — which is exactly what the product's own installer and PostgreSQL source build do (verify first, then disable verification inside the isolated build root). The `--dependencies` directory is only used for the basic runtime dependencies of ClusterGuard and MySQL, not for the PostgreSQL source code compilation toolchain, and it must contain at least the `libaio`, `ncurses-compat-libs`, and `numactl-libs` RPMs; the actual closure size depends on how it was collected — consult `PACKAGE-MANIFEST.txt` / `COLLECTION-INFO` inside the media.

### Official PostgreSQL Source Code Installation

PostgreSQL can directly use the official release source code package, and does not require users to pre-make binary packages. For example:

```text
/opt/postgresql-16.4.tar.bz2
```

The source code mode is not for three servers to compile separately. ClusterGuard will execute the following controlled process:

1. Before connecting to the remote, verify the safety of the compressed package path, single top-level directory, and official source code structure.
2. By default, the first data node is selected as the build node, or another one can be specified using `--postgresql-build-node`.
3. Compile only once on the build node, with the installation prefix fixed as `/opt/clusterguard/postgresql/<PORT>/software`.
4. Build `contrib`, verify `initdb`, `postgres`, `psql`, `pg_basebackup`, `pg_rewind`, `pg_controldata`, and `pg_config`.
5. Generate `BUILD-MANIFEST.json`, unified binary tar package, and SHA256 digest.
6. Distribute the identical products to all control nodes and data nodes, then initialize and synchronize stream replication.

The source code products will dynamically link to the target system libraries, so all control/data nodes must use the same CPU architecture, distribution version, and glibc. The installer will check before compiling, and will block directly if they are inconsistent.

The build node needs at least 6 GiB of temporary space. When rerunning after an interruption, only if the source code digest, builder digest, and product SHA256 are all consistent will the cache be reused; any inconsistency will recompile, avoiding mistakenly sending old products to new clusters.

The main offline package no longer includes the large PostgreSQL source code compilation dependency closure. When explicitly specifying
`--engine postgresql` and using the official source code package, the installer defaults to using
the software source already configured on the selected build node to resolve dependencies online. All compilation dependencies are only installed to a one-time DNF installroot,
and will not be written to the production host's software package database; repository signature verification remains enabled.

The installation plan states that PostgreSQL build dependencies are resolved online into an isolated build root by default. If the on-site repository, DNS,
or network is unavailable, installation stops and explicitly requests an independent PostgreSQL dependency package. Create this dependency add-on package on a connected mirror machine with the same distribution, major version, and architecture as the production node:

```bash
tools/收集RHEL离线依赖.sh \
  --output /tmp/clusterguard-pg-source-deps \
  --postgresql-source-build

tools/构建PostgreSQL依赖包.sh \
  --input /tmp/clusterguard-pg-source-deps \
  --output /secure/release \
  --version 2.2 \
  --release 1 \
  --platform rocky-8 \
  --arch x86_64
```

The generated filename is similar to
`clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64.tar.gz`.
Upload and verify the adjacent `.sha256` file, then extract and add the following to the installation command:

```bash
--postgresql-dependencies ./clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64/dependencies
```

The installer verifies the dependency add-on package's SHA256, RPM signature, CPU architecture, operating-system major version, and repository metadata,
and then only enable this local repository to complete the isolated build. If the signature, digest, repository metadata, or dependencies are missing, it will explicitly
report an error and stop.

Example of a three-node PostgreSQL 16.4 plan:

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -P 'SSH_PASSWORD' \
  -r /opt/postgresql-16.4.tar.bz2 \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine postgresql \
  --database-version 16.4 \
  --cluster-name pg16-production \
  --postgresql-allowed-cidr 192.168.102.0/24 \
  --accept-host-keys \
  --plan
```

After confirming that the plan displays "PostgreSQL official source code", "compile only once", the correct build node, and "default online installation to isolated build root", change the last line to `--execute` to execute. For a purely offline site, append the above `--postgresql-dependencies` parameter.

## 5. SSH Host Key and Password Handling

It is recommended to review the target host SSH fingerprint in advance and prepare the `known_hosts` file:

```bash
ssh-keyscan -H 192.168.102.152 192.168.102.153 192.168.102.154 > site/known_hosts
# Verify every host fingerprint through an independent trusted channel before using this file.
chmod 0600 site/known_hosts
```

For the first experimental environment, you can use `--accept-host-keys` to let the installer collect the current host key. Production environments prioritize using `--known-hosts site/known_hosts`.

During real installation, if `--ssh-key`, `-P`, or `CG_SSH_PASSWORD` are not passed, the installer will only prompt for a hidden password once at the beginning, and then automatically reuse it for all SSH and SCP operations. The simplest interaction is to directly execute the installation command without additional password handling.

Do not write the SSH password into the command line, Shell history, or change orders. When no interactive execution is needed, you can input and export the environment variable in the current Shell:

```bash
read -r -s -p 'SSH password: ' CG_SSH_PASSWORD
printf '\n'
export CG_SSH_PASSWORD
```

You can also use `--ssh-key /secure/keys/clusterguard-deploy_ed25519`. `-P` / `--ssh-password` are retained only for compatibility with existing automation and are not recommended in shared terminals, shell history, or environments where process arguments are visible.

### Different Passwords on Multiple Servers

You can directly pass in passwords in order of nodes. For example, after deduplication, the order of `-l/-n` is `152,153,154`, then:

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -p 'PASSWORD_FOR_152,PASSWORD_FOR_153,PASSWORD_FOR_154'
```

Lowercase `-p` indicates "node password list"; uppercase `-P` indicates "all nodes use the same password". The number of passwords must be exactly the same as the number of deduplicated nodes, and the installer will display the node order in the plan but will not display the password. If the password contains English commas, please use the credential file method below.

Create a credential file readable only by root for each node, for example `/root/clusterguard-node-ssh.credentials`:

```text
# NODE_ADDRESS=SSH_PASSWORD_FOR_THAT_NODE
192.168.102.152=first-node-password
192.168.102.153=second-node-password
192.168.102.154=third-node-password
```

```bash
chmod 600 /root/clusterguard-node-ssh.credentials
```

Then add the following to the installation command:

```bash
--ssh-credentials-file /root/clusterguard-node-ssh.credentials
```

This file must include each server involved in this `-l` and `-n`; the installer will check the integrity and `0600` permissions before starting. The password will not be displayed in the deployment plan, logs, status files, or command line. The node address cannot contain spaces; the password can contain `=`, but cannot have line breaks.

## 6. Three-node MySQL Hybrid Deployment

This is the most commonly used complete installation process. Three machines run both the ClusterGuard control plane and MySQL with Agent.

First, create a persistent site directory:

```bash
install -d -m 0700 /secure/clusterguard/mysql-ha-3306
```

### 6.1 Read-only Plan

The following command will not modify remote hosts:

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --database-port 3306 \
  --cluster-name mysql-ha-3306 \
  --vip 192.168.102.155 \
  --interface ens160 \
  --prefix 24 \
  --dependencies dependencies \
  --known-hosts /secure/clusterguard/mysql-ha-3306/known_hosts \
  --state-file /secure/clusterguard/mysql-ha-3306/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-ha-3306/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-ha-3306/site \
  --plan
```

Verify the control nodes, data nodes, database version, port, network card, VIP, and data directory in the plan. If there are any errors, modify the parameters and rerun the read-only plan.

### 6.2 Real Installation

Change the last `--plan` of the same command to `--execute`. During interactive execution, the installer will prompt for the cluster name to confirm:

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --database-port 3306 \
  --cluster-name mysql-ha-3306 \
  --vip 192.168.102.155 \
  --interface ens160 \
  --prefix 24 \
  --dependencies dependencies \
  --known-hosts /secure/clusterguard/mysql-ha-3306/known_hosts \
  --state-file /secure/clusterguard/mysql-ha-3306/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-ha-3306/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-ha-3306/site \
  --execute
```

For non-interactive automation, you can additionally add `-y`, but it should only be used in reviewed change jobs.

After installation is complete, the console address is:

```text
https://192.168.102.152:3000/
```

The initial account is `admin`. For a new installation, the control plane generates the initial password in `/var/lib/clusterguard/bootstrap-admin-password` (mode 0600, beside `metadata.json`). After an interactive installation, the installer reads it securely from the current Raft Leader and displays it in the terminal. Non-interactive standard output does not contain the plaintext; read the file as root on the control node where it was generated. Terminal recording may capture the displayed password, so protect the installation session and change the password immediately after the first login. If the file is absent, the administrator may already exist or may have changed the password. Do not use the former fixed default or delete cluster state to reinitialize it; use the administrator recovery procedure. Until the first password change, the console and API only allow authentication and password change.

### 6.3 MySQL 8.4 Deployment

Replace the two items in the above command with actual values. If `/opt` has multiple MySQL 8.4 packages, explicitly add `-r /opt/EXACT_FILENAME`:

```text
--database-version 8.4.10
--cluster-name mysql-ha-3306-84
```

Only one ClusterGuard cluster can belong to one IP:port. If 8.0 and 8.4 coexist, they must use different ports, independent data directories, independent VIP, independent `--state-file`, `--secrets-file`, and `--work-dir`.

## 7. Separated Deployment of Control Nodes and Data Nodes

The following example uses three arbitration control nodes and three independent MySQL data nodes:

```bash
./install_clusterguard.sh \
  -l 192.168.40.81,192.168.40.82,192.168.40.83 \
  -n 192.168.40.91,192.168.40.92,192.168.40.93 \
  -u root \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.4.10 \
  --database-port 3306 \
  --cluster-name mysql-production \
  --vip 192.168.40.100 \
  --interface ens160 \
  --prefix 24 \
  --known-hosts /secure/clusterguard/mysql-production/known_hosts \
  --state-file /secure/clusterguard/mysql-production/deployment-state.json \
  --secrets-file /secure/clusterguard/mysql-production/deployment-secrets.env \
  --work-dir /secure/clusterguard/mysql-production/site \
  --plan
```

First, execute `--plan`, confirm it is correct, and then change it to `--execute`. The number of control nodes must remain odd; the number of data nodes can be 1, 2, 3, or more.

## 8. Deploying Only Three-node Control Plane

When not deploying databases and Agents:

```bash
./install_clusterguard.sh \
  -l 192.168.40.81,192.168.40.82,192.168.40.83 \
  -u root \
  -ld /var/lib/clusterguard \
  --engine none \
  --control-only \
  --known-hosts /secure/clusterguard/control-plane/known_hosts \
  --state-file /secure/clusterguard/control-plane/deployment-state.json \
  --secrets-file /secure/clusterguard/control-plane/deployment-secrets.env \
  --work-dir /secure/clusterguard/control-plane/site \
  --plan
```

After confirmation, append `--execute`. Subsequently, connect to MySQL, PostgreSQL, Oracle Data Guard, or SQL Server Always On through the Console's "Cluster Management" and "Nodes" pages, and do not recreate the control plane status file.

## 9. Post-Installation Acceptance

### 9.1 Services and Raft

Execute on each control node:

```bash
systemctl is-enabled clusterguard-ha.service
systemctl is-active clusterguard-ha.service
systemctl status clusterguard-ha.service --no-pager
```

Execute on each data node:

```bash
systemctl is-enabled clusterguard-agent.service
systemctl is-active clusterguard-agent.service
systemctl status clusterguard-agent.service --no-pager
```

MySQL hybrid nodes should also verify:

```bash
systemctl is-enabled clusterguard-mysql-3306.service
systemctl is-active clusterguard-mysql-3306.service
ss -lnt | grep ':3306'
mysql -uroot -p -e 'SELECT @@hostname, @@port, @@read_only, @@super_read_only;'
readlink /tmp/mysql.sock
```

The ClusterGuard management service socket is located at `/run/clusterguard/mysql/3306/mysql.sock`, not in the data directory. The installer will create a password-free `/etc/clusterguard/mysql/default-client.cnf`, and include this file at the end of `/etc/my.cnf` and `/root/.my.cnf`, so existing configuration's old socket will not continue to overwrite the current instance. The standard 3306 port will also maintain `/tmp/mysql.sock` compatibility links through `/etc/tmpfiles.d/clusterguard-mysql-3306.conf`, and will automatically recover after reboot. The managed root password is still only saved in `/etc/clusterguard/mysql/3306-client.cnf` and site secret files with permissions of `0600`.

In the console, you should see: three control nodes, one Raft Leader, two Followers, one primary database, the rest of the replicas, and the unique VIP Owner.

### 9.2 VIP Uniqueness

Execute on the three data nodes separately:

```bash
ip -o -4 addr show dev ens160 | grep '192.168.102.155/24' || true
```

It must return only once on the current primary database node. Do not manually `ip addr add` bind the VIP, and do not manage the same VIP simultaneously through keepalived, Pacemaker, or other tools.

### 9.3 Restart Recovery

After planned maintenance or kernel upgrades, restart one Follower, then another Follower, and finally the Leader. After each recovery, confirm that the console has quorum, database replication is normal, and the VIP is still only on the primary database. Do not perform primary database switching after losing the majority of control nodes.

## 10. Must-be-kept Site Data

The following files contain cluster identity, certificates, control tokens, and database managed passwords, and must be 0600 permissions and included in encrypted backups:

```text
/secure/clusterguard/<CLUSTER_NAME>/deployment-state.json
/secure/clusterguard/<CLUSTER_NAME>/deployment-secrets.env
/secure/clusterguard/<CLUSTER_NAME>/site/
/secure/clusterguard/<CLUSTER_NAME>/known_hosts
```

When re-executing the same cluster installer, performing controlled repairs, or expanding, the same set of files must be reused. Do not delete, copy to other clusters, or overwrite existing running clusters with empty status files, otherwise it will destroy the consistency of resource UUIDs, certificates, and managed credentials.

## 11. Post-Installation Operation Boundaries

After installation is complete, the following operations are performed through the Console:

- Controlled primary database and VIP synchronization switching.
- Recovery of old primary and reattachment as a slave.
- Adding data nodes, automatically installing the database, synchronizing data, verifying replication, and submitting metadata.
- Adding control nodes, the total number of control nodes must remain odd.
- Modifying hostname, IP, port, endpoint alias, and other metadata.
- Scheduled shutdown, whole machine recovery, audit, report, and operation log viewing.

After database node failure, disk replacement, or system reinstallation, it should be initiated from the node recovery process in the Console, not manually copying the data directory or modifying the ClusterGuard metadata database by yourself.

## 12. Common Issues

### 12.1 Installer Only Outputs Plan

This is normal behavior. The installer defaults to read-only, and only appending `--execute` will modify remote hosts.

### 12.2 Error: "First execution must provide --known-hosts or explicitly use --accept-host-keys"

In production environments, provide the `--known-hosts` file that has been fingerprint-reviewed. In experimental environments, append `--accept-host-keys`.

### 12.3 Error: "Status file is inconsistent with the current cluster name, engine, or port"

This indicates that `--state-file` from a different cluster has been reused. Stop execution, restore the correct site directory; do not delete the original status file to bypass checks.

### 12.4 Missing Database Package Dependencies

When MySQL or control-plane runtime libraries are missing, collect signed RPMs on a mirror machine with the same distribution and architecture, and retry with `--dependencies dependencies`. When PostgreSQL source-build dependencies cannot be resolved online, create and upload an independent dependency add-on package, then retry with `--postgresql-dependencies <EXTRACTED_DIRECTORY>/dependencies`. Do not mix the two parameters, and do not skip verification.

### 12.5 Unable to Log in to Console

Check the control plane service, Raft quorum, and the node address accessed by the browser:

```bash
systemctl status clusterguard-ha.service --no-pager
journalctl -u clusterguard-ha.service -n 200 --no-pager
```

A new cluster starts with account `admin` and the initial password described in section 6.2. The password must be changed immediately after the first login. After it has been changed, use the new password; restarting the service does not restore the initial password, and the platform removes the bootstrap file.

### 12.6 VIP Not Bound or Multiple Owners

First, stop any external VIP management tools, then check the control node majority, current primary database role, Agent service, network card name, and CIDR. VIP is only allowed to be managed by ClusterGuard Agent; high-risk operations must be rejected when there is no quorum.

## 13. Uninstallation and Rollback

Uninstalling RPM individually will not delete `/etc/clusterguard/`, `/var/lib/clusterguard/`, and audit data:

```bash
dnf remove clusterguard-ha
```

Do not roll back by deleting the status directory in a running Raft cluster. Upgrades, rollbacks, control node retirement, and disaster recovery should all be completed in the controlled workflow of the Console, and audit and reports should be exported first.

## 14. Security Checklist

- Control nodes are always an odd number of at least 3.
- All nodes have synchronized clocks.
- SSH host keys have been independently verified.
- VIP is not managed by external HA tools.
- Database software and offline dependencies have been enterprise-approved and SHA256 verified.
- Site status, secrets, and certificates have been encrypted and backed up.
- The initial administrator password has been changed.
- A controlled switch, old primary reattachment, node restart recovery, and VIP uniqueness verification have been completed.
