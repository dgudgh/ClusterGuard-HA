package api

import (
	"regexp"
	"strings"
	"testing"
)

func consoleView(t *testing.T, name string) string {
	t.Helper()
	page := string(consoleHTML)
	section := regexp.MustCompile(`<section\b[^>]*\bdata-view="` + regexp.QuoteMeta(name) + `"[^>]*>`).FindStringIndex(page)
	if section == nil {
		t.Fatalf("console view %q not found", name)
	}
	start := section[0]
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

func TestSettingsUsesThreeSwitchableAdministrativeSections(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "settings")
	for _, contract := range []string{
		`class="view settings-view"`, `class="topology-section-tabs settings-section-tabs" role="tablist"`,
		`id="settings-status-tab" type="button" role="tab" aria-controls="settings-status-panel" aria-selected="true"`,
		`id="software-update-tab" type="button" role="tab" aria-controls="software-update-panel" aria-selected="false"`,
		`id="settings-account-tab" type="button" role="tab" aria-controls="settings-account-panel" aria-selected="false"`,
		`id="settings-status-panel" role="tabpanel" aria-labelledby="settings-status-tab"`,
		`id="software-update-panel" role="tabpanel" aria-labelledby="software-update-tab" hidden`,
		`id="settings-account-panel" role="tabpanel" aria-labelledby="settings-account-tab" hidden`,
		`class="settings-preference-sheet"`,
		`class="settings-preference-row" role="group" aria-labelledby="settings-account-title"`,
		`class="settings-preference-row" role="group" aria-labelledby="settings-display-title"`,
		`class="control-plane-strip"`,
		`id="open-software-update-dialog" class="primary-button" type="button">升级</button>`,
		`class="software-update-runtime" id="software-update-runtime" hidden`,
		`id="platform-current-version"`, `id="software-update-history"`,
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("settings missing switchable administrative section contract %q", contract)
		}
	}
	for _, contract := range []string{
		`id="software-update-dialog" class="software-update-dialog"`,
		`class="software-update-file-picker"`, `id="software-update-file-name"`,
		`class="software-update-metadata"`,
		`id="software-update-package-file" type="file" accept=".cgupgrade,.cgpatch,application/octet-stream"`,
		"--accent:#0071e3", "--canvas:#f5f5f7", "renderSelectedSoftwareUpdateFile",
		"const setSettingsSection = (section, focus = false) =>", "settingsSection: 'status'",
		"const softwareUpdates = await softwareUpdateRequest('/api/v1/platform/updates')",
		"void softwareUpdateRequest('/api/v1/platform/version').then(version =>",
		"state.platformVersion = version", "const previousSoftwareUpdates = state.softwareUpdates",
		"...previousSoftwareUpdates", "packages:Array.isArray(previousSoftwareUpdates.packages)",
		"已保留最近一次任务进度并继续重试", "renderSoftwareUpdateHistory(snapshot)",
		"系统升级期间无法进行自动切换，请注意关注。",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing settings design contract %q", contract)
		}
	}
	for _, forbidden := range []string{`class="settings-workspace"`, `class="settings-column settings-column-summary"`, `class="settings-column settings-column-tools"`} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("settings must not retain the stacked all-functions layout %q", forbidden)
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

func TestTopologyUsesThreeSwitchableHorizontalSections(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "topology")
	for _, contract := range []string{
		`class="topology-section-tabs" role="tablist"`,
		`id="topology-map-tab" type="button" role="tab" aria-controls="topology-map-panel" aria-selected="true"`,
		`id="topology-power-tab" type="button" role="tab" aria-controls="topology-power-panel" aria-selected="false"`,
		`id="topology-identity-tab" type="button" role="tab" aria-controls="topology-identity-panel" aria-selected="false"`,
		`id="topology-map-panel" role="tabpanel" aria-labelledby="topology-map-tab"`,
		`id="topology-power-panel" role="tabpanel" aria-labelledby="topology-power-tab" hidden`,
		`id="topology-identity-panel" role="tabpanel" aria-labelledby="topology-identity-tab" hidden`,
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("topology view missing switchable section contract %q", contract)
		}
	}
	for _, contract := range []string{
		".topology-section-tabs { display:grid; grid-template-columns:repeat(3,minmax(0,1fr));",
		"const setTopologySection = (section, focus = false) =>",
		"topologySection: 'map'",
		"event.key === 'ArrowRight'",
		"event.key === 'ArrowLeft'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing topology section switching contract %q", contract)
		}
	}
}

func TestDetachedTopologyNodesStackWithoutShrinkingTheGraph(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		".topology-frame {\n      display:flex;\n      height:420px;\n      flex-direction:column;",
		"max-width:980px;\n      min-width:760px;\n      flex:0 0 auto;",
		".replica-row .node-card { width:100%; }",
		".detached-nodes { width:100%; max-width:980px; flex:0 0 auto;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("topology canvas must keep normal card geometry when detached nodes are visible; missing %q", contract)
		}
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
	if !strings.Contains(page, "state.fleetOperationContext.running_count + state.fleetOperationContext.unreviewed_count") {
		t.Fatal("fleet summary must count only operations that still require operator action")
	}
	if strings.Contains(page, "['planned', 'running', 'blocked', 'indeterminate'].includes(operation.status)") {
		t.Fatal("historical planned and blocked records must not inflate the current action count")
	}
}

