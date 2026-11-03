package endpoint_test

import (
	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
	"context"
	"testing"
)

func TestGatewayFormerPrimaryRejoinChecksTheRecoveredTarget(t *testing.T) {
	for _, scenario := range []string{"current-owner", "former-owner", "no-route", "split-owner", "stale-grant", "same-identity", "transition", "partial", "canonical-stale"} {
		t.Run(scenario, func(t *testing.T) {
			f := newGatewayFixture(t)
			f.bootstrap(t)
			r := f.resolved()
			switch scenario {
			case "former-owner":
				for i := range f.wire.states {
					f.wire.states[i].LastGrant.TargetID = f.target.ResourceID
				}
			case "no-route":
				for i := range f.wire.states {
					f.wire.states[i].Active = false
				}
			case "split-owner":
				f.wire.states[1].LastGrant.TargetID = f.target.ResourceID
			case "stale-grant":
				for i := range f.wire.states {
					f.wire.states[i].LastGrant.Sequence--
				}
			case "partial":
				wire := &availableGatewayTransport{&partialGatewayTransport{gatewayTransport: f.wire, down: 1}}
				f.p = endpoint.NewGatewayProvider(f.i, f.l, map[string]endpoint.GatewayTransport{"hybrid": wire})
			case "canonical-stale":
				if err := f.i.CommitHAEndpointOwner(f.c.ResourceID, f.resource.ResourceID, f.target.ResourceID, true); err != nil {
					t.Fatal(err)
				}
			case "same-identity":
				r.Target = r.Primary
			case "transition":
				a, err := f.p.AuthorizeTransition(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				defer a.Cancel()
			}
			got := f.p.FormerPrimaryPrecheck(context.Background(), r)
			pass := len(got) == 1 && got[0].Status == model.CheckPass
			if pass != (scenario == "current-owner") {
				t.Fatalf("%s: %+v", scenario, got)
			}
		})
	}
}
