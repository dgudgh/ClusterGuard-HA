# ClusterGuard HA 2.1-45 Release Notes

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/release-2.1.45.md)
<!-- /LANGUAGE-SWITCH -->

## 1. Release Positioning

`2.1-45` is the final release version of the ClusterGuard HA 2.1 series. The corresponding Git tag is
`v2.1.45`, and the corresponding source code commit is:

```text
28425925a321655f38686942c6b081d235b0d288
```

GitHub Release:

<https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v2.1.45>

This version remains immutable after release. It is not allowed to overwrite tags, replace attachments, or reuse the filename `2.1-45` to publish different content. Any defects discovered should be fixed in subsequent version lines and re-released.

## 2. Formal Deliverables

| File | Purpose | SHA-256 |
| --- | --- | --- |
| `clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz` | Recommended complete offline deployment package | `d4a46bdfa4c95bb641a7d19f063d2f43219177658b01b914a31a1cd5d06ec590` |
| `clusterguard-ha-2.1-45.x86_64.rpm` | ClusterGuard node package | `1bc70109b556e973744bb05b5be0f53b075260910ae5def25518631ff8629a1c` |

The complete offline package also includes two `.sha256` files. Before production deployment, verify the digest:

```bash
sha256sum -c clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-2.1-45-offline-linux-x86_64.tar.gz
cd clusterguard-ha-2.1-45-offline-linux-x86_64
sha256sum -c SHA256SUMS
```

Only installing RPM does not automatically form a three-node control plane, database replication, and VIP arbitration. For new production clusters, use the `install_clusterguard.sh` from the complete offline package.

## 3. 2.1 Support Scope

The formal database support scope for the 2.1 release line is MySQL high availability control, including:

- A Raft control plane composed of three or more odd-numbered control nodes;
- MySQL topology discovery, health checks, candidate evaluation, and replication diagnostics;
- Controlled primary switch, automatic failover, and migration of primary and VIP consistency;
- Full rebuild when old primary reattachment and incremental recovery are not feasible;
- Fixed platform node identity, MySQL `server_uuid` identity, and endpoint alias correction;
- Safety Guard, operation locks, approvals, verification, audit, and reports;
- Agent local disconnection isolation, VIP unique ownership, and restart convergence;
- Node addition, repair, installation, synchronization, and lifecycle tasks;
- Chinese console, API, `cgctl`, systemd, and offline installation delivery.

MySQL compatible versions still require on-site actual database packages to complete acceptance. Although the version names are the same, different compilation options, authentication plugins, system libraries, storage, and network configurations make them not directly considered as the same set of production evidence.

## 4. Not Included in 2.1 Scope

The following capabilities must not be promised or delivered under the name of `2.1-45`:

- PostgreSQL formal installation, switchover, failure recovery, and node synchronization;
- Oracle Data Guard Broker formal production takeover;
- SQL Server Always On formal production takeover;
- Adding new scripts, new dependencies, or new behaviors to the already published attachments.

PostgreSQL is delivered starting from `2.2-1`. Oracle and SQL Server must enter subsequent versions only after completing independent test matrices and on-site acceptance.

## 5. New Installation Entry

For complete parameters and production checks, see [Offline Installation and Deployment Manual](offline-rpm-install.md). For a typical MySQL three-node hybrid deployment, first execute the read-only plan:

```bash
./install_clusterguard.sh \
  -l 192.168.102.152,192.168.102.153,192.168.102.154 \
  -n 192.168.102.152,192.168.102.153,192.168.102.154 \
  -u root \
  -P 'SSH_PASSWORD' \
  -ld /var/lib/clusterguard \
  -nd /data \
  --engine mysql \
  --database-version 8.0.44 \
  --cluster-name production-mysql \
  --vip 192.168.102.155 \
  --interface ens160 \
  --known-hosts ./site/known_hosts \
  --plan
```

After verifying the nodes, versions, ports, VIPs, network cards, data directories, and installation media, change the end to
`--execute`. Do not execute `--execute` as a separate Shell command.

## 6. Production Admission

Before formal business access, at least confirm the following:

1. Three control nodes are healthy, the Leader is unique, and the Raft majority is available.
2. Three database instance identities are unique, replication threads are normal, and the delay meets business requirements.
3. The VIP exists only on the current primary, and after any node loses the majority authorization, it can locally revoke the VIP and remain read-only.
4. The planned switchover, primary failure, old primary recovery, node restart, and complete shutdown process have all been verified on-site.
5. Operation logs, audit, reports, monitoring interfaces, and backup recovery processes are available.
6. Production network partition, storage anomalies, and concurrent operation tests meet on-site RTO/RPO requirements.

The `2.1-45` release does not equate to waiving on-site acceptance. On-site differences must be recorded as separate acceptance records.

## 7. Subsequent Versions

- The 2.1 series will no longer add new features.
- PostgreSQL and subsequent changes will be included in `codex/2.2-postgresql`.
- The first 2.2 formal package will use `2.2-1`, and subsequent deliveries will use `2.2-2`, `2.2-3` in sequence, without overwriting old packages.
- Detailed rules can be found in [Version and Release Policy](version-release-policy.md).
