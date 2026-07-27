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

func TestSettingsShowsAuthenticatedControlPlaneDiagnostics(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "settings")
	for _, label := range []string{"控制面状态", "本机控制器", "Raft 角色", "当前 Leader", "Quorum", "元数据版本", "运行时长", "活动操作", "节点任务"} {
		if !strings.Contains(view, label) {
			t.Fatalf("settings missing control-plane diagnostic %q", label)
		}
	}
	for _, contract := range []string{
		"controlPlane: null", "fetchResult('/api/v1/control-plane/status')", "renderControlPlaneStatus()",
		`id="control-plane-ready"`, `id="control-plane-local"`, `id="control-plane-role"`, `id="control-plane-leader"`,
		`id="control-plane-quorum"`, `id="control-plane-revision"`, `id="control-plane-uptime"`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing control-plane contract %q", contract)
		}
	}
}

func TestMobileNavigationKeepsHorizontalSwipeWithoutVisibleScrollbar(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{"scrollbar-width:none", "-ms-overflow-style:none", ".nav::-webkit-scrollbar { display:none; }"} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console mobile navigation missing %q", contract)
		}
	}
}

func TestMobileNavigationKeepsActiveViewVisible(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const keepActiveNavigationVisible = activeLink =>",
		"window.matchMedia('(max-width: 900px)').matches",
		"navigation.scrollTo({ left: targetLeft, behavior:'smooth' })",
		"keepActiveNavigationVisible(activeLink)",
		"window.requestAnimationFrame(() => keepActiveNavigationVisible(document.querySelector('[data-nav][aria-current=\"page\"]')))",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console mobile navigation missing active-view visibility contract %q", contract)
		}
	}
}

func TestConsoleAutoDismissesInformationalStatusButKeepsErrorsVisible(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"liveStatusTimer: null",
		"window.clearTimeout(state.liveStatusTimer)",
		"state.liveStatusTimer = window.setTimeout(() =>",
		"target.hidden = true",
		"if (!isError)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console live status lifecycle missing %q", contract)
		}
	}
}

func TestStatusBadgesDoNotWrapInCompactLayouts(t *testing.T) {
	if !strings.Contains(string(consoleHTML), ".badge { display:inline-flex; min-height:24px; flex:0 0 auto;") ||
		!strings.Contains(string(consoleHTML), "white-space:nowrap;") {
		t.Fatal("status badges must remain legible on one line")
	}
}

