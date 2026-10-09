package configuration

import (
	"clusterguard.io/ha/internal/consensus"
	"context"
	"fmt"
)

type Permit struct {
	TaskID   string `json:"task_id"`
	Revision uint64 `json:"revision"`
	NodeID   string `json:"node_id"`
}

func (m *Manager) Permit(ctx context.Context, p Permit) error {
	if m.Authority.RequireMutationAuthority(ctx) != nil {
		return fmt.Errorf("configuration restart requires current majority authority")
	}
	if m.Maintenance != nil {
		if e := m.Maintenance.Check(ctx); e != nil {
			return e
		}
	}
	t, ok := m.Repository.ConfigurationTask(p.TaskID)
	if !ok || !t.Active || t.Status != "running" || t.Revision != p.Revision || t.CurrentNode != p.NodeID {
		return fmt.Errorf("configuration restart subject changed")
	}
	members, e := m.Authority.ControllerMembers(ctx)
	if e != nil {
		return e
	}
	if len(members) != len(t.Members) {
		return fmt.Errorf("configuration membership changed")
	}
	selected := false
	for _, member := range members {
		id := string(member.ResourceID)
		found := false
		for _, want := range t.Members {
			if id == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("configuration membership changed")
		}
		if id == p.NodeID {
			for _, target := range t.Targets {
				if target.NodeID == id && target.APIAddress == member.APIAddress {
					selected = true
				}
			}
			continue
		}
		n, e := m.Peer.Node(ctx, member)
		if e != nil || !n.Ready || n.NodeID != id || n.SchemaVersion != SchemaVersion || n.Error != "" {
			return fmt.Errorf("other voters must be ready before controller restart")
		}
	}
	if !selected {
		return fmt.Errorf("configuration restart target changed")
	}
	return nil
}
func (p HTTPPeer) Permit(ctx context.Context, leader consensus.ControllerMember, permit Permit) error {
	return p.call(ctx, leader, "POST", "/api/v1/control-plane/configuration/permit", permit, nil)
}
