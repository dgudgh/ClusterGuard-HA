package mysql

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type candidateEvaluation struct {
	assessment          model.CandidateAssessment
	hasWarnings         bool
	missingTransactions uint64
	lagSeconds          int64
	exactVersion        bool
}

func evaluateCandidates(request adapter.CandidateRequest) []model.CandidateAssessment {
	evaluations := make([]candidateEvaluation, 0, len(request.Instances))
	incompleteCoverage := hasIncompleteProbeCoverage(request.Probes)
	for _, instance := range request.Instances {
		evaluations = append(evaluations, evaluateCandidate(request, instance, incompleteCoverage))
	}

	sort.Slice(evaluations, func(i, j int) bool {
		left := evaluations[i]
		right := evaluations[j]
		if left.assessment.Eligible != right.assessment.Eligible {
			return left.assessment.Eligible
		}
		if !left.assessment.Eligible {
			return string(left.assessment.InstanceID) < string(right.assessment.InstanceID)
		}
		if left.hasWarnings != right.hasWarnings {
			return !left.hasWarnings
		}
		if left.missingTransactions != right.missingTransactions {
			return left.missingTransactions < right.missingTransactions
		}
		if left.lagSeconds != right.lagSeconds {
			return left.lagSeconds < right.lagSeconds
		}
		if left.exactVersion != right.exactVersion {
			return left.exactVersion
		}
		return string(left.assessment.InstanceID) < string(right.assessment.InstanceID)
	})

	assessments := make([]model.CandidateAssessment, len(evaluations))
	nextRank := 1
	for index, evaluation := range evaluations {
		if evaluation.assessment.Eligible {
			evaluation.assessment.Rank = nextRank
			nextRank++
		}
		assessments[index] = evaluation.assessment
	}
	return assessments
}