func TestOverviewContainsFleetSummaryButNoTopologyGraph(t *testing.T) {
	view := consoleView(t, "overview")
	for _, label := range []string{"集群总数", "健康集群", "异常集群", "数据库实例", "控制节点", "待复核操作"} {
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

func TestOverviewCountsOnlyActionableOperationStates(t *testing.T) {
	page := string(consoleHTML)
	if !strings.Contains(page, "['running', 'indeterminate'].includes(operation.status)") {
		t.Fatal("fleet summary must count only operations that still require operator action")
	}
	if strings.Contains(page, "['planned', 'running', 'blocked', 'indeterminate'].includes(operation.status)") {
		t.Fatal("historical planned and blocked records must not inflate the current action count")
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

func TestConsoleClusterManagementUsesFocusedAdministratorModal(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="open-cluster-management-modal"`, `<dialog id="cluster-management-modal"`,
		`aria-labelledby="cluster-management-title"`, `id="cluster-create-tab"`, `id="cluster-retire-tab"`,
		`id="cluster-create-panel"`, `id="cluster-retire-panel"`, `id="cluster-management-result"`,
		"byId('open-cluster-management-modal').hidden = !canAdministerPlatform();",
		"byId('cluster-management-modal').showModal()", "byId('cluster-management-modal').close()",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing cluster management modal contract %q", contract)
		}
	}
	for _, forbidden := range []string{`id="cluster-password"`, `id="cluster-username"`, "cluster_credentials"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("cluster management modal must not collect database credentials %q", forbidden)
		}
	}
}

func TestConsoleClusterManagementRegistersDiscoversAndSelectsNewCluster(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="cluster-display-name"`, `id="cluster-engine"`, `id="cluster-endpoint-list"`,
		`id="add-cluster-endpoint"`, `id="submit-cluster-registration"`,
		"const addClusterEndpointRow =", "removeClusterEndpointRow", "at least one database endpoint",
		"const defaultPortForEngine =", "postgresql:5432", "applyClusterEngineDefaults",
		"byId('cluster-engine').addEventListener('change', applyClusterEngineDefaults)",
		"fetchResult('/api/v1/clusters', mutationOptions(payload))",
		"fetchResult(`/api/v1/clusters/${registered.cluster.resource_id}/discover`, mutationOptions({}))",
		"await loadClusters(registered.cluster.resource_id)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing cluster registration contract %q", contract)
		}
	}
}

func TestConsoleClusterManagementRetiresWithExactNameConfirmation(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="retire-cluster-name"`, `id="retire-cluster-id"`, `id="retire-cluster-confirmation"`,
		`id="submit-cluster-retirement"`, "confirmation === cluster.display_name",
		"fetchResult(`/api/v1/clusters/${cluster.resource_id}`, deletionOptions({ confirm_display_name: confirmation }))",
		"const confirmation = byId('retire-cluster-confirmation').value;",
		"仅从 ClusterGuard 活动清单移除", "不会停止数据库、删除数据或操作系统 VIP",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing guarded cluster retirement contract %q", contract)
		}
	}
	if strings.Contains(page, "const confirmation = byId('retire-cluster-confirmation').value.trim();") {
		t.Fatal("console trims the exact-name retirement confirmation")
	}
}

func TestConsoleClusterRetirementRecognizesCommittedDurabilityWarning(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"this.committed = committed",
		"payload.committed === true",
		"error instanceof APIError && error.committed",
		"退役已提交，但元数据目录同步需要复核",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing committed retirement warning contract %q", contract)
		}
	}
}

func TestConsoleClusterManagementKeepsActionsVisibleOnSmallScreens(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`.cluster-management-dialog[open] { display:grid; grid-template-rows:auto minmax(0,1fr) auto;`,
		`.cluster-management-dialog .dialog-body { min-height:0; overflow:auto;`,
		`.cluster-management-dialog .dialog-actions {`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing responsive cluster management contract %q", contract)
		}
	}
}

func TestTopologyShowsStableAndNativeIdentityAndMetadataModal(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "topology")
	for _, label := range []string{"固定节点名", "资源 ID", "原生身份", "主机名", "IP", "端口", "版本", "角色", "延迟", "VIP"} {
		if !strings.Contains(view, label) {
			t.Fatalf("topology missing identity label %q", label)
		}
	}
	for _, contract := range []string{
		`id="open-metadata-modal"`, `role="dialog"`, `aria-modal="true"`, `id="metadata-modal"`,
		"instance.node_id", "instance.resource_id", "const nativeIdentityLabel =", "const nativeIdentityValue =",
		"identity.server_uuid", "identity.resource_id", "identity.system_identifier",
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

func TestConsoleOffersNativePostgreSQLLifecycleOnlyWhenCapabilityIsReal(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="mysql-lifecycle-fields"`, `id="postgresql-lifecycle-fields"`,
		`id="node-postgresql-version"`, `id="node-postgresql-port"`,
		`id="node-postgresql-service"`, `id="node-postgresql-data-directory"`,
		"const lifecycleEngine =", "const postgresqlLifecycleAvailable =",
		"postgresql_basebackup_available", "postgresql_rewind_available",
		"const applyNodeLifecycleEngine =", "cluster.engine === 'postgresql'",
		"pg_basebackup", "pg_rewind", "postgresql_version:", "postgresql_port:",
		"postgresql_service:", "postgresql_data_directory:",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing native PostgreSQL lifecycle contract %q", contract)
		}
	}
	if strings.Contains(page, "节点安装与同步当前仅支持 MySQL") {
		t.Fatal("console still presents PostgreSQL lifecycle as a MySQL-only feature")
	}
}

func TestAboutDistinguishesConfiguredExecutionFromReadOnlyAndUnsupportedAdapters(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const observable = ['discover', 'topology', 'health']",
		"every(name => capability.features && capability.features[name] && capability.features[name].available)",
		"const label = executable ? '可执行' : observable ? '只读可用' : '未实现'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing truthful adapter capability contract %q", contract)
		}
	}
	if strings.Contains(page, "只读 / 未实现") {
		t.Fatal("console must not collapse read-only and unsupported adapters into one status")
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
		"idempotency_key:", "requested_by: state.currentUser.username",
		"fetchResult('/api/v1/operations/execute', mutationOptions(payload))", "await loadSelectedCluster()", "await loadOperationLog()",
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
	if strings.Contains(page, "attempt < 3") {
		t.Fatal("a plan-bound one-time approval must not be retried against a different plan")
	}
}