func TestOverviewAggregatesEveryRegisteredClusterInsteadOfOnlySelection(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"clusterDirectory: new Map()", "const loadFleetDetails = async (clusters, fleetReadEpoch) =>", "clusters.map(async cluster =>",
		"readClusterResult(`/api/v1/clusters/${cluster.resource_id}`)", "state.clusterDirectory.get(cluster.resource_id)",
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

func TestConsoleMovesAccountActionsIntoCompactSidebarMenu(t *testing.T) {
	page := string(consoleHTML)
	asideEnd := strings.Index(page, "</aside>")
	headerStart := strings.Index(page, `<header class="page-header">`)
	headerEnd := strings.Index(page[headerStart:], "</header>")
	if asideEnd < 0 || headerStart < 0 || headerEnd < 0 {
		t.Fatal("console shell regions are missing")
	}
	sidebar := page[:asideEnd]
	header := page[headerStart : headerStart+headerEnd]
	for _, contract := range []string{
		`id="account-menu-toggle"`, `id="current-user-avatar"`, `id="current-user-name"`,
		`id="current-user-role"`, `id="account-menu"`, `id="open-password-change"`, `id="logout-button"`,
	} {
		if !strings.Contains(sidebar, contract) {
			t.Fatalf("sidebar account menu missing %q", contract)
		}
		if strings.Contains(header, contract) {
			t.Fatalf("page header still contains account control %q", contract)
		}
	}
	for _, contract := range []string{
		"const setAccountMenuOpen = open =>", "setAccountMenuOpen(byId('account-menu').hidden)",
		"document.addEventListener('click', () => setAccountMenuOpen(false))", "event.key === 'Escape'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("account menu interaction missing %q", contract)
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
	for _, label := range []string{"固定节点名", "资源 ID", "原生身份", "主机名", "IP", "端口", "版本", "角色", "延迟", "业务入口"} {
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
	for _, label := range []string{"集群", "当前主库", "候选主库", "业务入口", "延迟", "执行切换", "旧主一键恢复"} {
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
}

func TestOperationsViewUsesSwitchableSwitchoverAndRecoverySections(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "operations")
	for _, contract := range []string{
		`class="topology-section-tabs operations-section-tabs" role="tablist"`,
		`id="operations-switchover-tab" type="button" role="tab" aria-controls="operations-switchover-panel" aria-selected="true"`,
		`id="operations-recovery-tab" type="button" role="tab" aria-controls="operations-recovery-panel" aria-selected="false"`,
		`id="operations-switchover-panel" role="tabpanel" aria-labelledby="operations-switchover-tab"`,
		`id="operations-recovery-panel" role="tabpanel" aria-labelledby="operations-recovery-tab" hidden`,
		`id="operation-switchover-target"`, `id="operation-recovery-target" hidden`,
		`id="operations-section-summary"`,
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("operations view missing switchable section contract %q", contract)
		}
	}
	for _, contract := range []string{
		"operationsSection: 'switchover'",
		"const operationsSectionOrder = ['switchover', 'recovery', 'disaster']",
		"const setOperationsSection = (section, focus = false) =>",
		"tab.addEventListener('click', () => setOperationsSection(tab.dataset.operationsSection))",
		"setOperationsSection(operationsSectionOrder[targetIndex], true)",
		"byId('operation-switchover-target').hidden = recovery",
		"byId('operation-recovery-target').hidden = !recovery",
		"recovery ? state.selectedRejoinId : state.selectedCandidateId",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing operations section switching contract %q", contract)
		}
	}
}

func TestConsoleRetriesOnlyPreCommitStalePlanConflicts(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const maxStalePlanAttempts = 3;",
		"const isRetryablePreCommitStalePlan = error =>",
		"error instanceof APIError && error.status === 409",
		"result.failure_class === 'stale_plan'",
		"result.status === 'blocked'",
		"['precheck', 'plan'].includes(result.stage)",
		"kind === 'switchover'",
		"attempt < maxStalePlanAttempts",
		"await loadSelectedCluster({ preserveOperationResult:true });",
		"target_id: targetID",
		"console-${kind}-${cluster.resource_id}-${targetID}-${Date.now()}-${attempt}",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing bounded pre-commit stale-plan retry contract %q", contract)
		}
	}

	predicateStart := strings.Index(page, "const isRetryablePreCommitStalePlan = error =>")
	predicateEnd := strings.Index(page[predicateStart:], "const executeOperation = async")
	if predicateStart < 0 || predicateEnd < 0 {
		t.Fatal("unable to inspect stale-plan retry predicate")
	}
	predicate := page[predicateStart : predicateStart+predicateEnd]
	for _, forbidden := range []string{"indeterminate", "execute", "failed"} {
		if strings.Contains(predicate, forbidden) {
			t.Fatalf("stale-plan retry predicate must not accept post-commit state %q", forbidden)
		}
	}
}

