# ClusterGuard HA 3.1.7.11 complete offline installation candidate

2026-11-04. Local internal test candidate: product 3.1.7.11, RPM 2.2-115, Linux x86_64. Clean-site installation and stable-release acceptance are not certified. RELEASE-INFO and the adjacent SHA-256 sidecar identify the actual build.

## Media and installation

clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz contains the HA RPM, Linux runtime bundle, Rocky 8 runtime dependencies, approved production MySQL templates, installer, complete Chinese/English installer option references and the patch verification public key. Vendor database binaries are not embedded. Supply approved vendor media on site: explicit -r takes precedence, followed by packages/database and then /opt. MySQL defaults to /usr/local/mysql and instance root /data/mysql/<port>; identity and read-only protections remain in force.

```bash
sha256sum -c clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.11-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.11-offline-linux-x86_64
./install_clusterguard.sh --help
```

Use the [installer option reference](offline-installer-parameters.md) to prepare site arguments and run --plan first. This media supports installation/controlled resumption; existing-cluster updates use the formal signed update channel. The immutable 3.1.7.11.cgpatch still requires its original RPM 2.2-113 baseline; RPM 115 is a new installation baseline. The independently deployed enterprise gateway binary is not included in HA media.

## Included changes and acceptance limits

Includes consistent Raft authorization reads, verified PG upstream provenance for source loss, and local causal update-history projection. The preceding hotfix with the same production source was installed on three controllers and exercised under transaction-bearing upgrade, weighted automatic PG failover/cloud exclusion, guarded rejoin/return and acknowledged-row retention. Those tests do not certify installation of this complete media.

Known unresolved defects: attempts accepted only by the former Leader may be absent from the new Leader's update history/API; PG former-primary rejoin interrupted the current primary's business entry, with reconnection roughly 21 seconds later. One final observation also reported a transient 181-second cloud PG link lag; the failure is retained and a separate identical strict rerun passed 62/50 healthy cycles. See the [actual field record](../development/ha-remaining-fixes-2026-11-03.md). This candidate is not a fix or zero-interruption certification for these defects.

Additional audit on 2026-11-04: the default dependencies neither declare nor carry acl, although MySQL directory preparation requires setfacl. The unchanged function exited 3 in a Linux child environment without that command. Uncached regression runs for three packages and the two Linux-specific tests passed, but do not certify clean three-node installation or close the field defects. This media remains unsuitable for stable delivery. This is an external clarification; existing archive bytes and its bundled notes are unchanged. See the [independent audit](../development/offline-3.1.7.11-2026-11-04.md).

The media verifier records artifact checks in verification.json. Clean-site full installation, the fault matrix, target-OS RPM/dependency signature trust and stable-release certification remain open. Source/artifact results are separate from field acceptance. No GitHub Release is published by this packaging task; signed patches are never public attachments.