func TestOperationsUseSimpleLocalAntiMistakeLockWithoutBypassingBackendGates(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="switch-lock"`, "switchUnlocked: false", "state.switchUnlocked = !state.switchUnlocked",
		"execute.disabled = state.operationRunning || !state.switchUnlocked", "操作锁定", "已解锁",
		"后端仍会执行 Safety Guard、集群操作锁、平台身份授权和验证",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing anti-mistake lock contract %q", contract)
		}
	}
}

func TestOperationSelectorsUseDatabaseInstanceLabels(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const instanceDisplayName =",
		"const candidateOptionLabel =",
		"new Option(candidateOptionLabel(instance, assessment), instance.resource_id)",
		"rejoin.append(new Option(instanceDisplayName(instance), instance.resource_id))",
		"不可切换：${candidateBlockReason(assessment)}",
		"存在 ${errant[1]} 个游离事务",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console operation selector must use database instance labels: missing %q", contract)
		}
	}
	if strings.Contains(page, "new Option(nodeName(instance), instance.resource_id)") {
		t.Fatal("console operation selector still exposes fixed control node names as database targets")
	}
	if strings.Contains(page, "const option = new Option(instanceDisplayName(instance), instance.resource_id)") {
		t.Fatal("candidate selector must explain disabled candidate reasons instead of only greying them out")
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

func TestNodeLifecycleUsesFocusedModalInsteadOfInlineForm(t *testing.T) {
	page := string(consoleHTML)
	nodesView := consoleView(t, "nodes")
	for _, contract := range []string{
		`id="open-node-lifecycle-modal"`, `<dialog id="node-lifecycle-modal"`,
		`aria-labelledby="node-lifecycle-title"`, `id="close-node-lifecycle-modal"`,
		`id="cancel-node-lifecycle"`, `id="node-lifecycle-result"`,
		"byId('node-lifecycle-modal').showModal()", "byId('node-lifecycle-modal').close()",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing node lifecycle modal contract %q", contract)
		}
	}
	if strings.Contains(nodesView, `id="node-action"`) || strings.Contains(nodesView, `class="node-form"`) {
		t.Fatal("node lifecycle form must not remain inline in the nodes workspace")
	}
}

func TestNodeLifecycleEntryReevaluatesAfterClusterSelection(t *testing.T) {
	page := string(consoleHTML)
	contract := "state.selectedClusterId = clusterId;\n      updateNodeLifecycleControls();"
	if !strings.Contains(page, contract) {
		t.Fatalf("console does not refresh node lifecycle controls after cluster selection: missing %q", contract)
	}
}

func TestNodeLifecycleModalKeepsActionsVisibleOnSmallScreens(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`.node-lifecycle-dialog[open] { display:grid; grid-template-rows:auto minmax(0,1fr) auto;`,
		`.node-lifecycle-dialog .dialog-body { min-height:0; max-height:none; overflow:auto;`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing responsive node modal contract %q", contract)
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
		"requestGeneration: 0", "const generation = ++state.requestGeneration;", "clearClusterView(preserveOperationResult);",
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

func TestConsoleRendersEngineSpecificPostgreSQLMetrics(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const metricDefinitionForEngine =", "engine === 'postgresql'",
		"active_connections", "transactions_total", "deadlocks_total",
		"database_size_bytes", "buffer_cache_hit_ratio", "max_transaction_age_seconds",
		"活动连接", "累计事务", "死锁", "数据库容量", "最长事务",
		`id="metrics-head-row"`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing PostgreSQL metric contract %q", contract)
		}
	}
}

func TestConsoleKeepsPasswordsTransientAndAPIDerivedTextUsesSafeDOM(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="login-password"`, `id="current-password"`, `id="new-password"`, `type="password"`,
		`autocomplete="current-password"`, `autocomplete="new-password"`,
		"state.currentUser", "textContent", `aria-live="polite"`, ":focus-visible",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing secure rendering contract %q", contract)
		}
	}
	for _, forbidden := range []string{
		`id="control-token"`, `id="admin-token"`, `id="approval-token"`, `id="lifecycle-token"`,
		"state.adminToken", "state.approvalToken", "state.lifecycleToken",
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("normal console must not expose credential field %q", forbidden)
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

func TestConsoleHasLoginAndForcedPasswordChange(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="login-shell"`, `id="console-shell"`, `id="login-form"`,
		`id="login-username"`, `id="login-password"`, `id="login-submit"`,
		`id="password-modal"`, `id="current-password"`, `id="new-password"`,
		`id="confirm-new-password"`, "/api/v1/auth/login", "/api/v1/auth/me",
		"/api/v1/auth/password", "must_change_password", "passwordChangeRequired",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing authenticated entry contract %q", contract)
		}
	}
}

func TestConsoleSendsCSRFOnMutatingRequests(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const csrfCookieValue = () =>", "clusterguard_csrf", "'X-CSRF-Token'",
		"credentials:'same-origin'", "const mutationOptions = body =>",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing authenticated mutation contract %q", contract)
		}
	}
	if strings.Contains(page, "'Authorization': `Bearer") {
		t.Fatal("browser console must authenticate with its platform session, not a bearer secret")
	}
}

func TestConsoleOperationDoesNotHandleApprovalToken(t *testing.T) {
	page := string(consoleHTML)
	for _, forbidden := range []string{
		`id="approval-token"`, `id="approval-modal"`, "state.approvalToken",
		"approval_token: state.approvalToken", "requireManualApproval", "clearApprovalToken",
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("platform console must not handle approval token %q", forbidden)
		}
	}
	for _, contract := range []string{
		"requested_by: state.currentUser.username",
		"fetchResult('/api/v1/operations/execute', mutationOptions(payload))",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("platform operation missing session-owned authorization contract %q", contract)
		}
	}
}

