package mysql

import (
	"context"
	"errors"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestDiscoverClassifiesOnlyConfiguredPrimaryACKLossForVIPRenewal(t *testing.T) {
	for _, modern := range []bool{true, false} {
		for _, tc := range []struct {
			name      string
			mutate    func(*semiSyncRunner)
			replica   bool
			required  bool
			wantACK   bool
			wantState model.HealthState
		}{
			{name: "both replicas disconnected", required: true, wantACK: true, wantState: model.HealthDegraded},
			{name: "waiting for first acknowledgement", required: true, wantACK: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.statusRows[0]["Value"] = "ON" }},
			{name: "acknowledgements restored", required: true, wantState: model.HealthHealthy, mutate: func(r *semiSyncRunner) { r.statusRows[0]["Value"] = "ON"; r.statusRows[1]["Value"] = "2" }},
			{name: "plugin unavailable", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.variableRows = nil; r.statusRows = nil }},
			{name: "source disabled", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.variableRows[0]["Value"] = "OFF" }},
			{name: "unsafe wait point", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.variableRows[5]["Value"] = "AFTER_COMMIT" }},
			{name: "missing wait count", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.variableRows[2]["Value"] = "0" }},
			{name: "malformed client count", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.statusRows[1]["Value"] = "unknown" }},
			{name: "status query denied", required: true, wantState: model.HealthDegraded, mutate: func(r *semiSyncRunner) { r.statusRowsErr = errors.New("access denied") }},
			{name: "replica cannot inherit primary renewal", required: true, replica: true, wantState: model.HealthDegraded},
			{name: "semi sync optional", wantState: model.HealthHealthy},
		} {
			t.Run(tc.name+map[bool]string{true: "/modern", false: "/legacy"}[modern], func(t *testing.T) {
				variables, status := readySemiSyncRows(modern)
				status[0]["Value"] = "OFF"
				status[1]["Value"] = "0"
				base := &fakeRunner{identity: identityRow("8.0.44", "0", "0")}
				if tc.replica {
					base = healthyReplicaRunner("8.0.44", modernReplicationRow())
					status[2]["Value"] = "OFF"
				}
				runner := &semiSyncRunner{base: base, variableRows: variables, statusRows: status}
				if tc.mutate != nil {
					tc.mutate(runner)
				}
				result, err := New(runner).RequireSemiSync(tc.required).Discover(context.Background(), adapterRequest())
				if err != nil {
					t.Fatal(err)
				}
				got := result.Instance.EngineMetadata["semi_sync_ack_missing"] == "true"
				if got != tc.wantACK || result.Instance.Health.State != tc.wantState {
					t.Fatalf("ack-only=%v health=%s want ack-only=%v health=%s; instance=%+v", got, result.Instance.Health.State, tc.wantACK, tc.wantState, result.Instance)
				}
				if tc.wantACK && (result.Instance.Role != model.RolePrimary || result.Instance.PromotionEligible || result.Instance.EngineMetadata["read_only"] != "false" || result.Instance.EngineMetadata["super_read_only"] != "false") {
					t.Fatalf("ACK loss changed writer facts or promotion eligibility: %+v", result.Instance)
				}
			})
		}
	}
}
