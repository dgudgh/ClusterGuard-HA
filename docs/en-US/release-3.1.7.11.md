# ClusterGuard HA 3.1.7.11 Complete Offline Installation Kit

2026-11-04. Published at the user's explicit direction as a **production-usable release for the confirmed MySQL site**. Product **3.1.7.11**, RPM installation baseline **2.2-115**, Linux x86_64; [GitHub v3.1.7.11](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.7.11). This scoped user acceptance does not certify every engine, topology or failure scenario.

## Media and identity

`clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz`: 34,215,397 bytes; SHA-256:

```text
718673d73647c3ebd46a1194b84bbf5415b06ea2442a105b0f9f80ecfa2406c1
```

Build commit: `f1fd4b026acad8a8cf952112c5e28895fba85c20`. The tag identifies that commit; later documentation on the main branch does not change the build identity. Includes the HA RPM, runtime bundle, Rocky8 runtime dependencies, original production MySQL templates, installer, bilingual option reference and patch-verification public key. Vendor database binaries and the independent enterprise gateway are not included.

Supply approved database media: explicit `-r` takes precedence, followed by `packages/database/`, then `/opt`. The accepted site used vendor MySQL8.0.44 media. Default software directory: `/usr/local/mysql`; instance root: `/data/mysql/<port>`. Node identity and read-only startup remain protected; authorized workflow enables primary writes.

```bash
sha256sum -c clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.11-offline-linux-x86_64
sha256sum -c SHA256SUMS
./install_clusterguard.sh --help
```

Use the [complete option reference](offline-installer-parameters.md) and check the site command with `--plan`. Set the initial MySQL root password through `--mysql-root-password` or secure environment input. Remote root is opt-in with `--mysql-root-remote-host`. Resume with the original state, secrets and work directory; existing secrets take precedence over a new password argument. No site credentials are published.

This media supports installation/controlled resumption. Existing clusters use the signed update channel. The immutable 3.1.7.11 hotfix still requires RPM2.2-113, distinct from installation baseline115. Signed update/hotfix packages remain local and enterprise delivery assets, never public attachments.

## Observed scope and user acceptance

- The user supplied a 2026-11-04 three-node MySQL8.0.44 installation transcript: strict configuration validation, initialization, database startup and replica synchronization completed on all nodes; HA, Helper, Agent and VIP reconciliation were configured.
- The installer reported successful verification at15:15:15. Its final checks cover ready/quorum state, consecutive healthy topology observations with one primary, exactly one VIP owner, HA/Agent/Helper services and the Helper socket. Execution after confirmation took about8minutes35seconds.
- The user then explicitly confirmed production usability and authorized GitHub publication. This is user-provided installation and usage acceptance, not a new independent Codex site inspection or a complete fault-matrix result.
- Fifteen dedicated media checks, actual Linux payload execution and RPM file-mapping checks passed. Asset download/size/SHA checks are recorded by this publication workflow.

The original `RELEASE-INFO` (`release_channel=candidate`), `verification.json` (`deployed=false`, `field_acceptance=false`) and bundled notes remain build-time snapshots. The same archive is published without rebuilding, re-signing or changing its bytes; this external note records subsequent installation and publication. The publisher RPM has header/payload digests but no publisher RPM digital signature; the patch public key is not an RPM signature.

## Included changes and known issues

Includes consistent Raft authorization reads, verified PG upstream provenance after source loss, and local causal update-history projection. Earlier hotfix tests exercised controller updates, acknowledged-row retention, weighted PG failover and guarded rejoin/return. Their successes and failures are retained separately.

The following defects remain unresolved:

1. **Missing acl dependency:** the default RPM/media dependencies do not cover `acl`; MySQL directory preparation requires `setfacl` and exits3 when it is missing. Check `command -v setfacl` on new targets and prepare trusted matching acl RPM/dependencies for fully offline sites.
2. **Cross-Leader history gaps:** attempts present only on a previous Leader may be absent from the new Leader's query; original files are retained.
3. **PG rejoin interruption:** former-primary rejoin previously interrupted the current writer's entry, with reconnection around21seconds later. A separate transient181-second cloud PG lag observation and its later recovery remain recorded.

The complete kit has not independently passed every network-partition, concurrency/failure and target-OS signing-trust scenario. This publication does not claim universal stable certification, zero interruption or closure of the PG defects. See the [installation/publication record](../development/offline-3.1.7.11-2026-11-04.md) and [original field failures](../development/ha-remaining-fixes-2026-11-03.md).
