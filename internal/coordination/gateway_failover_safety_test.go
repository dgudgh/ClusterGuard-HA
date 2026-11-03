package coordination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

func TestGatewayFailoverRequiresDirectDatabaseIsolation(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		for _, scenario := range []string{"stopped", "unreachable", "no-majority", "unsafe-role", "invalid-service"} {
			t.Run(string(engine)+"/"+scenario, func(t *testing.T) {
				r, inv, window, now := failoverSafetyFixture(t)
				r.Cluster.Engine = engine
				r.Primary.Engine = engine
				r.Target.Engine = engine
				r.PlanDigest = "sha256:" + strings.Repeat("a", 64)
				inv.haEndpoint.Provider = model.EndpointProviderGateway
				inv.haEndpoint.Kind = model.EndpointService
				inv.haEndpoint.ProviderRef = "trusted-profile"
				inv.haEndpoint.Interface = ""
				inv.haEndpoint.Prefix = 0
				inv.endpoint.Kind = model.EndpointService
				inv.endpoint.Hostname = "database-entry.example"
				inv.endpoint.Port = 3306
				if scenario == "invalid-service" {
					inv.endpoint.Kind = model.EndpointVIP
				}
				recordStableFailure(window, r.Cluster.ResourceID, now)
				calls := []string{}
				lease := &failoverLeaseStub{calls: &calls, lease: endpoint.Lease{ResourceID: model.NewResourceID(), Active: true, ExpiresAt: now.Add(time.Minute)}}
				tr := &failoverAgentTransportStub{calls: &calls, durableEvidence: true, restartReadOnly: true}
				if scenario == "unreachable" {
					tr.err = errors.New("Agent unavailable")
				}
				authority := failoverAuthorityStub{}
				if scenario == "no-majority" {
					authority.err = errors.New("no majority")
				}
				p := NewGuardedFailoverSafety(window, authority, inv, lease, tr, "agent-secret", func() time.Time { return now }, WithAgentQuorumFencing(15*time.Second))
				if p.agentQuorumEnabled(r) {
					t.Fatal("gateway must not gain the unreachable-Agent expiry fallback")
				}
				checks := p.Precheck(context.Background(), r)
				if scenario == "unreachable" || scenario == "invalid-service" {
					if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckFail {
						t.Fatalf("unsafe precheck: %+v", checks)
					}
					if err := p.Fence(context.Background(), r); err == nil {
						t.Fatal("unsafe isolation accepted")
					}
					return
				}
				if scenario == "no-majority" {
					calls = nil
					tr.calls = &calls
					lease.calls = &calls
					if err := p.Fence(context.Background(), r); err == nil || len(calls) != 0 {
						t.Fatalf("majority failure performed mutation: %v %v", err, calls)
					}
					return
				}
				if failoverCheckStatus(checks, "old_primary_fenced") != model.CheckPass {
					t.Fatalf("configured direct fencing blocked: %+v", checks)
				}
				calls = nil
				tr.calls = &calls
				lease.calls = &calls
				if err := p.Fence(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				if len(calls) != 2 || calls[0] != "lease" || calls[1] != agent.CommandSelfIsolate {
					t.Fatalf("fence order: %v", calls)
				}
				if scenario == "unsafe-role" {
					if engine == model.EngineMySQL {
						tr.restartReadOnly = false
					} else {
						tr.serviceRunning = true
					}
				}
				check := p.Verify(context.Background(), r)
				if (check.Status == model.CheckPass) != (scenario == "stopped") {
					t.Fatalf("isolation proof: %+v", check)
				}
				for _, req := range tr.requests {
					if req.VIP != "" || req.Interface != "" || req.Prefix != 0 || req.Engine != engine || req.ClusterID != r.Cluster.ResourceID {
						t.Fatalf("gateway request escaped native Agent scope: %+v", req)
					}
					sig, err := agent.SignRequest(req, "agent-secret")
					if err != nil || sig != req.Signature {
						t.Fatal("signature did not cover gateway request")
					}
					if req.Command == agent.CommandSelfIsolate && req.LeaseID != lease.lease.ResourceID {
						t.Fatal("isolation lacks exact transition lease")
					}
				}
			})
		}
	}
}
