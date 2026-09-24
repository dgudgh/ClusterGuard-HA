package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/store"
)

// The console shows what a node is actually running with. Two properties of the
// platform decide how that view has to be built:
//
//   - the configuration file is read exactly once, by config.Load at start-up,
//     and there is no reload signal, so an edit cannot reach a running process;
//   - the file is node-local, so three controllers can legitimately disagree.
//
// The view therefore reports the values in effect together with their origin
// (file or platform default) and states plainly that reloading is unsupported.

const configurationReloadNote = "本节点配置仅在进程启动时读取一次（cmd/clusterguard/main.go 的 config.Load），" +
	"控制台不提供热重载；此处的“重新读取文件”只重新读取文件用于显示，不会改变正在运行的进程，" +
	"参数改动需要滚动重启控制面。"

// configurationPresence records which keys the file actually contains. It is
// what separates "explicitly set to the platform default" from "the platform
// supplied the default because the key was absent".
type configurationPresence struct {
	sections map[string]map[string]json.RawMessage
}

func readConfigurationPresence(path string) configurationPresence {
	presence := configurationPresence{sections: make(map[string]map[string]json.RawMessage)}
	contents, err := os.ReadFile(path)
	if err != nil {
		return presence
	}
	top := make(map[string]json.RawMessage)
	if err := json.Unmarshal(contents, &top); err != nil {
		return presence
	}
	// Top-level keys live under the empty section so a caller can ask about
	// "https_address"/"metadata_path" without inventing a section name.
	presence.sections[""] = top
	for key, raw := range top {
		nested := make(map[string]json.RawMessage)
		if err := json.Unmarshal(raw, &nested); err != nil {
			continue
		}
		presence.sections[key] = nested
	}
	return presence
}

func (presence configurationPresence) has(section, key string) bool {
	nested, found := presence.sections[section]
	if !found {
		return false
	}
	_, present := nested[key]
	return present
}

func newConfigurationViewProvider(configuration config.File, path string, startedAt time.Time, policy func() store.ClusterPolicy) api.ConfigurationProvider {
	return api.ConfigurationProviderFunc(func(ctx context.Context) (api.ConfigurationView, error) {
		if err := ctx.Err(); err != nil {
			return api.ConfigurationView{}, err
		}
		return buildConfigurationView(configuration, path, startedAt, policy), nil
	})
}

