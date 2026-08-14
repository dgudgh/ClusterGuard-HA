# ClusterGuard HA — Cluster Power-Down and Automatic Recovery (Power Lifecycle Management) Test Report

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/power-lifecycle-test-report.md)
<!-- /LANGUAGE-SWITCH -->

- Production Candidate Version: `1.0.0-rc.20260809.13`
- Date: 2026-08-09
- Scope: State Machine + Storage + API + Real Agent Power-Down + Automatic Recovery + Multi-Engine + Console + Report + End-to-End Testing
- Result: **All warehouse `go test ./...`, key package race, `go build ./...`, `go vet ./...`, `git diff --check`, and lifecycle script `bash -n` passed; 152–154 real one-click power-down, control plane independent restart, three-node simultaneous restart, sudden primary failure, automatic takeover, and old primary reattachment all passed**

## Pre-Production Special Revalidation (`1.0.0-rc.20260809.13`)

### Planned Power-Down and Sudden Failure Identification

- Planned power-down is jointly identified by persistent `PowerOperation`, Recovery Freeze, and instance Maintenance; the state interface returns `planned_shutdown`, operator, request time, database state, and `automatic_failover_suppressed=true`.
- If there is no planned power-down and the primary database fails healthily, `unexpected_failure` is returned, automatic takeover remains enabled, and human-caused power-down is not misjudged as a failure, nor is a sudden failure misjudged as maintenance.
- Fixed the protection state reading of the latest failed lifecycle: as long as Freeze/Maintenance still exists, it continues to show as protected, until a subsequent completed lifecycle explicitly replaces it.
- The console displays four facts: "Power-Down Determination / Database State / Automatic Takeover / Control Plane Management"; the pop-up defaults to "Stop Database Service," clearly indicating that the ClusterGuard control plane remains running.

### Real Failure Timeline (Beijing Time)

| Time | Action | Result |
|---|---|---|
| 16:05 | Execute `mysql-ha-3306` one-click power-down (service mode) from the console | Three 3306 instances stop, VIP `.155` released; Power state is planned power-down, automatic takeover frozen |
| During power-down | Check ClusterGuard with another 3384 cluster | All three control planes are online; 3384 three instances and VIP `.160` are unaffected |
| During power-down | Send SIGTERM to `.154` control process | systemd automatically restarts, new PID takes effect, `NRestarts=1` |
| 16:08:10 | Three operating systems restart simultaneously | Approximately 10 seconds to recover 2/3 control planes, approximately 17 seconds to recover 3/3; 3306/3384, roles, replication, and VIP automatically recover |
| 16:10:12 | Bypass the platform to stop the original primary `.154` of 3306, simulate a sudden failure | Console determines `unexpected_failure`, automatic takeover not frozen |
| 16:10:51–16:11:14 | Automatic failover | `.152` becomes the only writable primary and takes over VIP `.155`; from failure to verification completion is approximately 62 seconds |
| 16:12:04 | Start the old primary `.154` after recovery | Maintain `read_only=1/super_read_only=1`, no VIP, no write contention |
| 16:15:15–16:15:29 | Page performs old primary reattachment | Operation `succeeded` and Verification passed; `.154` replicates from `.152`, IO/SQL=Yes/Yes, delay 0 |

Final facts: `.152` is the only writable 3306 primary and holds the only VIP `.155`; `.153/.154` are all read-only, replication threads are running, delay 0. Raft Leader is `.153`, 3 voters, quorum confirmed, no active operations or lifecycle tasks.

After the upgrade, a 60-second stability observation was conducted (sampled every 10 seconds, a total of 7 times): 3306 and 3384 clusters consistently maintained one primary and two replicas, healthy replicas, and zero delay. Raft commit/applied were consistent. All three `systemctl --failed` were empty, restore/finalize units were `Result=success/ExecMainStatus=0`, reconcile timer was enabled+active, control plane, Agent, and MySQL recent error-level logs were empty.

### Issues Discovered and Fixed in This Round

- In old primary reattachment, the dropdown list previously mistakenly listed healthy replicas as recovery candidates due to "historically being a primary" or missing thread fields. Now, only instances under maintenance, with health anomalies, replication threads explicitly abnormal, or historical primary with replication not yet recovered are accepted.
- After deploying `1.0.0-rc.20260809.13`, the old primary reattachment list in a healthy topology only displays "No recoverable nodes found"; the candidate primary list still correctly displays two healthy replicas.
- `clusterguard-ha.service` uses `Restart=always`, not relying on MySQL systemd unit; the management control plane can still run independently and self-recover when the database service is stopped.

