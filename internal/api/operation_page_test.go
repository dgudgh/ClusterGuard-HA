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
