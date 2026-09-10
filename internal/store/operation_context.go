package store

import (
	"sort"

	"clusterguard.io/ha/pkg/model"
)

// OperationConsoleContext is a read projection, never an execution authorization.
// Counts and historical identities cover every record; only the overview's recent
// activity is bounded. Full history remains in Operations and Operation.
type OperationConsoleContext struct {
	OperationCount      int
	RunningCount        int
	UnreviewedCount     int
	HistoricalSourceIDs []model.ResourceID
	Recent              []model.OperationRecord
}

func (repository *Repository) OperationConsoleContext(clusterID model.ResourceID) OperationConsoleContext {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := OperationConsoleContext{HistoricalSourceIDs: []model.ResourceID{}, Recent: []model.OperationRecord{}}
	sources := make(map[model.ResourceID]struct{})
	for _, record := range repository.snapshot.Operations {
		if clusterID != "" && record.Operation.ClusterID != clusterID {
			continue
		}
		result.OperationCount++
		if record.Status == model.OperationRunning {
			result.RunningCount++
		}
		if record.RequiresReview() {
			result.UnreviewedCount++
		}
		if clusterID != "" {
			if record.Plan.SourceID != "" && (record.Operation.Kind == model.OperationSwitchover || record.Operation.Kind == model.OperationFailover) {
				sources[record.Plan.SourceID] = struct{}{}
			}
			continue
		}
		// Select the newest five without cloning/sorting the full evidence history.
		position := sort.Search(len(result.Recent), func(index int) bool {
			other := result.Recent[index]
			return record.CreatedAt.After(other.CreatedAt) || (record.CreatedAt.Equal(other.CreatedAt) && record.ResourceID > other.ResourceID)
		})
		if position >= 5 {
			continue
		}
		item := model.OperationRecord{
			ResourceMeta: record.ResourceMeta, Operation: record.Operation, TargetID: record.TargetID,
			Status: record.Status, Stage: record.Stage, FailureClass: record.FailureClass,
			Plan: model.OperationPlan{SourceID: record.Plan.SourceID},
		}
		if record.Review != nil {
			review := *record.Review
			item.Review = &review
		}
		result.Recent = append(result.Recent, model.OperationRecord{})
		copy(result.Recent[position+1:], result.Recent[position:])
		result.Recent[position] = item
		if len(result.Recent) > 5 {
			result.Recent = result.Recent[:5]
		}
	}
	for source := range sources {
		result.HistoricalSourceIDs = append(result.HistoricalSourceIDs, source)
	}
	sort.Slice(result.HistoricalSourceIDs, func(i, j int) bool { return result.HistoricalSourceIDs[i] < result.HistoricalSourceIDs[j] })
	return result
}