### Deliverables

All three current soft links point to `/opt/clusterguard/releases/1.0.0-rc.20260809.13`. The final offline package path and SHA-256 are recorded in the external delivery list after the build is completed, avoiding self-referential summaries in the archive itself.

> This round of real testing covered "only stop database service" and "three operating systems restart simultaneously." Automatic power-on after complete power-off still depends on VMware, IPMI, BMC, or BIOS, which is not the capability of the ClusterGuard process on the powered-off node. This round did not simulate it as verified.

## Previous Production Candidate Revalidation (2026-08-09)

### Fixes

1. Before stopping the MySQL service, all nodes execute VIP release and local isolation; if any node isolation fails, the service stop phase is not entered.
2. The latest power operation is stably sorted by update time, creation time, and resource ID to avoid `/power/status` randomly returning old operations.
3. During the simultaneous boot of the three control nodes, `power/boot-detected`, `power/recovering`, topology discovery, `power/verify`, `power/complete`, and state reading of `000/500/502/503/504` perform bounded retries.
4. When the follower HTTP is ready but Raft logs are not yet caught up, the recovery script only reads `prechecking/shutdown_planned/shutting_down` and does not confirm `power_off/boot_detected/recovering/verifying` before prohibiting role changes and recovery actions; active operations, illegal states, and timeouts still fail-closed.
5. After recovery completion, a shutdown snapshot identity access control is added: the same `resource_id` in the snapshot must be recovered as a healthy primary, and all snapshot replicas must appear as healthy replica/standby roles; even if another node has become a healthy primary, the recovery freeze cannot be released prematurely.

### Real Server Results

Test nodes: `192.168.102.152`, `192.168.102.153`, `192.168.102.154`.

| Cluster | Primary | Replica | VIP | Result |
|---|---|---|---|---|
| `mysql-ha-3306` (MySQL 8.0.44) | 154:3306 | 152/153:3306 | `192.168.102.155` | Passed |
| `mysql-test-8.4` (MySQL 8.4.10) | 153:3384 | 152/154:3384 | `192.168.102.160` | Passed |

- Both clusters executed `precheck -> plan -> execute` via non-Leader 152 console sessions; execute returned HTTP 200, `power_off`, verification passed, recovery freeze active, 3 instances maintenance active.
- After power-down, all 6 MySQL services were `inactive`, two VIPs did not exist on all three nodes, and each node persisted two independent cluster snapshots.
- After the three simultaneous reboots, the boot ID changed on all three; control plane, Agent, reconcile timer, MySQL 3306, and MySQL 3384 all automatically started and were enabled.
- The first execution of restore/finalize was `Result=success`, `ExecMainStatus=0`, `NRestarts=0`; 153 encountered an HTTP 503 once within the Leader election window and successfully retried within the same oneshot, with no unit reboots or failure states on all three.
- Raft had 3 voters, Leader quorum confirmed, three nodes `state_revision=1068910`, `commit_index=applied_index=1283781`, no active operation/lifecycle task.
- 3306 primary `read_only=0/super_read_only=0`, two replicas were `1/1`, receiver/applier were ON, error code 0; 3384 also passed.
- Direct connection to VIP returned `orch-mysql03:3306:0:0` and `orch-mysql02:3384:0:0`, proving that VIP only points to the corresponding writable primary.
- Sampled every 10 seconds for 60 seconds, a total of 7 times: Raft, two three-node topologies, health status, single primary constraint, replication delay 0, power lifecycle completed, and VIP writable entry all passed.
- Three `systemctl --failed` were empty, MySQL current startup log error scan was clean.

### Deliverables

```text
/tmp/clusterguard-release-v11-final-20260809/clusterguard-ha-1.0.0-rc.20260809.11-linux-amd64.tar.gz
SHA-256: f614cfb5d9b36230d9dbb701e2151c8e2e4c669fa2b2b83b8b7941b2250871e3
```

At the time of this test, the three soft links all pointed to `/opt/clusterguard/releases/1.0.0-rc.20260809.11`; the current deployment version is based on the pre-production special revalidation at the top of the document.

> The acceptance scope is "console one-click safe shutdown of the database cluster + automatic startup and recovery after operating system restart." Automatic power-on after complete power-off is the capability of VMware/IPMI/server BIOS, not the capability of the local process on the powered-off node, and this round did not simulate it as a software capability.

---

## I. Overview of Deliverables (by Phase)