func buildConfigurationView(configuration config.File, path string, startedAt time.Time, policy func() store.ClusterPolicy) api.ConfigurationView {
	presence := readConfigurationPresence(path)
	view := api.ConfigurationView{
		Path:             path,
		ProcessStartedAt: startedAt.UTC(),
		ReloadSupported:  false,
		ReloadNote:       configurationReloadNote,
		Warnings:         append([]string{}, configuration.DeprecationWarnings...),
	}
	if info, err := os.Stat(path); err == nil {
		view.FilePresent = true
		view.FileModifiedAt = info.ModTime().UTC()
	} else {
		view.Warnings = append(view.Warnings, fmt.Sprintf("配置文件当前不可读（%v）；以下为进程启动时已生效的值。", err))
	}

	view.Sections = []api.ConfigurationSection{
		runtimeSection(configuration, presence),
		consensusSection(configuration, presence),
		engineSection("mysql", "MySQL", presence,
			configuration.MySQL.Enabled, configuration.MySQL.DiscoveryIntervalSeconds,
			configuration.MySQL.DiscoveryTimeoutSeconds,
			failoverSettings{
				enabled: configuration.MySQL.AutomaticFailoverEnabled,
				intervalSeconds: configuration.MySQL.AutomaticFailoverIntervalSeconds,
				retrySeconds:    configuration.MySQL.AutomaticFailoverRetrySeconds,
				minimumObservations: configuration.MySQL.AutomaticFailoverMinimumObservations,
				failureWindowSeconds: configuration.MySQL.AutomaticFailoverFailureWindowSeconds,
				operationTimeoutSeconds: configuration.MySQL.AutomaticFailoverOperationTimeoutSeconds,
			},
			[]api.ConfigurationValue{
				credentialValue(presence, "mysql", "discovery.password_env", "发现凭据", configuration.MySQL.Discovery.PasswordEnv),
				credentialValue(presence, "mysql", "operation.password_env", "操作凭据", configuration.MySQL.Operation.PasswordEnv),
				credentialValue(presence, "mysql", "replication.password_env", "复制凭据", configuration.MySQL.Replication.PasswordEnv),
			}),
		engineSection("postgresql", "PostgreSQL", presence,
			configuration.PostgreSQL.Enabled, configuration.PostgreSQL.DiscoveryIntervalSeconds,
			configuration.PostgreSQL.DiscoveryTimeoutSeconds,
			failoverSettings{
				enabled: configuration.PostgreSQL.AutomaticFailoverEnabled,
				intervalSeconds: configuration.PostgreSQL.AutomaticFailoverIntervalSeconds,
				retrySeconds:    configuration.PostgreSQL.AutomaticFailoverRetrySeconds,
				minimumObservations: configuration.PostgreSQL.AutomaticFailoverMinimumObservations,
				failureWindowSeconds: configuration.PostgreSQL.AutomaticFailoverFailureWindowSeconds,
				operationTimeoutSeconds: configuration.PostgreSQL.AutomaticFailoverOperationTimeoutSeconds,
			},
			[]api.ConfigurationValue{
				credentialValue(presence, "postgresql", "discovery.password_env", "发现凭据", configuration.PostgreSQL.Discovery.PasswordEnv),
				credentialValue(presence, "postgresql", "operation.password_env", "操作凭据", configuration.PostgreSQL.Operation.PasswordEnv),
				credentialValue(presence, "postgresql", "replication.password_env", "复制凭据", configuration.PostgreSQL.Replication.PasswordEnv),
			}),
		engineSection("oracle", "Oracle", presence,
			configuration.Oracle.Enabled, configuration.Oracle.DiscoveryIntervalSeconds,
			configuration.Oracle.DiscoveryTimeoutSeconds, failoverSettings{},
			[]api.ConfigurationValue{
				credentialValue(presence, "oracle", "discovery.password_env", "发现凭据", configuration.Oracle.Discovery.PasswordEnv),
				credentialValue(presence, "oracle", "operation.password_env", "操作凭据", configuration.Oracle.Operation.PasswordEnv),
			}),
		engineSection("sqlserver", "SQL Server", presence,
			configuration.SQLServer.Enabled, configuration.SQLServer.DiscoveryIntervalSeconds,
			configuration.SQLServer.DiscoveryTimeoutSeconds, failoverSettings{},
			[]api.ConfigurationValue{
				credentialValue(presence, "sqlserver", "discovery.password_env", "发现凭据", configuration.SQLServer.Discovery.PasswordEnv),
				credentialValue(presence, "sqlserver", "operation.password_env", "操作凭据", configuration.SQLServer.Operation.PasswordEnv),
			}),
		agentSection(configuration, presence),
		fencingSection(configuration, presence),
		nodeLifecycleSection(configuration, presence),
		clusterPolicySection(policy),
	}
	return view
}

func runtimeSection(configuration config.File, presence configurationPresence) api.ConfigurationSection {
	tlsValue := "未启用（明文 HTTP）"
	if strings.TrimSpace(configuration.TLSCertFile) != "" {
		tlsValue = fmt.Sprintf("已启用（证书 %s）", configuration.TLSCertFile)
	}
	values := []api.ConfigurationValue{
		configurationValue(presence, "", "http_address", "HTTP 监听地址", configuration.HTTPAddress, true, ""),
		configurationValue(presence, "", "tls_cert_file", "TLS", tlsValue, true,
			"证书与私钥路径只在节点本地；控制台不读取文件内容"),
		configurationValue(presence, "", "allow_insecure_http", "允许明文 HTTP",
			enabledText(configuration.AllowInsecureHTTP), true, ""),
		configurationValue(presence, "", "metadata_path", "元数据路径", configuration.MetadataPath, true,
			"该路径决定快照与引导口令文件的位置"),
		credentialValue(presence, "", "control_token_env", "控制令牌", configuration.ControlTokenEnv),
		credentialValue(presence, "", "monitoring_token_env", "监控令牌", configuration.MonitoringTokenEnv),
		credentialValue(presence, "", "approval_token_env", "审批令牌", configuration.ApprovalTokenEnv),
		credentialValue(presence, "", "bootstrap_admin_password_env", "引导管理员口令", configuration.BootstrapAdminPasswordEnv),
	}
	return api.ConfigurationSection{
		Key: "runtime", Label: "控制面运行参数",
		Note:   "HTTP/TLS/元数据路径属于节点本地引导配置，改动必须重启该节点。",
		Values: values,
	}
}

