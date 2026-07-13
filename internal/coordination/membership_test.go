package coordination

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func controller(id model.ResourceID, healthy bool) Controller {
	return Controller{ResourceID: id, Healthy: healthy}
}

func TestMembershipRequiresOddControllerSetAndLocalLeader(t *testing.T) {
	first, second, third := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	if _, err := NewMembership(first, first, []Controller{controller(first, true), controller(second, true)}); err == nil {
		t.Fatal("even controller membership was accepted")
	}
	membership, err := NewMembership(first, first, []Controller{controller(first, true), controller(second, true), controller(third, true)})
	if err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := membership.RequireMutationAuthority(context.Background()); err != nil {
		t.Fatalf("healthy leader was rejected: %v", err)
	}
	follower, err := NewMembership(second, first, []Controller{controller(first, true), controller(second, true), controller(third, true)})
	if err != nil {
		t.Fatalf("create follower membership: %v", err)
	}
	if err := follower.RequireMutationAuthority(context.Background()); err == nil {
		t.Fatal("non-leader mutation authority was accepted")
	}
}

func TestMembershipRejectsMutationWithoutControllerMajority(t *testing.T) {
	first, second, third := model.NewResourceID(), model.NewResourceID(), model.NewResourceID()
	membership, err := NewMembership(first, first, []Controller{controller(first, true), controller(second, false), controller(third, false)})
	if err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := membership.RequireMutationAuthority(context.Background()); err == nil {
		t.Fatal("minority controller accepted mutation authority")
	}
}
