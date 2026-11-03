package api

import (
	"clusterguard.io/ha/internal/agent"
	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

type delayedReadAuthority struct {
	apiMutationAuthorityStub
	apply   func()
	readErr error
	reads   int
}

func (a *delayedReadAuthority) SynchronizeRead(ctx context.Context) error {
	a.reads++
	if a.readErr != nil {
		return a.readErr
	}
	if a.apply != nil {
		a.apply()
		a.apply = nil
	}
	return ctx.Err()
}
func TestAgentDecisionWaitsForInheritedCommittedLease(t *testing.T) {
	for _, engine := range []model.Engine{model.EngineMySQL, model.EnginePostgreSQL} {
		t.Run(string(engine), func(t *testing.T) {
			now := time.Now().UTC()
			r := store.NewMemory()
			cluster, primary, _, lease := seedAgentReconcileEngineState(t, r, now, engine)
			if err := r.DeleteCoordinationLease(lease.ResourceID); err != nil {
				t.Fatal(err)
			}
			authority := &delayedReadAuthority{apiMutationAuthorityStub: apiMutationAuthorityStub{leaderID: model.NewResourceID(), leaderAddress: "leader:10009"}}
			authority.apply = func() {
				if err := r.PutCoordinationLease(coordination.LeaseRecord{Lease: lease, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			s := newAPIServer(t, r, adapter.NewUnsupported(engine), &fakeRefresher{}, WithMutationAuthority(authority), WithAgentReconcileSecret("agent-secret"))
			req := agent.ReconcileRequest{ClusterID: cluster.ResourceID, InstanceID: primary.ResourceID, RequestedAt: now, Nonce: "inherited-lease-decision"}
			if err := agent.SignReconcileRequest(&req, "agent-secret"); err != nil {
				t.Fatal(err)
			}
			resp := callAgentReconcile(t, s, req)
			var d agent.ReconcileResponse
			if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &d) != nil || d.Action != agent.ReconcileKeepVIP || authority.reads != 1 {
				t.Fatalf("signed stale-state isolation before applying committed lease: reads=%d status=%d body=%s", authority.reads, resp.Code, resp.Body.String())
			}
			authority.readErr = errors.New("FSM barrier unavailable")
			resp = callAgentReconcile(t, s, req)
			if resp.Code != http.StatusServiceUnavailable {
				t.Fatalf("barrier failure signed a decision: %d %s", resp.Code, resp.Body.String())
			}
		})
	}
}
