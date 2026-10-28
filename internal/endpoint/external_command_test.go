package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type externalOwnerCommit struct {
	clusterID    model.ResourceID
	haEndpointID model.ResourceID
	ownerID      model.ResourceID
	active       bool
}

type externalEndpointInventory struct {
	resources  []model.HAEndpoint
	endpoints  map[model.ResourceID]model.Endpoint
	commits    []externalOwnerCommit
	failCommit bool
}

func (inventory *externalEndpointInventory) HAEndpoints(clusterID model.ResourceID) []model.HAEndpoint {
	matched := make([]model.HAEndpoint, 0, len(inventory.resources))
	for _, resource := range inventory.resources {
		if resource.ClusterID == clusterID {
			matched = append(matched, resource)
		}
	}
	return matched
}

func (inventory *externalEndpointInventory) Endpoint(id model.ResourceID) (model.Endpoint, bool) {
	endpoint, exists := inventory.endpoints[id]
	return endpoint, exists
}

func (inventory *externalEndpointInventory) CommitHAEndpointOwner(clusterID model.ResourceID, haEndpointID model.ResourceID, ownerID model.ResourceID, active bool) error {
	if inventory.failCommit {
		return errors.New("cluster metadata store is unavailable")
	}
	inventory.commits = append(inventory.commits, externalOwnerCommit{clusterID: clusterID, haEndpointID: haEndpointID, ownerID: ownerID, active: active})
	// A resource points at its endpoint through EndpointID; the resource id and
	// the endpoint id are deliberately different values.
	var endpointID model.ResourceID
	for index := range inventory.resources {
		if inventory.resources[index].ResourceID == haEndpointID {
			inventory.resources[index].OwnerID = ownerID
			endpointID = inventory.resources[index].EndpointID
		}
	}
	if endpointID != "" {
		endpoint, exists := inventory.endpoints[endpointID]
		if exists {
			endpoint.InstanceID = ownerID
			inventory.endpoints[endpointID] = endpoint
		}
	}
	return nil
}

type externalRunnerCall struct {
	name    string
	args    []string
	payload []byte
}

type externalEndpointRunner struct {
	outputs []string
	errs    []error
	calls   []externalRunnerCall
}

func (runner *externalEndpointRunner) Run(ctx context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	runner.calls = append(runner.calls, externalRunnerCall{name: name, args: append([]string{}, arguments...), payload: append([]byte{}, input...)})
	index := len(runner.calls) - 1
	if index < len(runner.errs) && runner.errs[index] != nil {
		return nil, runner.errs[index]
	}
	if index < len(runner.outputs) {
		return []byte(runner.outputs[index]), nil
	}
	return []byte(observationPayload(true, "")), nil
}

// Resource identifiers are UUIDs in this system, so the fixtures use that shape
// rather than readable placeholders that would be rejected on shape alone and
// never exercise the real logic.
const (
	externalCommandClusterID    = model.ResourceID("11111111-1111-4111-8111-111111111111")
	externalCommandHAEndpointID = model.ResourceID("22222222-2222-4222-8222-222222222222")
	externalCommandSecondHAID   = model.ResourceID("77777777-7777-4777-8777-777777777777")
	externalCommandEndpointID   = model.ResourceID("33333333-3333-4333-8333-333333333333")
	externalCommandPrimaryID    = model.ResourceID("44444444-4444-4444-8444-444444444444")
	externalCommandTargetID     = model.ResourceID("55555555-5555-4555-8555-555555555555")
	externalCommandOperationID  = model.ResourceID("66666666-6666-4666-8666-666666666666")
	externalCommandStaleID      = model.ResourceID("99999999-9999-4999-8999-999999999999")
)