| Phase | Deliverables | Verification |
|-------|--------|------|
| 1 | PowerState state machine + PowerOperation model + protection markers (RecoveryFreeze/Maintenance) + power API framework + cgctl power subcommand | State machine testing + API testing |
| 2 | Real agent shutdown: `mysql_service_stop`/`mysql_service_start`/`mysql_power_status`/`node_poweroff` commands + LinuxPowerController + HMAC signed agent requests + four-phase real execution (persist → stop replica → stop primary → optional full power-down) | Agent + workflow + integration testing |
| 3 | (Merged into Phase 2/4) Agent power state commands + boot-detected reporting | Same as above |
| 4 | Automatic recovery: `clusterguard-cluster-restore.sh` (B4 calls power/boot-detected + power/recovering) + `clusterguard-cluster-finalize.sh` (power/verify + power/complete polling + stamp_recovered_at), protection release atomic delegation to Go side | Script `bash -n` + Go integration testing covers equivalent paths |
| 5 | Multi-engine dispatch: PostgreSQL skips persist_read_only, `postgresql_stop`/`postgresql_status`, engine-aware plan steps | Workflow + API testing |
| 6 | Web console: Read-only "Power Lifecycle" panel (status badge, protection marker, recent operation, history table), Chinese labels, does not handle approval token (design contract) | Console testing |
| 7 | Report: Durable workflow atomic disk terminal report + audit; `/api/v1/reports/{id}` JSON + HTML | Integration testing assertions (added) |
| 8 | End-to-end integration testing: 3 Go full-stack scenarios (service mode / poweroff mode / agent failure fail-closed) + lab bash script `scripts/integration/power-lifecycle-lab.sh` | `go test` + `bash -n` |

---

## II. Full Warehouse Test Results (go test ./... -count=1)

```
ok  	clusterguard.io/ha/cmd/cgctl	10.772s
ok  	clusterguard.io/ha/cmd/clusterguard	4.576s
ok  	clusterguard.io/ha/cmd/clusterguard-agent	1.160s
ok  	clusterguard.io/ha/internal/agent	3.486s
ok  	clusterguard.io/ha/internal/api	3.275s
ok  	clusterguard.io/ha/internal/approval	2.293s
ok  	clusterguard.io/ha/internal/auth	4.020s
ok  	clusterguard.io/ha/internal/config	4.776s
ok  	clusterguard.io/ha/internal/consensus	5.064s
?   	clusterguard.io/ha/internal/controlstate	[no test files]
ok  	clusterguard.io/ha/internal/coordination	3.917s
ok  	clusterguard.io/ha/internal/discovery	5.750s
ok  	clusterguard.io/ha/internal/endpoint	5.152s
ok  	clusterguard.io/ha/internal/lifecycle	5.120s
ok  	clusterguard.io/ha/internal/metrics	5.080s
ok  	clusterguard.io/ha/internal/observability	4.813s
ok  	clusterguard.io/ha/internal/recovery	4.995s
ok  	clusterguard.io/ha/internal/report	4.939s
ok  	clusterguard.io/ha/internal/runtime	5.632s
ok  	clusterguard.io/ha/internal/store	6.768s
ok  	clusterguard.io/ha/internal/workflow	5.319s
ok  	clusterguard.io/ha/pkg/adapter	5.368s
ok  	clusterguard.io/ha/pkg/identity	5.288s
ok  	clusterguard.io/ha/pkg/model	5.531s
ok  	clusterguard.io/ha/scripts	17.895s
```

Key packages (agent / workflow / api / endpoint / cgctl) have **495 test cases all passed, 0 failed**.

---

## III. Power Lifecycle Special Test Matrix

### 3.1 Agent Side (internal/agent)
| Test | Assertion |
|------|------|
| TestPowerControllerStopServiceRunsSystemctl / StartServiceRunsSystemctl / PowerOffRunsSystemctl | Exact commands (systemctl stop/start/poweroff) + stub runner |
| TestPowerControllerServiceStatusParsesIsActive / ReportsStoppedService | is-active output parsing, stopped → ServiceRunning=false |
| TestPowerControllerRejectsMissingServiceName | Unconfigured service name fail-closed |
| TestAgentPowerCommandsDispatchToController | Handle dispatches 4 new commands |
| TestAgentPowerCommandsBlockWithoutController | No controller → reject (fail-closed) |
| TestAgentPowerMutationRequiresPlanDigest | mutation (stop/start/poweroff) forces sha256 plan digest |

