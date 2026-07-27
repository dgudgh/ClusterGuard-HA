package postgresql

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type candidateEvaluation struct {
	assessment model.CandidateAssessment
	replayLSN  uint64
	lagSeconds int64
	warnings   bool
}

func evaluateCandidates(request adapter.CandidateRequest) []model.CandidateAssessment {
	evaluations := make([]candidateEvaluation, 0, len(request.Instances))
	for _, instance := range request.Instances {
		evaluations = append(evaluations, evaluateCandidate(request, instance))
	}
	sort.Slice(evaluations, func(leftIndex, rightIndex int) bool {
		left := evaluations[leftIndex]
		right := evaluations[rightIndex]
		if left.assessment.Eligible != right.assessment.Eligible {
			return left.assessment.Eligible
		}
		if !left.assessment.Eligible {
			return string(left.assessment.InstanceID) < string(right.assessment.InstanceID)
		}
		if left.replayLSN != right.replayLSN {
			return left.replayLSN > right.replayLSN
		}
		if left.lagSeconds != right.lagSeconds {
			return left.lagSeconds < right.lagSeconds
		}
		return string(left.assessment.InstanceID) < string(right.assessment.InstanceID)
	})

	result := make([]model.CandidateAssessment, len(evaluations))
	nextRank := 1
	for index, evaluation := range evaluations {
		if evaluation.assessment.Eligible {
			evaluation.assessment.Rank = nextRank
			nextRank++
		}
		result[index] = evaluation.assessment
	}
	return result
}

