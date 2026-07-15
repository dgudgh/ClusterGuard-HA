package mysql

import (
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

func resolvedObservationToken(resolved *adapter.ResolvedOperation) string {
	if resolved == nil {
		return ""
	}
	if token := strings.TrimSpace(resolved.ObservationToken); token != "" {
		return token
	}
	return string(resolved.Cluster.ResourceID) + "@" + resolved.Snapshot.ObservedAt.UTC().Format(time.RFC3339Nano)
}

func validateMySQLPlan(request adapter.OperationRequest, kind model.OperationKind, strictRevisions bool) error {
	if request.Operation.Kind != kind || request.Resolved == nil || request.Plan == nil {
		return fmt.Errorf("operation plan is incomplete")
	}
	resolved := request.Resolved
	plan := request.Plan
	if plan.OperationID != request.Operation.ResourceID || plan.ClusterID != resolved.Cluster.ResourceID ||
		plan.SourceID != resolved.Primary.ResourceID || plan.TargetID != resolved.Target.ResourceID || plan.TargetID != request.TargetID {
		return fmt.Errorf("operation plan resources changed")
	}
	if plan.Stage != model.StagePlan || plan.Digest == "" || plan.ObservationToken == "" {
		return fmt.Errorf("operation plan integrity fields are missing")
	}
	if plan.ObservationToken != resolvedObservationToken(resolved) {
		return fmt.Errorf("operation plan observation token changed")
	}
	digest, err := operationPlanDigest(*plan)
	if err != nil || digest != plan.Digest {
		return fmt.Errorf("operation plan digest changed")
	}
	if planHasBlockingChecks(plan.Checks) {
		return fmt.Errorf("operation plan contains blocking checks")
	}
	if plan.ResourceRevisions[resolved.Cluster.ResourceID] == 0 {
		return fmt.Errorf("operation plan cluster revision is missing")
	}
	for _, instance := range resolved.Snapshot.Instances {
		revision := plan.ResourceRevisions[instance.ResourceID]
		if revision == 0 || (strictRevisions && resolved.ObservationToken == "" && revision != instance.MetadataRevision) {
			return fmt.Errorf("operation plan instance revision changed")
		}
	}
	if strictRevisions && resolved.ObservationToken == "" && plan.ResourceRevisions[resolved.Cluster.ResourceID] != resolved.Cluster.MetadataRevision {
		return fmt.Errorf("operation plan cluster revision changed")
	}
	return nil
}

func planResourceRevisions(resolved *adapter.ResolvedOperation) map[model.ResourceID]uint64 {
	revisions := map[model.ResourceID]uint64{resolved.Cluster.ResourceID: resolved.Cluster.MetadataRevision}
	for _, instance := range resolved.Snapshot.Instances {
		revisions[instance.ResourceID] = instance.MetadataRevision
	}
	return revisions
}
