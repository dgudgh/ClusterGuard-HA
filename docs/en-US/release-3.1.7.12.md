# ClusterGuard HA 3.1.7.12 Complete Offline Installation Kit

2026-11-11. Product 3.1.7.12, RPM installation baseline 2.2-116, Linux x86_64. Fixes missing SSH trust entries after correcting target IPs and misleading root/systemd errors after SSH failure. Adds Chrony validation before clock changes. Output: release/3.1.7.12/; immutable 3.1.7.11 artifacts remain unchanged.

## Changes

- With explicit `--accept-host-keys`, check each current address/port and scan missing addresses only. Preserve existing pins; strict SSH checking still rejects changed keys.
- An explicit `--known-hosts` file stays read-only and must cover all targets. Failed, empty or wrong-address scans do not publish a partial cache.
- Preserve SSH diagnostics/exit status rather than automatically attributing transport failures to root permissions or missing systemd.
- The clock tool's `--check` validates chronyd, chronyc and a persistent chronyd.service. Check every node before changing the first node's timezone, configuration or service.

## Installation and resumption

```bash
sha256sum -c clusterguard-ha-3.1.7.12-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.7.12-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.7.12-offline-linux-x86_64
sha256sum -c SHA256SUMS
./install_clusterguard.sh --help
```

Use verified `-l`/`-n` lists and retain the original `--state-file`, `--secrets-file` and `--work-dir`. Existing secrets supply the MySQL password; the remote-root Host policy must match the existing state. See the [complete option reference](offline-installer-parameters.md). Updating SSH trust does not rebuild database identity or data; preserve the original site files and database directories.

Supply approved vendor database media separately. Defaults: /usr/local/mysql and /data/mysql/<port>. The independent enterprise gateway is not included. Chrony/acl are not automatically installed: targets must already have matching Chrony services and setfacl. Missing prerequisites are rejected without bypassing clock or security checks.

## Validation scope and known issues

See the [build/validation record](../development/offline-3.1.7.12-2026-11-11.md). Adjacent verification.json covers media checks, not three-node installation. Earlier version 11 site acceptance cannot certify version 12; this task does not install on user hosts or publish a new GitHub Release.

Cross-Leader update-history gaps, PostgreSQL rejoin interruption and installation failure when acl is absent remain unresolved. Publisher RPM signing trust, complete site installation and the fault matrix are not certified. The immutable 3.1.7.11 hotfix still requires its original RPM2.2-113 baseline, distinct from this complete media's new installation baseline.
