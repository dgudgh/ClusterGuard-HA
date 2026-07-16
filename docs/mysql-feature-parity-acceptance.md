# ClusterGuard HA MySQL Acceptance

Date: 2026-07-15

This record captures the destructive laboratory acceptance for the independent
ClusterGuard HA MySQL control path. It is evidence for this build, not a
substitute for environment-specific production qualification.

## Build Under Test

- branch: `codex/mysql-feature-parity`
- bundle: `clusterguard-ha-mysql-parity-rc61-linux-amd64.tar.gz`
- bundle SHA-256:
  `f8724fb4d79e3af57c32c4c5fb8be13d266a246b39752f5b6430cebd69ac7860`
- server binary SHA-256:
  `03eac92b7013f3788957fbe6b60c32cb291c6c4bc242b0ff12933c71c73c7539`
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
- The console fleet overview, compact topology, operation workbench, node
  lifecycle, metrics, searchable operation log, settings, and metadata dialog
  were exercised against the live three-controller API rather than fixture
  data.
- A browser-driven real switchover completed through all five visible stages:
  precheck, plan, safety gate, execute, and verify. The result was not declared
  successful until the durable operation reached `succeeded/report` and its
  verification passed.
- A stale read-after-write window was reproduced during a real switch, covered
  by a regression test, and fixed. The console now waits for the expected
  primary before rendering the verified result instead of replacing it with a
  transient follower snapshot.
- Read-only automatic refresh was then observed for 40 seconds after a second
  real switch. The verified result, five completed stages, selected cluster,
  and new primary remained intact across the 30-second refresh interval.
- A mutation sent to a follower failed closed and returned the current Leader
  API address. The same operation was executed on the Leader and recorded as
  operation `fd5e2277-efe0-46bb-b690-c55da3935783`.
- After final cleanup, every controller reported `orch-mysql03` at
  `192.168.102.154:3306` as the only primary for `mysql-ha-3306`; only that host
  owned VIP `192.168.102.155`, and the latest operation remained verified.

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
Chinese default language, and six-cluster API directory. Browser automation was
performed through a loopback-only TLS-terminating test bridge to the active
controller; it did not change the laboratory servers or production network
path. Visual checks covered the overview, topology, operation workbench, nodes,
metrics, logs, settings, about page, and metadata dialog. The operation log was
confirmed to show the local time, stable cluster name, source and target nodes,
status, and a collapsed raw workflow object that expands on demand.

## Platform Authentication And One-Time Approval Update

The 2026-07-16 authorization update adds Raft-replicated platform users and
sessions while retaining hash-only, one-time operation grants:

- a new metadata store bootstraps `admin` with temporary password `admin123`
  and `MustChangePassword=true`;
- the bootstrap password must be changed before any database or administrative
  mutation is accepted;
- passwords use Argon2id, browser sessions have an eight-hour absolute
  lifetime, and password change or logout revokes the active session;
- session-authenticated `admin` and `operator` execution builds the exact
  durable operation plan, creates a one-time grant inside the server, consumes
  it atomically at `APPROVE`, and never sends the approval secret to the
  browser;
- explicit service automation remains separate: the control Bearer
  authenticates the client, a plaintext grant is returned once, and the grant
  remains bound to cluster, engine, operation kind, target, observation, and
  plan;
- consumed, expired, mismatched, replayed, and stale-plan grants fail closed;
- the HA matrix supports browser-equivalent platform-session execution and
  preserves a separate explicit-grant mode for service-client replay tests;
- automatic failover uses a private incident authorization path and no human
  token, while retaining quorum, fencing, lock, verification, audit, and report.

Local API verification covered bootstrap login, mutation blocking before the
password change, session revocation, re-login, CSRF-protected reads and writes,
and sanitized responses. The full Go suite, shell matrix suite, source scans,
and builds gate this update. The authenticated bundle still requires a new
three-host destructive acceptance run before this section can claim live-lab
platform-session switchovers.

## Production Qualification Still Required

- ClusterGuard self-isolation and quorum gates are present, but production must
  integrate an independent out-of-band fence for failed hosts.
- The accepted rebuild used the logical-dump fallback. Clone and version-matched
  XtraBackup helpers must be qualified before selecting them in production.
- Backup retention, disaster recovery, external alert delivery, and long-term
  metrics retention remain deployment responsibilities.
- The destructive matrix must be repeated on the target hardware, storage,
  network, operating system, and exact MySQL packages.
