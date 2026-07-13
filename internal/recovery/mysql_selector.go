package recovery

import (
	"context"
	"fmt"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type CandidateEvaluator interface {
	EvaluateCandidates(context.Context, adapter.CandidateRequest) ([]model.CandidateAssessment, error)
}

type MySQLCandidateSelector struct {
	evaluator CandidateEvaluator
}

func NewMySQLCandidateSelector(evaluator CandidateEvaluator) *MySQLCandidateSelector {
	return &MySQLCandidateSelector{evaluator: evaluator}
}

func (selector *MySQLCandidateSelector) Select(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (model.ResourceID, error) {
	if selector == nil || selector.evaluator == nil {
		return "", fmt.Errorf("MySQL candidate evaluator is not configured")
	}
	primary := model.DatabaseInstance{}
	candidates := make([]model.DatabaseInstance, 0, len(snapshot.Instances)-1)
	for _, instance := range snapshot.Instances {
		if instance.Role == model.RolePrimary {
			if primary.ResourceID != "" {
				return "", fmt.Errorf("automatic failover requires exactly one recorded primary")
			}
			primary = instance
			continue
		}
		candidates = append(candidates, instance)
	}
	if !model.ValidResourceID(primary.ResourceID) {
		return "", fmt.Errorf("automatic failover requires one recorded primary")
	}
	assessments, err := selector.evaluator.EvaluateCandidates(ctx, adapter.CandidateRequest{
		Cluster: cluster, Primary: primary, Instances: candidates, Links: snapshot.Links,
		Probes: snapshot.Probes, ObservedAt: snapshot.ObservedAt,
		Policy: model.CandidatePolicy{MaximumLagSeconds: 30, RequireGTID: true},
	})
	if err != nil {
		return "", err
	}
	for _, assessment := range assessments {
		if assessment.Eligible && assessment.Rank == 1 {
			return assessment.InstanceID, nil
		}
	}
	return "", nil
}