func consensusSection(configuration config.File, presence configurationPresence) api.ConfigurationSection {
	peers := make([]string, 0, len(configuration.Consensus.Peers))
	for _, peer := range configuration.Consensus.Peers {
		entry := fmt.Sprintf("%s@%s", peer.ResourceID, peer.Address)
		if strings.TrimSpace(peer.APIAddress) != "" {
			entry = fmt.Sprintf("%s（API %s）", entry, peer.APIAddress)
		}
		peers = append(peers, entry)
	}
	sort.Strings(peers)
	peerValue := "未配置"
	if len(peers) > 0 {
		peerValue = strings.Join(peers, "；")
	}
	values := []api.ConfigurationValue{
		configurationValue(presence, "consensus", "enabled", "Raft 共识", enabledText(configuration.Consensus.Enabled), true, ""),
		configurationValue(presence, "consensus", "local_id", "本节点控制器 ID", string(configuration.Consensus.LocalID), true,
			"节点身份写入元数据，扩容或重建时不得伪造"),
		configurationValue(presence, "consensus", "bind_address", "Raft 监听", configuration.Consensus.BindAddress, true, ""),
		configurationValue(presence, "consensus", "advertise_address", "Raft 对外地址", configuration.Consensus.AdvertiseAddress, true,
			"必须是其他节点可达的地址，改错会让多数派失联"),
		configurationValue(presence, "consensus", "data_directory", "Raft 数据目录", configuration.Consensus.DataDirectory, true, ""),
		configurationValue(presence, "consensus", "bootstrap", "初始引导成员", enabledText(configuration.Consensus.Bootstrap), true, ""),
		configurationValue(presence, "consensus", "apply_timeout_seconds", "提交超时",
			secondsText(configuration.Consensus.ApplyTimeoutSeconds, "平台默认"), true, ""),
		configurationValue(presence, "consensus", "peers", "成员列表", peerValue, true, ""),
	}
	return api.ConfigurationSection{
		Key: "consensus", Label: "Raft 共识与成员",
		Note:   "每台控制器各有一份，三台之间 address 必须互不相同。",
		Values: values,
	}
}

type failoverSettings struct {
	enabled                 bool
	intervalSeconds         int
	retrySeconds            int
	minimumObservations     int
	failureWindowSeconds    int
	operationTimeoutSeconds int
}

