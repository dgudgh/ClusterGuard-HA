# ClusterGuard HA 3.1.2.9 Release Notes

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/release-3.1.2.9.md)
<!-- /LANGUAGE-SWITCH -->

## Version and Download

Product **3.1.2.9**, Linux x86_64. The RPM installation and upgrade compatibility baseline is **2.2-107**. Runtime identity, the media directory and GitHub tag use the four-part product version. RPM Version/Release remains a separate compatibility identity and does not mean the console still runs an old product.

Download the complete installation kit from [GitHub v3.1.2.9 prerelease](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.2.9). Existing 3.1.2.8 and historical artifacts retain their original bytes. The sole mainline is `codex/2.2-postgresql`; the exact build commit is recorded in `RELEASE-INFO` and the tag.

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.9/clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.9/clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.2.9-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.2.9-offline-linux-x86_64
bash install_clusterguard.sh --help
```

Before installing, generate and review the plan per the [offline installation manual](offline-rpm-install.md), then run the authorized installation. For an existing cluster, use the [update and rollback manual](update-and-patch.md) to run the signed upgrade path; a complete installation kit is not an uploadable signed upgrade package.

## Fixes in This Release

- **The installer no longer runs command substitution from a remote payload on the operator workstation.** The remote payload that configures the graphical rolling-upgrade Helper is a double-quoted string, and one comment line inside it contained a backtick pair `` `workspace check-file` ``. A backtick inside a double-quoted string is interpreted **locally on the operator workstation**, so installation printed `workspace: command not found` and silently blanked that comment line. The backticks have been removed. A new contract check requires that `scripts/install_clusterguard.sh` contain no backticks, backed by one mutation.
- **A freshly started `Type=simple` unit is now waited on with a bounded readiness retry.** `clusterguard-update-helper.service` is `Type=simple`, so `systemctl enable --now` returns once systemd has declared the unit active, while the managed process may not yet have bound `/run/clusterguard/update-helper.sock`. The immediately following `systemctl is-active --quiet` and `test -S` assertions therefore failed in a readiness race and aborted the whole step with `exit=1`, **even though the helper had actually started normally**. Both conditions are now retried within a bounded loop, and the two hard assertions remain after the wait so a genuinely unready helper still fails. A new contract check requires a retry before the helper readiness assertions, backed by one mutation.

Together these fixes let a **fresh installation complete every step in one pass**; previously they made installation abort at the "configure the software update trust chain" step.

## Accumulated Features

- Every Go build target of the complete installation kit and RPM injects the four-part product version, and all five payload commands (`clusterguard`, `cgctl`, `clusterguard-agent`, `clusterguard-k8s-fence-guard`, `clusterguard-update-helper`) report that identity via `--version`; the console and version API use the same product identity. The bundled `BUILD-INFO` and `RELEASE-INFO` record the product version and the RPM baseline.
- The complete-media verifier locates the runtime bundle by the actual four-part media version and checks the product identity, build source, the two embedded console payloads and every checksum. The build entry points refuse to overwrite an existing artifact.
- The complete kit accumulates mainline fixes including version/history projection, retry versus resume routing, update progress retention, and bilingual preference with localized historical results.
- Control-node parameter distribution: the 25 integer parameters allowed for page editing are pre-checked and double-confirmed before distribution, then applied with a controlled per-node restart and read back for the effective value; cluster-level parameters require selecting every voter. A failure retains the maintenance gate, recovered by retry or rollback.
- The runtime-parameter interface uses node cards, collapsed default groups and a fixed action bar. Parameters that can be distributed online are placed above, with the modified value in the last column; identity, network, credential, path and fencing parameters that require a dedicated flow are placed below without an edit box. Cluster policy keeps its own save path.

"Distribute online" means the console submits a controlled task; ordinary process startup parameters still take effect through a rolling restart, and re-reading a file only refreshes the display without hot reloading. See [parameter distribution implementation and boundaries](../development/backend/settings/configuration.md) and [page interaction](../development/frontend/settings/configuration.md).

## Complete-Media Contents and Support Boundaries

Contains the control plane, cgctl, Agent, Kubernetes fencing guard, restricted Update Helper, systemd units, installation/configuration/upgrade/recovery scripts, static Linux jq, Rocky Linux 8 x86_64 base runtime dependencies and repository metadata, configuration examples, signing public key, installation and operations manuals, and the AGPL-3.0-only / third-party licenses.

The same medium embeds the MySQL **8.0.44** vendor minimal binary package and the PostgreSQL **16.4** official source package, each with a checksum. Compilation dependencies for the PostgreSQL source are supplied as a separate dependency package or from a trusted build-node repository and are not included in the base runtime repository; a fully isolated environment must prepare matching build dependencies per the [database preparation manual](database-preparation.md) in advance. No production support claim is added for Oracle / SQL Server.

Public assets are the complete `.tar.gz`, the RPM, their SHA-256 sidecars, `RELEASE-INFO`, `verification.json` and the release notes; signed upgrade / hotfix artifacts remain local and in the contracted delivery channel only.

## Verification and Field Status

Verification scope for this release: Go regression, applicable race/vet, source contract and mutations (including the two backtick and readiness-wait rules added here), license/Markdown gates, and complete-media unpacking/checksum/source binding/embedded pages/executable versions, plus private upgrade companion signing and read-only inspection. Every result is governed by this verification record and the public `verification.json`; an isolated API browser is not field verification.

**Customer production site acceptance was not executed on the target Linux, and no field acceptance was run for this version's real database, per-node systemctl restart, quorum/VIP/recovery or rolling upgrade. FIELD remains OPEN, so this is a prerelease and makes no production-stable acceptance claim.** Historical acceptance reports describe only their own version and scope. Before deployment, verify the target system's RPM trust chain, configuration and applicable gates.
