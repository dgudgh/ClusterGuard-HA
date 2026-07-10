package api

import (
	"regexp"
	"strings"
	"testing"
)

func TestConsoleIsACompactReadOnlyMySQLTopologyView(t *testing.T) {
	page := string(consoleHTML)
	for _, label := range []string{"主库", "候选节点", "从库", "延迟", "版本", "IP", "端口", "VIP"} {
		if !strings.Contains(page, label) {
			t.Fatalf("console missing required label %q", label)
		}
	}
	for _, forbidden := range []string{"切换", "执行", "故障转移", "修复", "审批", "操作锁", "failover", "switchover"} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(forbidden)) {
			t.Fatalf("read-only console contains mutation term %q", forbidden)
		}
	}
	buttons := regexp.MustCompile(`(?i)<button\b`).FindAllStringIndex(page, -1)
	if len(buttons) != 1 || !strings.Contains(page, `id="refresh-topology"`) || !strings.Contains(page, "刷新拓扑") {
		t.Fatalf("console must contain exactly one database action button named 刷新拓扑; buttons=%d", len(buttons))
	}
}

func TestConsoleConsumesSelectedClusterReadAPIsAndPostsDiscovery(t *testing.T) {
	page := string(consoleHTML)
	for _, route := range []string{
		"/api/v1/clusters",
		"/api/v1/clusters/${clusterId}",
		"/api/v1/clusters/${clusterId}/topology",
		"/api/v1/clusters/${clusterId}/health",
		"/api/v1/clusters/${clusterId}/candidates",
		"/api/v1/clusters/${clusterId}/metrics",
	} {
		if !strings.Contains(page, route) {
			t.Fatalf("console does not consume %s", route)
		}
	}
	if !strings.Contains(page, "method: 'POST'") || !strings.Contains(page, "body: '{}'") || !strings.Contains(page, "/discover") {
		t.Fatal("refresh topology must POST an exact empty JSON object to the discovery route")
	}
	if !strings.Contains(page, "health.health.state") {
		t.Fatal("console must consume the current health response shape")
	}
}

func TestConsoleShowsVIPOnlyForAnActiveRegisteredVIPEndpoint(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"endpoint.active && endpoint.kind === 'vip'",
		"vipRow.hidden = !vipEndpoint",
		"clusterDetail.endpoints",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing conditional VIP contract %q", contract)
		}
	}
}

func TestConsoleBuildsAPIDerivedContentWithoutUnsafeHTMLInsertion(t *testing.T) {
	page := string(consoleHTML)
	if strings.Contains(page, ".innerHTML") || strings.Contains(page, "insertAdjacentHTML") || strings.Contains(page, "document.write") {
		t.Fatal("console must not insert API-derived strings as HTML")
	}
	for _, contract := range []string{"textContent", `aria-live="polite"`, `aria-label="选择数据库集群"`, ":focus-visible"} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing safe or accessible rendering contract %q", contract)
		}
	}
	if strings.Contains(page, "<script src=") || strings.Contains(page, "<link rel=\"stylesheet\"") {
		t.Fatal("console must not load external assets")
	}
}

func TestConsoleTopologyUsesFixedColumnsAndResponsiveConnectorRules(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"grid-template-columns:minmax(0,1fr) 72px minmax(0,1fr)",
		"border-radius:8px",
		"@media (max-width:900px)",
		".connector { display:none; }",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing topology layout contract %q", contract)
		}
	}
}
