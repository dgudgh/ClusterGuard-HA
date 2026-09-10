package postgresql

import (
	"context"
	"fmt"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type postgresqlAdvancingSourceRunner struct {
	sourceRows  []Row
	targetRow   Row
	sourceCalls int
}

func (runner *postgresqlAdvancingSourceRunner) Query(_ context.Context, endpoint adapter.Endpoint, _ adapter.Credentials, query string) ([]Row, error) {
	if query != identityQuery {
		return nil, fmt.Errorf("unexpected query")
	}
	if endpoint.Hostname == "pg-01" {
		index := runner.sourceCalls
		if index >= len(runner.sourceRows) {
			index = len(runner.sourceRows) - 1
		}
		runner.sourceCalls++
		return []Row{runner.sourceRows[index]}, nil
	}
	if endpoint.Hostname == "pg-02" {
		return []Row{runner.targetRow}, nil
	}
	return nil, fmt.Errorf("unexpected endpoint %s", endpoint.Hostname)
}

func (*postgresqlAdvancingSourceRunner) Exec(context.Context, adapter.Endpoint, adapter.Credentials, string) error {
	return nil
}

func TestPostgreSQLLiveSwitchoverPrecheckAcceptsEmptyBootstrapGUCWithBilateralEvidence(t *testing.T) {
	request, primaryNativeID, targetNativeID := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	primaryRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, "")
	if targetRow["primary_node_id"] != "" {
		t.Fatalf("fixture unexpectedly carries bootstrap source identity: %+v", targetRow)
	}
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	if err := instance.postgresqlLiveSwitchoverPrecheck(context.Background(), *request.Resolved); err != nil {
		t.Fatalf("bilateral evidence was rejected with an empty bootstrap GUC: %v", err)
	}
	if primaryNativeID == request.Resolved.Primary.ResourceID || targetNativeID == request.Resolved.Target.ResourceID {
		t.Fatalf("fixture did not separate platform and native identities")
	}
}

func TestPostgreSQLStandbyVerificationAcceptsEmptyBootstrapGUCWithBilateralEvidence(t *testing.T) {
	request, _, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	primaryRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, "")
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check, retryable := instance.postgresqlInstanceVerificationCheckOnce(
		context.Background(), *request.Resolved, "standby", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckPass || retryable {
		t.Fatalf("bilateral standby verification failed with an empty bootstrap GUC: check=%+v retryable=%t", check, retryable)
	}
}

func TestPostgreSQLLiveSwitchoverPrecheckRejectsCorrectGUCWithoutBilateralEvidence(t *testing.T) {
	request, primaryNativeID, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)

	for _, test := range []struct {
		name   string
		mutate func(Row, Row)
	}{
		{
			name: "receiver points at another host",
			mutate: func(_ Row, targetRow Row) {
				targetRow["receiver_sender_host"] = "pg-99"
			},
		},
		{
			name: "primary has no sender for target",
			mutate: func(primaryRow Row, _ Row) {
				primaryRow["replication_senders"] = "[]"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			primaryRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, primaryNativeID)
			test.mutate(primaryRow, targetRow)
			runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
				request.Resolved.Primary.Hostname: {primaryRow},
				request.Resolved.Target.Hostname:  {targetRow},
			}}
			instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

			if err := instance.postgresqlLiveSwitchoverPrecheck(context.Background(), *request.Resolved); err == nil {
				t.Fatalf("correct bootstrap GUC bypassed missing or conflicting bilateral evidence")
			}
		})
	}
}

func TestPostgreSQLNormalStreamingFailoverRejectsReceiverConflictDespiteCorrectGUC(t *testing.T) {
	request, primaryNativeID, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationFailover)
	primaryRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, primaryNativeID)
	targetRow["receiver_sender_host"] = "pg-99"
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	if err := instance.postgresqlLiveFailoverTargetPrecheck(context.Background(), *request.Resolved); err == nil {
		t.Fatalf("normal-streaming failover trusted the bootstrap GUC over a conflicting live receiver")
	}
}

func TestPostgreSQLStandbyVerificationRejectsMissingSenderDespiteCorrectGUC(t *testing.T) {
	request, primaryNativeID, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	primaryRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, primaryNativeID)
	primaryRow["replication_senders"] = "[]"
	runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
		request.Resolved.Primary.Hostname: {primaryRow},
		request.Resolved.Target.Hostname:  {targetRow},
	}}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check, _ := instance.postgresqlInstanceVerificationCheckOnce(
		context.Background(), *request.Resolved, "standby", request.Resolved.Target, model.RoleStandby, request.Resolved.Primary.ResourceID,
	)
	if check.Status != model.CheckFail {
		t.Fatalf("standby verification trusted the bootstrap GUC without a primary sender: %+v", check)
	}
}