func observationPayload(complete bool, message string, owners ...model.ResourceID) string {
	encoded, err := json.Marshal(externalCommandObservation{OwnerIDs: owners, Complete: complete, Message: message})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func externalCommandFixture(hookPath string) (*externalEndpointInventory, *externalEndpointRunner, adapter.ResolvedOperation) {
	inventory := &externalEndpointInventory{
		resources: []model.HAEndpoint{{
			ResourceMeta: model.ResourceMeta{ResourceID: externalCommandHAEndpointID, MetadataRevision: 7},
			ClusterID:    externalCommandClusterID,
			EndpointID:   externalCommandEndpointID,
			Kind:         model.EndpointService,
			DesiredRole:  model.RolePrimary,
			Provider:     model.EndpointProviderExternalCommand,
			ProviderRef:  hookPath,
			Healthy:      true,
		}},
		endpoints: map[model.ResourceID]model.Endpoint{
			externalCommandEndpointID: {
				ResourceMeta: model.ResourceMeta{ResourceID: externalCommandEndpointID, MetadataRevision: 3},
				ClusterID:    externalCommandClusterID,
				Kind:         model.EndpointService,
				Hostname:     "10.0.0.40",
				Port:         3306,
				Active:       true,
			},
		},
	}
	runner := &externalEndpointRunner{}
	resolved := adapter.ResolvedOperation{
		OperationID: externalCommandOperationID,
		Cluster:     model.DatabaseCluster{ResourceMeta: model.ResourceMeta{ResourceID: externalCommandClusterID}},
		Primary: model.DatabaseInstance{
			ResourceMeta: model.ResourceMeta{ResourceID: externalCommandPrimaryID},
			ClusterID:    externalCommandClusterID,
			Hostname:     "db-a",
		},
		Target: model.DatabaseInstance{
			ResourceMeta: model.ResourceMeta{ResourceID: externalCommandTargetID},
			ClusterID:    externalCommandClusterID,
			Hostname:     "db-b",
		},
	}
	return inventory, runner, resolved
}

func writeHookFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "cluster-endpoint-hook")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write hook fixture: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod hook fixture: %v", err)
	}
	return path
}

// umask would quietly strip the dangerous bits off anything written with
// os.WriteFile, leaving the test asserting against a file that was never risky.
func writePermissiveHook(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "permissive-hook")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write permissive fixture: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod permissive fixture: %v", err)
	}
	applied, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat permissive fixture: %v", err)
	}
	if applied.Mode().Perm()&0o022 == 0 {
		t.Fatalf("fixture setup failed: %s is not actually group- or world-writable (%v)", path, applied.Mode().Perm())
	}
	return path
}

func TestExternalCommandResourceSelectionRequiresExactlyOne(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	for _, count := range []int{0, 2} {
		inventory, _, resolved := externalCommandFixture(hook)
		if count == 0 {
			inventory.resources = nil
		} else {
			duplicate := inventory.resources[0]
			duplicate.ResourceID = externalCommandSecondHAID
			inventory.resources = append(inventory.resources, duplicate)
		}
		provider := NewExternalCommandProvider(inventory, &externalEndpointRunner{}, 0, 0)
		if _, err := provider.resource(resolved.Cluster.ResourceID); err == nil {
			t.Fatalf("selecting %d endpoint resources must fail", count)
		}
	}
}

// A resource owned by another provider may describe the same address, but this
// provider must never act on it - doing so would let a VIP endpoint be mutated
// by a different provider's rules.
func TestExternalCommandResourceSelectionIgnoresForeignProviders(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, _, resolved := externalCommandFixture(hook)
	provider := NewExternalCommandProvider(inventory, &externalEndpointRunner{}, 0, 0)

	inventory.resources[0].Provider = model.EndpointProviderLinuxVIP
	if _, err := provider.resource(resolved.Cluster.ResourceID); err == nil {
		t.Fatal("a VIP-owned resource must not be selected by the external command provider")
	}

	inventory.resources[0].Provider = model.EndpointProviderExternalCommand
	inventory.endpoints[externalCommandEndpointID] = model.Endpoint{
		ResourceMeta: model.ResourceMeta{ResourceID: externalCommandEndpointID},
		ClusterID:    externalCommandClusterID,
		Kind:         model.EndpointVIP,
		Active:       true,
	}
	if _, err := provider.resource(resolved.Cluster.ResourceID); err == nil {
		t.Fatal("a VIP-kind endpoint must not be selected by the external command provider")
	}
}