### 3.2 Workflow Side (internal/workflow, PowerShutdownAdapter)
| Test | Assertion |
|------|------|
| TestPowerShutdownAdapterBuildPlan / BuildPlanPostgreSQLSkipsPersist | MySQL 5 steps / PostgreSQL 4 steps (no persist_read_only, renumbered) |
| TestPowerShutdownAdapterExecuteAppliesProtectionsAndAdvancesState | Protection + state transition + nil transport degradation |
| TestPowerShutdownAdapterExecuteRunsAgentShutdown | Four-phase real execution: persist×3 → replica×2 → primary last |
| TestPowerShutdownAdapterExecuteAgentFailureKeepsProtections | Agent failure → stays in shutting_down, protection remains |
| TestPowerShutdownAdapterExecutePoweroffModePowersOffNodes | node_poweroff after all stop |
| TestPowerShutdownAdapterExecuteRejectsUnplannedState | Non-shutdown_planned rejected |
| TestPowerShutdownAdapterExecutePostgreSQLEngine | No persist_role; postgresql_stop×2 first replica then primary; engine=postgresql |
| TestPowerShutdownAdapterVerify / VerifyUsesAgentStatus / VerifyPoweroffModeAcceptsUnreachable / VerifyUsesEngineStatusCommand | Agent status verification; poweroff mode considers unreachable as passed; PG uses postgresql_status |
| TestPowerShutdownAdapterPrecheckRequiresShutdownPlanned / EngineAndCapabilities | Precondition + engine capability declaration |

### 3.3 API Side (internal/api)
| Test | Assertion |
|------|------|
| TestPowerLifecycleEndToEnd | Complete state machine flow (including cancel path) |
| TestPowerExecuteRequiresApprovalAndPlannedState | Approval + state gate |
| TestPowerFailKeepsProtectionsActive | Fail keeps protections active |
| TestPowerPrecheckUnknownCluster | Unknown cluster 404 |
| TestConsoleShowsPowerLifecyclePanel | Read-only panel contract: Power Lifecycle / power-state-badge / power-protection / power-history / powerStateLabels / loadPowerStatus(clusterId) / power/status fetch / state.powerStatus; **No approval-token (design contract TestConsoleOperationDoesNotHandleApprovalToken also passed)** |

### 3.4 End-to-End Integration Testing (internal/api/power_lifecycle_integration_test.go, added)
Real HTTP surface + recordable fake agent transport + durable workflow:

**TestPowerLifecycleIntegrationFullShutdownAndRecovery** (full service mode workflow)
- persist_role×3 → mysql_service_stop×3 (replica before primary, primary last) → no node_poweroff
- Each agent request: signature, OperationID, `sha256:` plan digest, engine=mysql
- After execute: RecoveryFreeze + 3 instances Maintenance all activated; state power_off
- boot-detected → recovering → verify → complete: protections all released, state completed
- **Phase 7 assertion (added)**: durable process disk terminal report (status=succeeded), `GET /api/v1/reports/{id}` returns non-empty audits, `/html` returns 200

**TestPowerLifecycleIntegrationPoweroffMode**
- node_poweroff×3, and each after the last mysql_service_stop
- After shutdown, mysql_power_status transport failed ("ssh: host is down") → verify considered passed
- State power_off

**TestPowerLifecycleIntegrationAgentFailureIsFailClosed**
- First replica mysql_service_stop failed → execute 409 (error)
- State stays in shutting_down; RecoveryFreeze remains; only 1 stop sent (fail-fast)
- power/fail → failed (terminal state); complete 409 rejected
- New precheck → 200 prechecking, **blocking_reasons non-empty** (survival protection reported accurately)

---

## IV. Real Defects Discovered and Fixed in Integration Testing

| # | Defect | Root Cause | Fix |
|---|------|------|------|
| 1 | durable post-execute verify always fails → execute returns 500 indeterminate | `powerFrozenInstance` requires `frozen.MetadataRevision == instance.MetadataRevision`, but `ApplyPowerProtections → SetMaintenance` bumps each instance's revision | Remove revision equality check; identity coordinates (hostname/IP/engine/port) still capture drift |
| 2 | Integration test execute 500 | Test fixture resolver did not set `OperationID` (production code `internal/workflow/resolver.go:251` sets `request.Resolved.OperationID = request.Operation.ResourceID`) | Add to resolver in `newPowerIntegrationServer` |

Both defects were exposed at the Go integration testing (non-mock unit test) level — proving the value of full-stack testing.

---

## V. Automatic Recovery Script (Phase 4)