func evaluateCandidate(request adapter.CandidateRequest, instance model.DatabaseInstance) candidateEvaluation {
	evaluation := candidateEvaluation{assessment: model.CandidateAssessment{
		InstanceID:   instance.ResourceID,
		Eligible:     true,
		RiskLevel:    "low",
		DataLossRisk: "unknown",
		Checks:       make([]model.Check, 0, 12),
	}}
	addCheck := func(name string, status model.CheckStatus, message string) {
		evaluation.assessment.Checks = append(evaluation.assessment.Checks, model.Check{Name: name, Status: status, Message: message})
		if status == model.CheckFail {
			evaluation.assessment.Eligible = false
		}
		if status == model.CheckWarn {
			evaluation.warnings = true
		}
	}
	healthyBoundProbe := hasCurrentHealthyProbe(request.Probes, instance.ResourceID, request.ObservedAt)
	reachableBoundProbe := hasCurrentReachableProbe(request.Probes, instance.ResourceID, request.ObservedAt)
	sourceDisconnected := request.Policy.AllowSourceDisconnected && reachableBoundProbe &&
		postgresqlSafeSourceLossEvidence(request.Cluster.EngineIdentity["system_identifier"], request.Primary, instance)

	if model.ValidResourceID(instance.ResourceID) && instance.ClusterID == request.Cluster.ResourceID &&
		instance.Engine == model.EnginePostgreSQL && request.Cluster.Engine == model.EnginePostgreSQL {
		addCheck("inventory_membership", model.CheckPass, "candidate belongs to the selected PostgreSQL cluster inventory")
	} else {
		addCheck("inventory_membership", model.CheckFail, "candidate is outside the selected PostgreSQL cluster inventory")
	}
	if healthyBoundProbe {
		addCheck("probe_evidence", model.CheckPass, "current bound probe confirms candidate reachability")
	} else if sourceDisconnected {
		addCheck("probe_evidence", model.CheckWarn, "current bound probe confirms the standby is reachable after primary source loss")
	} else {
		addCheck("probe_evidence", model.CheckFail, "current bound probe evidence is missing or stale")
	}
	if instance.Role == model.RoleStandby {
		addCheck("candidate_role", model.CheckPass, "candidate is a PostgreSQL standby")
	} else {
		addCheck("candidate_role", model.CheckFail, "candidate is not a PostgreSQL standby")
	}
	if instance.Health.State == model.HealthHealthy && instance.PromotionEligible {
		addCheck("candidate_health", model.CheckPass, "standby health and promotion evidence are current")
	} else if sourceDisconnected {
		addCheck("candidate_health", model.CheckWarn, "failover eligibility is derived from current read-only recovery and WAL evidence")
	} else {
		addCheck("candidate_health", model.CheckFail, "standby is not healthy or promotion eligible")
	}
	if instance.Maintenance {
		addCheck("maintenance", model.CheckFail, "candidate is in maintenance")
	} else {
		addCheck("maintenance", model.CheckPass, "candidate is not in maintenance")
	}

	clusterSystemID := strings.TrimSpace(request.Cluster.EngineIdentity["system_identifier"])
	primarySystemID := strings.TrimSpace(request.Primary.EngineIdentity["system_identifier"])
	candidateSystemID := strings.TrimSpace(instance.EngineIdentity["system_identifier"])
	if clusterSystemID != "" && clusterSystemID == primarySystemID && primarySystemID == candidateSystemID {
		addCheck("system_identifier", model.CheckPass, "cluster, primary, and candidate share one PostgreSQL system identifier")
	} else {
		addCheck("system_identifier", model.CheckFail, "PostgreSQL system identifiers do not match")
	}
	primaryNodeID := strings.ToLower(strings.TrimSpace(request.Primary.EngineIdentity["resource_id"]))
	upstreamNodeID := strings.ToLower(strings.TrimSpace(instance.Replication.SourceIdentity["resource_id"]))
	if model.ValidResourceID(model.ResourceID(primaryNodeID)) && upstreamNodeID == primaryNodeID {
		addCheck("replication_source", model.CheckPass, "standby follows the current primary identity")
	} else {
		addCheck("replication_source", model.CheckFail, "standby upstream identity does not match the current primary")
	}
	if instance.Replication.IOThread == model.ThreadRunning && instance.Replication.SQLThread == model.ThreadRunning &&
		!strings.EqualFold(strings.TrimSpace(instance.EngineMetadata["replay_paused"]), "true") {
		addCheck("wal_streaming", model.CheckPass, "WAL receiver and replay are active")
	} else if sourceDisconnected {
		addCheck("wal_streaming", model.CheckWarn, "WAL replay is complete while the receiver is stopped by confirmed primary source loss")
	} else {
		addCheck("wal_streaming", model.CheckFail, "WAL receiver or replay is not active")
	}

	primaryTimeline, primaryTimelineErr := strconv.ParseUint(strings.TrimSpace(request.Primary.EngineMetadata["timeline_id"]), 10, 32)
	candidateTimeline, candidateTimelineErr := strconv.ParseUint(strings.TrimSpace(instance.EngineMetadata["timeline_id"]), 10, 32)
	if primaryTimelineErr == nil && candidateTimelineErr == nil && primaryTimeline > 0 && primaryTimeline == candidateTimeline {
		addCheck("timeline", model.CheckPass, "candidate is on the current primary timeline")
	} else {
		addCheck("timeline", model.CheckFail, "candidate timeline does not match the current primary")
	}

	replayPosition := strings.TrimSpace(instance.Replication.ExecutedPosition)
	if replayPosition == "" {
		replayPosition = strings.TrimSpace(instance.EngineMetadata["replay_lsn"])
	}
	replayLSN, replayErr := parseLSN(replayPosition)
	primaryLSN, primaryErr := parseLSN(strings.TrimSpace(request.Primary.EngineMetadata["current_lsn"]))
	if replayErr != nil || primaryErr != nil {
		addCheck("wal_position", model.CheckFail, "primary or candidate WAL position is unavailable or invalid")
	} else {
		evaluation.replayLSN = replayLSN
		switch {
		case replayLSN < primaryLSN:
			difference := primaryLSN - replayLSN
			evaluation.assessment.DataLossRisk = fmt.Sprintf("%d WAL bytes behind", difference)
			addCheck("wal_position", model.CheckWarn, evaluation.assessment.DataLossRisk)
		case replayLSN == primaryLSN:
			evaluation.assessment.DataLossRisk = "none"
			addCheck("wal_position", model.CheckPass, "candidate replay position matches the primary sample")
		case sourceDisconnected:
			evaluation.assessment.DataLossRisk = "none"
			addCheck("wal_position", model.CheckPass, "candidate replay position is at or beyond the last primary sample")
		default:
			addCheck("wal_position", model.CheckFail, "candidate replay position is ahead of the primary sample")
		}
	}

	if instance.Replication.LagSeconds == nil || *instance.Replication.LagSeconds < 0 {
		addCheck("replication_lag", model.CheckFail, "candidate replication lag is unknown")
	} else {
		evaluation.lagSeconds = *instance.Replication.LagSeconds
		switch {
		case evaluation.lagSeconds > request.Policy.MaximumLagSeconds:
			addCheck("replication_lag", model.CheckFail, fmt.Sprintf("candidate lag %ds exceeds policy maximum %ds", evaluation.lagSeconds, request.Policy.MaximumLagSeconds))
		case evaluation.lagSeconds > 0:
			addCheck("replication_lag", model.CheckWarn, fmt.Sprintf("candidate replay is delayed by %ds", evaluation.lagSeconds))
		default:
			addCheck("replication_lag", model.CheckPass, "candidate has zero observed replay delay")
		}
	}

	if !evaluation.assessment.Eligible {
		evaluation.assessment.RiskLevel = "blocked"
	} else if evaluation.warnings {
		evaluation.assessment.RiskLevel = "warning"
	}
	return evaluation
}

