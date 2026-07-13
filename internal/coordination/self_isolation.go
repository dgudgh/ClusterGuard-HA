package coordination

import (
	"strings"
	"time"

	"clusterguard.io/ha/internal/endpoint"
	"clusterguard.io/ha/pkg/model"
)

type SelfIsolationAction string

const (
	SelfIsolationKeepVIP            SelfIsolationAction = "keep_vip"
	SelfIsolationBootstrapPrimary   SelfIsolationAction = "bootstrap_primary"
	SelfIsolationReleaseAndReadOnly SelfIsolationAction = "release_and_read_only"
)

type SelfIsolationEvidence struct {
	LocalInstanceID  model.ResourceID
	CurrentPrimaryID model.ResourceID
	CanonicalOwnerID model.ResourceID
	EndpointOwnerID  model.ResourceID
	Lease            endpoint.Lease
	TransitionTarget bool
	BootstrapTarget  bool
	Now              time.Time
}

type SelfIsolationDecision struct {
	Action SelfIsolationAction
	Reason string
}

func EvaluateSelfIsolation(evidence SelfIsolationEvidence) SelfIsolationDecision {
	isolate := func(reason string) SelfIsolationDecision {
		return SelfIsolationDecision{Action: SelfIsolationReleaseAndReadOnly, Reason: reason}
	}
	if !model.ValidResourceID(evidence.LocalInstanceID) {
		return isolate("local instance identity is invalid")
	}
	now := evidence.Now.UTC()
	lease := evidence.Lease
	if !lease.Active || !model.ValidResourceID(lease.ResourceID) || lease.OwnerID != evidence.LocalInstanceID || !lease.ExpiresAt.After(now) {
		return isolate("no active majority lease authorizes local VIP ownership")
	}
	if evidence.TransitionTarget {
		return SelfIsolationDecision{Action: SelfIsolationKeepVIP, Reason: "active controlled transition lease authorizes the target"}
	}
	if evidence.BootstrapTarget {
		if evidence.CurrentPrimaryID != "" {
			return isolate("reboot bootstrap is blocked while a current primary exists")
		}
		if evidence.CanonicalOwnerID != evidence.LocalInstanceID || evidence.EndpointOwnerID != evidence.LocalInstanceID {
			return isolate("canonical VIP ownership metadata does not select the rebooted primary")
		}
		return SelfIsolationDecision{Action: SelfIsolationBootstrapPrimary, Reason: "majority lease authorizes the verified rebooted primary"}
	}
	if evidence.CurrentPrimaryID != evidence.LocalInstanceID {
		return isolate("local instance is not the current primary")
	}
	if evidence.CanonicalOwnerID != evidence.LocalInstanceID || evidence.EndpointOwnerID != evidence.LocalInstanceID {
		return isolate("canonical VIP ownership metadata does not select the local primary")
	}
	return SelfIsolationDecision{Action: SelfIsolationKeepVIP, Reason: strings.TrimSpace("active majority lease and current-primary ownership are valid")}
}