func TestConsoleShowsAuthenticatedUserAndLogout(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="current-user-name"`, `id="current-user-role"`,
		`id="open-password-change"`, `id="logout-button"`,
		`id="settings-user-name"`, `id="settings-user-role"`,
		"/api/v1/auth/logout", "const renderAuthenticatedUser =",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing account contract %q", contract)
		}
	}
}

func TestConsoleUsesCompactAlignedResponsiveEnterpriseLayout(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"--radius:8px", "grid-template-columns:220px minmax(0,1fr)", "border-radius:8px",
		"grid-template-columns:minmax(0,1fr) 72px minmax(0,1fr)", "@media (max-width:900px)", "grid-template-columns:minmax(0,1fr);",
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
		".node-card {", "min-width:0;", ".node-line { width:100%;", "text-overflow:ellipsis; white-space:nowrap;",
		".panel-body { padding:18px; min-width:0; overflow-x:auto; }", ".inventory-table, .metrics-table { min-width:900px;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing mobile overflow constraint %q", contract)
		}
	}
}

func TestConsoleOverviewProvidesFleetTriageAndObservationFreshness(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "overview")
	for _, contract := range []string{
		`id="fleet-search"`, `id="fleet-health-filter"`, `id="fleet-visible-count"`,
		"最近观测", "异常优先", "fleetTopology: new Map()", "const filteredFleetClusters =",
		"fetchResult(`/api/v1/clusters/${cluster.resource_id}/topology`)",
		"row.dataset.clusterId = cluster.resource_id", "const selectClusterFromFleet =",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console overview missing fleet triage contract %q", contract)
		}
	}
	if !strings.Contains(view, "筛选集群") || !strings.Contains(view, "全部状态") {
		t.Fatal("overview must expose human-readable fleet filters")
	}
}

func TestConsoleOperationRendersDurableWorkflowProgressAndIndeterminateState(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "operations")
	for _, stage := range []string{"precheck", "plan", "safety", "execute", "verify"} {
		if !strings.Contains(view, `data-operation-stage="`+stage+`"`) {
			t.Fatalf("operations view missing durable workflow stage %q", stage)
		}
	}
	for _, contract := range []string{
		"const renderOperationProgress = operation =>", "const pollOperationProgress = async",
		"/api/v1/operations?idempotency_key=${encodeURIComponent(idempotencyKey)}",
		"['safety_guard', 'lock', 'approve']", "operation.verification && operation.verification.passed",
		"operation.status === 'indeterminate'", "结果不确定，停止执行并要求人工复核",
		"state.switchUnlocked = false", "renderOperationProgress(operation)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console operation progress missing contract %q", contract)
		}
	}
}

func TestConsolePreservesVerifiedOutcomeUntilReadAfterWriteTopologyConverges(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const clearClusterView = (preserveOperationResult = false) =>",
		"const beginClusterRequest = (clusterId, preserveOperationResult = false) =>",
		"const loadSelectedCluster = async ({ preserveOperationResult = false, expectedPrimaryID = '' } = {}) =>",
		"const topologyMatchesExpectedPrimary =",
		"convergenceAttempt < (expectedPrimaryID ? 10 : 1)",
		"await loadSelectedCluster({ preserveOperationResult:true, expectedPrimaryID: operation.target_id });",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing verified read-after-write contract %q", contract)
		}
	}
	refresh := strings.Index(page, "await loadSelectedCluster({ preserveOperationResult:true, expectedPrimaryID: operation.target_id });")
	success := strings.Index(page, "renderActionResult(`${label}：${t('operationSucceeded')}`);")
	if refresh < 0 || success < 0 || success < refresh {
		t.Fatal("verified success must be rendered after the refreshed topology has converged")
	}
}

func TestConsoleReadOnlyRefreshPreservesLatestVerifiedOutcome(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"if (state.selectedClusterId && !state.switchUnlocked && !state.operationRunning) loadSelectedCluster({ preserveOperationResult:true });",
		"await loadSelectedCluster({ preserveOperationResult:true });",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console read-only refresh must preserve the latest operation result: missing %q", contract)
		}
	}
}

func TestConsoleRelocksDestructiveActionWhenClusterOrTargetChanges(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const relockSwitch = () =>", "relockSwitch();\n      clearClusterView(preserveOperationResult);",
		"state.selectedCandidateId = event.target.value; relockSwitch(); renderOperationContext();",
		"state.selectedRejoinId = event.target.value; relockSwitch(); updateExecutionButtons();",
		"finally {\n        state.operationRunning = false;\n        relockSwitch();",
		"byId('operation-result').textContent = '等待操作。';",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console must relock target-specific destructive action: missing %q", contract)
		}
	}
}

func TestConsoleTopologyUsesStableCompactConnectorsAndAnomalyEvidence(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "topology")
	for _, contract := range []string{
		`id="topology-freshness"`, `id="topology-anomalies"`, "const renderTopologyAnomalies =",
		"--topology-node-height:104px", "height:var(--topology-node-height)",
		"grid-template-columns:minmax(0,1fr) 72px minmax(0,1fr)",
		"top:calc(var(--topology-node-height) / 2)", ".node-title, .node-line, .vip-chip { flex:0 0 auto; }",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console topology missing stable compact layout contract %q", contract)
		}
	}
	if !strings.Contains(view, "拓扑异常") || !strings.Contains(view, "观测新鲜度") {
		t.Fatal("topology must expose anomaly and observation evidence")
	}
	if !strings.Contains(page, "card.append(lineOne, lineTwo);") {
		t.Fatal("compact topology cards must keep immutable identity in the inventory instead of squeezing it into the card")
	}
	if strings.Contains(page, "card.append(lineOne, lineTwo, identity)") {
		t.Fatal("topology card must not compress resource identity and VIP into the fixed-height node card")
	}
}

func TestConsoleOrganizesNodeWorkflowAndFiltersOperationEvidence(t *testing.T) {
	page := string(consoleHTML)
	nodes := consoleView(t, "nodes")
	logView := consoleView(t, "operation-log")
	for _, label := range []string{"任务类型", "固定身份", "服务器连接", "数据库与同步"} {
		if !strings.Contains(page, label) {
			t.Fatalf("node lifecycle dialog missing form group %q", label)
		}
	}
	for _, label := range []string{"节点清单", "任务进度", `id="open-node-lifecycle-modal"`} {
		if !strings.Contains(nodes, label) {
			t.Fatalf("node workspace missing focused content %q", label)
		}
	}
	for _, contract := range []string{
		`id="log-search"`, `id="log-kind-filter"`, `id="log-status-filter"`,
		"const filteredOperations =", "renderOperationLog();", `id="metrics-observed-at"`,
		`id="refresh-interval"`, "const scheduleAutoRefresh =", "state.refreshTimer",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing evidence workflow contract %q", contract)
		}
	}
	if !strings.Contains(logView, "筛选操作日志") || !strings.Contains(logView, "全部类型") || !strings.Contains(logView, "全部状态") {
		t.Fatal("operation log must expose useful human filters")
	}
}

func TestConsoleDerivesTopologyAttentionFromObservedInstanceHealth(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const topologyAttentionReasons = () =>",
		"instance.health && instance.health.state",
		"const attentionReasons = topologyAttentionReasons();",
		"attentionReasons.length ? '需关注' : '健康'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("topology must not report healthy when observed instances are degraded or unknown: missing %q", contract)
		}
	}
}

func TestConsoleUsesClusterDisplayNameForLifecycleTasks(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const clusterDisplayName = clusterID =>",
		"['集群', clusterDisplayName(task.cluster_id)]",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("node lifecycle evidence must show the registered cluster name instead of an internal UUID: missing %q", contract)
		}
	}
}

func TestConsolePaginatesLargeOperationLogs(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="load-more-operation-log"`,
		"const operationLogPageSize = 50;",
		"logVisibleLimit: operationLogPageSize",
		"const visibleOperations = operations.slice(0, state.logVisibleLimit);",
		"state.logVisibleLimit += operationLogPageSize;",
		"state.logVisibleLimit = operationLogPageSize;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation evidence must remain responsive with large histories: missing %q", contract)
		}
	}
}