func hasCurrentHealthyProbe(probes []model.ProbeStatus, instanceID model.ResourceID, observedAt time.Time) bool {
	if observedAt.IsZero() {
		return false
	}
	for _, probe := range probes {
		if probe.InstanceID == instanceID && probe.DiscoveryObservedAt.Equal(observedAt) && probe.Health.State == model.HealthHealthy {
			return true
		}
	}
	return false
}

func hasCurrentReachableProbe(probes []model.ProbeStatus, instanceID model.ResourceID, observedAt time.Time) bool {
	if observedAt.IsZero() {
		return false
	}
	for _, probe := range probes {
		if probe.InstanceID != instanceID || !probe.DiscoveryObservedAt.Equal(observedAt) {
			continue
		}
		if probe.Health.State == model.HealthHealthy || probe.Health.State == model.HealthDegraded {
			return true
		}
	}
	return false
}

func postgresqlSafeSourceLossEvidence(systemIdentifier string, primary, candidate model.DatabaseInstance) bool {
	primaryFailed := primary.Health.State == model.HealthUnhealthy || primary.Health.State == model.HealthUnknown
	if !primaryFailed || primary.Role != model.RolePrimary || candidate.Role != model.RoleStandby ||
		(candidate.Health.State != model.HealthHealthy && candidate.Health.State != model.HealthDegraded) ||
		candidate.Engine != model.EnginePostgreSQL || primary.Engine != model.EnginePostgreSQL ||
		candidate.ClusterID != primary.ClusterID || candidate.Maintenance {
		return false
	}
	if !postgresqlStableInstanceIdentity(primary, strings.TrimSpace(systemIdentifier)) ||
		!postgresqlStableInstanceIdentity(candidate, strings.TrimSpace(systemIdentifier)) ||
		!postgresqlSourceIdentityMatches(candidate.Replication.SourceIdentity, primary, strings.TrimSpace(systemIdentifier)) {
		return false
	}
	inRecovery, inRecoveryErr := parsePostgreSQLBoolean(candidate.EngineMetadata["in_recovery"])
	readOnly, readOnlyErr := parsePostgreSQLBoolean(candidate.EngineMetadata["transaction_read_only"])
	replayPaused, replayPausedErr := parsePostgreSQLBoolean(candidate.EngineMetadata["replay_paused"])
	if inRecoveryErr != nil || readOnlyErr != nil || replayPausedErr != nil || !inRecovery || !readOnly || replayPaused {
		return false
	}
	if (candidate.Replication.IOThread != model.ThreadStopped && candidate.Replication.IOThread != model.ThreadConnecting) ||
		candidate.Replication.SQLThread != model.ThreadRunning || strings.TrimSpace(candidate.Replication.LastSQLError) != "" ||
		candidate.Replication.LagSeconds == nil || *candidate.Replication.LagSeconds != 0 {
		return false
	}
	primaryTimeline, primaryTimelineErr := strconv.ParseUint(strings.TrimSpace(primary.EngineMetadata["timeline_id"]), 10, 32)
	candidateTimeline, candidateTimelineErr := strconv.ParseUint(strings.TrimSpace(candidate.EngineMetadata["timeline_id"]), 10, 32)
	if primaryTimelineErr != nil || candidateTimelineErr != nil || primaryTimeline == 0 || primaryTimeline != candidateTimeline {
		return false
	}
	receivePosition := strings.TrimSpace(candidate.Replication.RetrievedPosition)
	if receivePosition == "" {
		receivePosition = strings.TrimSpace(candidate.EngineMetadata["receive_lsn"])
	}
	replayPosition := strings.TrimSpace(candidate.Replication.ExecutedPosition)
	if replayPosition == "" {
		replayPosition = strings.TrimSpace(candidate.EngineMetadata["replay_lsn"])
	}
	primaryLSN, primaryErr := parseLSN(strings.TrimSpace(primary.EngineMetadata["current_lsn"]))
	receiveLSN, receiveErr := parseLSN(receivePosition)
	replayLSN, replayErr := parseLSN(replayPosition)
	return primaryErr == nil && receiveErr == nil && replayErr == nil && receiveLSN == replayLSN && replayLSN >= primaryLSN
}
