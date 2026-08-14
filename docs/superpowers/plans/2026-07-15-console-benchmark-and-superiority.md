# ClusterGuard HA Console Benchmark and Superiority Implementation Plan

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/plans/2026-07-15-console-benchmark-and-superiority.md)
<!-- /LANGUAGE-SWITCH -->

> Execute according to TDD; add failed contract tests before any changes to production pages.

**Objective:** Upgrade the self-contained console to a fleet-priority, evidence-driven, real-executable enterprise-level high-availability workstation without changing the backend API.

**Technical Constraints:** Single-page HTML/CSS/JavaScript using Go `embed`; no CDN, no Node build dependencies; all API data rendered using secure DOM API.

---

## Task 1: Establish Benchmark Test Baseline

**Files:**
- Modify: `internal/api/console_test.go`

1. Add fleet filtering, observation freshness, and cluster row jump contract tests.
2. Add five-phase switch progress, one-time unlock, and no retry for indeterminate results tests.
3. Add compact topology, stable connection lines, and anomaly summary tests.
4. Add node process grouping, log filtering, and full internationalization tests.
5. Run `go test ./internal/api -run Console`, confirm new tests fail.

## Task 2: Refactor Console Shell and Overview

**Files:**
- Modify: `internal/api/console.html`

1. Unify 8px visual grid, sidebar, header, and content width.
2. Improve navigation semantics and current page state.
3. Add fleet search, health filtering, cluster row operations, and observation time.
4. Keep overview from leaking selected cluster topology.
5. Run overview-related tests.

## Task 3: Refactor Topology and Metadata Experience

**Files:**
- Modify: `internal/api/console.html`

1. Change topology to compact rounded rectangles and stable row-by-row connection structure.
2. Fix node information to three rows, VIP and preferred candidate use small badge.
3. Add anomaly summary and evidence freshness.
4. Separate immutable identity and mutable endpoint in metadata popup, add change notes.
5. Run topology, identity, and responsive tests.

## Task 4: Refactor Controlled Operation Workbench

**Files:**
- Modify: `internal/api/console.html`

1. Keep five context items and one switch button.
2. Add PRECHECK, PLAN, GATES, EXECUTE, VERIFY phase indicators.
3. Map backend status to succeeded/blocked/unsupported/indeterminate/failed.
4. Rebuild plan only for `stale_plan`; never automatically retry for indeterminate results.
5. Automatically lock back and refresh topology and operation logs upon execution completion.
6. Run operation contract tests.

## Task 5: Complete Node, Metrics, Logs, and Settings

**Files:**
- Modify: `internal/api/console.html`

1. Organize node form into four groups: action, identity, connection, and database.
2. Display task progress with phase, status, target, and report.
3. Display metrics with observation time, missing values are not faked as zero.
4. Add keyword, type, and status filtering to operation logs; retain collapsed original evidence.
5. Add memory-state refresh interval to settings, complete static and dynamic Chinese and English copy.
6. Run full page tests.

## Task 6: Regression and Delivery

**Files:**
- Modify: `docs/mysql-feature-parity-acceptance.md`
- Modify: `docs/operations.md`

1. Run `gofmt` (if Go files have changed).
2. Run `go test ./internal/api`.
3. Run `go test ./...`.
4. Run `go vet ./...`, `git diff --check`, and clean-room scan.
5. Build Linux binary and deploy to `192.168.102.152-154`.
6. Verify cluster, topology, candidate, operation, node, metrics, and logs page data sources via real API.
7. Execute one real switch and one old master re-hang on a controllable test cluster, verify successful semantics after VERIFY.
8. Update acceptance documentation and submit local branch.
