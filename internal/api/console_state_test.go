package api

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConsoleClusterLoadingKeepsSelectionAndFailsClosed(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="cluster-load-notice"`, `id="retry-cluster-load"`,
		"if (!sameCluster) clearClusterView(preserveOperationResult);",
		"byId('topology-risk').textContent = '未知';",
		"byId('topology-connector').hidden = true;",
		"canOperateClusters() && state.clusterDataReady && !state.clusterLoading && !state.clusterLoadError",
		"capabilityAvailable('execute') && cluster && cluster.resource_id === state.selectedClusterId",
		"if (!ordinaryOperationAllowed(kind, targetID)) return null;",
		"const ordinaryOperationAllowed = (kind, targetID) => !state.operationRunning && state.switchUnlocked",
		"const timer = window.setTimeout(() => controller.abort(), 15000);",
		"if (clusterId !== state.selectedClusterId) return;",
		"commitIfCurrent(generation, () => {\n        state.powerStatus = powerStatus || null;",
		"renderClusterLoadStatus('error'", "当前显示上次观测，不代表最新状态",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing selected-cluster loading contract %q", contract)
		}
	}
}

func TestConsoleClassicThemePreservesLayoutFixes(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"--canvas:#f5f5f7;", "--accent:#0071e3;", "--danger:#d70015;",
		"@media (prefers-reduced-motion:reduce)",
		"grid-auto-rows:max-content;", "letter-spacing:0;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing classic theme contract %q", contract)
		}
	}
	for _, removed := range []string{"--paper-grain", "--display-font", "paper-reveal"} {
		if strings.Contains(page, removed) {
			t.Fatalf("discarded paper theme must not remain: %q", removed)
		}
	}
}

func TestConsoleFleetToolbarKeepsAccessibleFiltersWithoutInstructionClutter(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`class="panel-header fleet-toolbar"`, `class="fleet-meta" aria-live="polite"`,
		`role="search" aria-label="筛选集群"`, `for="fleet-search"`, `for="fleet-health-filter"`,
		`class="visually-hidden">筛选集群`, `class="visually-hidden">健康状态`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing accessible fleet toolbar contract %q", contract)
		}
	}
	if strings.Contains(page, "异常优先 · 点击集群进入拓扑") {
		t.Fatal("fleet toolbar must not repeat navigation instructions below its title")
	}
}

func TestConsoleUpdateStateUsesLatestExecutionAndLiveMaintenance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the console state regression")
	}
	page := string(consoleHTML)
	start := strings.Index(page, "    const softwareUpdateJobTime =")
	end := strings.Index(page, "    const softwareUpdateProgressKey =")
	if start < 0 || end <= start {
		t.Fatal("console update state helpers were not found")
	}
	script := `const assert = require('node:assert/strict');
const state = {softwareUpdates:{packages:[]},controlPlane:{update_maintenance_active:false}};
` + page[start:end] + `
const item = (id,status,time,maintenance,mode='execute') => ({package:{patch_id:id,uploaded_at:time},job:{status,updated_at:time,maintenance_active:maintenance,mode}});
const old = item('old','failed','2026-09-07T07:38:47Z',true);
const done = item('new','succeeded','2026-09-07T09:49:11Z',false);
state.softwareUpdates.packages = [done,old];
assert.equal(activeSoftwareUpdate(),null,'completed upgrade must not revert to old failure');
state.controlPlane.update_maintenance_active = true;
assert.equal(activeSoftwareUpdate(),null,'unreleased live gate must be presented as orphaned, not assigned to an old failure');
state.softwareUpdates.packages = [{package:{patch_id:'upload'}},old];
assert.equal(activeSoftwareUpdate(),old,'an upload must not hide an unreleased failed execution');
state.controlPlane.update_maintenance_active = false;
assert.equal(activeSoftwareUpdate(),null,'live released gate overrides historical maintenance flag');
state.controlPlane = null;
assert.equal(activeSoftwareUpdate(),old,'unknown live state must preserve failure protection');
const queued = item('queued','queued','2026-09-07T10:00:00Z',false);
state.controlPlane = {update_maintenance_active:false};
state.softwareUpdates.packages = [old,queued];
assert.equal(activeSoftwareUpdate(),queued,'queued execution remains active before marker acquisition');
const staleRunning = item('stale','running','2026-09-07T06:10:48Z',true);
state.softwareUpdates.packages = [staleRunning,done,old];
assert.equal(activeSoftwareUpdate(),null,'newer success supersedes stale running history after Leader change');
assert.equal(latestSoftwareUpdateJob(true),done,'old running history must not disable upload');
const plan = item('plan','running','2026-09-07T10:01:00Z',false,'plan');
state.softwareUpdates.packages = [plan,done,old];
assert.equal(activeSoftwareUpdate(),null,'read-only plan does not pause automatic failover');
assert.equal(latestSoftwareUpdateJob(true),plan,'running plan must still prevent duplicate submission');
queued.job.verification_required = true;
state.softwareUpdates.packages = [queued,old];
assert.equal(activeSoftwareUpdate(),null,'unreadable job must not be rendered as a verified execution');
console.log('console update state regressions passed');
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("console update state regression failed: %v\n%s", err, output)
	}
}
