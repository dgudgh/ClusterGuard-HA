# Console Cluster Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add safe cluster registration and retirement to the authenticated ClusterGuard HA console.

**Architecture:** Reuse the existing atomic cluster registration API. Add one repository retirement transaction and expose it through the existing authenticated, CSRF-protected, quorum-leader mutation router. Add a single toolbar modal that calls these APIs and refreshes console state.

**Tech Stack:** Go 1.19+, `net/http`, replicated JSON metadata repository, embedded HTML/CSS/JavaScript console.

## Global Constraints

- Cluster retirement never contacts database hosts or deletes database data.
- Active operations, operation locks, ownership leases, and lifecycle tasks block retirement.
- Audit, report, operation, and terminal lifecycle history must remain stored.
- Browser mutations require administrator session authorization and CSRF validation.
- Use the existing `/api/v1` namespace and ClusterGuard HA naming only.

---

### Task 1: Atomic Cluster Retirement

**Files:**
- Create: `internal/store/clusters.go`
- Test: `internal/store/clusters_test.go`

**Interfaces:**
- Produces: `Repository.RetireCluster(clusterID model.ResourceID, confirmDisplayName, actor string) (ClusterRetirement, error)`.
- Produces: `ClusterRetirement` with the retired cluster and removed-resource counts.

- [ ] **Step 1: Write failing store tests**

Add tests proving exact-name confirmation, unknown-cluster handling, blocking for
running operations/locks/leases/lifecycle tasks, atomic removal of live
inventory, retention of historical records, retirement audit creation, and
persistence across repository reopen.

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/store -run 'TestRetireCluster' -count=1`

Expected: build failure because `RetireCluster` does not exist.

- [ ] **Step 3: Implement the repository transaction**

Implement validation and one cloned-snapshot commit. Remove only active cluster
inventory maps, retain historical maps, and append a sanitized audit event.

- [ ] **Step 4: Run focused store tests**

Run: `go test ./internal/store -run 'TestRetireCluster' -count=1`

Expected: PASS.

### Task 2: Authenticated Retirement API

**Files:**
- Modify: `internal/api/clusters.go`
- Modify: `internal/api/clusters_test.go`
- Modify: `internal/api/auth_test.go`

**Interfaces:**
- Consumes: `Repository.RetireCluster`.
- Produces: `DELETE /api/v1/clusters/{resource_id}` with body
  `{"confirm_display_name":"<name>"}`.

- [ ] **Step 1: Write failing API tests**

Cover 200 success, 400 malformed confirmation, 404 unknown cluster, 409 active
work, viewer/operator denial, administrator success, and leader-gate behavior.

- [ ] **Step 2: Run focused API tests and confirm failure**

Run: `go test ./internal/api -run 'Test.*Cluster.*(Retire|Delete)' -count=1`

Expected: FAIL with method not allowed or missing handler.

- [ ] **Step 3: Implement DELETE dispatch and response mapping**

Decode a bounded JSON body, derive the authenticated actor, call the store, and
map validation/not-found/conflict/persistence errors without exposing internal
paths or secrets.

- [ ] **Step 4: Run focused API tests**

Run: `go test ./internal/api -run 'Test.*Cluster.*(Retire|Delete)' -count=1`

Expected: PASS.

### Task 3: Cluster Management Modal

**Files:**
- Modify: `internal/api/console.html`
- Modify: `internal/api/console_test.go`

**Interfaces:**
- Consumes: `POST /api/v1/clusters`, `POST /api/v1/clusters/{id}/discover`, and
  `DELETE /api/v1/clusters/{id}`.
- Produces: toolbar button `open-cluster-management-modal` and modal
  `cluster-management-modal`.

- [ ] **Step 1: Write failing console contract tests**

Assert the toolbar entry, dialog controls, dynamic endpoint rows, exact-name
retirement confirmation, API calls, admin-only controls, selected-cluster
refresh, and responsive sticky action footer.

- [ ] **Step 2: Run console tests and confirm failure**

Run: `go test ./internal/api -run 'TestConsoleClusterManagement' -count=1`

Expected: FAIL because the modal markup and JavaScript do not exist.

- [ ] **Step 3: Implement responsive modal and actions**

Use the existing dialog and button system. Keep registration and retirement in
two clear sections, avoid nested cards, display an explicit non-destructive
scope warning, and disable retirement until confirmation matches.

- [ ] **Step 4: Run console tests**

Run: `go test ./internal/api -run 'TestConsoleClusterManagement' -count=1`

Expected: PASS.

### Task 4: Verification And Deployment

**Files:**
- Modify: `docs/operations.md`

**Interfaces:**
- Documents the cluster registration and retirement behavior and operator
  safety boundary.

- [ ] **Step 1: Run full verification**

Run: `gofmt -w internal/store/clusters.go internal/store/clusters_test.go internal/api/clusters.go internal/api/clusters_test.go internal/api/auth_test.go`

Run: `go test ./...`

Run: `go build ./...`

Run: `git diff --check`

Expected: all commands succeed.

- [ ] **Step 2: Deploy to the three test controllers**

Build the Linux binary, deploy followers before the current leader, restart
`clusterguard-ha.service`, and verify all three nodes are active with one Raft
leader and two followers.

- [ ] **Step 3: Perform browser acceptance**

Verify administrator registration, automatic discovery, blocked retirement
with active work, exact-name confirmation, successful retirement of a temporary
test cluster, cluster-selector refresh, and small-screen dialog usability.