func TestOperationsUseSimpleLocalAntiMistakeLockWithoutBypassingBackendGates(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="switch-lock"`, "switchUnlocked: false", "if (!operationLockAvailable()) return;",
		"if (state.switchUnlocked) relockSwitch();", "else { state.switchUnlocked = true; updateExecutionButtons(); }",
		"execute.disabled = !ordinaryOperationAllowed('switchover', state.selectedCandidateId)",
		"const ordinaryOperationAllowed = (kind, targetID) => !state.operationRunning && state.switchUnlocked", "操作锁定", "已解锁",
		"后端仍会执行 Safety Guard、集群操作锁、平台身份授权和验证",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing anti-mistake lock contract %q", contract)
		}
	}
}

func TestDisasterRecoverySharesOperationLockAcrossEntryPlanAndExecution(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const disasterContextReady = () =>", "const disasterActionAllowed = clusterID => state.switchUnlocked",
		"byId('open-disaster-recovery').disabled = !disasterActionAllowed()",
		"!disasterActionAllowed(cluster.resource_id) || byId('disaster-dialog').open",
		"disasterActive(recovery.task) || !disasterActionAllowed(recovery.clusterID)",
		"const lockEpoch = state.operationLockEpoch", "lockEpoch !== state.operationLockEpoch",
		"if (!disasterConfirmationValid()) { renderDisaster(); return; }",
		"byId('disaster-execute').disabled = !disasterConfirmationValid()",
		"recovery.task?.cluster_id === recovery.clusterID",
		"byId('disaster-confirm-name').value === recovery.name && byId('disaster-confirm-impact').checked",
		"if (selectionChanged && state.disaster) closeDisaster()",
		"state.disaster.ready = false", "state.operationLockEpoch++",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing disaster operation-lock contract %q", contract)
		}
	}
	if strings.Contains(page, "byId('switch-lock').hidden = disaster") {
		t.Fatal("disaster recovery must expose and honor the same operation lock")
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
		"const replicaIsRunning =", "health.replication === 'running'",
		"replication.io_thread === 'running' && replication.sql_thread === 'running'",
		"const formerPrimaryCandidates =", "historicalSourceIDs.has(instance.resource_id)",
		"const explicitlyBrokenReplication =", "const historicalPrimaryNeedsRecovery =",
		"return Boolean(instance.maintenance) || healthState !== 'healthy' || explicitlyBrokenReplication || historicalPrimaryNeedsRecovery",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing unavailable-action or former-primary filter contract %q", contract)
		}
	}
	for _, unsafeContract := range []string{
		"replication.io_thread !== 'running' || replication.sql_thread !== 'running'",
		"historicalSourceIDs.has(instance.resource_id) || instance.maintenance",
	} {
		if strings.Contains(page, unsafeContract) {
			t.Fatalf("console must not classify a healthy running replica as a former primary from missing or historical fields: found %q", unsafeContract)
		}
	}
}

func TestFormerPrimaryRejoinAndNodeLifecycleUseRealAPIs(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"recoverFormerPrimary(state.selectedRejoinId)", `id="execute-rejoin"`, "一键恢复为从库",
		"/api/v1/nodes/sync/capabilities", "/api/v1/nodes/sync/precheck", "/api/v1/nodes/sync/execute", "/api/v1/nodes/sync/tasks",
		`value="data"`, `value="controller"`, `value="mixed"`, `value="rebuild"`,
		`id="node-name"`, `id="node-resource-id"`, `id="sync-method"`,
		"控制节点最终数量必须为大于等于 3 的奇数", "修复节点复用原资源 ID 和固定节点名", "state.lifecycleCapability.available",
		"const formerPrimaryRebuildPayload =", "action:'rebuild'", "sync_method:'auto'", "rebuild:true",
		"const recoverFormerPrimary = async targetID =>", "/api/v1/operations/precheck", "required_binlog_available", "rebuild_required",
		"await runNodeSyncPayload(formerPrimaryRebuildPayload(targetID), intent)", "检测到 binlog 缺口或 GTID 分叉",
		"task.status !== 'succeeded'", "全量重建并恢复为从库完成",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing real node/rejoin contract %q", contract)
		}
	}
}

func TestFormerPrimaryRecoveryRefreshesDiscoveryBeforePrecheck(t *testing.T) {
	page := string(consoleHTML)
	start := strings.Index(page, "const recoverFormerPrimary = async targetID =>")
	if start < 0 {
		t.Fatal("console missing former-primary recovery function")
	}
	end := strings.Index(page[start:], "const formatMetric =")
	if end < 0 {
		t.Fatal("console former-primary recovery function has no stable end marker")
	}
	recovery := page[start : start+end]
	discover := strings.Index(recovery, "fetchResult(`/api/v1/clusters/${cluster.resource_id}/discover`, mutationOptions({}))")
	precheck := strings.Index(recovery, "fetchResult('/api/v1/operations/precheck', mutationOptions(operationPayload))")
	if discover < 0 {
		t.Fatal("former-primary recovery must refresh cluster discovery before making a recovery decision")
	}
	if precheck < 0 || discover > precheck {
		t.Fatal("former-primary recovery must refresh discovery before the recovery precheck")
	}
}

func TestFormerPrimaryRecoverySupportsPostgreSQLThroughUnifiedWorkflow(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"!['mysql', 'postgresql'].includes(cluster.engine)",
		"正在检查旧主 WAL 时间线及增量回挂条件",
		"正在执行 pg_rewind 增量回挂，必要时自动回退 pg_basebackup 全量同步",
		"旧主回挂完成，节点已作为只读流复制从库重新加入",
		"const operationFailureGuidance = error =>",
		"rebuild_failed:'旧主同步失败",
		"旧主同步失败，请确认当前主库、目标节点数据库服务和 Agent 均可达后重试",
		"operationFailureGuidance(error)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing PostgreSQL former-primary recovery contract %q", contract)
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

func TestNodeLifecycleAddsControllerMembersAsAnAtomicPair(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="controller-pair-group"`, `id="node-pair-name"`, `id="node-pair-resource-id"`,
		`id="node-pair-hostname"`, `id="node-pair-ip"`, `id="node-pair-ssh-user"`, `id="node-pair-ssh-port"`,
		"const controllerPairRequired = () =>", "kind: 'controller'", "targets.push(pairTarget)",
		"控制节点按一对加入", "保持 Raft 投票节点为大于等于 3 的奇数",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing paired controller lifecycle contract %q", contract)
		}
	}
	if !strings.Contains(page, "byId('node-kind').addEventListener('change', updateControllerPairMode)") {
		t.Fatal("controller pair form does not react to node kind changes")
	}
}

