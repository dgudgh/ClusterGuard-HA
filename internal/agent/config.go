package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

var (
	serviceNamePattern     = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
	osUserPattern          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	postgresSQLNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.@-]*$`)
	oracleNamePattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#.-]*$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hostnamePattern        = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
)

const dockerFenceDirectory = "/etc/clusterguard/docker"

type PostgreSQLPeer struct {
	InstanceID model.ResourceID `json:"instance_id"`
	NodeID     model.ResourceID `json:"node_id"`
	Hostname   string           `json:"hostname,omitempty"`
	IPAddress  string           `json:"ip_address,omitempty"`
	Port       int              `json:"port"`
}

type ClusterPolicy struct {
	RecoveryArchiveID             model.ResourceID  `json:"-"`
	RecoveryControllerAddresses   []string          `json:"-"`
	ClusterID                     model.ResourceID  `json:"cluster_id"`
	InstanceID                    model.ResourceID  `json:"instance_id"`
	RuntimeKind                   model.RuntimeKind `json:"runtime_kind,omitempty"`
	VIP                           string            `json:"vip"`
	Interface                     string            `json:"interface"`
	Prefix                        int               `json:"prefix"`
	MySQLPort                     int               `json:"mysql_port"`
	MySQLBinary                   string            `json:"mysql_binary,omitempty"`
	MySQLDefaultsFile             string            `json:"mysql_defaults_file"`
	MySQLService                  string            `json:"mysql_service,omitempty"`
	MySQLServerBinary             string            `json:"mysql_server_binary,omitempty"`
	MySQLServerDefaultsFile       string            `json:"mysql_server_defaults_file,omitempty"`
	DockerSwarmService            string            `json:"docker_swarm_service,omitempty"`
	DockerSwarmServiceID          string            `json:"docker_swarm_service_id,omitempty"`
	DockerMySQLBinary             string            `json:"docker_mysql_binary,omitempty"`
	DockerFenceFile               string            `json:"docker_fence_file,omitempty"`
	Engine                        model.Engine      `json:"engine,omitempty"`
	PostgreSQLNodeID              model.ResourceID  `json:"postgresql_node_id,omitempty"`
	PostgreSQLHostname            string            `json:"postgresql_hostname,omitempty"`
	PostgreSQLPort                int               `json:"postgresql_port,omitempty"`
	DockerPostgreSQLPort          int               `json:"docker_postgresql_port,omitempty"`
	PostgreSQLService             string            `json:"postgresql_service,omitempty"`
	PostgreSQLUser                string            `json:"postgresql_user,omitempty"`
	PostgreSQLDataDirectory       string            `json:"postgresql_data_directory,omitempty"`
	DockerPostgreSQLDataDirectory string            `json:"docker_postgresql_data_directory,omitempty"`
	PostgreSQLBinaryDirectory     string            `json:"postgresql_binary_directory,omitempty"`
	PostgreSQLPassfile            string            `json:"postgresql_passfile,omitempty"`
	PostgreSQLDatabase            string            `json:"postgresql_database,omitempty"`
	PostgreSQLReplicationUser     string            `json:"postgresql_replication_user,omitempty"`
	PostgreSQLPeers               []PostgreSQLPeer  `json:"postgresql_peers,omitempty"`
	OracleHome                    string            `json:"oracle_home,omitempty"`
	OracleSID                     string            `json:"oracle_sid,omitempty"`
	OracleOSUser                  string            `json:"oracle_os_user,omitempty"`
	OracleDGMGRLBinary            string            `json:"oracle_dgmgrl_binary,omitempty"`
	OracleUsername                string            `json:"oracle_username,omitempty"`
	OracleAuthenticationRole      string            `json:"oracle_authentication_role,omitempty"`
	OraclePasswordEnv             string            `json:"oracle_password_env,omitempty"`
	OraclePassword                string            `json:"-"`
	OracleConnectIdentifier       string            `json:"oracle_connect_identifier,omitempty"`
	OracleDatabaseUniqueName      string            `json:"oracle_database_unique_name,omitempty"`
	OracleBrokerConfiguration     string            `json:"oracle_broker_configuration,omitempty"`
	OracleMembers                 []string          `json:"oracle_members,omitempty"`
}

type Config struct {
	SharedSecret            string
	Clusters                map[model.ResourceID]ClusterPolicy
	ControllerURLs          []string
	ControllerCAFile        string
	ControllerServerName    string
	AllowInsecureHTTP       bool
	ReconcileTimeoutSeconds int
	IPBinary                string
	ARPingBinary            string
	MySQLBinary             string
	DockerBinary            string
	DockerConfigDirectory   string
	RoleStateDirectory      string
	DecisionStateDirectory  string
	MutationStateDirectory  string
}

type fileConfig struct {
	SharedSecretEnv         string          `json:"shared_secret_env"`
	ControllerURLs          []string        `json:"controller_urls,omitempty"`
	ControllerCAFile        string          `json:"controller_ca_file,omitempty"`
	ControllerServerName    string          `json:"controller_server_name,omitempty"`
	AllowInsecureHTTP       bool            `json:"allow_insecure_http,omitempty"`
	ReconcileTimeoutSeconds int             `json:"reconcile_timeout_seconds,omitempty"`
	IPBinary                string          `json:"ip_binary"`
	ARPingBinary            string          `json:"arping_binary"`
	MySQLBinary             string          `json:"mysql_binary"`
	DockerBinary            string          `json:"docker_binary,omitempty"`
	DockerConfigDirectory   string          `json:"docker_config_directory,omitempty"`
	RoleStateDirectory      string          `json:"role_state_directory"`
	DecisionStateDirectory  string          `json:"decision_state_directory,omitempty"`
	MutationStateDirectory  string          `json:"mutation_state_directory,omitempty"`
	Clusters                []ClusterPolicy `json:"clusters"`
}

func LoadConfig(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read agent configuration: %w", err)
	}
	var file fileConfig
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("decode agent configuration: %w", err)
	}
	secretEnvironment := strings.TrimSpace(file.SharedSecretEnv)
	if secretEnvironment == "" {
		return Config{}, fmt.Errorf("shared_secret_env is required")
	}
	configuration := Config{
		SharedSecret: os.Getenv(secretEnvironment), Clusters: make(map[model.ResourceID]ClusterPolicy),
		ControllerURLs: append([]string{}, file.ControllerURLs...), ControllerCAFile: strings.TrimSpace(file.ControllerCAFile),
		ControllerServerName: strings.TrimSpace(file.ControllerServerName), AllowInsecureHTTP: file.AllowInsecureHTTP,
		ReconcileTimeoutSeconds: file.ReconcileTimeoutSeconds,
		IPBinary:                strings.TrimSpace(file.IPBinary), ARPingBinary: strings.TrimSpace(file.ARPingBinary),
		MySQLBinary: strings.TrimSpace(file.MySQLBinary), DockerBinary: strings.TrimSpace(file.DockerBinary),
		DockerConfigDirectory:  strings.TrimSpace(file.DockerConfigDirectory),
		RoleStateDirectory:     strings.TrimSpace(file.RoleStateDirectory),
		DecisionStateDirectory: strings.TrimSpace(file.DecisionStateDirectory),
		MutationStateDirectory: strings.TrimSpace(file.MutationStateDirectory),
	}
	if strings.TrimSpace(configuration.SharedSecret) == "" {
		return Config{}, fmt.Errorf("agent shared secret environment variable %s is empty", secretEnvironment)
	}
	if configuration.IPBinary == "" {
		configuration.IPBinary = "/sbin/ip"
	}
	if configuration.ARPingBinary == "" {
		configuration.ARPingBinary = "/usr/sbin/arping"
	}
	if configuration.MySQLBinary == "" {
		configuration.MySQLBinary = "/usr/local/mysql/bin/mysql"
	}
	if configuration.DockerBinary == "" {
		configuration.DockerBinary = "/usr/bin/docker"
	}
	if !filepath.IsAbs(configuration.DockerBinary) || filepath.Base(configuration.DockerBinary) != "docker" {
		return Config{}, fmt.Errorf("docker_binary must be an absolute docker path")
	}
	if configuration.RoleStateDirectory == "" {
		configuration.RoleStateDirectory = "/var/lib/clusterguard-agent/roles"
	}
	if !filepath.IsAbs(configuration.RoleStateDirectory) {
		return Config{}, fmt.Errorf("role_state_directory must be an absolute path")
	}
	if configuration.DockerConfigDirectory == "" {
		configuration.DockerConfigDirectory = filepath.Join(filepath.Dir(configuration.RoleStateDirectory), "docker-cli")
	}
	if !filepath.IsAbs(configuration.DockerConfigDirectory) {
		return Config{}, fmt.Errorf("docker_config_directory must be an absolute path")
	}
	if configuration.DecisionStateDirectory == "" {
		configuration.DecisionStateDirectory = "/var/lib/clusterguard-agent/decisions"
	}
	if configuration.MutationStateDirectory == "" {
		configuration.MutationStateDirectory = "/var/lib/clusterguard-agent/mutations"
	}
	if configuration.ReconcileTimeoutSeconds <= 0 {
		configuration.ReconcileTimeoutSeconds = 5
	}
	if configuration.ControllerCAFile != "" && !filepath.IsAbs(configuration.ControllerCAFile) {
		return Config{}, fmt.Errorf("controller_ca_file must be an absolute path")
	}
	if !filepath.IsAbs(configuration.DecisionStateDirectory) {
		return Config{}, fmt.Errorf("decision_state_directory must be an absolute path")
	}
	if !filepath.IsAbs(configuration.MutationStateDirectory) {
		return Config{}, fmt.Errorf("mutation_state_directory must be an absolute path")
	}
	for index, raw := range configuration.ControllerURLs {
		normalized, err := normalizeControllerURL(raw, configuration.AllowInsecureHTTP)
		if err != nil {
			return Config{}, err
		}
		configuration.ControllerURLs[index] = normalized
	}
	for _, policy := range file.Clusters {
		if !model.ValidResourceID(policy.ClusterID) || !model.ValidResourceID(policy.InstanceID) {
			return Config{}, fmt.Errorf("agent cluster and instance UUIDs are required")
		}
		if _, exists := configuration.Clusters[policy.ClusterID]; exists {
			return Config{}, fmt.Errorf("duplicate agent cluster policy")
		}
		policy.MySQLBinary = strings.TrimSpace(policy.MySQLBinary)
		if policy.MySQLBinary != "" && !filepath.IsAbs(policy.MySQLBinary) {
			return Config{}, fmt.Errorf("cluster mysql_binary must be an absolute path")
		}
		if policy.Engine == "" {
			policy.Engine = model.EngineMySQL
		}
		if policy.RuntimeKind == "" {
			policy.RuntimeKind = model.RuntimeLinux
		}
		if !policy.RuntimeKind.Valid() {
			return Config{}, fmt.Errorf("agent runtime_kind must be linux, docker, or kubernetes")
		}
		if !policy.Engine.Valid() || (policy.Engine != model.EngineMySQL && policy.Engine != model.EnginePostgreSQL && policy.Engine != model.EngineOracle) {
			return Config{}, fmt.Errorf("agent cluster engine must be mysql, postgresql, or oracle")
		}
		if policy.RuntimeKind == model.RuntimeKubernetes {
			return Config{}, fmt.Errorf("kubernetes workloads require the Kubernetes runtime controller and cannot use the host Agent")
		}
		if policy.RuntimeKind == model.RuntimeDocker && policy.Engine != model.EngineMySQL && policy.Engine != model.EnginePostgreSQL {
			return Config{}, fmt.Errorf("Docker host Agent supports MySQL and PostgreSQL policies only")
		}
		if policy.Engine == model.EngineMySQL {
			if err := validateMySQLPolicy(&policy); err != nil {
				return Config{}, err
			}
		}
		if policy.Engine == model.EnginePostgreSQL {
			if err := validatePostgreSQLPolicy(&policy); err != nil {
				return Config{}, err
			}
			// Only literal hosts from the validated, operator-owned controller
			// configuration qualify; recovery does not expand DNS or CIDRs.
			for _, raw := range configuration.ControllerURLs {
				controller, _ := url.Parse(raw)
				if address := net.ParseIP(controller.Hostname()); address != nil {
					policy.RecoveryControllerAddresses = append(policy.RecoveryControllerAddresses, address.String())
				}
			}
		}
		if policy.Engine == model.EngineOracle {
			if err := validateOraclePolicy(&policy); err != nil {
				return Config{}, err
			}
		}
		configuration.Clusters[policy.ClusterID] = policy
	}
	if len(configuration.Clusters) == 0 {
		return Config{}, fmt.Errorf("at least one agent cluster policy is required")
	}
	return configuration, nil
}

func validateMySQLPolicy(policy *ClusterPolicy) error {
	if policy == nil {
		return fmt.Errorf("MySQL agent policy is required")
	}
	policy.MySQLService = strings.TrimSpace(policy.MySQLService)
	policy.MySQLServerBinary = strings.TrimSpace(policy.MySQLServerBinary)
	policy.MySQLServerDefaultsFile = strings.TrimSpace(policy.MySQLServerDefaultsFile)
	policy.MySQLDefaultsFile = strings.TrimSpace(policy.MySQLDefaultsFile)
	policy.DockerSwarmService = strings.TrimSpace(policy.DockerSwarmService)
	policy.DockerSwarmServiceID = strings.TrimSpace(policy.DockerSwarmServiceID)
	policy.DockerMySQLBinary = strings.TrimSpace(policy.DockerMySQLBinary)
	policy.DockerFenceFile = strings.TrimSpace(policy.DockerFenceFile)
	if policy.RuntimeKind == model.RuntimeDocker {
		if policy.MySQLPort < 1 || policy.MySQLPort > 65535 {
			return fmt.Errorf("Docker MySQL policy requires a valid port")
		}
		if !serviceNamePattern.MatchString(policy.DockerSwarmService) {
			return fmt.Errorf("Docker Swarm service allowlist entry is invalid")
		}
		if policy.DockerSwarmServiceID != "" && !serviceNamePattern.MatchString(policy.DockerSwarmServiceID) {
			return fmt.Errorf("Docker Swarm service identity is invalid")
		}
		if policy.DockerMySQLBinary == "" {
			policy.DockerMySQLBinary = "/usr/bin/mysql"
		}
		if !filepath.IsAbs(policy.DockerMySQLBinary) || filepath.Base(policy.DockerMySQLBinary) != "mysql" {
			return fmt.Errorf("Docker MySQL client binary must be an absolute mysql path inside the container")
		}
		if !filepath.IsAbs(policy.MySQLDefaultsFile) {
			return fmt.Errorf("Docker MySQL defaults file must be an absolute path inside the container")
		}
		if !filepath.IsAbs(policy.DockerFenceFile) {
			return fmt.Errorf("Docker restart fence file must be an absolute host path")
		}
		policy.DockerFenceFile = filepath.Clean(policy.DockerFenceFile)
		relativeFencePath, err := filepath.Rel(dockerFenceDirectory, policy.DockerFenceFile)
		if err != nil || relativeFencePath == "." || relativeFencePath == ".." || strings.HasPrefix(relativeFencePath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("Docker restart fence file must be below %s", dockerFenceDirectory)
		}
		if policy.MySQLService != "" || policy.MySQLServerBinary != "" || policy.MySQLServerDefaultsFile != "" {
			return fmt.Errorf("Docker MySQL policy must use docker_swarm_service and docker_fence_file instead of systemd restart fields")
		}
		return nil
	}
	configured := policy.MySQLService != "" || policy.MySQLServerBinary != "" || policy.MySQLServerDefaultsFile != ""
	if !configured {
		return nil
	}
	if policy.MySQLPort < 1 || policy.MySQLPort > 65535 {
		return fmt.Errorf("MySQL durable restart fence requires a valid port")
	}
	if !serviceNamePattern.MatchString(policy.MySQLService) {
		return fmt.Errorf("MySQL durable restart fence service is invalid")
	}
	if !filepath.IsAbs(policy.MySQLServerBinary) || filepath.Base(policy.MySQLServerBinary) != "mysqld" {
		return fmt.Errorf("MySQL durable restart fence server binary must be an absolute mysqld path")
	}
	if !filepath.IsAbs(policy.MySQLServerDefaultsFile) {
		return fmt.Errorf("MySQL durable restart fence defaults file must be absolute")
	}
	return nil
}

func validateOraclePolicy(policy *ClusterPolicy) error {
	if policy == nil {
		return fmt.Errorf("Oracle agent policy is required")
	}
	policy.OracleHome = strings.TrimSpace(policy.OracleHome)
	policy.OracleSID = strings.TrimSpace(policy.OracleSID)
	policy.OracleOSUser = strings.TrimSpace(policy.OracleOSUser)
	policy.OracleDGMGRLBinary = strings.TrimSpace(policy.OracleDGMGRLBinary)
	policy.OracleUsername = strings.TrimSpace(policy.OracleUsername)
	policy.OracleAuthenticationRole = strings.ToLower(strings.TrimSpace(policy.OracleAuthenticationRole))
	policy.OraclePasswordEnv = strings.TrimSpace(policy.OraclePasswordEnv)
	policy.OracleConnectIdentifier = strings.TrimSpace(policy.OracleConnectIdentifier)
	policy.OracleDatabaseUniqueName = strings.TrimSpace(policy.OracleDatabaseUniqueName)
	policy.OracleBrokerConfiguration = strings.TrimSpace(policy.OracleBrokerConfiguration)
	if !filepath.IsAbs(policy.OracleHome) || !filepath.IsAbs(policy.OracleDGMGRLBinary) ||
		filepath.Clean(policy.OracleDGMGRLBinary) != filepath.Join(filepath.Clean(policy.OracleHome), "bin", "dgmgrl") {
		return fmt.Errorf("Oracle home and DGMGRL binary must use the expected absolute path")
	}
	if !osUserPattern.MatchString(policy.OracleOSUser) || !oracleNamePattern.MatchString(policy.OracleUsername) {
		return fmt.Errorf("Oracle operating-system user or administrative user is invalid")
	}
	if policy.OracleAuthenticationRole == "" {
		policy.OracleAuthenticationRole = "sysdg"
	}
	if policy.OracleAuthenticationRole != "sysdg" && policy.OracleAuthenticationRole != "sysdba" {
		return fmt.Errorf("Oracle authentication role must be sysdg or sysdba")
	}
	for name, value := range map[string]string{
		"SID": policy.OracleSID, "connect identifier": policy.OracleConnectIdentifier,
		"database unique name": policy.OracleDatabaseUniqueName, "broker configuration": policy.OracleBrokerConfiguration,
	} {
		if !oracleNamePattern.MatchString(value) {
			return fmt.Errorf("Oracle %s is invalid", name)
		}
	}
	if !environmentNamePattern.MatchString(policy.OraclePasswordEnv) {
		return fmt.Errorf("Oracle password environment variable is invalid")
	}
	policy.OraclePassword = os.Getenv(policy.OraclePasswordEnv)
	if strings.TrimSpace(policy.OraclePassword) == "" || strings.ContainsAny(policy.OraclePassword, "\r\n\x00") {
		return fmt.Errorf("Oracle password environment variable %s is empty or invalid", policy.OraclePasswordEnv)
	}
	if len(policy.OracleMembers) < 2 {
		return fmt.Errorf("Oracle broker member allowlist must contain at least two databases")
	}
	seen := make(map[string]struct{}, len(policy.OracleMembers))
	localFound := false
	for index := range policy.OracleMembers {
		member := strings.TrimSpace(policy.OracleMembers[index])
		if !oracleNamePattern.MatchString(member) {
			return fmt.Errorf("Oracle broker member allowlist entry is invalid")
		}
		key := strings.ToLower(member)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate Oracle broker member")
		}
		seen[key] = struct{}{}
		policy.OracleMembers[index] = member
		if strings.EqualFold(member, policy.OracleDatabaseUniqueName) {
			localFound = true
		}
	}
	if !localFound {
		return fmt.Errorf("Oracle local database is outside the broker member allowlist")
	}
	return nil
}

func validatePostgreSQLPolicy(policy *ClusterPolicy) error {
	if policy == nil {
		return fmt.Errorf("PostgreSQL agent policy is required")
	}
	policy.PostgreSQLService = strings.TrimSpace(policy.PostgreSQLService)
	policy.PostgreSQLHostname = strings.TrimSpace(policy.PostgreSQLHostname)
	if policy.PostgreSQLHostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("resolve PostgreSQL local hostname: %w", err)
		}
		policy.PostgreSQLHostname = strings.TrimSpace(hostname)
	}
	policy.PostgreSQLUser = strings.TrimSpace(policy.PostgreSQLUser)
	policy.PostgreSQLDataDirectory = strings.TrimSpace(policy.PostgreSQLDataDirectory)
	policy.DockerPostgreSQLDataDirectory = strings.TrimSpace(policy.DockerPostgreSQLDataDirectory)
	policy.PostgreSQLBinaryDirectory = strings.TrimSpace(policy.PostgreSQLBinaryDirectory)
	policy.PostgreSQLPassfile = strings.TrimSpace(policy.PostgreSQLPassfile)
	policy.PostgreSQLDatabase = strings.TrimSpace(policy.PostgreSQLDatabase)
	policy.PostgreSQLReplicationUser = strings.TrimSpace(policy.PostgreSQLReplicationUser)
	if !model.ValidResourceID(policy.PostgreSQLNodeID) {
		return fmt.Errorf("PostgreSQL native node UUID is required")
	}
	if !hostnamePattern.MatchString(policy.PostgreSQLHostname) {
		return fmt.Errorf("PostgreSQL local hostname is invalid")
	}
	if policy.PostgreSQLPort < 1 || policy.PostgreSQLPort > 65535 {
		return fmt.Errorf("PostgreSQL agent port is invalid")
	}
	if !osUserPattern.MatchString(policy.PostgreSQLUser) {
		return fmt.Errorf("PostgreSQL operating-system user is invalid")
	}
	if !postgresSQLNamePattern.MatchString(policy.PostgreSQLDatabase) || !postgresSQLNamePattern.MatchString(policy.PostgreSQLReplicationUser) {
		return fmt.Errorf("PostgreSQL database or replication user is invalid")
	}
	for name, path := range map[string]string{
		"data directory": policy.PostgreSQLDataDirectory, "binary directory": policy.PostgreSQLBinaryDirectory, "passfile": policy.PostgreSQLPassfile,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("PostgreSQL %s must be an absolute path", name)
		}
	}
	policy.DockerSwarmService = strings.TrimSpace(policy.DockerSwarmService)
	policy.DockerSwarmServiceID = strings.TrimSpace(policy.DockerSwarmServiceID)
	if policy.RuntimeKind == model.RuntimeDocker {
		if policy.DockerPostgreSQLPort < 1 || policy.DockerPostgreSQLPort > 65535 {
			return fmt.Errorf("Docker PostgreSQL container port is invalid")
		}
		if !serviceNamePattern.MatchString(policy.DockerSwarmService) {
			return fmt.Errorf("Docker Swarm service allowlist entry is invalid")
		}
		if policy.DockerSwarmServiceID != "" && !serviceNamePattern.MatchString(policy.DockerSwarmServiceID) {
			return fmt.Errorf("Docker Swarm service identity is invalid")
		}
		if policy.PostgreSQLService != "" {
			return fmt.Errorf("Docker PostgreSQL policy must use docker_swarm_service instead of a systemd service")
		}
		if !filepath.IsAbs(policy.DockerPostgreSQLDataDirectory) || filepath.Clean(policy.DockerPostgreSQLDataDirectory) == string(filepath.Separator) {
			return fmt.Errorf("Docker PostgreSQL data directory inside the container must be a safe absolute path")
		}
	} else {
		if !serviceNamePattern.MatchString(policy.PostgreSQLService) {
			return fmt.Errorf("PostgreSQL service is invalid")
		}
		if policy.DockerPostgreSQLPort != 0 || policy.DockerPostgreSQLDataDirectory != "" || policy.DockerSwarmService != "" || policy.DockerSwarmServiceID != "" {
			return fmt.Errorf("Linux PostgreSQL policy cannot contain Docker runtime fields")
		}
	}
	seen := make(map[model.ResourceID]struct{}, len(policy.PostgreSQLPeers))
	seenNodeIDs := make(map[model.ResourceID]struct{}, len(policy.PostgreSQLPeers))
	for index := range policy.PostgreSQLPeers {
		peer := &policy.PostgreSQLPeers[index]
		peer.Hostname = strings.TrimSpace(peer.Hostname)
		peer.IPAddress = strings.TrimSpace(peer.IPAddress)
		if !model.ValidResourceID(peer.InstanceID) || peer.InstanceID == policy.InstanceID || !model.ValidResourceID(peer.NodeID) || peer.NodeID == policy.PostgreSQLNodeID || peer.Port < 1 || peer.Port > 65535 ||
			(peer.Hostname == "" && peer.IPAddress == "") {
			return fmt.Errorf("PostgreSQL peer allowlist entry is invalid")
		}
		if peer.Hostname != "" && !hostnamePattern.MatchString(peer.Hostname) {
			return fmt.Errorf("PostgreSQL peer hostname is invalid")
		}
		if peer.IPAddress != "" && net.ParseIP(peer.IPAddress) == nil {
			return fmt.Errorf("PostgreSQL peer IP address is invalid")
		}
		if _, duplicate := seen[peer.InstanceID]; duplicate {
			return fmt.Errorf("duplicate PostgreSQL peer identity")
		}
		if _, duplicate := seenNodeIDs[peer.NodeID]; duplicate {
			return fmt.Errorf("duplicate PostgreSQL native peer identity")
		}
		seen[peer.InstanceID] = struct{}{}
		seenNodeIDs[peer.NodeID] = struct{}{}
	}
	return nil
}
