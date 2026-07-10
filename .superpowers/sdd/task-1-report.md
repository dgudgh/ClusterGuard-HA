# Task 1 Report: Common Topology, Metrics, and Candidate Contracts

## Implementation

- Added portable topology contracts in `pkg/model/topology.go`:
  `ThreadState`, `ReplicationStatus`, `MetricSample`, `TopologySnapshot`,
  `CandidatePolicy`, and `CandidateAssessment`, using the brief's exact field
  names, JSON tags, and enum values.
- Extended `model.DatabaseInstance` with `Replication`, `Maintenance`,
  `PromotionEligible`, and `EngineMetadata`.
- Added `CapabilityMetrics`, `CapabilityCandidates`, `CandidateRequest`, and
  the `Metrics` and `EvaluateCandidates` methods to the adapter contract.
- Extended `adapter.UnsupportedAdapter` to advertise both new capabilities as
  unavailable and return `adapter.ErrUnsupported` from both methods. The
  PostgreSQL, Oracle, and SQL Server skeletons inherit this behavior unchanged.
- Added explicit temporary unsupported methods and unavailable capability
  entries to the MySQL adapter. Later tasks can replace these placeholders
  when their implementations are tested.

## Tests

### RED evidence

After adding the contract tests and before production changes, ran:

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./pkg/model ./pkg/adapter -count=1
```

Result: exit code `1`, with the expected missing-contract failures:

```text
pkg/model/topology_test.go:10:3: unknown field Replication in struct literal of type DatabaseInstance
pkg/model/topology_test.go:10:16: undefined: ReplicationStatus
pkg/model/topology_test.go:12:20: undefined: ThreadRunning
pkg/model/topology_test.go:13:20: undefined: ThreadRunning
pkg/model/topology_test.go:17:14: instance.Replication undefined
pkg/adapter/registry_test.go:55:36: undefined: adapter.CapabilityMetrics
pkg/adapter/registry_test.go:58:36: undefined: adapter.CapabilityCandidates
pkg/adapter/registry_test.go:61:26: candidate.Metrics undefined
pkg/adapter/registry_test.go:64:26: candidate.EvaluateCandidates undefined
pkg/adapter/registry_test.go:64:75: undefined: adapter.CandidateRequest
FAIL
```

### Focused GREEN evidence

Ran:

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./pkg/model ./pkg/adapter ./adapters/... -count=1
```

Result: exit code `0`.

```text
ok   clusterguard.io/ha/pkg/model
ok   clusterguard.io/ha/pkg/adapter
ok   clusterguard.io/ha/adapters/mysql
?    clusterguard.io/ha/adapters/oracle [no test files]
?    clusterguard.io/ha/adapters/postgresql [no test files]
?    clusterguard.io/ha/adapters/sqlserver [no test files]
```

Also ran `git diff --check` successfully.

### Full GREEN evidence

Ran the required full suite once before committing:

```text
PATH=/Users/zhaolongjie/sdk/go1.26.2/bin:$PATH go test ./...
```

Result: exit code `0`; all packages passed, including MySQL, API, config,
store, workflow, adapter, identity, and model packages. The adapter packages
without test files compiled successfully.

## Files changed

- Created `pkg/model/topology.go`.
- Created `pkg/model/topology_test.go`.
- Modified `pkg/model/model.go`.
- Modified `pkg/adapter/adapter.go`.
- Modified `pkg/adapter/registry_test.go`.
- Modified `adapters/mysql/mysql.go`.

The permitted PostgreSQL, Oracle, and SQL Server files were not edited because
their existing embedding of `adapter.UnsupportedAdapter` automatically supplies
the new methods and unavailable capability states without redundant wrappers.

## Self-review

- Confirmed the new interface signatures use `DiscoverRequest` for metrics and
  the exact `CandidateRequest` shape from the brief.
- Confirmed unsupported methods return the shared sentinel `ErrUnsupported`,
  so `errors.Is` remains reliable.
- Confirmed all four adapters are covered by the new fail-closed test,
  including explicit capability checks and method calls.
- Confirmed MySQL's existing discovery and health behavior was not changed.
- Confirmed no files outside the permitted implementation/test set and the
  explicitly requested report path were modified.

## Concerns

- The default shell `PATH` does not contain Go in this environment. Verification
  used the available Go 1.26.2 toolchain at
  `/Users/zhaolongjie/sdk/go1.26.2/bin/go`; the repository declares Go 1.19 and
  the tested code remains compatible with the existing module.
- The MySQL capability entries are intentionally unavailable placeholders for
  the later metrics and candidate implementation tasks.