// The hook runs with control-plane authority, so the paths accepted here decide
// who can execute code as the cluster manager.
func TestExternalCommandHookRejectsUnsafePaths(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	directory := t.TempDir()
	link := filepath.Join(directory, "linked-hook")
	if err := os.Symlink(hook, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	// Each refusal is pinned by the rule that should produce it. A symlink would
	// also be caught by the regular-file rule, so without naming the expected
	// reason removing the symlink check would go unnoticed.
	cases := []struct {
		name       string
		reference  string
		currentUID int
		want       string
	}{
		{"empty", "   ", os.Getuid(), "does not name a hook executable"},
		{"relative path", "scripts/hook.sh", os.Getuid(), "absolute path"},
		{"missing file", filepath.Join(directory, "not-present.sh"), os.Getuid(), "not readable"},
		{"symbolic link", link, os.Getuid(), "symbolic link"},
		{"directory", directory, os.Getuid(), "regular file"},
		{"group writable", writePermissiveHook(t, 0o775), os.Getuid(), "group- or world-writable"},
		{"world writable", writePermissiveHook(t, 0o777), os.Getuid(), "group- or world-writable"},
		{"foreign owner", hook, 424242, "owned by root"},
	}
	for _, testCase := range cases {
		_, err := validateHookFile(testCase.reference, testCase.currentUID)
		if err == nil {
			t.Fatalf("hook reference %q (%s) must be rejected", testCase.reference, testCase.name)
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Fatalf("%s must be refused by the %q rule, got %q", testCase.name, testCase.want, err)
		}
	}
	if _, err := validateHookFile(hook, os.Getuid()); err != nil {
		t.Fatalf("a root-or-self owned 0755 regular file must be accepted: %v", err)
	}
}

// The hook is told where traffic should go, not who to trust. Leaking engine
// credentials to it would turn endpoint configuration into credential theft.
func TestExternalCommandPayloadExcludesCredentials(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	resolved.Credentials = adapter.Credentials{Username: "haadmin", Password: "super-secret-password", Database: "production"}
	resolved.ReplicationCredentials = adapter.Credentials{Username: "repl", Password: "repl-secret", Database: "production"}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)

	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckPass {
		t.Fatalf("precheck should pass, got %+v", checks)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("precheck must invoke the hook exactly once, got %d", len(runner.calls))
	}
	payload := string(runner.calls[0].payload)
	for _, secret := range []string{"super-secret-password", "repl-secret", "haadmin", "production"} {
		if strings.Contains(payload, secret) {
			t.Fatalf("hook payload must not carry %q: %s", secret, payload)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(runner.calls[0].payload, &decoded); err != nil {
		t.Fatalf("hook payload must be JSON: %v", err)
	}
	if _, exists := decoded["credentials"]; exists {
		t.Fatal("hook payload must not contain a credentials field")
	}
	if decoded["target_instance_id"] != string(externalCommandTargetID) {
		t.Fatalf("hook payload must name the transition target, got %v", decoded["target_instance_id"])
	}
}

// No cluster field may reach a command line. Everything travels as one argv
// entry plus a JSON body on stdin, so a hook reference containing shell syntax
// cannot grow a second command.
func TestExternalCommandStageIsASingleArgument(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	inventory.resources[0].ProviderRef = hook + "; rm -rf /tmp/clusterguard-never-created"
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)

	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("a hook reference containing shell syntax must fail closed, got %+v", checks)
	}
	if len(runner.calls) != 0 {
		t.Fatal("no hook may run before the hook reference itself is validated")
	}

	inventory.resources[0].ProviderRef = hook
	// Verification compares the hook's answer against the recorded owner, so the
	// fixture has to describe an endpoint already converged on the target.
	inventory.resources[0].OwnerID = externalCommandTargetID
	converged := inventory.endpoints[externalCommandEndpointID]
	converged.InstanceID = externalCommandTargetID
	inventory.endpoints[externalCommandEndpointID] = converged
	runner.outputs = []string{observationPayload(true, "", externalCommandTargetID)}
	if check := provider.Verify(context.Background(), resolved); check.Status != model.CheckPass {
		t.Fatalf("verify should pass, got %+v", check)
	}
	call := runner.calls[0]
	if call.name != hook {
		t.Fatalf("hook must be executed by its own path, got %q", call.name)
	}
	if len(call.args) != 1 || call.args[0] != string(stageVerify) {
		t.Fatalf("stage must arrive as exactly one argument, got %v", call.args)
	}
}

func TestExternalCommandPrecheckFailsClosed(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.errs = []error{errors.New("azure probe unreachable")}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	checks := provider.Precheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("a failing hook must produce a failing check, got %+v", checks)
	}
	if !strings.Contains(checks[0].Message, "azure probe unreachable") {
		t.Fatalf("the check must carry the hook's failure reason, got %q", checks[0].Message)
	}
}

// A hook that cannot see the whole data plane has not proven anybody is absent.
// Acting anyway is how two instances end up accepting writes.
func TestExternalCommandTransferRefusesIncompleteObservation(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{observationPayload(false, "only 2 of 3 nodes answered")}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	err := provider.Transfer(context.Background(), resolved)
	if err == nil {
		t.Fatal("incomplete ownership coverage must refuse the transfer")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("refusal must name coverage, got %v", err)
	}
	if len(inventory.commits) != 0 {
		t.Fatal("no ownership metadata may be committed after a refused transfer")
	}
}