func engineSection(section, label string, presence configurationPresence, enabled bool, discoveryInterval, discoveryTimeout int, failover failoverSettings, credentials []api.ConfigurationValue) api.ConfigurationSection {
	discoveryValue := "未启用（该引擎不参与发现）"
	if enabled && discoveryInterval > 0 {
		discoveryValue = fmt.Sprintf("%d 秒", discoveryInterval)
	}
	// The per-engine discovery timeout default differs (MySQL/PostgreSQL fall
	// back to one second, Oracle/SQL Server to ten), so the view must not print
	// a single invented number for a value it was not given.
	timeoutValue := "未启用（该引擎不参与发现）"
	if enabled {
		timeoutValue = secondsText(discoveryTimeout, "平台默认")
	}
	values := []api.ConfigurationValue{
		configurationValue(presence, section, "enabled", "纳管状态", enabledText(enabled), true, ""),
		configurationValue(presence, section, "discovery_interval_seconds", "发现节奏", discoveryValue, true,
			"未配置该键时引擎不进入发现调度"),
		configurationValue(presence, section, "discovery_timeout_seconds", "发现超时", timeoutValue, true, ""),
	}
	restartNote := "当前只来自配置文件：改动需重启控制面；集群策略可覆盖此值且即时生效"
	if failover.enabled || failover.intervalSeconds > 0 || failover.minimumObservations > 0 {
		values = append(values,
			configurationValue(presence, section, "automatic_failover_enabled", "自动故障切换", enabledText(failover.enabled), true, ""),
			configurationValue(presence, section, "automatic_failover_interval_seconds", "切换评估间隔",
				secondsText(failover.intervalSeconds, "平台默认"), true, ""),
			configurationValue(presence, section, "automatic_failover_retry_seconds", "切换重试间隔",
				secondsText(failover.retrySeconds, "平台默认"), true, ""),
			configurationValue(presence, section, "automatic_failover_minimum_observations", "故障证据观测次数",
				strconv.Itoa(failover.minimumObservations), true, restartNote),
			configurationValue(presence, section, "automatic_failover_failure_window_seconds", "故障证据窗口",
				fmt.Sprintf("%d 秒", failover.failureWindowSeconds), true,
				"证据窗口必须长于发现节奏，否则窗口永远填不满"),
			configurationValue(presence, section, "automatic_failover_operation_timeout_seconds", "切换操作预算",
				fmt.Sprintf("%d 秒", failover.operationTimeoutSeconds), true,
				"操作预算必须长于验证阶段（MySQL/PostgreSQL 30 秒、Oracle 4 分钟）"),
		)
	}
	values = append(values, credentials...)
	return api.ConfigurationSection{
		Key:    "engine:" + section,
		Label:  "引擎：" + label,
		Note:   "范围与默认值来自 internal/config：证据窗口短于发现节奏则永远不满足，操作预算短于验证阶段会被中途掐断。",
		Values: values,
	}
}

func agentSection(configuration config.File, presence configurationPresence) api.ConfigurationSection {
	values := []api.ConfigurationValue{
		configurationValue(presence, "agent", "enabled", "数据面 Agent", enabledText(configuration.Agent.Enabled), true, ""),
		configurationValue(presence, "agent", "user", "Agent 用户", textValue(configuration.Agent.User, "未配置"), true, ""),
		credentialValue(presence, "agent", "identity_file", "Agent 私钥", configuration.Agent.IdentityFile),
		configurationValue(presence, "agent", "known_hosts_file", "known_hosts", textValue(configuration.Agent.KnownHostsFile, "未配置"), true, ""),
		configurationValue(presence, "agent", "ssh_binary", "SSH 可执行文件", textValue(configuration.Agent.SSHBinary, "平台默认"), true, ""),
		configurationValue(presence, "agent", "agent_binary", "Agent 可执行文件", textValue(configuration.Agent.AgentBinary, "平台默认"), true, ""),
		configurationValue(presence, "agent", "agent_config_path", "Agent 配置路径", textValue(configuration.Agent.AgentConfigPath, "平台默认"), true, ""),
		configurationValue(presence, "agent", "command_timeout_seconds", "命令超时",
			secondsText(configuration.Agent.CommandTimeoutSeconds, "平台默认"), true, ""),
		configurationValue(presence, "agent", "mutation_timeout_seconds", "变更超时",
			secondsText(configuration.Agent.MutationTimeoutSeconds, "平台默认"), true, ""),
		configurationValue(presence, "agent", "max_concurrent_sessions", "最大并发会话",
			countText(configuration.Agent.MaxConcurrentSessions), true, ""),
		credentialValue(presence, "agent", "shared_secret_env", "共享密钥", configuration.Agent.SharedSecretEnv),
	}
	return api.ConfigurationSection{
		Key: "agent", Label: "数据面 Agent",
		Note:   "Agent 负责本地隔离与就绪标记上报。",
		Values: values,
	}
}

