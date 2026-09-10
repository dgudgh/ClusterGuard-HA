package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestOperationConsoleContextCoversEntireHistory(t *testing.T) {
	repository := NewMemory()
	clusters := []model.ResourceID{model.NewResourceID(), model.NewResourceID()}
	source := model.NewResourceID()
	now := time.Now().UTC()
	for i := 0; i < 1000; i++ {
		id := model.ResourceID(fmt.Sprintf("operation-%04d", i))
		record := model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: id, CreatedAt: now.Add(time.Duration(i) * time.Second)},
			Operation:    model.Operation{ClusterID: clusters[i%2], Kind: model.OperationSwitchover},
			Status:       model.OperationSucceeded,
			Plan:         model.OperationPlan{SourceID: source, Steps: []model.PlanStep{{Postcondition: strings.Repeat("evidence", 1000)}}},
		}
		if i == 0 {
			record.Status = model.OperationRunning
			record.Plan.SourceID = "oldest-primary"
		}
		if i == 1 || i == 998 {
			record.Status = model.OperationIndeterminate
		}
		if i == 998 {
			record.Review = &model.OperationReview{Note: "immutable review"}
		}
		repository.snapshot.Operations[id] = record
	}
	for pass := 0; pass < 5; pass++ {
		global := repository.OperationConsoleContext("")
		if global.OperationCount != 1000 || global.RunningCount != 1 || global.UnreviewedCount != 1 || len(global.Recent) != 5 || len(global.HistoricalSourceIDs) != 0 {
			t.Fatalf("incomplete global context: %+v", global)
		}
		for index, record := range global.Recent {
			if record.ResourceID != model.ResourceID(fmt.Sprintf("operation-%04d", 999-index)) || len(record.Plan.Steps) != 0 {
				t.Fatalf("recent activity order/evidence mismatch: %+v", record)
			}
		}
		global.Recent[1].Review.Note = "caller mutation"
		if repository.snapshot.Operations["operation-0998"].Review.Note != "immutable review" {
			t.Fatal("context aliases review")
		}
		first := repository.OperationConsoleContext(clusters[0])
		if first.OperationCount != 500 || first.RunningCount != 1 || first.UnreviewedCount != 0 || len(first.Recent) != 0 {
			t.Fatalf("wrong cluster context: %+v", first)
		}
		if len(first.HistoricalSourceIDs) != 2 || !containsResourceID(first.HistoricalSourceIDs, "oldest-primary") {
			t.Fatal("old primary beyond recent activity was lost")
		}
		second := repository.OperationConsoleContext(clusters[1])
		if second.OperationCount != 500 || second.RunningCount != 0 || second.UnreviewedCount != 1 || !reflect.DeepEqual(second.HistoricalSourceIDs, []model.ResourceID{source}) {
			t.Fatal("cluster context leaked identities or missed old pending operation")
		}
	}
	if len(repository.Operations("")) != 1000 || len(repository.Operations(clusters[0])[0].Plan.Steps) != 1 {
		t.Fatal("context read truncated full history/evidence")
	}
	empty := repository.OperationConsoleContext(model.NewResourceID())
	if empty.OperationCount != 0 || empty.HistoricalSourceIDs == nil || empty.Recent == nil {
		t.Fatal("empty context must have arrays")
	}
}

func containsResourceID(ids []model.ResourceID, expected model.ResourceID) bool {
	for _, id := range ids {
		if id == expected {
			return true
		}
	}
	return false
}

func TestOperationConsoleContextTiedTimestampsAndKinds(t *testing.T) {
	repository := NewMemory()
	cluster := model.NewResourceID()
	for i := 0; i < 8; i++ {
		id := model.ResourceID(fmt.Sprintf("id-%d", i))
		kind := model.OperationReplicationRepair
		if i == 0 {
			kind = model.OperationFailover
		}
		repository.snapshot.Operations[id] = model.OperationRecord{ResourceMeta: model.ResourceMeta{ResourceID: id}, Operation: model.Operation{ClusterID: cluster, Kind: kind}, Plan: model.OperationPlan{SourceID: id}}
	}
	for i := 0; i < 10; i++ {
		context := repository.OperationConsoleContext("")
		if context.Recent[0].ResourceID != "id-7" || context.Recent[4].ResourceID != "id-3" {
			t.Fatal("nondeterministic recent selection")
		}
	}
	if !reflect.DeepEqual(repository.OperationConsoleContext(cluster).HistoricalSourceIDs, []model.ResourceID{"id-0"}) {
		t.Fatal("non-primary operation source included")
	}
}
