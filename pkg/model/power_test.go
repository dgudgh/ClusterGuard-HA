package model

import "testing"

func TestPowerStateValidation(t *testing.T) {
	for _, state := range []PowerState{PowerNormal, PowerPrechecking, PowerMaintenance,
		PowerShutdownPlanned, PowerShuttingDown, PowerPoweredOff, PowerBootDetected,
		PowerRecovering, PowerVerifying, PowerCompleted, PowerFailed} {
		if !state.Valid() {
			t.Errorf("%s should be a valid power state", state)
		}
	}
	if PowerState("sideways").Valid() {
		t.Error("unknown power state must be invalid")
	}
}

func TestTerminalPowerState(t *testing.T) {
	for _, state := range []PowerState{PowerCompleted, PowerFailed} {
		if !TerminalPowerState(state) {
			t.Errorf("%s should be terminal", state)
		}
	}
	for _, state := range []PowerState{PowerNormal, PowerPrechecking, PowerMaintenance,
		PowerShutdownPlanned, PowerShuttingDown, PowerPoweredOff, PowerBootDetected,
		PowerRecovering, PowerVerifying} {
		if TerminalPowerState(state) {
			t.Errorf("%s should not be terminal", state)
		}
	}
}

func TestValidPowerTransitionHappyPath(t *testing.T) {
	path := []PowerState{
		PowerNormal, PowerPrechecking, PowerMaintenance, PowerShutdownPlanned,
		PowerShuttingDown, PowerPoweredOff, PowerBootDetected, PowerRecovering,
		PowerVerifying, PowerCompleted,
	}
	for index := 0; index < len(path)-1; index++ {
		if !ValidPowerTransition(path[index], path[index+1]) {
			t.Errorf("expected valid transition %s -> %s", path[index], path[index+1])
		}
	}
}

func TestValidPowerTransitionFailureReachableFromAnyActiveState(t *testing.T) {
	for _, state := range []PowerState{PowerNormal, PowerPrechecking, PowerMaintenance,
		PowerShutdownPlanned, PowerShuttingDown, PowerPoweredOff, PowerBootDetected,
		PowerRecovering, PowerVerifying} {
		if !ValidPowerTransition(state, PowerFailed) {
			t.Errorf("expected %s -> failed to be allowed (fail-closed path)", state)
		}
	}
}

func TestValidPowerTransitionCancellationPaths(t *testing.T) {
	if !ValidPowerTransition(PowerPrechecking, PowerNormal) {
		t.Error("expected prechecking -> normal (cancel) to be allowed")
	}
	if !ValidPowerTransition(PowerShutdownPlanned, PowerNormal) {
		t.Error("expected shutdown_planned -> normal (cancel) to be allowed")
	}
}

func TestValidPowerTransitionTerminalIsImmutable(t *testing.T) {
	if ValidPowerTransition(PowerCompleted, PowerNormal) {
		t.Error("completed -> normal must be blocked")
	}
	if ValidPowerTransition(PowerCompleted, PowerFailed) {
		t.Error("completed -> failed must be blocked")
	}
	if ValidPowerTransition(PowerFailed, PowerNormal) {
		t.Error("failed -> normal must be blocked (fail-closed: no auto recovery)")
	}
	if ValidPowerTransition(PowerFailed, PowerCompleted) {
		t.Error("failed -> completed must be blocked")
	}
}

func TestValidPowerTransitionSkipsAreBlocked(t *testing.T) {
	if ValidPowerTransition(PowerNormal, PowerPoweredOff) {
		t.Error("normal -> power_off must be blocked")
	}
	if ValidPowerTransition(PowerNormal, PowerCompleted) {
		t.Error("normal -> completed must be blocked")
	}
	if ValidPowerTransition(PowerPrechecking, PowerShuttingDown) {
		t.Error("prechecking -> shutting_down must be blocked")
	}
	if ValidPowerTransition(PowerPoweredOff, PowerRecovering) {
		t.Error("power_off -> recovering must be blocked (boot-detected first)")
	}
	if ValidPowerTransition(PowerBootDetected, PowerCompleted) {
		t.Error("boot_detected -> completed must be blocked (verify first)")
	}
}

func TestPowerOperationTypeValidation(t *testing.T) {
	if !PowerService.Valid() || !PowerPowerOff.Valid() {
		t.Error("service and poweroff operation types must be valid")
	}
	if PowerOperationType("reboot").Valid() {
		t.Error("unknown operation type must be invalid")
	}
}

func TestPowerOperationShutdownTime(t *testing.T) {
	operation := PowerOperation{State: PowerNormal}
	if _, recorded := operation.ShutdownTime(); recorded {
		t.Error("normal state must not record a shutdown time")
	}
	operation.State = PowerShuttingDown
	if _, recorded := operation.ShutdownTime(); !recorded {
		t.Error("shutting_down state must record a shutdown time")
	}
}
