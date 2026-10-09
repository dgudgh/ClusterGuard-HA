# ClusterGuard HA 3.1.2.8 Release Notes

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/release-3.1.2.8.md)
<!-- /LANGUAGE-SWITCH -->

## Version and Download

Product **3.1.2.8**, Linux x86_64. The RPM installation and upgrade compatibility baseline is **2.2-106**. Runtime identity, the media directory and GitHub tag use the four-part product version. RPM Version/Release remains a separate compatibility identity.

Download the complete installation kit from [GitHub v3.1.2.8 prerelease](https://github.com/dgudgh/ClusterGuard-HA/releases/tag/v3.1.2.8). Existing 3.1.2.7 and historical artifacts retain their original bytes. The sole mainline is `codex/2.2-postgresql`; the exact build commit is recorded in `RELEASE-INFO` and the tag.

```bash
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.8/clusterguard-ha-3.1.2.8-offline-linux-x86_64.tar.gz
curl -fLO https://github.com/dgudgh/ClusterGuard-HA/releases/download/v3.1.2.8/clusterguard-ha-3.1.2.8-offline-linux-x86_64.tar.gz.sha256
sha256sum -c clusterguard-ha-3.1.2.8-offline-linux-x86_64.tar.gz.sha256
tar -xzf clusterguard-ha-3.1.2.8-offline-linux-x86_64.tar.gz
cd clusterguard-ha-3.1.2.8-offline-linux-x86_64
bash install_clusterguard.sh --help
```

Use the [installation guide](offline-rpm-install.md) to review the plan before an authorized installation. Existing clusters use the [signed update and rollback workflow](update-and-patch.md); the complete installation kit is not a signed uploadable update package.

## Fixes and Cumulative Features

- All Go targets in complete media and RPM builds receive the four-part product identity. Runtime APIs and the console share this identity; `BUILD-INFO` and `RELEASE-INFO` retain both product version and RPM baseline.
- Media verification resolves the actual four-part bundle path and checks product identity, source revision, both embedded consoles and all checksums. Builders refuse to overwrite existing deliverables.
- Includes mainline fixes for version/history projection, hotfix retry versus rolling resume, progress continuity, language preferences and localized update results.
- Controller parameter distribution supports 25 allowed integer parameters, prechecks, confirmation, controlled sequential restarts and effective-value readback. Cluster parameters require all voting members. Failed tasks retain the maintenance gate until retry or rollback recovers them.
- Node cards, collapsed parameter groups and a fixed action bar organize the configuration page. Editable groups appear first, with modification inputs in the final column; parameters requiring a dedicated workflow appear below without editors. Cluster policy retains its independent save operation.

Online distribution submits a controlled task; process startup parameters still take effect through rolling restarts. Rereading a file refreshes the view without hot reload. See [backend scope](../development/backend/settings/configuration.md) and [frontend behavior](../development/frontend/settings/configuration.md).

## Media Contents and Support Boundary

Includes the controller, cgctl, Agent, Kubernetes fencing guard, restricted Update Helper, systemd units, installation/configuration/update/recovery scripts, static Linux jq, the Rocky Linux 8 x86_64 runtime repository, configuration examples, a signing public key, operational manuals, AGPL-3.0-only and third-party license texts.

The kit embeds the vendor MySQL **8.0.44** minimal binary archive and PostgreSQL **16.4** official source archive with checksums. PostgreSQL compilation dependencies require a separate dependency pack or trusted repositories on the build node; they are not in the small runtime repository. Prepare matching build dependencies for disconnected environments using the [database preparation guide](database-preparation.md). This release adds no Oracle or SQL Server production support claim.

Public attachments contain the complete kit, RPM, their checksums, `RELEASE-INFO`, `verification.json` and release notes. Signed update/hotfix artifacts remain local or on the contracted delivery channel.

## Verification and Site Status

Verification covers Go regressions, applicable race/vet, source contract mutations, license/Markdown gates, real Chrome with isolated API fixtures, archive extraction/checksums/source binding/embedded pages/native executable identity, and signature/read-only inspection of a private update companion. Consult the current evidence and public `verification.json`; fixture browser checks are not site acceptance.

**No target Linux installation or current native database/systemctl restart/quorum/VIP/recovery/rolling-update site acceptance was executed. FIELD remains OPEN; this is a prerelease without a production acceptance claim.** Historical acceptance reports describe only their matching artifacts. Verify OS RPM trust, configuration and applicable gates before deployment.