func TestNodeLifecycleTasksExposeEveryBuiltInExecutionStage(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const lifecycleStageOrder = ['preflight', 'install', 'synchronize', 'configure_replication', 'verify', 'metadata_commit']",
		"安全预检", "安装运行时", "数据同步", "配置复制", "健康验证", "提交元数据",
		"lifecycle-stage-flow", "查看执行明细", "task.log_tail", "task.checks",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing lifecycle stage evidence contract %q", contract)
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
		"readClusterResult(`/api/v1/operations?${query}`, controller.signal)", "state.logLoaded = true;",
		"operation.plan.source_id", "operation.target_id", "operation.operation.kind", "operation.status",
		"document.createElement('details')", "document.createElement('summary')", "JSON.stringify(record, null, 2)",
		"details.addEventListener('toggle', loadRaw)", "if (!details.open || rawLoaded || rawLoading) return;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation log missing contract %q", contract)
		}
	}
	if strings.Contains(page, "details.open = true") {
		t.Fatal("raw operation evidence must be collapsed by default")
	}
}

func TestOperationLogDefaultsToHeaderClusterAndRejectsCrossScopeResponses(t *testing.T) {
	page := string(consoleHTML)
	view := consoleView(t, "operation-log")
	for _, contract := range []string{
		`id="log-cluster-filter"`, `<option value="all">全部集群</option>`,
		"logClusterId: ''", "const renderOperationLogClusterFilter = () =>",
		"if (selectionChanged) followOperationLogCluster(clusterId);",
		"followOperationLogCluster(byId('cluster-select').value)",
		"if (!state.logClusterId) { renderOperationLog(); return; }",
		"item.operation.cluster_id !== requestedClusterId",
		"const visibleOperations = state.allOperations;",
		"if (state.logClusterId !== 'all') query.set('cluster_id', state.logClusterId);",
		"const scopeLabel = operationLogScopeLabel();",
		"if (selected === 'operation-log' && state.currentUser) void loadOperationLog(false);",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation log must follow the header unless explicitly overridden: missing %q", contract)
		}
	}
	if !strings.Contains(view, "集群范围") {
		t.Fatal("operation log must retain an explicit scope selector")
	}
	if strings.Contains(page, "logClusterId: 'all'") {
		t.Fatal("operation log must not silently default to all clusters")
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
		"--radius:8px", "grid-template-columns:232px minmax(0,1fr)", "border-radius:8px",
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
		"readClusterResult(`/api/v1/clusters/${cluster.resource_id}/topology`)",
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

func TestConsoleRecoversOperationOutcomeWhenDatabaseVIPMoves(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const recoverOperationAfterConnectionLoss = async (idempotencyKey, label) =>",
		"const operationResponseTrackingDelayMs = 12000;",
		"const operationOutcomeTrackingTimeoutMs = 30 * 60 * 1000;",
		"const controlEntryRecoveryTimeoutMs = 2 * 60 * 1000;",
		"const operationStatusRequestTimeoutMs = 5000;",
		"const fetchTrackedOperation = async idempotencyKey =>",
		"window.setTimeout(() => controller.abort(), operationStatusRequestTimeoutMs)",
		"const operation = await fetchTrackedOperation(idempotencyKey);",
		"后台正在执行，正在持续跟踪操作状态",
		"/api/v1/operations?idempotency_key=${encodeURIComponent(idempotencyKey)}",
		"{ signal:controller.signal }",
		"controlEntryUnavailableSince = 0;",
		"Date.now() - controlEntryUnavailableSince >= controlEntryRecoveryTimeoutMs",
		"terminalOperationStatuses.has(operation.status)",
		"[0, 404, 502, 503, 504].includes(error.status)",
		"const executionOutcome = fetchResult('/api/v1/operations/execute', mutationOptions(payload)).then(",
		"const initialOutcome = await Promise.race([",
		"resolve({ tracking:true })",
		"const isRecoverableOperationSubmissionError = error =>",
		"error.message === 'operation state conflict'",
		"error.result && error.result.status === 'running'",
		"if (!isRecoverableOperationSubmissionError(initialOutcome.error)) throw initialOutcome.error;",
		"operation = await recoverOperationAfterConnectionLoss(idempotencyKey, label);",
		"控制入口恢复超时，操作结果待确认，请查看操作日志",
		"操作已超过页面跟踪时限，后台仍可能继续执行",
		"error.committed && error.status === 0",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing VIP-move outcome recovery contract %q", contract)
		}
	}
}

func TestConsolePreservesVerifiedOutcomeUntilReadAfterWriteTopologyConverges(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const clearClusterView = (preserveOperationResult = false) =>",
		"const beginClusterRequest = (clusterId, preserveOperationResult = false, operationIntent = null) =>",
		"const loadSelectedCluster = async ({ preserveOperationResult = false, expectedPrimaryID = '', operationIntent = null } = {}) =>",
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
		"if (state.selectedClusterId && !state.operationRunning && !state.lifecycleRunning && !byId('node-lifecycle-modal').open && !state.clusterLoading) loadSelectedCluster({ preserveOperationResult:true });",
		"await loadSelectedCluster({ preserveOperationResult:true });",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console read-only refresh must preserve the latest operation result: missing %q", contract)
		}
	}
	if strings.Contains(page, "state.selectedClusterId && !state.switchUnlocked && !state.operationRunning") {
		t.Fatal("unlocking a destructive action must not pause topology auto-refresh")
	}
}

