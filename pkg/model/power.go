package model

import "time"

// PowerOperationType distinguishes service-level shutdown (MySQL only, host
// stays up) from full-server power-off.
type PowerOperationType string

const (
	PowerService  PowerOperationType = "service"
	PowerPowerOff PowerOperationType = "poweroff"
)

func (operationType PowerOperationType) Valid() bool {
	return operationType == PowerService || operationType == PowerPowerOff
}

// PowerState is the core state machine of the power lifecycle. A cluster
// passes through these states between a planned shutdown and the completed
// automatic recovery.
type PowerState string

const (
	PowerNormal          PowerState = "normal"
	PowerPrechecking     PowerState = "prechecking"
	PowerMaintenance     PowerState = "maintenance"
	PowerShutdownPlanned PowerState = "shutdown_planned"
	PowerShuttingDown    PowerState = "shutting_down"
	PowerPoweredOff      PowerState = "power_off"
	PowerBootDetected    PowerState = "boot_detected"
	PowerRecovering      PowerState = "recovering"
	PowerVerifying       PowerState = "verifying"
	PowerCompleted       PowerState = "completed"
	PowerFailed          PowerState = "failed"
)

func (state PowerState) Valid() bool {
	switch state {
	case PowerNormal, PowerPrechecking, PowerMaintenance, PowerShutdownPlanned,
		PowerShuttingDown, PowerPoweredOff, PowerBootDetected, PowerRecovering,
		PowerVerifying, PowerCompleted, PowerFailed:
		return true
	default:
		return false
	}
}

// TerminalPowerState reports whether the state is terminal. Terminal states
// cannot transition; FAILED in particular never auto-recovers (fail-closed:
// protections stay active until a human intervenes).
func TerminalPowerState(state PowerState) bool {
	return state == PowerCompleted || state == PowerFailed
}

var allowedPowerTransitions = map[PowerState][]PowerState{
	PowerNormal:          {PowerPrechecking, PowerFailed},
	PowerPrechecking:     {PowerMaintenance, PowerNormal, PowerFailed},
	PowerMaintenance:     {PowerShutdownPlanned, PowerFailed},
	PowerShutdownPlanned: {PowerShuttingDown, PowerNormal, PowerFailed},
	PowerShuttingDown:    {PowerPoweredOff, PowerFailed},
	PowerPoweredOff:      {PowerBootDetected, PowerFailed},
	PowerBootDetected:    {PowerRecovering, PowerFailed},
	PowerRecovering:      {PowerVerifying, PowerFailed},
	PowerVerifying:       {PowerCompleted, PowerFailed},
	// COMPLETED and FAILED are terminal: no transitions out.
	PowerCompleted: {},
	PowerFailed:    {},
}

// ValidPowerTransition reports whether the power operation may move from one
// state to another. FAILED is reachable from every non-terminal state, and no
// transition leaves FAILED or COMPLETED (fail-closed).
func ValidPowerTransition(from, to PowerState) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if to == PowerFailed && !TerminalPowerState(from) {
		return true
	}
	for _, candidate := range allowedPowerTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// PowerInstanceRef is a compact reference to one cluster instance inside a
// power snapshot. It captures the coordinates needed to restore or verify the
// cluster without a live discovery.
type PowerInstanceRef struct {
	InstanceID ResourceID `json:"instance_id"`
	Hostname   string     `json:"hostname"`
	IPAddress  string     `json:"ip_address"`
	Port       int        `json:"port"`
}

// PowerVIPRef captures the VIP coordinates at shutdown time.
type PowerVIPRef struct {
	EndpointID ResourceID `json:"endpoint_id"`
	IPAddress  string     `json:"ip_address"`
	Interface  string     `json:"interface"`
}

// PowerSnapshot captures the pre-shutdown cluster topology so that restore
// and verification can independently detect drift after boot.
type PowerSnapshot struct {
	ClusterID        ResourceID         `json:"cluster_id"`
	ClusterName      string             `json:"cluster_name"`
	Engine           Engine             `json:"engine"`
	Primary          PowerInstanceRef   `json:"primary"`
	Replicas         []PowerInstanceRef `json:"replicas"`
	VIP              *PowerVIPRef       `json:"vip,omitempty"`
	CapturedAt       time.Time          `json:"captured_at"`
	LocalInstanceID  ResourceID         `json:"local_instance_id,omitempty"`
	LocalServiceName string             `json:"local_service_name,omitempty"`
}

// PowerOperation is the persistent, Raft-replicated record of one power
// lifecycle workflow. It lives in its own snapshot map so it is independent
// of the short-lived OperationRecord lifecycle.
type PowerOperation struct {
	ResourceMeta
	ClusterID     ResourceID         `json:"cluster_id"`
	Engine        Engine             `json:"engine"`
	OperationType PowerOperationType `json:"operation_type"`
	State         PowerState         `json:"state"`
	Mode          string             `json:"mode,omitempty"`
	RequestedBy   string             `json:"requested_by"`
	ApprovedBy    string             `json:"approved_by,omitempty"`
	// OperationID links to the standard OperationRecord that initiated the
	// shutdown (kind=power_shutdown), producing the audit and report trail.
	OperationID  ResourceID     `json:"operation_id,omitempty"`
	AutoRecovery bool           `json:"auto_recovery"`
	Snapshot     *PowerSnapshot `json:"snapshot,omitempty"`
	Message      string         `json:"message,omitempty"`
	StartedAt    time.Time      `json:"started_at"`
	CompletedAt  time.Time      `json:"completed_at,omitempty"`
}

// ShutdownTime returns when the shutdown was initiated, if any.
func (operation PowerOperation) ShutdownTime() (time.Time, bool) {
	if operation.State == PowerNormal || operation.State == PowerPrechecking {
		return time.Time{}, false
	}
	return operation.StartedAt, true
}
