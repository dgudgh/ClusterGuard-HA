# ClusterGuard HA Offline Installation

This procedure installs ClusterGuard HA without allowing the target network to
reach the Internet. Build the release bundle on a trusted connected build host,
transfer one immutable archive plus site-specific protected inputs, and run the
same artifact on every controller.

## 1. Supported Release Shape

The standard bundle contains statically linked Linux binaries for:

- `clusterguard`
- `cgctl`
- `clusterguard-agent`
- controller and data-node installation helpers
- systemd and logrotate units
- example controller and Agent configuration
- an internal `SHA256SUMS` manifest

The bundle does not redistribute database vendor software. Stage the matching
database clients and server packages separately because their licenses,
architectures, and operating-system dependencies differ.

| Enabled capability | Required offline prerequisite |
| --- | --- |
| Base install | systemd, bash, coreutils, findutils, shadow utilities, tar, gzip, CA certificates |
| MySQL control | `mysql`; managed node installation also needs a supported MySQL tar archive |
| PostgreSQL control | `psql`; node sync also needs `pg_basebackup`, `pg_rewind`, and `pg_controldata` |
| Oracle direct control | Oracle Instant Client or Oracle home with `sqlplus` and `dgmgrl` |
| Oracle Agent control | `sqlplus` and `dgmgrl` on each Oracle database node |
| SQL Server control | Microsoft `sqlcmd` plus its offline ODBC dependencies |
| Node lifecycle | OpenSSH client; `sshpass` only for temporary password bootstrap |
| VIP ownership | `iproute` and `arping` on every data node |

ClusterGuard preflight checks the clients required by each enabled adapter and
fails before host mutation when a dependency is missing.

## 2. Build the Immutable Bundle

Use a Go 1.22-or-newer build host. Supply a Linux `jq` binary matching the
target architecture. The builder rejects a macOS binary when producing Linux
media.

```bash
VERSION=1.0.0
mkdir -p dist
./scripts/build-clusterguard-bundle.sh \
  --output "$PWD/dist" \
  --version "$VERSION" \
  --goos linux \
  --goarch amd64 \
  --jq-binary /secure/toolchain/jq-linux-amd64

cd dist
sha256sum "clusterguard-ha-${VERSION}-linux-amd64.tar.gz" \
  > "clusterguard-ha-${VERSION}-linux-amd64.tar.gz.sha256"
```

Keep the archive and outer checksum together. Sign the checksum with the
organization release key when a signing process is available.

## 3. Prepare Protected Site Inputs

Prepare a separate directory for each node. Do not commit this directory:

```text
site-input/
  clusterguard.json
  clusterguard.env
  agent.json
  assets/
    tls/
      ca.crt
      server.crt
      server.key
      raft-ca.crt
      raft.crt
      raft.key
    ssh/
      controller_ed25519
      controller_known_hosts
    mysql/
      3306-client.cnf
    postgresql/
      5432.pass
```

Use unique controller and Agent secrets, per-node TLS identities, and immutable
platform UUIDs. Never copy a live `/var/lib/clusterguard/` directory to create a
new controller. Generate a new node UUID with `uuidgen` or
`cat /proc/sys/kernel/random/uuid`, then keep it stable for that physical node.

Set protected input permissions before transfer:

```bash
chmod 600 site-input/clusterguard.env
find site-input/assets -type f -name '*.key' -exec chmod 600 {} +
find site-input/assets -type f -name '*_ed25519' -exec chmod 600 {} +
```

## 4. Transfer and Verify

Move the archive, its checksum, database client packages, database server
archives, and the node's protected input directory through the approved offline
media path. On every target:

```bash
sha256sum -c clusterguard-ha-1.0.0-linux-amd64.tar.gz.sha256
tar -xzf clusterguard-ha-1.0.0-linux-amd64.tar.gz
cd clusterguard-ha-1.0.0-linux-amd64
sha256sum -c SHA256SUMS
```

Install the vendor database clients from the transferred RPM, DEB, or tar media
before running ClusterGuard preflight. Do not configure an operating-system
repository that points outside the offline network.

## 5. Preflight Without Mutation

For a colocated controller and data node:

```bash
sudo ./scripts/clusterguard-install.sh \
  --bundle-dir "$PWD" \
  --role mixed \
  --node-name cg-node-0001 \
  --node-id 11111111-1111-4111-8111-111111111111 \
  --config /secure/input/clusterguard.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent.json \
  --assets-dir /secure/input/assets
```

Use `--role controller` without `--agent-config` for a controller-only host.
Use `--role data` without `--config` for a data-only host. The command above is
read-only: it verifies the manifest, JSON, node identity, systemd, native
clients, Agent network tools, and runtime assets, then prints the install plan.

## 6. Execute the Reviewed Plan

Repeat the identical command with `--execute`:

```bash
sudo ./scripts/clusterguard-install.sh \
  --bundle-dir "$PWD" \
  --role mixed \
  --node-name cg-node-0001 \
  --node-id 11111111-1111-4111-8111-111111111111 \
  --config /secure/input/clusterguard.json \
  --env-file /secure/input/clusterguard.env \
  --agent-config /secure/input/agent.json \
  --assets-dir /secure/input/assets \
  --execute
```

The installer creates `/etc/clusterguard/`, `/var/lib/clusterguard/`, and
`/var/log/clusterguard/`; installs the binaries and services; and starts only
the services appropriate for the selected role.

## 7. Three-Controller Rollout

Install all controllers from the same bundle. Use different node IDs and TLS
certificates, but one consistent peer list and shared control-plane secret set.
Bring up the initial Raft membership, then verify:

```bash
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:8088/healthz
curl --fail --cacert /etc/clusterguard/tls/ca.crt \
  https://127.0.0.1:8088/readyz
cgctl --server https://127.0.0.1:8088 status
```

Do not activate periodic VIP reconciliation during initial installation.
Register each cluster and canonical endpoint, verify a majority ownership
lease, and perform one successful reconciliation on every node. Then repeat
the installer with `--activate-agent-reconcile --execute`.

## 8. Upgrade and Rollback

Copy the new archive beside the old one and run preflight first. Upgrade one
follower, verify readiness, upgrade the next follower, and upgrade the Leader
last. Keep the previous archive until the cluster has passed discovery,
operation, verification, audit, and report checks.

Rollback uses the previous bundle with the same configuration and node UUID.
Never replace replicated metadata with an older snapshot unless the whole
controller cluster is stopped and the documented disaster-recovery procedure
is being followed.
