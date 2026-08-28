package runtime

import (
	"context"
	"strings"
	"time"

	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/consensus"
	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

type consensusStatusReader interface {
	Status(context.Context) consensus.Status
}

type consensusMembershipReader interface {
	ControllerMembers(context.Context) ([]consensus.ControllerMember, error)
}

type controlPlaneStatusProvider struct {
	repository *store.Repository
	consensus  consensusStatusReader
	startedAt  time.Time
	now        func() time.Time
}

func newControlPlaneStatusProvider(repository *store.Repository, reader consensusStatusReader, startedAt time.Time) *controlPlaneStatusProvider {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &controlPlaneStatusProvider{repository: repository, consensus: reader, startedAt: startedAt.UTC(), now: time.Now}
}

func (provider *controlPlaneStatusProvider) ControlPlaneStatus(ctx context.Context) (api.ControlPlaneStatus, error) {
	status := api.ControlPlaneStatus{
		Mode: "standalone", Role: "standalone", Ready: true, ReadinessReason: "ready",
		StartedAt: provider.startedAt, DataNodeMembers: []api.DataNodeMemberStatus{},
	}
	if provider.now != nil {
		status.UptimeSeconds = int64(provider.now().UTC().Sub(provider.startedAt).Seconds())
		if status.UptimeSeconds < 0 {
			status.UptimeSeconds = 0
		}
	}
	if provider.repository != nil {
		status.StateRevision = provider.repository.StateRevision()
		status.ClusterCount = len(provider.repository.Clusters())
		status.DataNodeMembers = api.ActiveDataNodeMembers(provider.repository.Nodes())
		for _, operation := range provider.repository.Operations("") {
			switch operation.Status {
			case model.OperationRunning:
				status.ActiveOperations++
			case model.OperationIndeterminate:
				if operation.RequiresReview() {
					status.IndeterminateOperations++
				}
			}
		}
		for _, task := range provider.repository.LifecycleTasks() {
			switch task.Status {
			case lifecycle.TaskPlanned, lifecycle.TaskQueued, lifecycle.TaskRunning, lifecycle.TaskVerifying:
				status.ActiveLifecycleTasks++
			}
		}
	}
	if provider.consensus == nil {
		return status, nil
	}
	raftStatus := provider.consensus.Status(ctx)
	if !raftStatus.Enabled {
		return status, nil
	}
	status.Mode = "raft"
	status.LocalControllerID = raftStatus.LocalControllerID
	status.Role = raftStatus.Role
	status.LeaderID = raftStatus.LeaderID
	status.LeaderAddress = raftStatus.LeaderAddress
	status.LeaderAPIAddress = raftStatus.LeaderAPIAddress
	status.LeaderKnown = raftStatus.LeaderKnown
	status.VoterCount = raftStatus.VoterCount
	status.QuorumConfirmed = raftStatus.QuorumConfirmed
	status.MutationAuthority = raftStatus.MutationAuthority
	status.SnapshotCASActive = raftStatus.SnapshotCASActive
	status.ReplicatedLogCompressionActive = raftStatus.ReplicatedLogCompressionActive
	status.Term = raftStatus.Term
	status.LastIndex = raftStatus.LastIndex
	status.CommitIndex = raftStatus.CommitIndex
	status.AppliedIndex = raftStatus.AppliedIndex
	if membership, ok := provider.consensus.(consensusMembershipReader); ok {
		members, err := membership.ControllerMembers(ctx)
		if err == nil {
			status.ControllerMembers = make([]api.ControllerMemberStatus, 0, len(members))
			for _, member := range members {
				status.ControllerMembers = append(status.ControllerMembers, api.ControllerMemberStatus{
					ResourceID: member.ResourceID, RaftAddress: member.Address, APIAddress: member.APIAddress,
				})
			}
		}
	}
	status.Ready, status.ReadinessReason = raftReadiness(raftStatus)
	return status, nil
}

func raftReadiness(status consensus.Status) (bool, string) {
	if !status.Enabled {
		return false, "controller_state_unavailable"
	}
	if !status.LeaderKnown {
		return false, "leader_unavailable"
	}
	if status.CommitIndex > status.AppliedIndex {
		return false, "metadata_catchup"
	}
	switch strings.ToLower(status.Role) {
	case "leader":
		if !status.QuorumConfirmed || !status.MutationAuthority {
			return false, "quorum_unavailable"
		}
		return true, "ready"
	case "follower":
		if strings.TrimSpace(status.LeaderAPIAddress) == "" {
			return false, "leader_api_unavailable"
		}
		return true, "ready"
	default:
		return false, "leader_election_in_progress"
	}
}