func fencingSection(configuration config.File, presence configurationPresence) api.ConfigurationSection {
	values := []api.ConfigurationValue{
		configurationValue(presence, "fencing", "enabled", "隔离增强程序", enabledText(configuration.Fencing.Enabled), true, ""),
		configurationValue(presence, "fencing", "executable_path", "隔离程序路径",
			textValue(configuration.Fencing.ExecutablePath, "未配置（仅用 Agent 本地隔离）"), true, ""),
		configurationValue(presence, "fencing", "timeout_seconds", "隔离超时",
			secondsText(configuration.Fencing.TimeoutSeconds, "平台默认"), true, ""),
		configurationValue(presence, "fencing", "agent_quorum_enabled", "Agent 多数派门禁",
			enabledText(configuration.Fencing.AgentQuorumEnabled), true, ""),
		configurationValue(presence, "fencing", "agent_quorum_grace_seconds", "多数派宽限",
			secondsText(configuration.Fencing.AgentQuorumGraceSeconds, "平台默认"), true, ""),
	}
	return api.ConfigurationSection{
		Key: "fencing", Label: "隔离与 Agent 多数派",
		Note:   "外部隔离程序失败时平台不降级为“假装隔离成功”。",
		Values: values,
	}
}

func nodeLifecycleSection(configuration config.File, presence configurationPresence) api.ConfigurationSection {
	values := []api.ConfigurationValue{
		configurationValue(presence, "node_lifecycle", "enabled", "节点生命周期", enabledText(configuration.NodeLifecycle.Enabled), true, ""),
		configurationValue(presence, "node_lifecycle", "executor_path", "执行器路径",
			textValue(configuration.NodeLifecycle.ExecutorPath, "未配置"), true, ""),
		configurationValue(presence, "node_lifecycle", "package_repository", "软件源",
			textValue(configuration.NodeLifecycle.PackageRepository, "未配置"), true, ""),
		configurationValue(presence, "node_lifecycle", "known_hosts_file", "known_hosts",
			textValue(configuration.NodeLifecycle.KnownHostsFile, "未配置"), true, ""),
		credentialValue(presence, "node_lifecycle", "identity_file", "节点操作私钥", configuration.NodeLifecycle.IdentityFile),
		configurationValue(presence, "node_lifecycle", "jq_binary", "jq 路径",
			textValue(configuration.NodeLifecycle.JQBinary, "平台默认"), true, ""),
		configurationValue(presence, "node_lifecycle", "control_certificate_validity_days", "控制面证书有效期",
			daysText(configuration.NodeLifecycle.ControlCertificateValidityDays), true, ""),
		credentialValue(presence, "node_lifecycle", "ssh_password_env", "节点 SSH 口令", configuration.NodeLifecycle.SSHPasswordEnv),
		credentialValue(presence, "node_lifecycle", "mysql_root_password_env", "MySQL root 口令", configuration.NodeLifecycle.MySQLRootPasswordEnv),
		configurationValue(presence, "node_lifecycle", "mysql_root_remote_host", "MySQL 远程 root",
			textValue(configuration.NodeLifecycle.MySQLRootRemoteHost, "未开放远程 root"), true,
			"为空表示控制面不创建、也不修改远程 root"),
		credentialValue(presence, "node_lifecycle", "replication_password_env", "复制口令", configuration.NodeLifecycle.ReplicationPasswordEnv),
		credentialValue(presence, "node_lifecycle", "postgresql_admin_password_env", "PostgreSQL 管理口令", configuration.NodeLifecycle.PostgreSQLAdminPasswordEnv),
		credentialValue(presence, "node_lifecycle", "postgresql_replication_password_env", "PostgreSQL 复制口令", configuration.NodeLifecycle.PostgreSQLReplicationPasswordEnv),
	}
	return api.ConfigurationSection{
		Key: "node_lifecycle", Label: "节点生命周期与受管凭据",
		Note:   "凭据一律只按环境变量引用显示；控制面不返回其内容，也不写入元数据。",
		Values: values,
	}
}

