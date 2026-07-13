package api

import (
	"strings"
	"testing"
)

func consoleView(t *testing.T, name string) string {
	t.Helper()
	page := string(consoleHTML)
	startMarker := `data-view="` + name + `"`
	start := strings.Index(page, startMarker)
	if start < 0 {
		t.Fatalf("console view %q not found", name)
	}
	start = strings.LastIndex(page[:start], "<section")
	if start < 0 {
		t.Fatalf("console view %q section start not found", name)
	}
	rest := page[start+len("<section"):]
	endOffset := strings.Index(rest, "<section")
	if endOffset < 0 {
		endOffset = strings.Index(rest, "</main>")
	}
	if endOffset < 0 {
		t.Fatalf("console view %q section end not found", name)
	}
	return page[start : start+len("<section")+endOffset]
}

func TestConsoleUsesClusterGuardIdentityAndContainsNoLegacyProductIdentity(t *testing.T) {
	page := string(consoleHTML)
	for _, identity := range []string{
		"<title>ClusterGuard HA Console</title>",
		"ClusterGuard HA Console",
		"Multi-DB HA Control",
		"多数据库高可用控制平台",
	} {
		if !strings.Contains(page, identity) {
			t.Fatalf("console missing product identity %q", identity)
		}
	}
	for _, forbidden := range []string{"orchestrator", "orchctl", "Enterprise HA", "MySQL Control"} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(forbidden)) {
			t.Fatalf("console contains legacy product identity %q", forbidden)
		}
	}
}

func TestConsoleHasEightFocusedViewsWithChineseAsDefault(t *testing.T) {
	page := string(consoleHTML)
	if !strings.Contains(page, `<html lang="zh-CN">`) || !strings.Contains(page, `language: 'zh-CN'`) {
		t.Fatal("console must start in Chinese")
	}
	for _, view := range []struct{ id, label string }{
		{"overview", "总览"}, {"topology", "拓扑"}, {"operations", "操作"}, {"nodes", "节点"},
		{"metrics", "指标"}, {"operation-log", "操作日志"}, {"about", "关于"}, {"settings", "设置"},
	} {
		if !strings.Contains(page, `data-nav="`+view.id+`"`) || !strings.Contains(page, `data-view="`+view.id+`"`) {
			t.Fatalf("console missing %s navigation/view", view.id)
		}
		if !strings.Contains(page, view.label) {
			t.Fatalf("console missing Chinese label %q", view.label)
		}
	}
	if !strings.Contains(page, "const translations =") || !strings.Contains(page, "'en-US'") || !strings.Contains(page, `id="language-select"`) {
		t.Fatal("console must provide Chinese/English translation controls")
	}
}

func TestOverviewContainsFleetSummaryButNoTopologyGraph(t *testing.T) {
	view := consoleView(t, "overview")
	for _, label := range []string{"集群总数", "健康集群", "异常集群", "数据库实例", "控制节点", "待处理操作"} {
		if !strings.Contains(view, label) {
			t.Fatalf("overview missing %q", label)
		}
	}
	for _, forbidden := range []string{"topology-grid", "复制拓扑", "node-card"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("overview must not render topology content %q", forbidden)
		}
	}
}

func TestOverviewAggregatesEveryRegisteredClusterInsteadOfOnlySelection(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"clusterDirectory: new Map()", "const loadFleetDetails = async clusters =>", "clusters.map(async cluster =>",
		"fetchResult(`/api/v1/clusters/${cluster.resource_id}`)", "state.clusterDirectory.get(cluster.resource_id)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("overview missing all-cluster aggregation contract %q", contract)
		}
	}
}

func TestClusterSelectorUsesStableClusterNameOnly(t *testing.T) {
	page := string(consoleHTML)
	if !strings.Contains(page, "option.textContent = cluster.display_name || cluster.resource_id;") {
		t.Fatal("cluster selector must display only the stable cluster name")
	}
	for _, forbidden := range []string{
		"`${cluster.display_name} (", "cluster.hostname", "cluster.primary", "cluster.endpoint",
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("cluster selector must not append mutable endpoint text: %q", forbidden)
		}
	}
}

