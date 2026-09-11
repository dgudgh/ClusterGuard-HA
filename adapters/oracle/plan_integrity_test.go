package oracle

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func bindOracleOperationTestPlan(t *testing.T, a *Adapter, request *adapter.OperationRequest) {
	t.Helper()
	plan, err := a.BuildPlan(context.Background(), *request)
	if err != nil {
		t.Fatal(err)
	}
	request.Plan = &plan
	request.Resolved.PlanDigest = plan.Digest
}

func TestOracleVerificationAllowsNewObservationButRejectsPlanTampering(t *testing.T) {
	r := oracleOperationRequest(model.OperationSwitchover)
	zero := int64(0)
	controller := &brokerControllerStub{executable: true, statuses: map[model.ResourceID]BrokerControlStatus{
		r.Resolved.Primary.ResourceID: {Database: "CGPROD1", Role: "PHYSICAL STANDBY", ConfigurationStatus: "SUCCESS"},
		r.Resolved.Target.ResourceID:  {Database: "CGPROD2", Role: "PRIMARY", ConfigurationStatus: "SUCCESS", TransportLagSeconds: &zero, ApplyLagSeconds: &zero},
	}}
	a := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
	bindOracleOperationTestPlan(t, a, &r)
	r.Resolved.ObservationToken = "post-transition-observation"
	r.Resolved.Target.MetadataRevision++
	if v, err := a.Verify(context.Background(), r); err != nil || !v.Passed {
		t.Fatalf("post-transition verification: %+v %v", v, err)
	}
	r.Plan.Summary = "tampered"
	if v, err := a.Verify(context.Background(), r); err == nil || v.Passed {
		t.Fatalf("tampered verification: %+v %v", v, err)
	}
	r.Plan = nil
	if v, err := a.Verify(context.Background(), r); err == nil || v.Passed {
		t.Fatalf("missing plan verification: %+v %v", v, err)
	}
}

func TestOracleExecutionRejectsUnboundPlanBeforeMutation(t *testing.T) {
	for _, name := range []string{"missing", "tampered", "stale-observation", "changed-cluster", "changed-target", "missing-revision", "wrong-operation", "unbound-digest", "blocking-check"} {
		t.Run(name, func(t *testing.T) {
			controller := &brokerControllerStub{executable: true}
			a := NewWithProviders(UnsupportedBrokerRunner{}, UnsupportedSQLPlusRunner{}, controller)
			r := oracleOperationRequest(model.OperationSwitchover)
			p, err := a.BuildPlan(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			r.Plan = &p
			r.Resolved.PlanDigest = p.Digest
			switch name {
			case "missing":
				r.Plan = nil
			case "tampered":
				r.Plan.Summary = "changed"
			case "stale-observation":
				r.Resolved.ObservationToken = "changed"
			case "changed-cluster":
				r.Resolved.Cluster.ResourceID = model.NewResourceID()
			case "changed-target":
				r.Resolved.Target.ResourceID = model.NewResourceID()
			case "missing-revision":
				delete(r.Plan.ResourceRevisions, r.Resolved.Primary.ResourceID)
			case "wrong-operation":
				r.Resolved.OperationID = model.NewResourceID()
			case "unbound-digest":
				r.Resolved.PlanDigest = ""
			case "blocking-check":
				r.Plan.Checks = append(r.Plan.Checks, model.Check{Name: "blocked", Status: model.CheckFail})
				r.Plan.Digest, err = oracleOperationPlanDigest(*r.Plan)
				if err != nil {
					t.Fatal(err)
				}
				r.Resolved.PlanDigest = r.Plan.Digest
			}
			execution, err := a.Execute(adapter.WithOperationLeaseID(context.Background(), model.NewResourceID()), r)
			if err == nil || execution.Status != model.OperationBlocked || controller.target != "" {
				t.Fatalf("unbound plan mutated: status=%s err=%v target=%s", execution.Status, err, controller.target)
			}
		})
	}
}