func TestConsoleRelocksDestructiveActionWhenClusterOrTargetChanges(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const relockSwitch = (preserveIntent = null) =>", "relockSwitch(operationIntent?.clusterID === clusterId ? operationIntent : null);",
		"state.selectedCandidateId = event.target.value; relockSwitch(); renderOperationContext();",
		"state.selectedRejoinId = event.target.value; relockSwitch(); updateExecutionButtons();",
		"state.activeOperationIntent !== preserveIntent) state.activeOperationIntent.cancelled = true",
		"if (state.activeOperationIntent === intent)", "state.operationRunning = false;\n          relockSwitch();",
		"requireOperationIntent(intent);", "requireOperationIntent(operationIntent);",
		"state.currentUser !== intent.user", "state.selectedClusterId !== intent.clusterID",
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
		"--topology-node-height:116px", "height:var(--topology-node-height)",
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
	if !strings.Contains(page, "card.append(lineOne, lineTwo, lineThree);") {
		t.Fatal("compact topology cards must show placement while keeping immutable identity in the inventory")
	}
	if strings.Contains(page, "card.append(lineOne, lineTwo, identity)") {
		t.Fatal("topology card must not compress resource identity and VIP into the fixed-height node card")
	}
}

func TestConsoleDistinguishesHostContainerAndKubernetesRuntimeLayers(t *testing.T) {
	page := string(consoleHTML)
	if strings.Contains(page, "Kubernetes（当前不支持接管）") || strings.Contains(page, "Kubernetes · 不支持接管") {
		t.Fatal("console still labels the implemented Kubernetes MySQL runtime as unsupported")
	}
	topology := consoleView(t, "topology")
	operations := consoleView(t, "operations")
	for _, contract := range []string{
		`id="topology-runtime"`,
		`id="topology-entry"`,
		`id="operation-runtime"`,
		"const runtimeProfileForDetail = detail =>",
		"const runtimeProfileLabel = detail =>",
		"const instanceRuntimePlacement = (instance, detail = state.clusterDetail) =>",
		"const activeWriterEndpoint = (detail = state.clusterDetail) =>",
		"const writerEndpointLabel = writer =>",
		"主机层（物理机/虚拟机）",
		"容器层（Docker Swarm）",
		"Kubernetes StatefulSet",
		"宿主机 VIP",
		"Kubernetes Service",
		"运行层未登记",
		"workload_bindings",
		"runtime_profile",
		"ha_endpoints",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing runtime boundary contract %q", contract)
		}
	}
	for _, heading := range []string{"运行层", "工作负载", "宿主/调度位置", "业务入口"} {
		if !strings.Contains(topology, heading) {
			t.Fatalf("topology identity table missing runtime heading %q", heading)
		}
	}
	if !strings.Contains(operations, "运行与入口边界") {
		t.Fatal("operations view must expose the workload runtime and client entry boundary")
	}
	if !strings.Contains(operations, "主库与业务入口同步切换") || strings.Contains(operations, "主库与 VIP 同步切换") {
		t.Fatal("operations view must describe both VIP and Kubernetes Service through the generic writer endpoint")
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
		`id="log-search"`, `id="log-cluster-filter"`, `id="log-kind-filter"`, `id="log-status-filter"`,
		"const refreshOperationLogFilters =", "renderOperationLog();", `id="metrics-observed-at"`,
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

func TestConsoleUsesAuthoritativeReplicationLinksForPostgreSQLStandbys(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const replicasForPrimary = (instances, primary, topology = state.topology) =>",
		"topology.links || []",
		"link.source_instance_id === primary.resource_id",
		"linkedTargetIDs.has(instance.resource_id)",
		"['replica', 'standby'].includes(instance.role)",
		"const replicas = replicasForPrimary(instances, primary, state.topology);",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console must derive database-neutral replica placement from topology links: missing %q", contract)
		}
	}
}

func TestConsoleRendersTypedProbeEvidenceWithoutInventingRuntimeState(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const probeOutcome = probe =>",
		"summary.includes('database probe failed')",
		"const instanceAvailability = (instance, topology = state.topology) =>",
		"probeOutcome(probe) === 'database_unavailable'",
		"label:'数据库未启动或不可达'",
		"probeOutcome(probe) === 'credentials_unavailable'",
		"label:'凭据不可用'",
		"label:'尚未采集'",
		"if (!instanceAvailability(instance).available) return '无当前角色'",
		"instanceAvailability(instance, topology).available &&",
		"['replica', 'standby'].includes(instance.role)",
		"detachedNodes.hidden = detached.length === 0",
		"const observedPrimaryInstances = (instances, topology = state.topology) =>",
		"instance.role === 'primary' && instanceAvailability(instance, topology).available",
		"return primaries.length === 1 ? primaries[0] : undefined",
		"return '冲突写主'",
		"已拒绝选择当前主库",
		"const instanceOwnsActiveWriterEndpoint = instance =>",
		"writer.endpoint.instance_id === instance.resource_id",
		"const candidate = availability.available && assessment && assessment.eligible && assessment.rank === 1",
		"if (writerEndpoint && instanceOwnsActiveWriterEndpoint(instance))",
		"instance ? instanceRoleText(instance) : '-'",
		"const primary = currentObservedPrimary(instances);",
		"currentObservedPrimary(topology.instances || [], topology)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console must render probe evidence explicitly instead of showing an offline node as unknown: missing %q", contract)
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
		"const operationLogPageSize = 20;",
		"new URLSearchParams({ view:'page', limit:String(operationLogPageSize) })",
		"if (cursor) query.set('cursor', cursor);",
		"loadOperationLog(false, true)",
		"state.logNextCursor = page.next_cursor; state.logRemaining = page.remaining;",
		"state.logController !== controller",
		"page.items.length > operationLogPageSize",
		"renderOperationLog(append)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation evidence must remain responsive with large histories: missing %q", contract)
		}
	}
	if strings.Contains(page, "view=summary") || strings.Contains(page, "logVisibleLimit") {
		t.Fatal("log pagination must not download all summaries and slice in the browser")
	}
}