func TestTopologyShowsStableAndNativeIdentityAndMetadataModal(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "topology")
	for _, label := range []string{"固定节点名", "资源 ID", "server_uuid", "主机名", "IP", "端口", "版本", "角色", "延迟", "VIP"} {
		if !strings.Contains(view, label) {
			t.Fatalf("topology missing identity label %q", label)
		}
	}
	for _, contract := range []string{
		`id="open-metadata-modal"`, `role="dialog"`, `aria-modal="true"`, `id="metadata-modal"`,
		"instance.node_id", "instance.resource_id", "instance.engine_identity.server_uuid",
		"/api/v1/metadata/reconcile/precheck", "/api/v1/metadata/reconcile/execute",
		"/api/v1/clusters/${instance.cluster_id}/discover",
		"endpoint_id: endpoint.resource_id", "metadata_revision",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing topology metadata contract %q", contract)
		}
	}
	if !strings.Contains(view, "修改元数据") {
		t.Fatal("metadata action must be scoped to the topology view")
	}
}

func TestOperationsViewRunsOneRealGuardedSwitchover(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "operations")
	for _, label := range []string{"集群", "当前主库", "候选主库", "VIP", "延迟", "执行切换", "旧主回挂恢复"} {
		if !strings.Contains(view, label) {
			t.Fatalf("operations view missing %q", label)
		}
	}
	if strings.Count(view, `id="execute-switchover"`) != 1 {
		t.Fatal("controlled switch area must expose one execution button")
	}
	for _, contract := range []string{
		"/api/v1/operations/execute", "executeOperation('switchover'", "target_id:", "state.selectedCandidateId",
		"approval_token: state.approvalToken", "idempotency_key:", "requested_by: state.operator",
		"'Authorization': `Bearer ${state.controlToken}`", "await loadSelectedCluster()", "await loadOperationLog()",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console switch is not wired to the real workflow: missing %q", contract)
		}
	}
	for _, forbidden := range []string{"dry-run", "演练模式", "模拟切换", "fakeExecute", "setTimeout(() => success"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("console must not expose simulated execution %q", forbidden)
		}
	}
}

