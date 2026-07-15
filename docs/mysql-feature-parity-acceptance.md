# ClusterGuard HA MySQL Acceptance

Date: 2026-07-15

This record captures the destructive laboratory acceptance for the independent
ClusterGuard HA MySQL control path. It is evidence for this build, not a
substitute for environment-specific production qualification.

## Build Under Test

- branch: `codex/mysql-feature-parity`
- bundle: `clusterguard-ha-mysql-parity-rc58-linux-amd64.tar.gz`
- bundle SHA-256:
  `4b49745a2003cf8720ddbac95cc6cbba230a1e20246fa88216a994e4f946abca`
- server binary SHA-256:
  `13e93038239c771c4641bdd5a1b0f151760cac9fa863dd550065d72724b361c6`
- MySQL sync helper SHA-256:
  `35d1c9e2bb9d2da4b61c01b7ec124c5ca5128b877e1ffde460a720c5126e529e`

The archive manifest contains 20 entries and verifies with
`shasum -a 256 -c SHA256SUMS`.

## Laboratory Topology

Controllers and colocated data nodes:

| Address | Fixed node name | Role |
| --- | --- | --- |
| `192.168.102.152` | `cg-node-0001` | controller + data |
| `192.168.102.153` | `cg-node-0002` | controller + data |
| `192.168.102.154` | `cg-node-0003` | controller + data |

Registered clusters:

| Cluster | Port | VIP |
| --- | ---: | --- |
| `mysql-ha-3306` | 3306 | `192.168.102.155` |
| `mysql-ha-3307` | 3307 | `192.168.102.156` |
| `mysql-test-5.7` | 3357 | `192.168.102.157` |
| `mysql-test-8.0` | 3380 | `192.168.102.158` |
| `mysql-test-8.4` | 3384 | `192.168.102.160` |
| `mysql-test-9.7` | 3397 | `192.168.102.164` |

Every cluster had three native-identity-verified instances and one dedicated
VIP. Resource identity used immutable platform UUIDs and MySQL `server_uuid`,
not hostname and port.

## Acceptance Results

- Six-cluster baseline smoke checks passed.
- Twenty round-robin and thirty random real switchovers completed: `50/50`.
- Primaries were distributed across nodes and distinct cluster VIPs remained
  isolated during concurrent activity.
- Leader service loss elected a replacement and all controller snapshots
  converged.
- Loss of controller quorum failed closed: all 18 instances became read-only
  and all six VIPs were removed. Quorum restoration converged automatically.
- A real primary network partition triggered an 8.0 failover after about 55
  seconds. The new primary owned the only VIP and all replicas followed it.
- The recovered former primary passed fast rejoin and remained read-only.
- A full reboot of `192.168.102.154` restored controller, agent, and reconciliation
  timer services; six-cluster health and VIP uniqueness recovered.
- Repeated recovered-primary MySQL restarts did not create a writer or VIP
  conflict.
- Divergent-node rebuild reused the original platform UUID and fixed node name.
  A deliberately injected target-only schema was removed before donor import;
  GTID sets then matched in both directions, replication threads were running,
  lag was zero, and no VIP remained on the rebuilt replica.
- Actual hostname, IP, and MySQL port were changed and reconciled, then restored.
  The same resource UUID and `server_uuid` remained; old endpoints became
  aliases and no duplicate node was created.
- Final topology snapshots were identical on all three controllers and all six
  cluster smoke checks passed after cleanup.

## Local Verification

The release candidate is gated by:

```bash
gofmt -w $(rg --files -g '*.go')
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/clusterguard
go build ./cmd/cgctl
go build ./cmd/clusterguard-agent
git diff --check
```

Configuration JSON, shell syntax, clean-room naming, dependency hygiene, bundle
contents, and manifest checks are also part of the final release gate.

The live laboratory root served the expected `ClusterGuard HA Console` title,
Chinese default language, and six-cluster API directory. Interactive screenshot
automation was not available because the in-app browser rejected the laboratory
self-signed certificate under its URL security policy. Console structure,
responsive constraints, real API wiring, and safe rendering remain covered by
the `internal/api` console contract tests.

## Production Qualification Still Required

- ClusterGuard self-isolation and quorum gates are present, but production must
  integrate an independent out-of-band fence for failed hosts.
- The accepted rebuild used the logical-dump fallback. Clone and version-matched
  XtraBackup helpers must be qualified before selecting them in production.
- Backup retention, disaster recovery, external alert delivery, and long-term
  metrics retention remain deployment responsibilities.
- The destructive matrix must be repeated on the target hardware, storage,
  network, operating system, and exact MySQL packages.
