package endpoint_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type synchronizedGateway struct {
	mu   sync.Mutex
	wire *gatewayTransport
}

func (s *synchronizedGateway) Statuses(ctx context.Context, id model.ResourceID) ([]endpoint.GatewayStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wire.Statuses(ctx, id)
}
func (s *synchronizedGateway) Apply(ctx context.Context, g model.GatewayGrant, validate func(context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wire.Apply(ctx, g, validate)
}
func TestGatewayLongTransitionRenewsOnlyActivatedBoundRoute(t *testing.T) {
	f := gatewayFixtureWithEngine(t, 5432, 5433, model.EnginePostgreSQL)
	wire := &synchronizedGateway{wire: f.wire}
	f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": wire})
	f.bootstrap(t)
	resolved := f.resolved()
	auth, err := f.p.AuthorizeTransition(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Cancel()
	// A pending withdrawal is never changed into an activation by the heartbeat.
	time.Sleep(6 * time.Second)
	states, err := wire.Statuses(context.Background(), f.c.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Active || s.LastGrant.Mode != "withdraw" {
			t.Fatal("renewal activated a pending transition")
		}
	}
	if err = f.p.Transfer(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	original := f.i.GatewayGrant(f.c.ResourceID)
	until := time.Now().Add(17 * time.Second)
	for time.Now().Before(until) {
		time.Sleep(time.Second)
		states, err = wire.Statuses(context.Background(), f.c.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			g := s.LastGrant
			if !s.Active || !g.ExpiresAt.After(time.Now()) || g.Epoch != original.Epoch || g.LeaseID != original.LeaseID || g.TargetID != original.TargetID || g.OperationID != original.OperationID {
				t.Fatalf("long recovery lost or changed authorized route: %+v", s)
			}
		}
	}
	if f.i.GatewayGrant(f.c.ResourceID).Sequence <= original.Sequence {
		t.Fatal("activated transition route was not renewed")
	}
	if err = auth.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	// An actual authority rejection cancels the transition; it cannot mint more grants.
	f.a.blocked.Store(true)
	select {
	case <-auth.Context.Done():
	case <-time.After(7 * time.Second):
		t.Fatal("lost majority did not cancel route renewal")
	}
	auth.Cancel()
	final := f.i.GatewayGrant(f.c.ResourceID)
	time.Sleep(time.Second)
	if f.i.GatewayGrant(f.c.ResourceID) != final {
		t.Fatal("canceled route continued publishing")
	}
}