func TestExternalCommandTransferRefusesForeignOwner(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	for _, owners := range [][]model.ResourceID{
		{externalCommandPrimaryID},
		{externalCommandPrimaryID, externalCommandTargetID},
	} {
		inventory, runner, resolved := externalCommandFixture(hook)
		runner.outputs = []string{observationPayload(true, "", owners...)}
		provider := NewExternalCommandProvider(inventory, runner, 0, 0)
		if err := provider.Transfer(context.Background(), resolved); err == nil {
			t.Fatalf("transfer must refuse when ownership is %v", owners)
		}
		if len(inventory.commits) != 0 {
			t.Fatal("ownership metadata must not move while the data plane is held elsewhere")
		}
	}
}

// The target already owning the endpoint is a valid state, not a failure, and
// must not trigger a redundant move.
func TestExternalCommandTransferSkipsMoveWhenTargetOwns(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	owning := observationPayload(true, "", externalCommandTargetID)
	runner.outputs = []string{owning, owning}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("idempotent transfer must succeed: %v", err)
	}
	for _, call := range runner.calls {
		if len(call.args) == 1 && call.args[0] == string(stageTransfer) {
			t.Fatal("transfer must not move an endpoint the target already owns")
		}
	}
	if len(inventory.commits) != 1 || inventory.commits[0].ownerID != externalCommandTargetID {
		t.Fatalf("ownership metadata must still be committed, got %+v", inventory.commits)
	}
}

func TestExternalCommandTransferMovesAndConvergesMetadata(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	// The sequence is observe, transfer, observe, so the hook answers three
	// times: nothing owns it, then the transfer acknowledgement, then the target.
	runner.outputs = []string{
		observationPayload(true, ""),
		observationPayload(true, ""),
		observationPayload(true, "", externalCommandTargetID),
	}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	if err := provider.Transfer(context.Background(), resolved); err != nil {
		t.Fatalf("transfer must succeed: %v", err)
	}
	stages := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		stages = append(stages, call.args[0])
	}
	if len(stages) != 3 || stages[0] != string(stageObserve) || stages[1] != string(stageTransfer) || stages[2] != string(stageObserve) {
		t.Fatalf("expected observe-transfer-observe, got %v", stages)
	}
	if len(inventory.commits) != 1 || inventory.commits[0].ownerID != externalCommandTargetID || !inventory.commits[0].active {
		t.Fatalf("ownership metadata must converge on the target, got %+v", inventory.commits)
	}
}

// Physical ownership moved but the record did not. Reporting success here would
// leave the next failover reasoning about an owner nobody else can see.
func TestExternalCommandTransferFailsWhenMetadataDoesNotConverge(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{
		observationPayload(true, ""),
		observationPayload(true, "", externalCommandTargetID),
	}
	inventory.failCommit = true
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	if err := provider.Transfer(context.Background(), resolved); err == nil {
		t.Fatal("a metadata commit failure must fail the transfer")
	}
}

func TestExternalCommandVerifyDetectsStaleMetadata(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{observationPayload(true, "", externalCommandTargetID)}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	inventory.resources[0].OwnerID = externalCommandStaleID
	check := provider.Verify(context.Background(), resolved)
	if check.Status != model.CheckFail || !strings.Contains(check.Message, "stale") {
		t.Fatalf("stale metadata must fail verification, got %+v", check)
	}
}

func TestExternalCommandFormerPrimaryPrecheckRejectsRetainedOwnership(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{observationPayload(true, "", externalCommandPrimaryID)}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	checks := provider.FormerPrimaryPrecheck(context.Background(), resolved)
	if len(checks) != 1 || checks[0].Status != model.CheckFail {
		t.Fatalf("a former primary still holding the endpoint must fail, got %+v", checks)
	}
	if !strings.Contains(checks[0].Message, "former primary") {
		t.Fatalf("the refusal must name the former primary, got %q", checks[0].Message)
	}
}

// The second observation is the one that proves the move actually landed on the
// intended instance. Without pinning it here, the later metadata re-read masks a
// removed check: the transfer still fails, but for the wrong reason.
func TestExternalCommandTransferRefusesWrongOwnerAfterMove(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{
		observationPayload(true, ""),
		observationPayload(true, ""),
		observationPayload(true, "", externalCommandPrimaryID),
	}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	err := provider.Transfer(context.Background(), resolved)
	if err == nil {
		t.Fatal("a move that lands ownership back on the former primary must fail")
	}
	if !strings.Contains(err.Error(), "converge") {
		t.Fatalf("refusal must name the convergence failure, got %v", err)
	}
	if len(inventory.commits) != 0 {
		t.Fatal("ownership metadata must not move while the wrong instance owns the endpoint")
	}
}