func TestConsoleConsolidatesAutomaticRecoveryRetriesByIncident(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"state.logRecordCount = page.record_count",
		"incident_attempt_count",
		"'log-record-count', `${state.logRecordCount} 条原始记录`",
		"原始返回（最近一次，事故共",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("automatic recovery retries must remain auditable without flooding the operator timeline: missing %q", contract)
		}
	}
}

func TestConsoleShowsBlockingCheckWithoutExpandingRawOperationJSON(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const operationLogFailureDetail = operation =>",
		"planChecks.find(check => check.status === 'fail')",
		"prechecks.find(check => check.status === 'fail')",
		"operation-log-message",
		"primary failure has not remained stable for the configured observation window",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("operation log must expose the blocking safety check without raw JSON: missing %q", contract)
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
		"const visibleOperations = state.allOperations;", "state.logNextCursor = page.next_cursor;",
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

// TestConsoleShowsPowerLifecyclePanel verifies the topology view surfaces the
// power lifecycle: a dedicated panel fed by power/status, per-state Chinese
// labels and protection markers, plus one controlled shutdown entry point.
// The browser never asks the operator to paste an approval token: an
// administrator session receives a server-issued one-time approval.
func TestConsoleShowsPowerLifecyclePanel(t *testing.T) {
	page := string(consoleHTML)
	topology := consoleView(t, "topology")
	for _, contract := range []string{
		"数据库启停",
		`id="power-state-badge"`,
		`id="power-protection"`,
		`id="power-history"`,
		`id="power-outage-kind"`,
		`id="power-database-state"`,
		`id="power-failover-policy"`,
		`id="power-control-plane"`,
		"powerStateLabels",
		"powerModeLabel",
		"outage_classification",
		"计划停库",
		"突发故障",
		"管理控制面保持运行",
		"loadPowerStatus(clusterId)",
		"/api/v1/clusters/${clusterId}/power/status",
		"state.powerStatus",
		"powerStatus.power_operation",
		"recovery_freeze",
		"已下电",
		"恢复验证中",
		"已失败（需人工介入）",
		`id="open-power-shutdown"`,
		`id="power-shutdown-dialog"`,
		`id="power-confirm-name"`,
		`id="confirm-power-shutdown"`,
		`id="power-shared-clusters"`,
		`/power/cancel`,
		"coResidentPowerClusters",
		"co_resident_clusters",
		"同宿主集群已安全停库",
		"power/precheck",
		"power/plan",
		"power/execute",
		"输入完整集群名称",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console power lifecycle missing %q", contract)
		}
	}
	if !strings.Contains(topology, "数据库启停") {
		t.Fatalf("power lifecycle panel must live in the topology view")
	}
	for _, forbidden := range []string{`id="approval-token"`, `name="approval_token"`} {
		if strings.Contains(topology, forbidden) {
			t.Fatalf("console must not expose raw approval tokens, found %q", forbidden)
		}
	}
}

func TestConsoleProvidesAdminOnlySignedSoftwareUpdateWorkflow(t *testing.T) {
	page := string(consoleHTML)
	settings := consoleView(t, "settings")
	for _, contract := range []string{
		`id="software-update-panel"`,
		`id="open-software-update-dialog"`,
		`id="software-update-dialog" class="software-update-dialog"`,
		`id="software-update-package-file"`,
		`id="upload-software-update"`,
		`id="execute-software-update"`,
		`id="resume-software-update"`,
		`id="rollback-software-update"`,
		`id="software-update-confirmation-dialog"`,
		`id="software-update-confirmation-input"`,
		`id="software-update-progress-dialog"`,
		`id="software-update-progress-track" role="progressbar"`,
		`id="software-update-progress-event-list"`,
		`<div class="software-update-progress" id="software-update-inline-progress">`,
		`id="toggle-software-update-progress" type="button" aria-expanded="false" aria-controls="software-update-events"`,
		`id="software-update-events" hidden`,
		`id="software-update-progress-toggle-label">展开</span>`,
		`const setInlineSoftwareUpdateProgressExpanded = expanded => {`,
		`byId('software-update-progress-toggle-label').textContent = expanded ? '收起' : '展开'`,
		`events.hidden = !expanded`,
		`setInlineSoftwareUpdateProgressExpanded(!expanded)`,
		`setInlineSoftwareUpdateProgressExpanded(false)`,
		"form.append('package', file, file.name)",
		"fetchResult('/api/v1/platform/updates'",
		"const prepareSoftwareUpdateExecution = async () =>",
		"const planned = await startSoftwareUpdate('plan', '', patchID)",
		"const waitForSoftwareUpdatePlan = async (patchID, waiting) =>",
		"prepared = await waitForSoftwareUpdatePlan(patchID, waiting)",
		"byId('execute-software-update').addEventListener('click', prepareSoftwareUpdateExecution)",
		"state.softwareUpdateAction = mode",
		"byId('software-update-tab').hidden = !canAdministerPlatform()",
		"setSettingsSection(state.settingsSection)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing signed software update contract %q", contract)
		}
	}
	if !strings.Contains(settings, "版本更新") || !strings.Contains(page, "支持 .cgupgrade，兼容旧 .cgpatch") {
		t.Fatal("software update entry must be presented in settings and upload guidance must be presented in the dialog")
	}
	if strings.Contains(settings, `id="software-update-package-file"`) {
		t.Fatal("software update upload controls must not remain inline in the settings panel")
	}
	if strings.Contains(page, `id="plan-software-update"`) {
		t.Fatal("read-only planning must be folded into the rolling-upgrade action")
	}
	if strings.Contains(page, `<details class="software-update-progress"`) {
		t.Fatal("inline software update progress must use an explicit expand/collapse button")
	}
}