func TestOperationsUseSimpleLocalAntiMistakeLockWithoutBypassingBackendGates(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="switch-lock"`, "switchUnlocked: false", "state.switchUnlocked = !state.switchUnlocked",
		"execute.disabled = !state.switchUnlocked", "操作锁定", "已解锁",
		"后端仍会执行 Safety Guard、集群操作锁和审批校验",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing anti-mistake lock contract %q", contract)
		}
	}
}

func TestConsoleExplainsUnavailableExecutionAndFiltersFormerPrimaryCandidates(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const capabilityReason =", "当前环境不可执行：${capabilityReason('execute')}",
		"const formerPrimaryCandidates =", "historicalSourceIDs.has(instance.resource_id)",
		"instance.maintenance || instance.health.state !== 'healthy'", "replication.io_thread !== 'running'", "replication.sql_thread !== 'running'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing unavailable-action or former-primary filter contract %q", contract)
		}
	}
}

func TestFormerPrimaryRejoinAndNodeLifecycleUseRealAPIs(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"executeOperation('former_primary_rejoin'", `id="execute-rejoin"`,
		"/api/v1/nodes/sync/capabilities", "/api/v1/nodes/sync/precheck", "/api/v1/nodes/sync/execute", "/api/v1/nodes/sync/tasks",
		`value="data"`, `value="controller"`, `value="mixed"`, `value="rebuild"`,
		`id="node-name"`, `id="node-resource-id"`, `id="sync-method"`,
		"控制节点最终数量必须为大于等于 3 的奇数", "修复节点复用原资源 ID 和固定节点名", "state.lifecycleCapability.available",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing real node/rejoin contract %q", contract)
		}
	}
}

func TestOperationLogShowsUsefulSummaryAndKeepsRawEvidenceCollapsed(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "operation-log")
	for _, label := range []string{"时间", "集群", "原主库", "目标节点", "操作类型", "状态", "原始返回"} {
		if !strings.Contains(view, label) {
			t.Fatalf("operation log missing %q", label)
		}
	}
	for _, contract := range []string{
		"/api/v1/operations?cluster_id=${state.selectedClusterId}",
		"operation.plan.source_id", "operation.target_id", "operation.operation.kind", "operation.status",
		"document.createElement('details')", "document.createElement('summary')", "JSON.stringify(operation, null, 2)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation log missing contract %q", contract)
		}
	}
	if strings.Contains(page, "details.open = true") {
		t.Fatal("raw operation evidence must be collapsed by default")
	}
}

func TestConsoleConsumesCoherentClusterEvidenceAndRealMonitoringAPIs(t *testing.T) {
	page := string(consoleHTML)
	for _, route := range []string{
		"/api/v1/clusters", "/api/v1/clusters/${clusterId}", "/api/v1/clusters/${clusterId}/topology",
		"/api/v1/clusters/${clusterId}/health", "/api/v1/clusters/${clusterId}/candidates",
		"/api/v1/clusters/${clusterId}/metrics", "/api/v1/nodes", "/api/v1/capabilities",
	} {
		if !strings.Contains(page, route) {
			t.Fatalf("console does not consume %s", route)
		}
	}
	for _, contract := range []string{
		"requestGeneration: 0", "const generation = ++state.requestGeneration;", "clearClusterView();",
		"const observationQuery = `observation_id=${encodeURIComponent(topology.observed_at)}`;",
		"/health?${observationQuery}", "/candidates?${observationQuery}", "/metrics?${observationQuery}",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing coherent evidence contract %q", contract)
		}
	}
}

func TestConsoleUsesNativeMetricNamesAndFormatsBufferRatioAsPercent(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{"running_threads", "slow_queries_per_second", "buffer_pool_hit_ratio", "const formatPercent =", "`${(value * 100).toFixed(1)}%`"} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing native metric display contract %q", contract)
		}
	}
	if strings.Contains(page, "threads_running") {
		t.Fatal("console uses the wrong running-thread metric name")
	}
}

func TestSecretsStayInMemoryAndAPIDerivedTextUsesSafeDOM(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="control-token"`, `id="approval-token"`, `type="password"`, `autocomplete="off"`,
		"state.controlToken =", "state.approvalToken =", "textContent", `aria-live="polite"`, ":focus-visible",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing secure rendering contract %q", contract)
		}
	}
	for _, forbidden := range []string{".innerHTML", "insertAdjacentHTML", "document.write", "localStorage", "sessionStorage", "window.prompt("} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("console contains unsafe or persistent client behavior %q", forbidden)
		}
	}
	if strings.Contains(page, "<script src=") || strings.Contains(page, `<link rel="stylesheet"`) {
		t.Fatal("console must remain self-contained")
	}
}

func TestConsoleUsesCompactAlignedResponsiveEnterpriseLayout(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"--radius:8px", "grid-template-columns:220px minmax(0,1fr)", "border-radius:8px",
		"grid-template-columns:minmax(0,1fr) 64px minmax(0,1fr)", "@media (max-width:900px)", "grid-template-columns:minmax(0,1fr);",
		".topology-connector { display:none; }", "letter-spacing:0",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing layout contract %q", contract)
		}
	}
}

func TestConsoleContainsLongIdentityWithinMobileViewport(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		".node-card {", "min-width:0;", ".identity-line { width:100%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }",
		".panel-body { padding:18px; min-width:0; overflow-x:auto; }", ".inventory-table, .metrics-table { min-width:900px;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing mobile overflow constraint %q", contract)
		}
	}
}
