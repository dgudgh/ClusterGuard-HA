package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/model"
)

// The test substitutes only the host's service manager and privilege switch.
// All PostgreSQL inspection, rewind, backup, HBA and replication commands are real.
type recoveryFixtureServiceRunner struct {
	f     *recoveryPGFixture
	ports map[string]int
}

type recoveryFixtureDecisions func(context.Context, ClusterPolicy) (ReconcileResponse, error)

func (f recoveryFixtureDecisions) Decision(ctx context.Context, p ClusterPolicy) (ReconcileResponse, error) {
	return f(ctx, p)
}

func (r recoveryFixtureServiceRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	if command == "/fixture/systemctl" {
		name := args[len(args)-1]
		if _, ok := r.f.policies[name]; !ok {
			return nil, fmt.Errorf("unknown fixture service")
		}
		if args[0] == "show" {
			if r.f.running[name] {
				return []byte("active"), nil
			}
			return []byte("inactive"), nil
		}
		pgArgs := []string{"-D", r.f.dirs[name], "-w", "-t", "20"}
		switch args[0] {
		case "start":
			pgArgs = append(pgArgs, "-l", filepath.Join(r.f.root, name+".log"), "-o", fmt.Sprintf("-p %d -k %s", r.ports[name], r.f.socket[name]), "start")
		case "stop":
			if !r.f.running[name] {
				return nil, nil
			}
			pgArgs = append(pgArgs, "-m", "fast", "stop")
		default:
			return nil, fmt.Errorf("unknown fixture service action")
		}
		out, err := exec.CommandContext(ctx, filepath.Join(r.f.bin, "pg_ctl"), pgArgs...).CombinedOutput()
		if err == nil {
			r.f.running[name] = args[0] == "start"
		}
		return out, err
	}
	if command == "/fixture/runuser" {
		if len(args) < 4 || args[2] != "--" {
			return nil, fmt.Errorf("bad privilege wrapper")
		}
		command, args = args[3], args[4:]
	}
	return (OSCommandRunner{}).Run(ctx, command, args...)
}