func TestConsoleConsolidatesAutomaticRecoveryRetriesByIncident(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const automaticRecoveryIncidentKey = operation =>",
		"record.requested_by !== 'clusterguard-automatic-recovery'",
		"const consolidateOperationIncidents = operations =>",
		"incident_attempt_count",
		"个事件 · ${rawRecordCount} 条原始记录",
		"原始返回（最近一次，事故共",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("automatic recovery retries must remain auditable without flooding the operator timeline: missing %q", contract)
		}
	}
}

func TestConsoleExplainsFollowerQuorumAndUsesControllerNames(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const controllerDisplayName = controllerID =>",
		"const controlRoleText = role =>",
		"status.leader_known ? `${status.voter_count || 0} 个投票节点，由 Leader 确认`",
		"controllerDisplayName(status.local_controller_id)",
		"controllerDisplayName(status.leader_id)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("control-plane status must be readable without misreporting follower quorum: missing %q", contract)
		}
	}
}

func TestConsoleFormatsMetricsAndOrdersEvidenceForOperators(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const formatMetric = (value, digits = 2) =>",
		"{ label:'QPS', key:'qps', aggregate:'sum' }", "{ label:'连接', key:'connections', aggregate:'sum', digits:0 }",
		"{ label:'运行线程', key:'running_threads', aggregate:'sum', digits:0 }", "formatMetricValue(item, aggregate(item))",
		"const newestOperationFirst =", "filteredOperations().slice().sort(newestOperationFirst)",
		"const newestTaskFirst =", "state.lifecycleTasks.slice().sort(newestTaskFirst).slice(0, 8)",
		`id="lifecycle-task-count"`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing operator scanability contract %q", contract)
		}
	}
}

func TestConsoleTranslatesPrimaryWorkspaceLanguageInsteadOfOnlyNavigation(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const t = key =>", `data-i18n="overviewTitle"`, `data-i18n="topologyTitle"`,
		`data-i18n="operationsTitle"`, `data-i18n="nodesTitle"`, `data-i18n="metricsTitle"`,
		"overviewTitle:'运行总览'", "overviewTitle:'Fleet Overview'",
		"operationSucceeded:'操作完成，验证通过。'", "operationSucceeded:'Operation completed and verified.'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing full workspace translation contract %q", contract)
		}
	}
}