// clusterPolicySection shows the replicated half of failover tuning. Unlike
// everything above it, these values are cluster-wide, changeable from the
// console, audited, and applied by the next round: no restart. An empty policy
// is the normal state and means every node keeps using its own configuration
// file.
func clusterPolicySection(policy func() store.ClusterPolicy) api.ConfigurationSection {
	section := api.ConfigurationSection{
		Key:   "cluster_policy",
		Label: "集群策略（复制存储）",
		Note: "策略存在 Raft 复制存储里：控制台可改、每次改动都会记审计，运行时下一轮即生效，无需重启。" +
			"未设置的字段继续沿用上面的配置文件取值。",
	}
	current := store.ClusterPolicy{}
	if policy != nil {
		current = policy()
	}
	if len(current.Engines) == 0 {
		section.Values = []api.ConfigurationValue{{
			Key:             "cluster_policy",
			Label:           "策略覆盖",
			Value:           "未设置（全部沿用节点配置文件）",
			Source:          api.ConfigurationSourceDefault,
			RestartRequired: false,
			Note:            "在此设置后：仅覆盖指定字段，清空即恢复配置文件取值。",
		}}
		return section
	}
	engines := make([]string, 0, len(current.Engines))
	for engine := range current.Engines {
		engines = append(engines, engine)
	}
	sort.Strings(engines)
	for _, engine := range engines {
		settings := current.Engines[engine]
		section.Values = append(section.Values, api.ConfigurationValue{
			Key:             "engine:" + engine,
			Label:           "引擎策略：" + engine,
			Value:           clusterPolicyValueText(settings),
			Source:          api.ConfigurationSourcePolicy,
			RestartRequired: false,
			Note:            "即时生效；改动会重置该引擎已累积的故障证据序列",
		})
	}
	if updatedBy := strings.TrimSpace(current.UpdatedBy); updatedBy != "" {
		section.Values = append(section.Values, api.ConfigurationValue{
			Key:             "updated_by",
			Label:           "最近修改",
			Value:           updatedBy + " · " + current.UpdatedAt.Local().Format("2006-01-02 15:04:05"),
			Source:          api.ConfigurationSourcePolicy,
			RestartRequired: false,
			Note:            "完整操作记录见审计",
		})
	}
	return section
}

func clusterPolicyValueText(settings store.ClusterEnginePolicy) string {
	parts := make([]string, 0, 4)
	if settings.AutomaticFailoverMinimumObservations > 0 {
		parts = append(parts, fmt.Sprintf("故障证据观测次数 %d", settings.AutomaticFailoverMinimumObservations))
	}
	if settings.AutomaticFailoverFailureWindowSeconds > 0 {
		parts = append(parts, fmt.Sprintf("证据窗口 %d 秒", settings.AutomaticFailoverFailureWindowSeconds))
	}
	if settings.AutomaticFailoverOperationTimeoutSeconds > 0 {
		parts = append(parts, fmt.Sprintf("切换操作预算 %d 秒", settings.AutomaticFailoverOperationTimeoutSeconds))
	}
	if settings.AutomaticFailoverSuppressed {
		parts = append(parts, "维护抑制：暂停自动切换")
	}
	return strings.Join(parts, "、")
}

func configurationValue(presence configurationPresence, section, key, label, value string, restartRequired bool, note string) api.ConfigurationValue {
	source := api.ConfigurationSourceDefault
	// The empty section holds top-level keys, so it is a real lookup and must
	// not be skipped; skipping it would mark every top-level key as a default.
	if presence.has(section, key) {
		source = api.ConfigurationSourceFile
	}
	return api.ConfigurationValue{
		Key:             key,
		Label:           label,
		Value:           value,
		Source:          source,
		RestartRequired: restartRequired,
		Note:            note,
	}
}

func credentialValue(presence configurationPresence, section, key, label, envName string) api.ConfigurationValue {
	value := "未配置"
	if trimmed := strings.TrimSpace(envName); trimmed != "" {
		value = "环境变量 " + trimmed
	}
	credential := configurationValue(presence, section, key, label, value, true,
		"凭据只按环境变量引用展示：控制台不读取、也不返回其内容")
	credential.CredentialRef = true
	return credential
}

func textValue(text, fallback string) string {
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		return trimmed
	}
	return fallback
}

func enabledText(flag bool) string {
	if flag {
		return "启用"
	}
	return "停用"
}

func secondsText(seconds int, fallback string) string {
	if seconds <= 0 {
		return fallback
	}
	return fmt.Sprintf("%d 秒", seconds)
}

func countText(count int) string {
	if count <= 0 {
		return "平台默认"
	}
	return strconv.Itoa(count)
}

func daysText(days int) string {
	if days <= 0 {
		return "平台默认"
	}
	return fmt.Sprintf("%d 天", days)
}
