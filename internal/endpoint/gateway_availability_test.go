package endpoint_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"clusterguard.io/ha/internal/coordination"
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

// AvailableStatuses/RenewStable are deliberately optional: old transports must
// retain their all-node requirement, and transition methods never use these.
type partialGatewayTransport struct {
	*gatewayTransport
	down int
}

func (t *partialGatewayTransport) Statuses(ctx context.Context, id model.ResourceID) ([]endpoint.GatewayStatus, error) {
	if t.down >= 0 {
		return nil, errors.New("gateway offline")
	}
	return t.gatewayTransport.Statuses(ctx, id)
}

// The concrete availability type is added with the implementation; the first
// regression below uses only the existing public transport contract.
func TestLegacyGatewayTransportStillRequiresCompleteObservation(t *testing.T) {
	f := newGatewayFixture(t)
	f.bootstrap(t)
	before := f.i.GatewayGrant(f.c.ResourceID)
	wire := &partialGatewayTransport{gatewayTransport: f.wire, down: 1}
	f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": wire})
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	// Without a bounded partial-observation capability it must still fail closed.
	if err := keeper.RunOnce(context.Background()); err == nil {
		t.Fatal("legacy transport silently bypassed full observation")
	}
	if f.i.GatewayGrant(f.c.ResourceID) != before {
		t.Fatal("legacy transport allocated a partial route")
	}
}

// A capable transport reports only authenticated observations and an explicit
// missing count. It must not claim that an unavailable gateway was observed.
type availableGatewayTransport struct{ *partialGatewayTransport }

func (t *availableGatewayTransport) AvailableStatuses(ctx context.Context, id model.ResourceID) (endpoint.GatewayAvailability, error) {
	states, _ := t.gatewayTransport.Statuses(ctx, id)
	if t.down == -2 {
		return endpoint.GatewayAvailability{Unavailable: len(states)}, errors.New("all gateways offline")
	}
	out := endpoint.GatewayAvailability{}
	for n, s := range states {
		if n == t.down {
			out.Unavailable++
		} else {
			out.Statuses = append(out.Statuses, s)
		}
	}
	return out, nil
}
func (t *availableGatewayTransport) RenewStable(ctx context.Context, g model.GatewayGrant, expected []endpoint.GatewayStatus, validate func(context.Context) error) (int, error) {
	updated := 0
	for n := range t.states {
		if n == t.down {
			continue
		}
		if err := validate(ctx); err != nil {
			return updated, err
		}
		s := &t.states[n]
		if !s.Active || s.LastGrant.Epoch != g.Epoch || s.LastGrant.TargetID != g.TargetID {
			return updated, errors.New("wrong route")
		}
		s.LastGrant = g
		updated++
	}
	return updated, nil
}
func availableFixture(t *testing.T) (*gatewayFixture, *availableGatewayTransport, *coordination.OwnershipKeeper) {
	t.Helper()
	f := newGatewayFixture(t)
	f.bootstrap(t)
	wire := &availableGatewayTransport{&partialGatewayTransport{gatewayTransport: f.wire, down: 1}}
	f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": wire})
	keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
	return f, wire, keeper
}
func TestGatewayAvailablePeerRenewsOnlyStableWriter(t *testing.T) {
	f, wire, keeper := availableFixture(t)
	before := f.i.GatewayGrant(f.c.ResourceID)
	offline := wire.states[1].LastGrant
	observation, err := f.p.ObserveOwnership(context.Background(), f.c, f.i.top)
	if err != nil || observation.Complete || len(observation.OwnerIDs) != 1 {
		t.Fatal("partial ownership was not represented honestly", observation, err)
	}
	if err = keeper.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.i.GatewayGrant(f.c.ResourceID)
	if after.Epoch != before.Epoch || after.TargetID != before.TargetID || after.Sequence <= before.Sequence || wire.states[0].LastGrant != after || wire.states[1].LastGrant != offline {
		t.Fatal("partial renewal changed identity or touched unavailable peer")
	}
	if f.p.Precheck(context.Background(), f.resolved())[0].Status != model.CheckFail {
		t.Fatal("partial transport was allowed into a new switch")
	}
	if _, err = f.p.AuthorizeTransition(context.Background(), f.resolved()); err == nil {
		t.Fatal("partial state authorized a transition")
	}
}
func TestGatewayPartialAvailabilityNeverBootstrapsOrRebinds(t *testing.T) {
	for _, name := range []string{"all offline", "wrong epoch", "wrong target", "bad backend", "restarted", "expired", "stale SQL", "minority"} {
		t.Run(name, func(t *testing.T) {
			f, wire, keeper := availableFixture(t)
			before := f.i.GatewayGrant(f.c.ResourceID)
			switch name {
			case "all offline":
				wire.down = -2
			case "wrong epoch":
				wire.states[0].LastGrant.Epoch++
			case "wrong target":
				wire.states[0].LastGrant.TargetID = f.target.ResourceID
			case "bad backend":
				wire.states[0].Targets[string(f.source.ResourceID)] = "127.0.0.1:9999"
			case "restarted":
				wire.states[0].Active = false
			case "expired":
				wire.states[0].LastGrant.ExpiresAt = time.Now().Add(-time.Second)
			case "stale SQL":
				f.i.top.ObservedAt = time.Now().Add(-time.Minute)
			case "minority":
				f.a.blocked.Store(true)
			}
			_ = keeper.RunOnce(context.Background())
			if f.i.GatewayGrant(f.c.ResourceID) != before {
				t.Fatal("unsafe partial state reserved a grant")
			}
		})
	}
	t.Run("no original route", func(t *testing.T) {
		f := newGatewayFixture(t)
		wire := &availableGatewayTransport{&partialGatewayTransport{gatewayTransport: f.wire, down: 1}}
		f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": wire})
		keeper := coordination.NewOwnershipKeeper(f.i, f.p, f.l, f.a, nil, time.Second, 15*time.Second)
		if err := keeper.RunOnce(context.Background()); err == nil {
			t.Fatal("partial observation bootstrapped")
		}
		if f.i.GatewayGrant(f.c.ResourceID).Epoch != 0 {
			t.Fatal("partial observation allocated its first grant")
		}
	})
}

func TestGatewayViewDistinguishesServingFromFullAvailability(t *testing.T) {
	f, _, _ := availableFixture(t)
	view, err := f.p.InspectGateway(context.Background(), f.c.ResourceID)
	if err != nil || !view.Serving || view.Ready || view.UnavailableGateways != 1 || view.SingleGateway {
		t.Fatal("partial service was disguised as full availability", view, err)
	}
}