func evaluateCandidate(request adapter.CandidateRequest, instance model.DatabaseInstance, incompleteCoverage bool) candidateEvaluation {
	evaluation := candidateEvaluation{
		assessment: model.CandidateAssessment{
			InstanceID: instance.ResourceID,
			Eligible:   true,
			RiskLevel:  "low",
			Checks:     make([]model.Check, 0, 11),
		},
	}
	addCheck := func(name string, status model.CheckStatus, message string) {
		evaluation.assessment.Checks = append(evaluation.assessment.Checks, model.Check{Name: name, Status: status, Message: message})
		if status == model.CheckFail {
			evaluation.assessment.Eligible = false
		}
		if status == model.CheckWarn {
			evaluation.hasWarnings = true
		}
	}

	if model.ValidResourceID(instance.ResourceID) && instance.ClusterID == request.Cluster.ResourceID && instance.Engine == model.EngineMySQL && request.Cluster.Engine == model.EngineMySQL {
		addCheck("inventory_membership", model.CheckPass, "candidate belongs to the selected MySQL cluster inventory")
	} else {
		addCheck("inventory_membership", model.CheckFail, "candidate is outside the selected MySQL cluster inventory")
	}
	if instance.Role == model.RoleReplica {
		addCheck("candidate_role", model.CheckPass, "candidate is a replica")
	} else {
		addCheck("candidate_role", model.CheckFail, "candidate is not a replica")
	}
	if instance.Health.State == model.HealthHealthy || instance.Health.State == model.HealthDegraded {
		addCheck("reachability", model.CheckPass, "candidate is reachable")
	} else {
		addCheck("reachability", model.CheckFail, "candidate reachability is not confirmed")
	}
	if instance.PromotionEligible {
		addCheck("promotion_eligibility", model.CheckPass, "candidate is marked promotion eligible")
	} else {
		addCheck("promotion_eligibility", model.CheckFail, "candidate is not marked promotion eligible")
	}
	if !instance.Maintenance {
		addCheck("maintenance", model.CheckPass, "candidate is not in maintenance")
	} else {
		addCheck("maintenance", model.CheckFail, "candidate is in maintenance")
	}
	if instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning {
		addCheck("replication_threads", model.CheckPass, "replication IO and SQL threads are running")
	} else {
		addCheck("replication_threads", model.CheckFail, "replication IO and SQL threads must both be running")
	}
	primaryServerUUID := strings.ToLower(strings.TrimSpace(request.Primary.EngineIdentity["server_uuid"]))
	sourceServerUUID := strings.ToLower(strings.TrimSpace(instance.Replication.SourceIdentity["server_uuid"]))
	if primaryServerUUID != "" && sourceServerUUID == primaryServerUUID {
		addCheck("replication_source", model.CheckPass, "candidate replicates from the current primary")
	} else {
		addCheck("replication_source", model.CheckFail, "candidate replication source does not match the current primary")
	}
	if instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds < 0 {
		addCheck("replication_lag", model.CheckFail, "candidate replication lag is unknown or invalid")
	} else {
		evaluation.lagSeconds = *instance.Replication.LagSeconds
		switch {
		case evaluation.lagSeconds > request.Policy.MaximumLagSeconds:
			addCheck("replication_lag", model.CheckFail, fmt.Sprintf("candidate lag %ds exceeds policy maximum %ds", evaluation.lagSeconds, request.Policy.MaximumLagSeconds))
		case evaluation.lagSeconds > 0:
			addCheck("replication_lag", model.CheckWarn, fmt.Sprintf("candidate is %ds behind the current primary", evaluation.lagSeconds))
		default:
			addCheck("replication_lag", model.CheckPass, "candidate has zero observed replication lag")
		}
	}
	primaryGTIDEnabled := strings.EqualFold(strings.TrimSpace(request.Primary.EngineMetadata["gtid_mode"]), "ON")
	candidateGTIDEnabled := strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["gtid_mode"]), "ON")
	if !request.Policy.RequireGTID || (primaryGTIDEnabled && candidateGTIDEnabled) {
		addCheck("gtid_mode", model.CheckPass, "GTID mode satisfies candidate policy")
	} else {
		addCheck("gtid_mode", model.CheckFail, "GTID mode is required on the primary and candidate")
	}

	primaryGTID, primaryGTIDError := ParseGTIDSet(request.Primary.EngineMetadata["gtid_executed"])
	candidateGTID, candidateGTIDError := ParseGTIDSet(instance.Replication.ExecutedPosition)
	if primaryGTIDError != nil || candidateGTIDError != nil {
		addCheck("gtid_consistency", model.CheckFail, "primary or candidate GTID position is invalid")
	} else {
		comparison := CompareGTIDSets(primaryGTID, candidateGTID)
		evaluation.missingTransactions = comparison.MissingTransactions
		if comparison.ErrantTransactions > 0 {
			addCheck("gtid_consistency", model.CheckFail, fmt.Sprintf("candidate has %d errant transactions", comparison.ErrantTransactions))
		} else {
			addCheck("gtid_consistency", model.CheckPass, "candidate has no errant transactions")
		}
	}

	primaryVersion := strings.TrimSpace(request.Primary.EngineMetadata["version"])
	candidateVersion := strings.TrimSpace(instance.EngineMetadata["version"])
	primaryFamily, primaryVersionError := mysqlReleaseFamily(primaryVersion)
	candidateFamily, candidateVersionError := mysqlReleaseFamily(candidateVersion)
	if primaryVersionError == nil && candidateVersionError == nil && primaryFamily == candidateFamily {
		evaluation.exactVersion = primaryVersion == candidateVersion
		addCheck("version_compatibility", model.CheckPass, fmt.Sprintf("candidate is in MySQL release family %s", candidateFamily))
	} else {
		addCheck("version_compatibility", model.CheckFail, "candidate MySQL release family is incompatible with the current primary")
	}
	if incompleteCoverage {
		addCheck("probe_coverage", model.CheckWarn, "one or more explicit inventory probes are incomplete")
	} else {
		addCheck("probe_coverage", model.CheckPass, "explicit probe observations are complete")
	}

	if evaluation.missingTransactions == 0 {
		evaluation.assessment.DataLossRisk = "none"
	} else {
		evaluation.assessment.DataLossRisk = fmt.Sprintf("%d missing transactions", evaluation.missingTransactions)
	}
	if !evaluation.assessment.Eligible {
		evaluation.assessment.RiskLevel = "blocked"
	} else if evaluation.hasWarnings {
		evaluation.assessment.RiskLevel = "warning"
	}
	return evaluation
}

func hasIncompleteProbeCoverage(probes []model.ProbeStatus) bool {
	for _, probe := range probes {
		if probe.Health.State != model.HealthHealthy {
			return true
		}
	}
	return false
}

func mysqlReleaseFamily(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid MySQL version %q", version)
	}
	major, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL version %q", version)
	}
	minor, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid MySQL version %q", version)
	}
	return fmt.Sprintf("%d.%d", major, minor), nil
}
