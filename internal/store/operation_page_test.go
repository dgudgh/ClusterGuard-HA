package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func pageTestRepository(count int) *Repository {
	repository := NewMemory()
	base := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		id := model.ResourceID(fmt.Sprintf("operation-%06d", i))
		cluster := model.ResourceID(fmt.Sprintf("cluster-%d", i%2))
		repository.snapshot.Operations[id] = model.OperationRecord{
			ResourceMeta: model.ResourceMeta{ResourceID: id, CreatedAt: base.Add(time.Duration(i/3) * time.Second)},
			Operation:    model.Operation{ClusterID: cluster, Engine: model.EnginePostgreSQL, Kind: model.OperationSwitchover},
			Status:       model.OperationSucceeded, TargetID: "target",
			Plan: model.OperationPlan{SourceID: "source", Steps: []model.PlanStep{{Postcondition: strings.Repeat("evidence ", 1000)}}},
		}
	}
	return repository
}

func TestOperationLogPageTraversesAllHistoryWithStableTies(t *testing.T) {
	repository := pageTestRepository(1003)
	query := OperationPageQuery{Limit: 20}
	seen := map[model.ResourceID]bool{}
	var previous *OperationPagePosition
	for {
		page, err := repository.OperationLogPage(query)
		if err != nil || page.Total != 1003 || page.RecordCount != 1003 || len(page.Items) > 20 || page.Remaining != 1003-len(seen)-len(page.Items) {
			t.Fatalf("invalid page: %+v %v", page, err)
		}
		for _, item := range page.Items {
			position := operationPosition(item.Record)
			if seen[item.Record.ResourceID] || (previous != nil && !operationPositionNewer(*previous, position)) || len(item.Record.Plan.Steps) != 0 || len(item.Record.Attempts) != 0 {
				t.Fatal("duplicate, unstable order, or full evidence in page")
			}
			seen[item.Record.ResourceID] = true
			previous = &position
		}
		if page.Next == nil {
			break
		}
		query.Before = page.Next
	}
	if len(seen) != 1003 || len(repository.Operations("")) != 1003 || len(repository.snapshot.Operations["operation-000000"].Plan.Steps) != 1 {
		t.Fatal("paging truncated history or evidence")
	}
}

func TestOperationLogPageFiltersAndNewInsert(t *testing.T) {
	repository := pageTestRepository(81)
	first, _ := repository.OperationLogPage(OperationPageQuery{Limit: 20})
	newest := repository.snapshot.Operations[first.Items[0].Record.ResourceID]
	newest.ResourceID = "newest"
	newest.CreatedAt = newest.CreatedAt.Add(time.Hour)
	repository.snapshot.Operations[newest.ResourceID] = newest
	second, _ := repository.OperationLogPage(OperationPageQuery{Limit: 20, Before: first.Next})
	if second.Total != 82 || second.Items[0].Record.ResourceID != "operation-000060" || second.Remaining != 41 {
		t.Fatal("insert shifted page boundary")
	}
	old := repository.snapshot.Operations["operation-000000"]
	old.Status = model.OperationFailed
	old.Operation.Kind = model.OperationFailover
	old.TargetID = "ancient-target"
	old.Plan.Checks = []model.Check{{Name: "guard", Status: model.CheckFail, Message: "immutable"}}
	old.Review = &model.OperationReview{Note: "immutable"}
	repository.snapshot.Operations[old.ResourceID] = old
	page, err := repository.OperationLogPage(OperationPageQuery{ClusterID: "cluster-0", Search: " ANCIENT-target ", Status: model.OperationFailed, Kind: model.OperationFailover})
	if err != nil || page.Total != 1 || page.Items[0].Record.ResourceID != old.ResourceID {
		t.Fatal("filters only searched first page", err)
	}
	page.Items[0].Record.Review.Note = "mutation"
	page.Items[0].Record.Plan.Checks[0].Message = "mutation"
	if old.Review.Note != "immutable" || old.Plan.Checks[0].Message != "immutable" {
		t.Fatal("page aliases evidence")
	}
	page, _ = repository.OperationLogPage(OperationPageQuery{Search: "evidence"})
	if page.Total != 0 {
		t.Fatal("metadata search read raw evidence")
	}
	for _, query := range []OperationPageQuery{{Limit: -1}, {Limit: 101}, {Kind: "invalid"}, {Status: "invalid"}, {Search: strings.Repeat("a", 257)}} {
		if _, err := repository.OperationLogPage(query); err == nil {
			t.Fatal("invalid filter accepted")
		}
	}
}

func TestOperationLogPageGroupsIncidentsBeforePagingAndFiltering(t *testing.T) {
	repository := pageTestRepository(45)
	for i := 0; i < 40; i++ {
		id := model.ResourceID(fmt.Sprintf("operation-%06d", i))
		record := repository.snapshot.Operations[id]
		record.Operation.RequestedBy = "clusterguard-automatic-recovery"
		record.Operation.Kind = model.OperationFailover
		record.IdempotencyKey = fmt.Sprintf("automatic-failover:shared-incident:%d", i+1)
		record.Status = model.OperationFailed
		if i >= 38 {
			record.Status = model.OperationSucceeded
		}
		repository.snapshot.Operations[id] = record
	}
	page, _ := repository.OperationLogPage(OperationPageQuery{Limit: 3})
	if page.Total != 7 || page.RecordCount != 45 || page.Remaining != 4 {
		t.Fatal("wrong incident count")
	}
	page, _ = repository.OperationLogPage(OperationPageQuery{Kind: model.OperationFailover, Status: model.OperationSucceeded})
	if len(page.Items) != 2 || page.Total != 2 || page.RecordCount != 40 {
		t.Fatal("lost incidents or merged across clusters")
	}
	for _, item := range page.Items {
		if item.Attempts != 20 || !item.FirstAt.Before(item.Record.CreatedAt) {
			t.Fatal("incident count cut at page boundary")
		}
	}
	page, _ = repository.OperationLogPage(OperationPageQuery{Kind: model.OperationFailover, Status: model.OperationFailed})
	if page.Total != 0 {
		t.Fatal("old failed attempt hid latest succeeded state")
	}
}

func BenchmarkOperationLogPage(b *testing.B) {
	repository := pageTestRepository(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repository.OperationLogPage(OperationPageQuery{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOperationLegacyFullClone(b *testing.B) {
	repository := pageTestRepository(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repository.Operations("")
	}
}
