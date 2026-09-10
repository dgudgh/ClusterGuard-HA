package api

import (
	"strings"
	"testing"
)

func TestConsoleClusterLoadingKeepsSelectionAndFailsClosed(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`id="cluster-load-notice"`, `id="retry-cluster-load"`,
		"if (!sameCluster) clearClusterView(preserveOperationResult);",
		"byId('topology-risk').textContent = '未知';",
		"byId('topology-connector').hidden = true;",
		"canOperateClusters() && state.clusterDataReady && !state.clusterLoading && !state.clusterLoadError",
		"capabilityAvailable('execute') && cluster && cluster.resource_id === state.selectedClusterId",
		"if (!ordinaryOperationAllowed(kind, targetID)) return null;",
		"const ordinaryOperationAllowed = (kind, targetID) => !state.operationRunning && state.switchUnlocked",
		"const timer = window.setTimeout(() => controller.abort(), 15000);",
		"if (clusterId !== state.selectedClusterId) return;",
		"commitIfCurrent(generation, () => {\n        state.powerStatus = powerStatus || null;",
		"renderClusterLoadStatus('error'", "当前显示上次观测，不代表最新状态",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing selected-cluster loading contract %q", contract)
		}
	}
}

func TestConsoleClassicThemePreservesLayoutFixes(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		"--canvas:#f5f5f7;", "--accent:#0071e3;", "--danger:#d70015;",
		"@media (prefers-reduced-motion:reduce)",
		"grid-auto-rows:max-content;", "letter-spacing:0;",
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing classic theme contract %q", contract)
		}
	}
	for _, removed := range []string{"--paper-grain", "--display-font", "paper-reveal"} {
		if strings.Contains(page, removed) {
			t.Fatalf("discarded paper theme must not remain: %q", removed)
		}
	}
}

func TestConsoleFleetToolbarKeepsAccessibleFiltersWithoutInstructionClutter(t *testing.T) {
	page := string(consoleHTML)
	for _, contract := range []string{
		`class="panel-header fleet-toolbar"`, `class="fleet-meta" aria-live="polite"`,
		`role="search" aria-label="筛选集群"`, `for="fleet-search"`, `for="fleet-health-filter"`,
		`class="visually-hidden">筛选集群`, `class="visually-hidden">健康状态`,
	} {
		if !strings.Contains(page, contract) {
			t.Fatalf("missing accessible fleet toolbar contract %q", contract)
		}
	}
	if strings.Contains(page, "异常优先 · 点击集群进入拓扑") {
		t.Fatal("fleet toolbar must not repeat navigation instructions below its title")
	}
}