func TestPostgreSQLLiveReplicationCheckUsesBilateralEvidenceAndFixedPrimarySample(t *testing.T) {
	request, _, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	initialSource, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, "")
	advancedSource, _ := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, "")
	initialSource["current_lsn"] = "0/5000060"
	advancedSource["current_lsn"] = "0/9000000"
	targetRow["receive_lsn"] = "0/5000060"
	targetRow["replay_lsn"] = "0/5000060"
	runner := &postgresqlAdvancingSourceRunner{
		sourceRows: []Row{initialSource, advancedSource},
		targetRow:  targetRow,
	}
	instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

	check := instance.postgresqlLiveReplicationCheck(context.Background(), *request.Resolved, "wal_position")
	if check.Status != model.CheckPass {
		t.Fatalf("target that reached the fixed primary sample was rejected after the source advanced: %+v", check)
	}
	if runner.sourceCalls != 2 {
		t.Fatalf("source probes=%d, want one fixed sample and one bilateral observation", runner.sourceCalls)
	}
}

func TestPostgreSQLLiveReplicationCheckRejectsCorrectGUCWithoutBilateralEvidence(t *testing.T) {
	request, primaryNativeID, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationSwitchover)
	for _, test := range []struct {
		name   string
		mutate func(Row, Row)
	}{
		{
			name: "receiver conflict",
			mutate: func(_ Row, targetRow Row) {
				targetRow["receiver_sender_host"] = "pg-99"
			},
		},
		{
			name: "sender absent",
			mutate: func(sourceRow Row, _ Row) {
				sourceRow["replication_senders"] = "[]"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourceRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, primaryNativeID)
			test.mutate(sourceRow, targetRow)
			runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
				request.Resolved.Primary.Hostname: {sourceRow},
				request.Resolved.Target.Hostname:  {targetRow},
			}}
			instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})

			if check := instance.postgresqlLiveReplicationCheck(context.Background(), *request.Resolved, "wal_position"); check.Status != model.CheckFail {
				t.Fatalf("live WAL check trusted the bootstrap GUC without bilateral evidence: %+v", check)
			}
		})
	}
}

func TestPostgreSQLRepairResumeVerificationUsesBilateralEvidence(t *testing.T) {
	for _, test := range []struct {
		name              string
		bootstrapSourceID bool
		mutate            func(Row, Row)
		want              model.CheckStatus
	}{
		{name: "empty GUC with bilateral evidence", want: model.CheckPass},
		{
			name:              "correct GUC with receiver conflict",
			bootstrapSourceID: true,
			mutate: func(_ Row, targetRow Row) {
				targetRow["receiver_sender_host"] = "pg-99"
			},
			want: model.CheckFail,
		},
		{
			name:              "correct GUC with sender absent",
			bootstrapSourceID: true,
			mutate: func(sourceRow Row, _ Row) {
				sourceRow["replication_senders"] = "[]"
			},
			want: model.CheckFail,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, primaryNativeID, _ := postgresqlRequestWithDistinctNativeNodeIdentities(model.OperationReplicationRepair)
			request.Parameters = map[string]string{"action": postgresqlRepairResume}
			request.Credentials = adapter.Credentials{Username: "operator", Database: "postgres"}
			request.Resolved.Credentials = request.Credentials
			bootstrapSourceID := model.ResourceID("")
			if test.bootstrapSourceID {
				bootstrapSourceID = primaryNativeID
			}
			sourceRow, targetRow := postgresqlBilateralLiveRows(request.Resolved.Primary, request.Resolved.Target, bootstrapSourceID)
			if test.mutate != nil {
				test.mutate(sourceRow, targetRow)
			}
			runner := &postgresqlExecutableRunner{rowsByHost: map[string][]Row{
				request.Resolved.Primary.Hostname: {sourceRow},
				request.Resolved.Target.Hostname:  {targetRow},
			}}
			instance := NewWithProviders(runner, postgresqlEndpointStub{executable: true}, &postgresqlNodeControllerStub{executable: true}, postgresqlFailoverSafetyStub{})
			plan, err := instance.BuildPlan(context.Background(), request)
			if err != nil {
				t.Fatalf("build repair plan: %v", err)
			}
			request.Plan = &plan
			request.Resolved.PlanDigest = plan.Digest

			verification, err := instance.Verify(context.Background(), request)
			if err != nil {
				t.Fatalf("verify repair: %v", err)
			}
			if len(verification.Checks) != 1 || verification.Checks[0].Status != test.want {
				t.Fatalf("repair verification=%+v, want status %s", verification, test.want)
			}
		})
	}
}