func TestSoftwareUpdateDialogPresentsWarningUploadMetadataAndActionInOrder(t *testing.T) {
	page := string(consoleHTML)
	start := strings.Index(page, `<dialog id="software-update-dialog"`)
	if start < 0 {
		t.Fatal("software update dialog is missing")
	}
	endOffset := strings.Index(page[start:], `</dialog>`)
	if endOffset < 0 {
		t.Fatal("software update dialog is not closed")
	}
	dialog := page[start : start+endOffset]
	positions := []struct {
		name     string
		contract string
	}{
		{name: "operation alert", contract: `id="software-update-dialog-alert"`},
		{name: "maintenance warning", contract: `class="software-update-warning"`},
		{name: "package file", contract: `id="software-update-package-file"`},
		{name: "validated package metadata", contract: `id="software-update-package" hidden`},
		{name: "validation result", contract: `id="software-update-validation" data-state="idle"`},
		{name: "rolling upgrade action", contract: `id="execute-software-update"`},
	}
	previous := -1
	for _, item := range positions {
		current := strings.Index(dialog, item.contract)
		if current < 0 {
			t.Fatalf("software update dialog missing %s", item.name)
		}
		if current <= previous {
			t.Fatalf("software update dialog must place %s after the preceding workflow step", item.name)
		}
		previous = current
	}
}

func TestSoftwareUpdateDialogEnablesRollingUpgradeOnlyAfterValidation(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="software-update-validation-title">等待上传校验`,
		`id="execute-software-update" class="primary-button" type="button" disabled>滚动升级`,
		`softwareUpdateValidationState: 'idle'`,
		`state.softwareUpdateValidationState = file ? 'selected'`,
		`state.softwareUpdateValidationState = 'validating'`,
		`state.softwareUpdateValidationState = 'verified'`,
		`state.softwareUpdateValidationState = 'failed'`,
		`validating: ['正在校验'`,
		`verified: ['校验完成'`,
		`failed: ['校验失败'`,
		`.software-update-actions button[hidden] { display:none; }`,
		`packagePanel.hidden = !(pending && !selectedFile && validationState === 'verified')`,
		`validationState !== 'verified'`,
		`state.softwareUpdateValidationMessage = ` + "`" + `升级包 ${uploaded.patch_id} 已通过签名与兼容性校验。` + "`" + `;`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("software update validation workflow is missing %q", contract)
		}
	}
	if !strings.Contains(page, `</div>
      <div class="software-update-validation" id="software-update-validation"`) {
		t.Fatal("validation status and rolling action must remain visible outside the hidden package metadata container")
	}
}

func TestSoftwareUpdateSummaryDistinguishesPendingAndCompletedTargets(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="software-update-target-label">待升级目标版本`,
		`id="software-update-latest-target">尚无待升级包`,
		"const renderSoftwareUpdateTargetSummary = (latest, pending) =>",
		"label.textContent = pending ? '待升级目标版本' : status === 'succeeded' ? '最近完成版本' : '最近处理版本'",
		"value.textContent = `${latest.package.target_version || '-'} · ${softwareUpdateStatusText(status)}`",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("software update summary must explain target-version state: missing %q", contract)
		}
	}
	if strings.Contains(page, `<span>最近目标版本</span>`) {
		t.Fatal("ambiguous recent target version label must not return")
	}
}

func TestSoftwareUpdateErrorsStayAtTopOfOpenDialog(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="software-update-dialog-alert" data-level="error" role="alert" aria-live="assertive" hidden`,
		`.software-update-dialog-alert[hidden] { display:none; }`,
		"const setSoftwareUpdateDialogAlert = (message = '', level = 'error') =>",
		"if (message && !(dialog && dialog.open)) setLiveStatus(message, level !== 'info')",
		"setSoftwareUpdateDialogAlert('只读升级计划正在生成，完成后将自动打开确认框。', 'info')",
		"setSoftwareUpdateDialogAlert(`升级计划生成失败：${error.message}`)",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("software update warning must stay visible inside the modal: missing %q", contract)
		}
	}
	if strings.Contains(page, "setLiveStatus('升级计划尚未准备完成，请检查升级状态后重试。', true)") {
		t.Fatal("plan readiness warning must not be rendered behind the modal")
	}
}

func TestSoftwareUpdateDialogDoesNotPresentCompletedHistoryAsPendingPackage(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"const pendingSoftwareUpdate = () => {",
		"!['succeeded', 'rolled_back'].includes(job.status) ? latest : null",
		"const pending = pendingSoftwareUpdate()",
		"packagePanel.hidden = !(pending && !selectedFile && validationState === 'verified')",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("software update dialog must separate pending packages from completed history: missing %q", contract)
		}
	}
	if strings.Contains(page, "packagePanel.hidden = !latest") {
		t.Fatal("completed latest history must not automatically populate the software update dialog")
	}
}

func TestConsoleShowsRecoverableStructuredSoftwareUpdateProgress(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="open-software-update-progress"`,
		`id="background-software-update-progress"`,
		`data-update-progress-stage="prepare"`,
		`data-update-progress-stage="nodes"`,
		`data-update-progress-stage="verify"`,
		`data-update-progress-stage="complete"`,
		`class="software-update-stage-marker"`,
		`class="software-update-stage-copy"`,
		`dialog.software-update-progress-dialog[open] { display:grid; height:min(760px,calc(100vh - 24px));`,
		`dialog.software-update-progress-dialog[open] > .software-update-progress-body { display:grid; min-height:0; max-height:none; grid-template-rows:max-content max-content minmax(0,1fr); align-content:stretch; gap:12px; overflow:hidden;`,
		`.software-update-progress-events { display:grid; min-height:0; max-height:100%; grid-template-rows:auto minmax(0,1fr); overflow:hidden;`,
		`.software-update-progress-event-list { display:grid; height:100%; min-height:0; max-height:100%; align-content:start; overflow-x:hidden; overflow-y:scroll;`,
		`.software-update-events { display:grid; max-height:280px; gap:6px; overflow-y:auto;`,
		`.software-update-events[hidden] { display:none; }`,
		`.software-update-progress-toggle[aria-expanded="true"] .software-update-progress-toggle-icon`,
		`.software-update-stage { position:relative; display:grid; min-width:0; min-height:70px; justify-items:center; align-content:start; gap:7px;`,
		`.software-update-stage:not(:last-child)::after { position:absolute; z-index:0; top:13px; left:calc(50% + 18px);`,
		`.software-update-stage.done:not(:last-child)::after { background:#278b57; }`,
		`.software-update-stage-marker { position:relative; z-index:1; display:grid; width:28px; height:28px; place-items:center; border:2px solid #c3cec9; border-radius:50%;`,
		`.software-update-stage.active .software-update-stage-marker { border-color:var(--accent); background:var(--accent); color:#fff; box-shadow:0 0 0 4px var(--accent-soft); }`,
		`.software-update-stage.error .software-update-stage-marker { border-color:var(--danger); background:var(--danger); color:#fff; box-shadow:0 0 0 4px var(--danger-soft); }`,
		"stage.setAttribute('aria-current', 'step')",
		"stage.removeAttribute('aria-current')",
		"row.dataset.status = event.status || ''",
		"job && job.progress || {}",
		"维护门禁仍然生效，但当前 Leader 没有活动升级记录",
		"升级维护未闭环",
		"升级待确认",
		"events.slice().reverse()",
		"const pinnedToLatest = previousScrollTop <= 4",
		"eventList.scrollTop = pinnedToLatest ? 0 : Math.max(0, previousScrollTop + (eventList.scrollHeight - previousScrollHeight))",
		"job && job.verification_required",
		"softwareUpdateDateText",
		"控制面维护状态与节点版本需要独立核验",
		"verificationRequired ? '待核验'",
		"state.softwareUpdateProgressDismissed !== key",
		"openSoftwareUpdateProgress()",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing recoverable software update progress contract %q", contract)
		}
	}
	if strings.Contains(page, "events.slice(-8)") {
		t.Fatal("software update progress must keep the full event history inside the scrollable log viewport")
	}
}

