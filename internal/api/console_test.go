package api

import (
	"regexp"
	"strconv"
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
	for _, contract := range []string{
		`id="control-token"`,
		`type="password"`,
		`autocomplete="off"`,
		"'Authorization': `Bearer ${controlToken}`",
		"byId('control-token').value.trim()",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("refresh topology is missing in-memory control authentication contract %q", contract)
		}
	}
	if strings.Contains(page, "window.prompt(") || strings.Contains(page, "localStorage") || strings.Contains(page, "sessionStorage") {
		t.Fatal("control token must use a non-persistent inline password field")
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

func TestConsoleInvalidatesEverySelectionRequestAndClearsBeforeFetching(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"requestGeneration: 0",
		"const generation = ++state.requestGeneration;",
		"clearClusterView();",
		"const commitIfCurrent = (generation, commit) =>",
		"if (generation !== state.requestGeneration) return false;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing request-generation contract %q", contract)
		}
	}

	loadStart := strings.Index(page, "const loadSelectedCluster = async () => {")
	loadEnd := strings.Index(page, "const loadClusters = async () => {")
	if loadStart < 0 || loadEnd <= loadStart {
		t.Fatal("loadSelectedCluster source not found")
	}
	loadSource := page[loadStart:loadEnd]
	begin := strings.Index(loadSource, "const generation = beginClusterRequest(clusterId);")
	firstAwait := strings.Index(loadSource, "await ")
	if begin < 0 || firstAwait < 0 || begin > firstAwait {
		t.Fatal("cluster request generation and stale-view clearing must happen before the first await")
	}
	if strings.Count(loadSource, "commitIfCurrent(generation") < 2 {
		t.Fatal("both successful data and error status must commit through the generation guard")
	}
}

func TestConsoleRefreshInvalidatesInFlightClusterReadsBeforePosting(t *testing.T) {
	page := string(consoleHTML)
	refreshStart := strings.Index(page, "const refreshTopology = async () => {")
	if refreshStart < 0 {
		t.Fatal("refreshTopology source not found")
	}
	refreshEndOffset := strings.Index(page[refreshStart:], "byId('cluster-select').addEventListener")
	if refreshEndOffset <= 0 {
		t.Fatal("refreshTopology source end not found")
	}
	refreshEnd := refreshStart + refreshEndOffset
	refreshSource := page[refreshStart:refreshEnd]
	tokenValidation := strings.Index(refreshSource, "const controlToken = byId('control-token').value.trim();")
	invalidate := strings.Index(refreshSource, "const generation = ++state.requestGeneration;")
	post := strings.Index(refreshSource, "await fetchResult(")
	if tokenValidation < 0 || invalidate < 0 || post < 0 || tokenValidation > invalidate || invalidate > post {
		t.Fatal("refresh must validate the token, then invalidate in-flight reads before the discovery POST")
	}
	if strings.Contains(refreshSource, "const generation = state.requestGeneration;") {
		t.Fatal("refresh must not reuse a generation owned by an earlier cluster read")
	}
}

func TestConsolePreservesAPIErrorsAndOnlyToleratesExplicitNoEvidenceConflicts(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"class APIError extends Error",
		"this.status = status;",
		"throw new APIError('控制 API 返回了无效数据', response.status);",
		"error.status === 409 && noEvidenceMessages.has(error.message)",
		"throw error;",
		"const unavailableSections =",
		"部分数据不可用",
		"候选评估",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing selective API error contract %q", contract)
		}
	}
	if strings.Contains(page, "const optionalResult =") || strings.Contains(page, "catch (_) {\n        return fallback;") {
		t.Fatal("console must not convert every optional request failure into fallback data")
	}
}

func TestConsoleAggregatesQPSOnlyWhenFiniteEvidenceExists(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const qpsSamples = metricInstances",
		".filter(value => Number.isFinite(value));",
		"const qps = qpsSamples.reduce",
		"qpsSamples.length ? qps.toFixed(1) : '-'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing finite-QPS evidence contract %q", contract)
		}
	}
	if strings.Contains(page, "metricInstances.length ? qps.toFixed(1) : '-'") {
		t.Fatal("instance presence must not be treated as QPS evidence")
	}
}

func TestConsoleHidesCenterAndBranchLinesWithoutAPrimary(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="topology-grid"`,
		".topology-grid.no-primary .replica-row::before { display:none; }",
		"classList.toggle('no-primary', !primary)",
		"byId('connector').hidden = !primary || replicas.length === 0;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing no-primary connector contract %q", contract)
		}
	}
}

func TestConsoleUsesCompactTopologyAndLocalizedHealthAndLag(t *testing.T) {
	page := string(consoleHTML)
	match := regexp.MustCompile(`\.topology-surface \{ min-height:(\d+)px;`).FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatal("console topology surface must declare a stable compact desktop min-height")
	}
	height, err := strconv.Atoi(match[1])
	if err != nil || height < 190 || height > 210 {
		t.Fatalf("desktop topology min-height = %q, want 190-210px", match[1])
	}
	for _, contract := range []string{
		"healthy: '健康'",
		"degraded: '降级'",
		"unhealthy: '异常'",
		"unknown: '未知'",
		"healthText(health.health.state)",
		"if (instance.role === 'primary') return '-';",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing localized display contract %q", contract)
		}
	}
}