| Script | Key Changes | Verification |
|------|----------|------|
| `scripts/clusterguard-cluster-restore.sh` | B4 calls power/boot-detected → power/recovering (tolerates 409, idempotent) → discover | `bash -n` clean |
| `scripts/clusterguard-cluster-finalize.sh` | F4 goes power/verify → power/complete polling (0=complete / 2=lifecycle failure→protection remains / 1=continue polling); stamp_recovered_at; F5 CRITICAL path retains fail-closed explanation | `bash -n` clean |
| `scripts/integration/power-lifecycle-lab.sh` | **Added**: L1–L6 end-to-end lab acceptance script (see below) | `bash -n` clean |

Design highlights:
- B3 local read_only persistence clearance retained (still robust when local node or control plane is unavailable)
- Protection release uniformly delegated to Go side `powerComplete` atomic release (no longer raw curl per node)
- verify idempotent (tolerates 200/409); complete blocks and polls until timeout, fail-closed

---

## VI. Lab Execution List (152–154)

### 6.1 Real Execution Results (2026-08-07 UTC, 152–154 ✅ executed)

Complete acceptance was performed in the real three-node lab: cluster `8785b695-40f3-4892-825d-3f784a6ca95d` (mysql-ha-3306, engine=mysql), nodes orch-mysql01/02/03 (192.168.102.152/153/154), VIP 192.168.102.155, and control-plane Leader 152 (`https://192.168.102.152:3000`, TLS plus Bearer token). The test used service mode, and all timestamps are node UTC.

### 6.2 End-to-End Acceptance (Script-Driven)

```bash
# Prerequisites: control plane on port 3000, a registered healthy cluster, and an agent on every node (agent.json includes MySQLService)
export CG_CONTROL_TOKEN='...'                # Control token; keep it in the environment and never commit it
export CG_LAB_SSH_TARGETS="root@192.168.102.152 root@192.168.102.153 root@192.168.102.154"
bash scripts/integration/power-lifecycle-lab.sh service     # Complete service-mode workflow
bash scripts/integration/power-lifecycle-lab.sh poweroff    # Full-host poweroff mode; nodes must support remote power-on
```

Script assertions (exit 0 only if all pass):
- L1 cluster healthy → L2 precheck/plan → shutdown_planned
- L3 approval + execute → power_off and recovery_freeze=true
- L4 SSH probe: service mode all nodes MySQL stopped / poweroff mode nodes unreachable
- L5 restart nodes → boot-detected → recovering → verify → complete → completed
- L6 all protections released (frozen=false, protected=false)

> Note: The complete lifecycle (L1–L6 equivalent path) in 6.1 was manually executed and passed on real 152–154; 6.2 script is a repeatable automated acceptance entry.

### 6.3 Real Full Machine Shutdown and VMware Out-of-Band Startup (2026-08-09, 152–154 ✅ executed)

This round executed `poweroff`, not a database service shutdown simulation. Before initiating a full machine shutdown on 3306, the platform first included MySQL 8.4, UPSQL 2.3, and PostgreSQL 16 on the same host in planned shutdown; then all three Linux virtual machines were shut down. The VMware host was `192.168.102.68`, and only the following three confirmed VMX were operated:

- `G:\ibfluxdb+es\influxdb02\influxdb02.vmx` (192.168.102.152)
- `G:\ibfluxdb+es\influxdb03\influxdb03.vmx` (192.168.102.153)
- `G:\ibfluxdb+es\influxdb04\orch-db03.vmx` (192.168.102.154)

**On-site Results (Asia/Shanghai)**

| Time | Verification | Result |
|------|------|------|
| 17:46–17:47 | Safety Guard, Lock, Approval, Execute, Verify, Audit, Report | workflow operation `138a0235-0c08-49ef-a023-556e09452f9d` succeeded; 12 audit stages complete; report `6479e2b7-4eda-43cb-992b-f3378ff7f382` succeeded |
| 17:47 | Delayed full machine shutdown | All three SSH, 3000, and database ports unreachable; three target VMX disappeared from `vmrun list` |
| 17:50 | VMware out-of-band startup | After confirming no running processes on the target VM, clear the three remaining `.vmx.lck`; only start the above three VMX, no other virtual machines were operated |
| 17:51:10 / 17:51:21 / 17:51:29 | System startup | 152, 153, 154 started respectively; `clusterguard-ha`, Agent, and reconcile timer all automatically recovered |
| 18:10 | Recovery closure | power operation `81f78b12-a6ee-4b84-8d85-8e8cd0d809e9` → completed; `protected=false`, `recovery_freeze=false`, maintenance empty |

