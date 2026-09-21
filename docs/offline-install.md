# ClusterGuard HA Offline Installation

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](zh-CN/offline-install.md)
<!-- /LANGUAGE-SWITCH -->


The sealed MySQL production release is ClusterGuard HA `2.1-45`. PostgreSQL
delivery starts with the `2.2` line. The supported production bootstrap path is
the complete offline kit and its multi-node installer:

```text
clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
  install_clusterguard.sh
  packages/clusterguard-ha-2.1-45.x86_64.rpm
  packages/database/
  dependencies/
  docs/ClusterGuard-HA-离线安装与部署手册.md
```

The listing above uses the sealed `2.1-45` MySQL-only kit. Every PostgreSQL step on
this page requires a `2.2` or later kit; do not run them with a `2.1-45` kit.

The authoritative, current deployment guide is
[`docs/en-US/offline-rpm-install.md`](en-US/offline-rpm-install.md). It covers:

- reviewed SSH host keys and protected site state;
- three-node odd-numbered control-plane installation;
- mixed and separated controller/data-node layouts;
- MySQL 8.0/8.4, UPSQL-compatible package staging, dependencies, VIP and
  validation;
- subsequent node lifecycle and recovery boundaries.

See the sealed [2.1-45 release notes](en-US/release-2.1.45.md) and the
[version/release policy](en-US/version-release-policy.md) before building or
publishing a new package.

Use `install_clusterguard.sh --plan` before every production deployment. It
does not change remote hosts unless `--execute` is explicitly supplied.
VIP-backed MySQL HA defaults to database-level automatic failover using stable
MySQL failure evidence, a Raft-majority transition lease, and the local Agent's
fail-closed VIP/read-only reconciliation. `--fencer` is an optional second layer
for sites with BMC, PDU, cloud, or hypervisor isolation. Use
`--manual-failover-only` only when automatic recovery is intentionally disabled; it
cannot be combined with `--fencer` or `--fencer-assets`.

Do not use legacy per-node configuration examples as a replacement for the
multi-node installer. The per-node `clusterguard-install.sh` helper is used by
the platform lifecycle workflow after the control plane exists; it is not the
initial production bootstrap procedure.

An approved database archive can be embedded into a formal kit with the
repeatable `--database-package` builder option. Otherwise, place the approved
MySQL or UPSQL-compatible binary archive, or a PostgreSQL binary or official
release-source archive, under `packages/database/`. The installer discovers a
single engine/version match automatically, so `-r` is only needed to resolve an
intentional ambiguity. PostgreSQL source mode compiles once on the selected
data node, verifies the resulting binaries, creates a build manifest and
SHA256 digest, and distributes that exact artifact to all nodes.

The main kit contains only the small, signed ClusterGuard and MySQL runtime
repository. PostgreSQL source-build dependencies are online-first: the selected
build node resolves signed packages from its configured repositories into a
disposable DNF installroot, without modifying the host package database. If
online resolution fails, create and upload the separate PostgreSQL dependency
add-on with `tools/收集RHEL离线依赖.sh --postgresql-source-build` and
`tools/构建PostgreSQL依赖包.sh`, then retry with
`--postgresql-dependencies <extracted-directory>/dependencies`.