func TestRecoveryPostgreSQLActualGuardedStartAndRebuild(t *testing.T) {
	f := newRecoveryPGFixture(t)
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(secret)
	f.query("pg02", "SET password_encryption='scram-sha-256'; CREATE ROLE "+current.Username+" LOGIN SUPERUSER PASSWORD '"+password+"'; ALTER ROLE cg_fixture PASSWORD '"+password+"'")
	for _, name := range []string{"pg01", "pg03"} {
		deadline := time.Now().Add(10 * time.Second)
		for f.query(name, "SELECT count(*) FROM pg_roles WHERE rolname='"+current.Username+"'") != "1" {
			if time.Now().After(deadline) {
				t.Fatal("role did not replicate")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	ports := map[string]int{}
	var peers []PostgreSQLPeer
	passfile := filepath.Join(f.root, "recovery.pass")
	var passwordLines strings.Builder
	for _, name := range []string{"pg01", "pg02", "pg03"} {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[name] = listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		p := f.policies[name]
		p.PostgreSQLUser = current.Username
		p.PostgreSQLDatabase = "postgres"
		p.PostgreSQLReplicationUser = "cg_fixture"
		p.PostgreSQLPort = ports[name]
		p.PostgreSQLPassfile = passfile
		p.PostgreSQLService = name
		p.PostgreSQLBinaryDirectory = f.bin
		f.policies[name] = p
		peers = append(peers, PostgreSQLPeer{InstanceID: p.InstanceID, NodeID: p.PostgreSQLNodeID, IPAddress: "127.0.0.1", Port: p.PostgreSQLPort})
		fmt.Fprintf(&passwordLines, "127.0.0.1:%d:*:*:%s\n", p.PostgreSQLPort, password)
	}
	if err = os.WriteFile(passfile, []byte(passwordLines.String()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pg01", "pg03"} {
		f.stop(name, "fast")
	}
	f.query("pg02", "INSERT INTO recovery_fixture VALUES(3,'recovery-authority')")
	f.stop("pg02", "fast")
	for name, p := range f.policies {
		p.PostgreSQLPeers = peers
		f.policies[name] = p
		f.configure(name, "listen_addresses='127.0.0.1'\nport="+strconv.Itoa(ports[name]))
	}
	runner := recoveryFixtureServiceRunner{f, ports}
	c, err := NewPostgreSQLController(runner, "/fixture/systemctl", "/fixture/runuser")
	if err != nil {
		t.Fatal(err)
	}
	c.pollInterval = 50 * time.Millisecond
	members, evidence := f.evidence()
	proofs, err := disaster.CompareWAL(context.Background(), evidence, f)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := disaster.Select(members, evidence, proofs)
	if err != nil || primary != f.policies["pg02"].InstanceID {
		t.Fatalf("primary selection: %v", err)
	}
	byMember := map[model.ResourceID]model.RecoveryEvidence{}
	for _, e := range evidence {
		byMember[e.InstanceID] = e
	}
	id := model.NewResourceID()
	lease := model.NewResourceID()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := f.policies["pg02"]
	decisions := recoveryFixtureDecisions(func(ctx context.Context, policy ClusterPolicy) (ReconcileResponse, error) {
		action := ReconcileRecoveryReplica
		if policy.InstanceID == p.InstanceID {
			action = ReconcileRecoveryPrimary
		}
		return ReconcileResponse{ClusterID: policy.ClusterID, InstanceID: policy.InstanceID, RecoveryTaskID: id, LeaseID: lease, Action: action, ValidUntil: time.Now().UTC().Add(10 * time.Second)}, ctx.Err()
	})
	services := map[model.ResourceID]*Service{}
	call := func(policy ClusterPolicy, request Request) {
		t.Helper()
		service := services[policy.InstanceID]
		if service == nil {
			calls := []string{}
			var err error
			service, err = NewService(Config{SharedSecret: "agent-secret", Clusters: map[model.ResourceID]ClusterPolicy{policy.ClusterID: policy}}, &fakeVIPController{}, fakeRoleController{calls: &calls}, time.Now, WithPostgreSQLController(c), WithRecoveryDecisions(decisions), WithMutationLedger(testMutationLedger(t)))
			if err != nil {
				t.Fatal(err)
			}
			services[policy.InstanceID] = service
		}
		request.Engine = model.EnginePostgreSQL
		request.ClusterID = policy.ClusterID
		request.OperationID = model.NewResourceID()
		request.LeaseID = lease
		request.RecoveryTaskID = id
		request.RecoveryFingerprint = byMember[policy.InstanceID].Fingerprint
		request.PlanDigest = disaster.Digest(id)
		request.ExpiresAt = time.Now().UTC().Add(time.Minute)
		request = signedAgentRequest(t, request)
		for _, fingerprint := range []string{"", strings.TrimPrefix(request.RecoveryFingerprint, "sha256:"), "sha256:" + request.RecoveryFingerprint} {
			bad := request
			bad.RecoveryFingerprint = fingerprint
			bad = signedAgentRequest(t, bad)
			if _, _, ok := service.validate(bad); ok {
				t.Fatalf("accepted malformed fingerprint for %s", request.Command)
			}
		}
		response := service.Handle(ctx, request)
		if response.Status != StatusOK {
			t.Fatalf("signed %s: %s %s", request.Command, response.Message, response.Error)
		}
	}
	call(p, Request{Command: CommandRecoveryGuard})
	call(p, Request{Command: CommandRecoveryStart})
	source := PostgreSQLPeer{InstanceID: p.InstanceID, NodeID: p.PostgreSQLNodeID, IPAddress: "127.0.0.1", Port: p.PostgreSQLPort}
	for _, name := range []string{"pg01", "pg03"} {
		p := f.policies[name]
		call(p, Request{Command: CommandRecoveryRebuild, SourceInstanceID: source.InstanceID, SourceNodeID: source.NodeID, SourceIPAddress: source.IPAddress, SourceHostname: source.Hostname, SourcePort: source.Port})
		output, err := c.psql(ctx, p, "SELECT count(*) FROM recovery_fixture")
		if err != nil || strings.TrimSpace(string(output)) != "3" {
			t.Fatalf("reconstructed rows %s: %v", name, err)
		}
		output, err = c.psql(ctx, p, "SELECT current_setting('clusterguard.node_id')")
		if err != nil || strings.TrimSpace(string(output)) != string(p.PostgreSQLNodeID) {
			t.Fatal("replica identity changed")
		}
	}
	if err = c.RecoveryGuardVerify(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	output, err := c.psql(ctx, p, "SELECT count(*) FROM pg_stat_replication WHERE state='streaming'")
	if err != nil || strings.TrimSpace(string(output)) != "2" {
		t.Fatalf("two streaming links not verified: %v", err)
	}
	output, err = c.psql(ctx, p, "SELECT count(*) FROM pg_replication_slots s JOIN pg_stat_replication r ON r.pid=s.active_pid WHERE s.slot_type='physical' AND s.active AND s.slot_name='cg_' || replace(r.application_name,'-','')")
	if err != nil || strings.TrimSpace(string(output)) != "2" {
		t.Fatalf("two native-identity-bound active slots not verified: %v", err)
	}
	if err = c.RecoveryGuardRelease(ctx, p, id); err != nil {
		t.Fatal(err)
	}
	t.Log("real PostgreSQL selection, guarded startup, retained-data rebuild and two streaming replicas passed")
}