**Issues discovered and fixed in this round**

1. One replica of MySQL 3306 was already caught up, but because it was not the current semi-synchronous ACK node, it showed degraded, causing the recovery verification to be stuck. Now, recovery closure is allowed only when the primary semi-synchronous confirmation count is met, replication threads are normal, delay is 0, source is consistent, and GTID is completely identical; even if GTID is missing one entry, it will still block.
2. UPSQL 2.3 (MySQL 5.7.23 branch) does not support `SET PERSIST_ONLY`. The recovery script now first independently removes runtime read-only, then decides whether to clear persistent read-only based on the server's main version; 5.7 skips persistent syntax, 8.x continues execution.
3. The shutdown state classification reuses the same strict semi-synchronous evidence, no longer misreporting safe standby ACK idle state as `unexpected_failure`.

**Database and Access Verification After Recovery**

| Cluster | Primary | Replica | VIP | Result |
|------|------|------|-----|------|
| MySQL 8.0 / 3306 | 152, writable | 153/154, read-only, IO/SQL running, lag 0 | 192.168.102.155, only 152 holds | Passed; standby ACK node remains visible degraded prompt, but shutdown classification is normal/running |
| MySQL 8.4 / 3384 | 153, writable | 152/154, read-only, IO/SQL running, lag 0 | 192.168.102.160, only 153 holds | Healthy |
| UPSQL 2.3 / 3360 | 152, writable | 153/154, read-only, IO/SQL running, lag 0 | 192.168.102.165, only 152 holds | Healthy |
| PostgreSQL 16 / 5432 | 152, primary | 153/154, streaming standby, WAL receive=replay | 192.168.102.166, only 152 holds | Healthy |

All four VIP service ports established actual TCP connections from the client. All three machines' 3000/3306/3360/3384/5432 ports are listening; systemd failed unit count is 0. Final Raft is 3 voters, quorum confirmed, mutation authority=true, ready=true.

### 6.4 Manual Spot Checks
| Item | Command |
|----|------|
| Node read_only has been persisted | `ssh 152 cat /var/lib/mysql/mysqld-auto.cnf \| grep read_only` |
| Agent command direct test | `ssh 152 "/usr/local/libexec/clusterguard-agent-stdio --config /etc/clusterguard/agent.json" <<< '{"command":"mysql_power_status",...}'` |
| Report viewing | `curl -sk -H "Authorization: Bearer $CG_CONTROL_TOKEN" https://127.0.0.1:3000/api/v1/reports | jq` → get report id → `.../reports/{id}` JSON / `/html` |

### 6.5 Fault Simulation
| Scenario | Expected |
|------|------|
| SSH disconnected during shutdown | execute 409; state shutting_down; protection remains; `power/fail` → failed; new precheck reports blocking reasons |
| Primary not ready during recovery | verify fails; complete polling does not end; protection not released (fail-closed) |

---

## VII. fail-closed Guarantee (Regression Assertion)

| Scenario | Behavior | Test |
|------|------|------|
| Agent step failure | Execute fails; stays in shutting_down; protection remains | Adapter + integration test |
| Agent not equipped with controller | Command rejected | TestAgentPowerCommandsBlockWithoutController |
| Transport not configured | Degraded to only protection marker (Phase 1 behavior) | ExecuteAppliesProtectionsAndAdvancesState |
| After power/fail | Terminal state failed; complete 409 | Integration test |
| New lifecycle after failure | precheck 200 + blocking_reasons non-empty | Integration test |
| Console | Read-only panel, does not carry approval | TestConsoleShowsPowerLifecyclePanel |

---

## VIII. Known Boundaries and Next Steps

- ✅ **Real lab acceptance has been completed (Task #19)**: The complete lifecycle (precheck → plan → approval → execute → power_off → recovery → verify → complete → completed → protection release) in service mode has passed on 152–154, including fail-closed demonstration and report generation, see 6.1.
- Phase 3 (periodic health reporting) is merged into Phase 2/4 as planned, no independent delivery.
- ✅ **Real poweroff acceptance has been completed**: Three virtual machine full machine shutdown, VMware out-of-band startup, systemd self-start, Raft recovery, four co-hosted databases recovery, VIP unique ownership, and protection release have been tested, see 6.3.
- Multi-engine adaptation currently covers MySQL + PostgreSQL; Oracle shutdown is not in the scope of this version (broker shutdown process is retained for future).
