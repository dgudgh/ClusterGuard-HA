package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestOperationSummaryRetainsEveryRecordAndLazyDetail(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	clusters := []model.ResourceID{model.NewResourceID(), model.NewResourceID()}
	source := model.NewResourceID()
	var first model.ResourceID
	for index := 0; index < 12; index++ {
		record, _, err := repository.CreateOperation(model.OperationRecord{
			Operation: model.Operation{ClusterID: clusters[index%2], Engine: model.EngineMySQL, Kind: model.OperationSwitchover, RequestedBy: "operator"},
			TargetID:  model.NewResourceID(), IdempotencyKey: fmt.Sprintf("summary-%d", index),
			Plan: model.OperationPlan{SourceID: source, Steps: []model.PlanStep{{Index: 1, Name: "verify", Owner: "controller", Postcondition: strings.Repeat("full evidence ", 5000)}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = record.ResourceID
		}
	}
	full := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations", nil)
	summary := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=summary", nil)
	if full.Code != http.StatusOK || summary.Code != http.StatusOK {
		t.Fatal("operation lists failed")
	}
	var envelope struct {
		Result []operationListItem `json:"result"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result) != 12 {
		t.Fatalf("summary truncated history: %d", len(envelope.Result))
	}
	for _, item := range envelope.Result {
		if !item.Summary || item.Plan.SourceID != source || item.IdempotencyKey == "" {
			t.Fatalf("missing list identity: %+v", item)
		}
	}
	if summary.Body.Len()*10 >= full.Body.Len() {
		t.Fatalf("summary not lightweight: summary=%d full=%d", summary.Body.Len(), full.Body.Len())
	}
	filtered := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=summary&cluster_id="+string(clusters[0]), nil)
	if err := json.Unmarshal(filtered.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Result) != 6 {
		t.Fatalf("cluster history truncated: %d", len(envelope.Result))
	}
	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(first), nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "full evidence") {
		t.Fatal("lazy detail lost full evidence")
	}
	if len(repository.Operations("")) != 12 {
		t.Fatal("summary read mutated history")
	}
	for _, clusterID := range []model.ResourceID{"", clusters[0]} {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=context&cluster_id="+string(clusterID), nil)
		var context struct {
			Result struct {
				View                string              `json:"view"`
				ClusterID           model.ResourceID    `json:"cluster_id"`
				OperationCount      int                 `json:"operation_count"`
				HistoricalSourceIDs []model.ResourceID  `json:"historical_source_ids"`
				Recent              []operationListItem `json:"recent"`
			} `json:"result"`
		}
		if response.Code != http.StatusOK {
			t.Fatalf("context failed: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &context); err != nil {
			t.Fatal(err)
		}
		if context.Result.View != "context" || context.Result.ClusterID != clusterID {
			t.Fatal("context discriminator/scope missing")
		}
		if clusterID == "" {
			if context.Result.OperationCount != 12 || len(context.Result.Recent) != 5 {
				t.Fatal("global counts/activity incomplete")
			}
		} else {
			if context.Result.OperationCount != 6 || len(context.Result.HistoricalSourceIDs) != 1 || context.Result.HistoricalSourceIDs[0] != source || len(context.Result.Recent) != 0 {
				t.Fatal("cluster projection incomplete")
			}
			if response.Body.Len() > 512 {
				t.Fatalf("cluster context too large: %d", response.Body.Len())
			}
		}
		if strings.Contains(response.Body.String(), "full evidence") {
			t.Fatal("context included audit payload")
		}
	}
	if len(repository.Operations("")) != 12 {
		t.Fatal("context read mutated history")
	}
	invalid := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=context&cluster_id=invalid", nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatal("context bypassed cluster ID validation")
	}
	t.Logf("all 12 records: full=%d bytes summary=%d bytes", full.Body.Len(), summary.Body.Len())
}

func TestOperationSummaryPreservesFailureAndReviewWithoutSecrets(t *testing.T) {
	record := model.OperationRecord{
		Status: model.OperationFailed, Message: "password=do-not-expose",
		Plan:   model.OperationPlan{SourceID: model.NewResourceID(), Checks: []model.Check{{Name: "replication", Status: model.CheckFail, Message: "password=do-not-expose"}}},
		Review: &model.OperationReview{ReviewedBy: "operator", Note: "token=do-not-expose"},
	}
	summary := summarizeOperation(record)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "do-not-expose") {
		t.Fatal("summary leaked secret")
	}
	if summary.Status != record.Status || summary.Review == nil || len(summary.Plan.Checks) != 1 || summary.Plan.Checks[0].Status != model.CheckFail {
		t.Fatal("summary lost failure or review")
	}
	if record.Review.Note != "token=do-not-expose" || record.Plan.Checks[0].Message != "password=do-not-expose" {
		t.Fatal("projection mutated stored evidence")
	}
}

func TestOperationLogPageAPIAndLazyDetail(t *testing.T) {
	server, repository := newDurableOperationAPIServer(t)
	cluster := model.NewResourceID()
	for i := 0; i < 47; i++ {
		_, _, err := repository.CreateOperation(model.OperationRecord{
			Operation: model.Operation{ClusterID: cluster, Engine: model.EnginePostgreSQL, Kind: model.OperationSwitchover, RequestedBy: "operator"},
			TargetID:  model.NewResourceID(), IdempotencyKey: fmt.Sprintf("page-%d", i),
			Plan: model.OperationPlan{SourceID: model.NewResourceID(), Steps: []model.PlanStep{{Index: 1, Name: "verify", Owner: "controller", Postcondition: strings.Repeat("retained evidence ", 5000)}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var envelope struct {
		Result struct {
			View        string              `json:"view"`
			Items       []operationPageItem `json:"items"`
			Total       int                 `json:"total"`
			RecordCount int                 `json:"record_count"`
			Limit       int                 `json:"limit"`
			Remaining   int                 `json:"remaining"`
			Next        string              `json:"next_cursor"`
		} `json:"result"`
	}
	seen := map[model.ResourceID]bool{}
	cursor, firstCursor := "", ""
	var detailID model.ResourceID
	for _, count := range []int{20, 20, 7} {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=page&cursor="+url.QueryEscape(cursor), nil)
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil {
			t.Fatalf("page failed: %s", response.Body.String())
		}
		page := envelope.Result
		if page.View != "page" || len(page.Items) != count || page.Total != 47 || page.RecordCount != 47 || page.Limit != 20 || page.Remaining != 47-len(seen)-count {
			t.Fatal("invalid page metadata")
		}
		if strings.Contains(response.Body.String(), "retained evidence") || response.Body.Len() > 50000 {
			t.Fatal("page copied full evidence")
		}
		for _, item := range page.Items {
			if !item.Summary || seen[item.ResourceID] {
				t.Fatal("duplicate or missing summary marker")
			}
			seen[item.ResourceID] = true
			detailID = item.ResourceID
		}
		cursor = page.Next
		if firstCursor == "" {
			firstCursor = cursor
		}
	}
	if cursor != "" || len(seen) != 47 || len(repository.Operations("")) != 47 {
		t.Fatal("missing history or endless cursor")
	}
	detail := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations/"+string(detailID), nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "retained evidence") {
		t.Fatal("lazy detail lost audit evidence")
	}
	for _, suffix := range []string{"limit=0", "limit=101", "limit=bad", "kind=invalid", "status=invalid", "q=" + strings.Repeat("a", 257), "cursor=invalid", "cursor=" + strings.Repeat("A", 1025), "q=changed&cursor=" + url.QueryEscape(firstCursor), "cluster_id=" + string(cluster) + "&cursor=" + url.QueryEscape(firstCursor)} {
		response := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=page&"+suffix, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid filter: %s %d", suffix, response.Code)
		}
	}
	empty := callJSON(t, server.Handler(), http.MethodGet, "/api/v1/operations?view=page&q=missing", nil)
	if json.Unmarshal(empty.Body.Bytes(), &envelope) != nil || envelope.Result.Items == nil || len(envelope.Result.Items) != 0 || envelope.Result.Total != 0 || envelope.Result.Next != "" {
		t.Fatal("bad empty page")
	}
}
