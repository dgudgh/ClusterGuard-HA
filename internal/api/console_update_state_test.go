package api

import (
	"os/exec"
	"strings"
	"testing"
)

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