func TestExternalCommandRunHookEnforcesOutputLimit(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, _, resolved := externalCommandFixture(hook)
	resource, err := NewExternalCommandProvider(inventory, &externalEndpointRunner{}, 0, 0).resource(resolved.Cluster.ResourceID)
	if err != nil {
		t.Fatalf("resource lookup: %v", err)
	}
	request := NewExternalCommandProvider(inventory, &externalEndpointRunner{}, 0, 0).request(stagePrecheck, resolved, resource)

	quiet := NewExternalCommandProvider(inventory, &externalEndpointRunner{outputs: []string{"{\"complete\":true}"}}, 0, 64)
	if _, err := quiet.runHook(context.Background(), hook, stagePrecheck, request); err != nil {
		t.Fatalf("a small hook reply must be accepted: %v", err)
	}
	noisy := NewExternalCommandProvider(inventory, &externalEndpointRunner{outputs: []string{strings.Repeat("x", 512)}}, 0, 64)
	_, err = noisy.runHook(context.Background(), hook, stagePrecheck, request)
	if err == nil {
		t.Fatal("oversized hook output must be refused outright")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("the refusal must name the output limit, got %v", err)
	}
}

// An untrusted hook must not be able to exhaust the control plane's memory by
// streaming output back.
func TestExternalCommandRejectsOversizedOutput(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{observationPayload(true, strings.Repeat("x", 512))}
	provider := NewExternalCommandProvider(inventory, runner, 0, 64)
	if err := provider.Transfer(context.Background(), resolved); err == nil {
		t.Fatal("oversized hook output must fail rather than be truncated silently")
	}
}

func TestExternalCommandObserveOwnershipReportsCoverage(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, runner, resolved := externalCommandFixture(hook)
	runner.outputs = []string{observationPayload(true, "", externalCommandTargetID)}
	provider := NewExternalCommandProvider(inventory, runner, 0, 0)
	observation, err := provider.ObserveOwnership(context.Background(), resolved.Cluster, model.TopologySnapshot{
		Instances: []model.DatabaseInstance{resolved.Primary, resolved.Target},
	})
	if err != nil {
		t.Fatalf("observe must succeed: %v", err)
	}
	if !observation.Complete {
		t.Fatal("coverage must be reported from the hook")
	}
	if len(observation.OwnerIDs) != 1 || observation.OwnerIDs[0] != externalCommandTargetID {
		t.Fatalf("observed owners must come from the hook, got %v", observation.OwnerIDs)
	}
	if len(observation.ObservedInstanceIDs) != 2 {
		t.Fatalf("observation must list the instances it covered, got %v", observation.ObservedInstanceIDs)
	}
}

func TestExternalCommandDefaultsApplied(t *testing.T) {
	provider := NewExternalCommandProvider(&externalEndpointInventory{}, &externalEndpointRunner{}, 0, 0)
	if provider.timeout != defaultExternalCommandTimeout {
		t.Fatalf("a zero timeout must fall back to %v, got %v", defaultExternalCommandTimeout, provider.timeout)
	}
	if provider.maxOutput != defaultExternalCommandMaxOutput {
		t.Fatalf("a zero output limit must fall back to %d, got %d", defaultExternalCommandMaxOutput, provider.maxOutput)
	}
	if !provider.Executable(context.Background()) {
		t.Fatal("a configured provider must be executable")
	}
	if (&ExternalCommandProvider{}).Executable(context.Background()) {
		t.Fatal("an unconfigured provider must not be executable")
	}
}

// A slow hook must not hang a failover indefinitely; the deadline has to reach
// the process rather than only being checked afterwards.
func TestExternalCommandBoundsHookDuration(t *testing.T) {
	hook := writeHookFile(t, 0o755)
	inventory, _, resolved := externalCommandFixture(hook)
	lookup := NewExternalCommandProvider(inventory, &externalEndpointRunner{}, 25*time.Millisecond, 0)
	resource, err := lookup.resource(resolved.Cluster.ResourceID)
	if err != nil {
		t.Fatalf("resource lookup: %v", err)
	}
	provider := NewExternalCommandProvider(inventory, blockingRunner{}, 25*time.Millisecond, 0)

	done := make(chan error, 1)
	go func() {
		_, doneErr := provider.runHook(context.Background(), hook, stagePrecheck, provider.request(stagePrecheck, resolved, resource))
		done <- doneErr
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hook that outruns the deadline must fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not return within twice its timeout")
	}
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
