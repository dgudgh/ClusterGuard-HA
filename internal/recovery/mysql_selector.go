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

type DatabaseCandidateSelector struct {
	evaluator CandidateEvaluator
	engine    model.Engine
	policy    model.CandidatePolicy
}

func NewMySQLCandidateSelector(evaluator CandidateEvaluator) *DatabaseCandidateSelector {
	return &DatabaseCandidateSelector{
		evaluator: evaluator,
		engine:    model.EngineMySQL,
		policy: model.CandidatePolicy{
			MaximumLagSeconds:       30,
			RequireGTID:             true,
			AllowSourceDisconnected: true,
		},
	}
}

func NewPostgreSQLCandidateSelector(evaluator CandidateEvaluator) *DatabaseCandidateSelector {
	return &DatabaseCandidateSelector{
		evaluator: evaluator,
		engine:    model.EnginePostgreSQL,
		policy: model.CandidatePolicy{
			MaximumLagSeconds:       0,
			RequireGTID:             false,
			AllowSourceDisconnected: true,
		},
	}
}

func (selector *DatabaseCandidateSelector) Select(ctx context.Context, cluster model.DatabaseCluster, snapshot model.TopologySnapshot) (model.ResourceID, error) {
	if selector == nil || selector.evaluator == nil {
		return "", fmt.Errorf("database candidate evaluator is not configured")
	}
	if cluster.Engine != selector.engine {
		return "", fmt.Errorf("candidate selector for %s cannot evaluate %s cluster", selector.engine, cluster.Engine)
	}
	if !model.ValidResourceID(cluster.ResourceID) || snapshot.ClusterID != cluster.ResourceID {
		return "", fmt.Errorf("candidate topology is outside the selected cluster")
	}
	if len(snapshot.Instances) == 0 {
		return "", fmt.Errorf("automatic failover requires a non-empty topology snapshot")
	}
	primary := model.DatabaseInstance{}
	candidates := make([]model.DatabaseInstance, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		if !model.ValidResourceID(instance.ResourceID) || instance.ClusterID != cluster.ResourceID || instance.Engine != cluster.Engine {
			return "", fmt.Errorf("candidate topology contains an out-of-scope instance")
		}
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
		Policy: selector.policy,
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
