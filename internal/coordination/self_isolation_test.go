package coordination

import (
	"testing"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

func TestEvaluateSelfIsolationRequiresCurrentOwnershipLease(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	localID := model.NewResourceID()
	base := SelfIsolationEvidence{
		LocalInstanceID: localID, CurrentPrimaryID: localID, CanonicalOwnerID: localID, EndpointOwnerID: localID, Now: now,
		Lease: endpoint.Lease{ResourceID: model.NewResourceID(), OwnerID: localID, ExpiresAt: now.Add(30 * time.Second), Active: true},
	}
	if decision := EvaluateSelfIsolation(base); decision.Action != SelfIsolationKeepVIP {
		t.Fatalf("valid ownership decision=%+v", decision)
	}
	unsafe := []struct {
		name   string
		mutate func(*SelfIsolationEvidence)
	}{
		{name: "missing lease", mutate: func(value *SelfIsolationEvidence) { value.Lease = endpoint.Lease{} }},
		{name: "expired lease", mutate: func(value *SelfIsolationEvidence) { value.Lease.ExpiresAt = now.Add(-time.Second) }},
		{name: "other owner", mutate: func(value *SelfIsolationEvidence) { value.Lease.OwnerID = model.NewResourceID() }},
		{name: "not current primary", mutate: func(value *SelfIsolationEvidence) { value.CurrentPrimaryID = model.NewResourceID() }},
		{name: "canonical mismatch", mutate: func(value *SelfIsolationEvidence) { value.CanonicalOwnerID = model.NewResourceID() }},
		{name: "endpoint mismatch", mutate: func(value *SelfIsolationEvidence) { value.EndpointOwnerID = model.NewResourceID() }},
	}
	for _, test := range unsafe {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if decision := EvaluateSelfIsolation(value); decision.Action != SelfIsolationReleaseAndReadOnly {
				t.Fatalf("unsafe ownership decision=%+v", decision)
			}
		})
	}
}

func TestEvaluateSelfIsolationAllowsActiveTransitionTarget(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 0, 0, 0, time.UTC)
	localID := model.NewResourceID()
	decision := EvaluateSelfIsolation(SelfIsolationEvidence{
		LocalInstanceID:  localID,
		CurrentPrimaryID: model.NewResourceID(),
		CanonicalOwnerID: model.NewResourceID(),
		EndpointOwnerID:  model.NewResourceID(),
		TransitionTarget: true,
		Now:              now,
		Lease:            endpoint.Lease{ResourceID: model.NewResourceID(), OwnerID: localID, ExpiresAt: now.Add(30 * time.Second), Active: true},
	})
	if decision.Action != SelfIsolationKeepVIP {
		t.Fatalf("active transition decision=%+v", decision)
	}
}

func TestEvaluateSelfIsolationAuthorizesOnlyProvenRebootBootstrapTarget(t *testing.T) {
	now := time.Date(2026, time.July, 13, 21, 30, 0, 0, time.UTC)
	localID := model.NewResourceID()
	base := SelfIsolationEvidence{
		LocalInstanceID: localID, CanonicalOwnerID: localID, EndpointOwnerID: localID, BootstrapTarget: true, Now: now,
		Lease: endpoint.Lease{ResourceID: model.NewResourceID(), OwnerID: localID, ExpiresAt: now.Add(30 * time.Second), Active: true},
	}
	if decision := EvaluateSelfIsolation(base); decision.Action != SelfIsolationBootstrapPrimary {
		t.Fatalf("valid reboot bootstrap decision=%+v", decision)
	}

	unsafe := []struct {
		name   string
		mutate func(*SelfIsolationEvidence)
	}{
		{name: "current primary exists", mutate: func(value *SelfIsolationEvidence) { value.CurrentPrimaryID = localID }},
		{name: "canonical mismatch", mutate: func(value *SelfIsolationEvidence) { value.CanonicalOwnerID = model.NewResourceID() }},
		{name: "endpoint mismatch", mutate: func(value *SelfIsolationEvidence) { value.EndpointOwnerID = model.NewResourceID() }},
		{name: "lease missing", mutate: func(value *SelfIsolationEvidence) { value.Lease = endpoint.Lease{} }},
	}
	for _, test := range unsafe {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if decision := EvaluateSelfIsolation(value); decision.Action != SelfIsolationReleaseAndReadOnly {
				t.Fatalf("unsafe reboot bootstrap decision=%+v", decision)
			}
		})
	}
}