func TestConsoleMakesAutomaticFailoverWarningPersistentDuringUpgrade(t *testing.T) {
	page := string(consoleHTML)
	const warning = "系统升级期间无法进行自动切换，请注意关注。"
	if strings.Count(page, warning) < 3 {
		t.Fatalf("mandatory update warning must appear in settings, confirmation, and the global maintenance banner")
	}
	for _, contract := range []string{
		`id="software-update-global-warning"`,
		`role="alert" aria-live="assertive"`,
		"state.controlPlane.update_maintenance_active",
		"softwareUpdateIsActive(item.job)",
		"banner.hidden = !(active || statusActive)",
		"active ? 2000 : 30000",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing persistent update warning contract %q", contract)
		}
	}
}

func TestConsoleRequiresTypedUpgradePackageIDBeforeMutatingSoftwareUpdate(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"byId('software-update-confirmation-input').value === patchID",
		"尚未输入完整升级包 ID，不能执行。",
		"await startSoftwareUpdate(mode, patchID, patchID)",
		"mode === 'rollback' ? 'danger-button' : 'primary-button'",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console must require typed patch confirmation before execute/resume/rollback: missing %q", contract)
		}
	}
}

func TestConsoleExplainsInstallAndRollingUpgradePackages(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"选择 ClusterGuard 签名升级包",
		"这是用于新装或重装的完整离线安装包，不能直接用于滚动升级。",
		"请选择同一 Release 提供的 .cgupgrade 签名升级包。",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console must explain package purpose: missing %q", contract)
		}
	}
}

func TestConsoleProvidesAuditableIndeterminateOperationReview(t *testing.T) {
	page := string(consoleHTML)
	logView := consoleView(t, "operation-log")
	for _, contract := range []string{
		`id="operation-review-dialog"`,
		`id="operation-review-note" maxlength="1024"`,
		`id="submit-operation-review"`,
		"复核不会把该操作改成成功。",
		"原始“结果不确定”状态和全部执行证据继续保留",
		"operation.status === 'indeterminate' && !review && canOperateClusters()",
		"fetchResult(`/api/v1/operations/${operationId}/review`",
		"已复核 · 结果不确定",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("console missing operation review contract %q", contract)
		}
	}
	if !strings.Contains(logView, "操作日志") || !strings.Contains(page, "✓ 标记已复核") {
		t.Fatal("operation review entry must be available from the operation log")
	}
}
