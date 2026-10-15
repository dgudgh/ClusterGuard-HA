# ClusterGuard HA 3.1.3.6 Complete Offline Installation Candidate

## Identity and scope

Product 3.1.3.6, RPM baseline 2.2-114, sole directory release/3.1.3.6/. This optimizes database archive inspection and repeated media distribution, with stage timing. Existing 3.1.3.5 and previously delivered artifacts remain byte-identical.

This is a local complete installation-test candidate. SOURCE, media structure and field acceptance are recorded separately. Production signature ART and full FIELD for this version remain OPEN; no GitHub Release has been created.

## Changes

- MySQL archive inspection reads the complete stream once into a private listing. Readability, single-root, path traversal and management-marker checks still precede installation mutations.
- Every node recomputes SHA256 before reusing media. Different content, including the same filename, uploads to a 0600 temporary file; remote verification precedes atomic publication as root:root/0640. Failed transfers retain previous bytes.
- Symbolic-link targets and directories are rejected. Existing /data and /data/packages ownership and permissions remain unchanged; they must be root-owned and not group/other writable. Engine cache directories are verified as root:root/0750.
- Live progress reports archive, extraction, configuration validation, initialization and startup duration; distribution logs distinguish per-node transfer and reuse. Only fixed-format metadata is forwarded, preserving error codes and credential redaction.

Approved production cnf, path defaults, failed-initialization preservation, locks, read-only startup and writer-lease rules retain their current behavior.

## Measurements and use

With the same approximately 831 MiB vendor archive on the same Linux node, archive inspection took 81.15 seconds before and 39.53 seconds after, about 51.3% less. This single old-then-new comparison establishes only that stage, not complete installation duration. Real SSH/SCP in an independent directory verified reuse, same-length different content, corrupted transfers, symlinks and permissions. Test files were removed; running databases were not reinstalled.

Use the existing installation command; no speed option is required. Preserve original state, secrets and work-dir when repeating installation. Reuse does not bypass management identity, residue protection or acceptance checks. The complete tar is for CLI installation, not a console cgpatch upload.

See the [complete CLI reference](offline-installer-parameters.md), [installation manual](offline-rpm-install.md) and [development record](../development/mysql-install-performance-2026-10-15.md). Fresh installation and replication/VIP FIELD for this version remain unexecuted; running databases were not reset for timing.
