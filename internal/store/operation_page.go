package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const DefaultOperationPageSize = 20
const MaximumOperationPageSize = 100

type OperationPagePosition struct {
	CreatedAt time.Time        `json:"created_at"`
	ID        model.ResourceID `json:"id"`
}

type OperationPageQuery struct {
	ClusterID model.ResourceID
	Kind      model.OperationKind
	Status    model.OperationStatus
	Search    string
	Limit     int
	Before    *OperationPagePosition
}

type OperationPageEntry struct {
	Record       model.OperationRecord
	Attempts     int
	FirstAt      time.Time
	ClusterLabel string
	SourceLabel  string
	TargetLabel  string
}

type OperationPage struct {
	Items       []OperationPageEntry
	Total       int
	RecordCount int
	Remaining   int
	Next        *OperationPagePosition
}

func operationPosition(record model.OperationRecord) OperationPagePosition {
	at := record.CreatedAt
	if at.IsZero() {
		at = record.Operation.CreatedAt
	}
	return OperationPagePosition{CreatedAt: at, ID: record.ResourceID}
}

func operationPositionNewer(left, right OperationPagePosition) bool {
	return left.CreatedAt.After(right.CreatedAt) || (left.CreatedAt.Equal(right.CreatedAt) && left.ID > right.ID)
}

func operationIncidentKey(record model.OperationRecord) string {
	key := record.IdempotencyKey
	if record.Operation.RequestedBy != "clusterguard-automatic-recovery" || !strings.HasPrefix(key, "automatic-failover:") {
		return ""
	}
	separator := strings.LastIndex(key, ":")
	if separator <= len("automatic-failover:") {
		return ""
	}
	attempt, err := strconv.Atoi(key[separator+1:])
	if err != nil || attempt < 1 {
		return ""
	}
	return string(record.Operation.ClusterID) + "\x00" + key[:separator]
}

// OperationLogPage scans only record metadata, retaining at most Limit candidates.
// Full execution evidence is neither cloned nor sorted on a list request.
func (repository *Repository) OperationLogPage(query OperationPageQuery) (OperationPage, error) {
	result := OperationPage{Items: []OperationPageEntry{}}
	if query.Limit == 0 {
		query.Limit = DefaultOperationPageSize
	}
	if query.Limit < 1 || query.Limit > MaximumOperationPageSize ||
		(query.Kind != "" && !validOperationKind(query.Kind)) ||
		(query.Status != "" && !validOperationStatus(query.Status)) || len(query.Search) > 256 {
		return result, validationError("invalid operation page filter or limit")
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	type selection struct {
		position OperationPagePosition
		attempts int
		firstAt  time.Time
	}
	selected := make([]selection, 0, query.Limit+1)
	incidents := make(map[string]selection)
	search := strings.ToLower(strings.TrimSpace(query.Search))
	address := func(id model.ResourceID) string {
		instance, found := repository.snapshot.Instances[id]
		if !found {
			return string(id)
		}
		host := instance.Hostname
		if host == "" {
			host = instance.IPAddress
		}
		if host == "" {
			host = instance.DisplayName
		}
		return fmt.Sprintf("%s:%d", host, instance.Port)
	}
	consider := func(candidate selection) {
		record := repository.snapshot.Operations[candidate.position.ID]
		if (query.Kind != "" && record.Operation.Kind != query.Kind) || (query.Status != "" && record.Status != query.Status) {
			return
		}
		if search != "" {
			cluster := repository.snapshot.Clusters[record.Operation.ClusterID]
			values := []string{cluster.DisplayName, string(record.Operation.ClusterID), string(record.Operation.Engine),
				string(record.Operation.Kind), string(record.Status), string(record.Plan.SourceID), string(record.TargetID), address(record.Plan.SourceID), address(record.TargetID)}
			for _, id := range []model.ResourceID{record.Plan.SourceID, record.TargetID} {
				instance := repository.snapshot.Instances[id]
				values = append(values, instance.Hostname, instance.IPAddress, instance.DisplayName)
			}
			if !strings.Contains(strings.ToLower(strings.Join(values, " ")), search) {
				return
			}
		}
		result.Total++
		result.RecordCount += candidate.attempts
		if query.Before != nil && !operationPositionNewer(*query.Before, candidate.position) {
			return
		}
		result.Remaining++
		position := sort.Search(len(selected), func(index int) bool { return operationPositionNewer(candidate.position, selected[index].position) })
		if position >= query.Limit {
			return
		}
		selected = append(selected, selection{})
		copy(selected[position+1:], selected[position:])
		selected[position] = candidate
		if len(selected) > query.Limit {
			selected = selected[:query.Limit]
		}
	}
	for _, record := range repository.snapshot.Operations {
		if query.ClusterID != "" && record.Operation.ClusterID != query.ClusterID {
			continue
		}
		position := operationPosition(record)
		candidate := selection{position: position, attempts: 1, firstAt: position.CreatedAt}
		key := operationIncidentKey(record)
		if key == "" {
			consider(candidate)
			continue
		}
		if previous, exists := incidents[key]; exists {
			candidate.attempts += previous.attempts
			if previous.firstAt.Before(candidate.firstAt) {
				candidate.firstAt = previous.firstAt
			}
			if operationPositionNewer(previous.position, position) {
				candidate.position = previous.position
			}
		}
		incidents[key] = candidate
	}
	for _, incident := range incidents {
		consider(incident)
	}
	for _, selected := range selected {
		record := repository.snapshot.Operations[selected.position.ID]
		item := model.OperationRecord{
			ResourceMeta: record.ResourceMeta, Operation: record.Operation, TargetID: record.TargetID,
			IdempotencyKey: record.IdempotencyKey, Status: record.Status, Stage: record.Stage,
			FailureClass: record.FailureClass, Message: record.Message,
			Plan:      model.OperationPlan{SourceID: record.Plan.SourceID},
			Execution: model.Execution{Status: record.Execution.Status, Message: record.Execution.Message},
		}
		if record.Review != nil {
			review := *record.Review
			item.Review = &review
		}
		for _, checks := range [][]model.Check{record.Plan.Checks, record.Precheck} {
			for _, check := range checks {
				if check.Status == model.CheckFail {
					item.Plan.Checks = []model.Check{{Name: check.Name, Status: check.Status, Message: check.Message}}
					break
				}
			}
			if len(item.Plan.Checks) > 0 {
				break
			}
		}
		result.Items = append(result.Items, OperationPageEntry{Record: item, Attempts: selected.attempts, FirstAt: selected.firstAt,
			ClusterLabel: repository.snapshot.Clusters[record.Operation.ClusterID].DisplayName,
			SourceLabel:  address(record.Plan.SourceID), TargetLabel: address(record.TargetID)})
	}
	result.Remaining -= len(result.Items)
	if result.Remaining > 0 && len(selected) > 0 {
		position := selected[len(selected)-1].position
		result.Next = &position
	}
	return result, nil
}
