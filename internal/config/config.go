// Package config loads the standalone ClusterGuard HA runtime configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type Credential struct {
	Username    string `json:"username"`
	Database    string `json:"database,omitempty"`
	PasswordEnv string `json:"password_env"`
	Password    string `json:"-"`
}

type MySQL struct {
	Enabled                          bool       `json:"enabled"`
	DiscoveryIntervalSeconds         int        `json:"discovery_interval_seconds,omitempty"`
	DiscoveryTimeoutSeconds          int        `json:"discovery_timeout_seconds,omitempty"`
	AutomaticFailoverEnabled         bool       `json:"automatic_failover_enabled,omitempty"`
	AutomaticFailoverIntervalSeconds int        `json:"automatic_failover_interval_seconds,omitempty"`
	AutomaticFailoverRetrySeconds    int        `json:"automatic_failover_retry_seconds,omitempty"`
	Discovery                        Credential `json:"discovery"`
	Operation                        Credential `json:"operation"`
	Replication                      Credential `json:"replication"`
}

type PostgreSQL struct {
	Enabled                          bool       `json:"enabled"`
	DiscoveryIntervalSeconds         int        `json:"discovery_interval_seconds,omitempty"`
	DiscoveryTimeoutSeconds          int        `json:"discovery_timeout_seconds,omitempty"`
	AutomaticFailoverEnabled         bool       `json:"automatic_failover_enabled,omitempty"`
	AutomaticFailoverIntervalSeconds int        `json:"automatic_failover_interval_seconds,omitempty"`
	AutomaticFailoverRetrySeconds    int        `json:"automatic_failover_retry_seconds,omitempty"`
	Discovery                        Credential `json:"discovery"`
	Operation                        Credential `json:"operation,omitempty"`
	Replication                      Credential `json:"replication,omitempty"`
}

type Oracle struct {
	Enabled                  bool       `json:"enabled"`
	DiscoveryIntervalSeconds int        `json:"discovery_interval_seconds,omitempty"`
	DiscoveryTimeoutSeconds  int        `json:"discovery_timeout_seconds,omitempty"`
	Discovery                Credential `json:"discovery"`
	Operation                Credential `json:"operation,omitempty"`
}

type SQLServer struct {
	Enabled                  bool       `json:"enabled"`
	DiscoveryIntervalSeconds int        `json:"discovery_interval_seconds,omitempty"`
	DiscoveryTimeoutSeconds  int        `json:"discovery_timeout_seconds,omitempty"`
	Discovery                Credential `json:"discovery"`
	Operation                Credential `json:"operation,omitempty"`
}

type Agent struct {
	Enabled                bool   `json:"enabled"`
	User                   string `json:"user"`
	IdentityFile           string `json:"identity_file"`
	KnownHostsFile         string `json:"known_hosts_file"`
	SSHBinary              string `json:"ssh_binary,omitempty"`
	AgentBinary            string `json:"agent_binary,omitempty"`
	AgentConfigPath        string `json:"agent_config_path,omitempty"`
	CommandTimeoutSeconds  int    `json:"command_timeout_seconds,omitempty"`
	MutationTimeoutSeconds int    `json:"mutation_timeout_seconds,omitempty"`
	SharedSecretEnv        string `json:"shared_secret_env"`
	SharedSecret           string `json:"-"`
}

type ConsensusPeer struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
	APIAddress string           `json:"api_address,omitempty"`
}

type Consensus struct {
	Enabled                bool             `json:"enabled"`
	SnapshotCASEnabled     bool             `json:"snapshot_cas_enabled"`
	AllowInsecureTransport bool             `json:"allow_insecure_transport,omitempty"`
	TLSCertFile            string           `json:"tls_cert_file,omitempty"`
	TLSKeyFile             string           `json:"tls_key_file,omitempty"`
	TLSCAFile              string           `json:"tls_ca_file,omitempty"`
	LocalID                model.ResourceID `json:"local_id"`
	BindAddress            string           `json:"bind_address"`
	AdvertiseAddress       string           `json:"advertise_address"`
	DataDirectory          string           `json:"data_directory"`
	Bootstrap              bool             `json:"bootstrap"`
	ApplyTimeoutSeconds    int              `json:"apply_timeout_seconds,omitempty"`
	Peers                  []ConsensusPeer  `json:"peers"`
}

type NodeLifecycle struct {
	Enabled                          bool            `json:"enabled"`
	ExecutorPath                     string          `json:"executor_path"`
	PackageRepository                string          `json:"package_repository"`
	KnownHostsFile                   string          `json:"known_hosts_file"`
	IdentityFile                     string          `json:"identity_file,omitempty"`
	JQBinary                         string          `json:"jq_binary"`
	ControlJoinHelper                string          `json:"control_join_helper,omitempty"`
	CloneHelper                      string          `json:"clone_helper,omitempty"`
	XtraBackupHelper                 string          `json:"xtrabackup_helper,omitempty"`
	PostgreSQLInstallHelper          string          `json:"postgresql_install_helper,omitempty"`
	PostgreSQLSyncHelper             string          `json:"postgresql_sync_helper,omitempty"`
	SSHPasswordEnv                   string          `json:"ssh_password_env,omitempty"`
	MySQLRootPasswordEnv             string          `json:"mysql_root_password_env"`
	ReplicationPasswordEnv           string          `json:"replication_password_env"`
	PostgreSQLAdminPasswordEnv       string          `json:"postgresql_admin_password_env,omitempty"`
	PostgreSQLReplicationPasswordEnv string          `json:"postgresql_replication_password_env,omitempty"`
	SSHPassword                      string          `json:"-"`
	MySQLRootPassword                string          `json:"-"`
	ReplicationPassword              string          `json:"-"`
	PostgreSQLAdminPassword          string          `json:"-"`
	PostgreSQLReplicationPassword    string          `json:"-"`
	CloneAvailable                   bool            `json:"clone_available"`
	XtraBackupVersions               map[string]bool `json:"xtrabackup_versions,omitempty"`
	LogicalDumpAllowed               bool            `json:"logical_dump_allowed"`
	PostgreSQLBaseBackupAvailable    bool            `json:"postgresql_basebackup_available"`
	PostgreSQLRewindAvailable        bool            `json:"postgresql_rewind_available"`
}

type Fencing struct {
	Enabled        bool   `json:"enabled"`
	ExecutablePath string `json:"executable_path"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type File struct {
	HTTPAddress         string        `json:"http_address"`
	AllowInsecureHTTP   bool          `json:"allow_insecure_http,omitempty"`
	TLSCertFile         string        `json:"tls_cert_file,omitempty"`
	TLSKeyFile          string        `json:"tls_key_file,omitempty"`
	TLSCAFile           string        `json:"tls_ca_file,omitempty"`
	MetadataPath        string        `json:"metadata_path"`
	ControlTokenEnv     string        `json:"control_token_env"`
	ControlToken        string        `json:"-"`
	MonitoringTokenEnv  string        `json:"monitoring_token_env"`
	MonitoringToken     string        `json:"-"`
	ApprovalTokenEnv    string        `json:"approval_token_env"`
	ApprovalToken       string        `json:"-"`
	DeprecationWarnings []string      `json:"-"`
	MySQL               MySQL         `json:"mysql"`
	PostgreSQL          PostgreSQL    `json:"postgresql"`
	Oracle              Oracle        `json:"oracle"`
	SQLServer           SQLServer     `json:"sqlserver"`
	Agent               Agent         `json:"agent"`
	Consensus           Consensus     `json:"consensus"`
	Fencing             Fencing       `json:"fencing"`
	NodeLifecycle       NodeLifecycle `json:"node_lifecycle"`
}

func Load(path string) (File, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("read configuration: %w", err)
	}
	configuration := File{}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return File{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return File{}, fmt.Errorf("configuration contains multiple JSON values")
		}
		return File{}, fmt.Errorf("decode configuration: %w", err)
	}
	if strings.TrimSpace(configuration.HTTPAddress) == "" {
		configuration.HTTPAddress = "127.0.0.1:8088"
	}
	configuration.HTTPAddress = strings.TrimSpace(configuration.HTTPAddress)
	if _, _, err := net.SplitHostPort(configuration.HTTPAddress); err != nil {
		return File{}, fmt.Errorf("http_address is invalid")
	}
	configuration.TLSCertFile = strings.TrimSpace(configuration.TLSCertFile)
	configuration.TLSKeyFile = strings.TrimSpace(configuration.TLSKeyFile)
	configuration.TLSCAFile = strings.TrimSpace(configuration.TLSCAFile)
	if (configuration.TLSCertFile == "") != (configuration.TLSKeyFile == "") {
		return File{}, fmt.Errorf("tls_cert_file and tls_key_file must be configured together")
	}
	if configuration.TLSCertFile != "" && (!filepath.IsAbs(configuration.TLSCertFile) || !filepath.IsAbs(configuration.TLSKeyFile)) {
		return File{}, fmt.Errorf("TLS certificate and key paths must be absolute")
	}
	if configuration.TLSCAFile != "" && !filepath.IsAbs(configuration.TLSCAFile) {
		return File{}, fmt.Errorf("TLS CA path must be absolute")
	}
	if configuration.TLSCertFile == "" && !httpAddressIsLoopback(configuration.HTTPAddress) {
		if !configuration.AllowInsecureHTTP {
			return File{}, fmt.Errorf("plaintext HTTP on a non-loopback address is disabled; configure TLS or explicitly set allow_insecure_http")
		}
		configuration.DeprecationWarnings = append(configuration.DeprecationWarnings,
			"plaintext HTTP is enabled on a non-loopback address; credentials and sessions are exposed in transit")
	}
	if strings.TrimSpace(configuration.MetadataPath) == "" {
		return File{}, fmt.Errorf("metadata_path is required")
	}
	if environment := strings.TrimSpace(configuration.ControlTokenEnv); environment != "" {
		configuration.ControlTokenEnv = environment
		configuration.ControlToken = os.Getenv(environment)
		if strings.TrimSpace(configuration.ControlToken) == "" {
			return File{}, fmt.Errorf("control token environment variable %s is empty", environment)
		}
	}
	if environment := strings.TrimSpace(configuration.MonitoringTokenEnv); environment != "" {
		configuration.MonitoringTokenEnv = environment
		configuration.MonitoringToken = os.Getenv(environment)
		if strings.TrimSpace(configuration.MonitoringToken) == "" {
			return File{}, fmt.Errorf("monitoring token environment variable %s is empty", environment)
		}
	}
	if environment := strings.TrimSpace(configuration.ApprovalTokenEnv); environment != "" {
		configuration.ApprovalTokenEnv = environment
		configuration.ApprovalToken = os.Getenv(environment)
		configuration.DeprecationWarnings = append(configuration.DeprecationWarnings,
			"CG_APPROVAL_TOKEN is deprecated for database operations and automatic recovery; use one-time approval grants")
	}
	if configuration.MySQL.AutomaticFailoverEnabled && !configuration.MySQL.Enabled {
		return File{}, fmt.Errorf("MySQL must be enabled before automatic failover can be enabled")
	}
	if configuration.PostgreSQL.AutomaticFailoverEnabled && !configuration.PostgreSQL.Enabled {
		return File{}, fmt.Errorf("PostgreSQL must be enabled before automatic failover can be enabled")
	}
	if configuration.MySQL.Enabled {
		if configuration.MySQL.DiscoveryIntervalSeconds <= 0 {
			configuration.MySQL.DiscoveryIntervalSeconds = 5
		}
		if configuration.MySQL.DiscoveryTimeoutSeconds <= 0 {
			configuration.MySQL.DiscoveryTimeoutSeconds = 4
		}
		if configuration.MySQL.AutomaticFailoverIntervalSeconds <= 0 {
			configuration.MySQL.AutomaticFailoverIntervalSeconds = 5
		}
		if configuration.MySQL.AutomaticFailoverRetrySeconds <= 0 {
			configuration.MySQL.AutomaticFailoverRetrySeconds = 30
		}
		for name, credential := range map[string]*Credential{
			"discovery":   &configuration.MySQL.Discovery,
			"operation":   &configuration.MySQL.Operation,
			"replication": &configuration.MySQL.Replication,
		} {
			if err := resolveCredential("MySQL", name, credential); err != nil {
				return File{}, err
			}
		}
	}
	if configuration.PostgreSQL.Enabled {
		if configuration.PostgreSQL.DiscoveryIntervalSeconds <= 0 {
			configuration.PostgreSQL.DiscoveryIntervalSeconds = 5
		}
		if configuration.PostgreSQL.DiscoveryTimeoutSeconds <= 0 {
			configuration.PostgreSQL.DiscoveryTimeoutSeconds = 4
		}
		if configuration.PostgreSQL.AutomaticFailoverIntervalSeconds <= 0 {
			configuration.PostgreSQL.AutomaticFailoverIntervalSeconds = 5
		}
		if configuration.PostgreSQL.AutomaticFailoverRetrySeconds <= 0 {
			configuration.PostgreSQL.AutomaticFailoverRetrySeconds = 30
		}
		if err := resolveCredential("PostgreSQL", "discovery", &configuration.PostgreSQL.Discovery); err != nil {
			return File{}, err
		}
		if configuration.PostgreSQL.Discovery.Database == "" {
			configuration.PostgreSQL.Discovery.Database = "postgres"
		}
		operationConfigured := credentialConfigured(configuration.PostgreSQL.Operation)
		replicationConfigured := credentialConfigured(configuration.PostgreSQL.Replication)
		if operationConfigured != replicationConfigured {
			return File{}, fmt.Errorf("PostgreSQL operation and replication credentials must be configured together")
		}
		if configuration.PostgreSQL.AutomaticFailoverEnabled && !operationConfigured {
			return File{}, fmt.Errorf("PostgreSQL automatic failover requires operation and replication credentials")
		}
		if operationConfigured {
			if err := resolveCredential("PostgreSQL", "operation", &configuration.PostgreSQL.Operation); err != nil {
				return File{}, err
			}
			if err := resolveCredential("PostgreSQL", "replication", &configuration.PostgreSQL.Replication); err != nil {
				return File{}, err
			}
			if configuration.PostgreSQL.Operation.Database == "" {
				configuration.PostgreSQL.Operation.Database = configuration.PostgreSQL.Discovery.Database
			}
			if configuration.PostgreSQL.Replication.Database == "" {
				configuration.PostgreSQL.Replication.Database = configuration.PostgreSQL.Discovery.Database
			}
		}
	}
	if configuration.Oracle.Enabled {
		if configuration.Oracle.DiscoveryIntervalSeconds <= 0 {
			configuration.Oracle.DiscoveryIntervalSeconds = 15
		}
		if configuration.Oracle.DiscoveryTimeoutSeconds <= 0 {
			configuration.Oracle.DiscoveryTimeoutSeconds = 10
		}
		if err := resolveCredential("Oracle", "discovery", &configuration.Oracle.Discovery); err != nil {
			return File{}, err
		}
		if credentialConfigured(configuration.Oracle.Operation) {
			if err := resolveCredential("Oracle", "operation", &configuration.Oracle.Operation); err != nil {
				return File{}, err
			}
			if configuration.Oracle.Operation.Database == "" {
				configuration.Oracle.Operation.Database = configuration.Oracle.Discovery.Database
			}
		}
	}
	if configuration.SQLServer.Enabled {
		if configuration.SQLServer.DiscoveryIntervalSeconds <= 0 {
			configuration.SQLServer.DiscoveryIntervalSeconds = 15
		}
		if configuration.SQLServer.DiscoveryTimeoutSeconds <= 0 {
			configuration.SQLServer.DiscoveryTimeoutSeconds = 10
		}
		if err := resolveCredential("SQL Server", "discovery", &configuration.SQLServer.Discovery); err != nil {
			return File{}, err
		}
		if configuration.SQLServer.Discovery.Database == "" {
			configuration.SQLServer.Discovery.Database = "master"
		}
		if credentialConfigured(configuration.SQLServer.Operation) {
			if err := resolveCredential("SQL Server", "operation", &configuration.SQLServer.Operation); err != nil {
				return File{}, err
			}
			if configuration.SQLServer.Operation.Database == "" {
				configuration.SQLServer.Operation.Database = configuration.SQLServer.Discovery.Database
			}
		}
	}
	if configuration.Agent.Enabled {
		configuration.Agent.User = strings.TrimSpace(configuration.Agent.User)
		configuration.Agent.IdentityFile = strings.TrimSpace(configuration.Agent.IdentityFile)
		configuration.Agent.KnownHostsFile = strings.TrimSpace(configuration.Agent.KnownHostsFile)
		configuration.Agent.SharedSecretEnv = strings.TrimSpace(configuration.Agent.SharedSecretEnv)
		if configuration.Agent.CommandTimeoutSeconds <= 0 {
			configuration.Agent.CommandTimeoutSeconds = 30
		}
		if configuration.Agent.MutationTimeoutSeconds <= 0 {
			configuration.Agent.MutationTimeoutSeconds = 1800
		}
		if configuration.Agent.User == "" || configuration.Agent.IdentityFile == "" || configuration.Agent.KnownHostsFile == "" || configuration.Agent.SharedSecretEnv == "" {
			return File{}, fmt.Errorf("agent user, identity_file, known_hosts_file, and shared_secret_env are required when agent transport is enabled")
		}
		configuration.Agent.SharedSecret = os.Getenv(configuration.Agent.SharedSecretEnv)
		if strings.TrimSpace(configuration.Agent.SharedSecret) == "" {
			return File{}, fmt.Errorf("agent shared secret environment variable %s is empty", configuration.Agent.SharedSecretEnv)
		}
	}
	if configuration.Consensus.Enabled {
		if err := validateConsensus(&configuration.Consensus); err != nil {
			return File{}, err
		}
		insecurePeerAPI, err := validateConsensusPeerAPIs(configuration)
		if err != nil {
			return File{}, err
		}
		if insecurePeerAPI {
			configuration.DeprecationWarnings = append(configuration.DeprecationWarnings,
				"plaintext controller peer API transport is enabled; credentials and sessions are exposed in transit")
		}
		if configuration.Consensus.AllowInsecureTransport && consensusUsesExternalNetwork(configuration.Consensus) && configuration.Consensus.TLSCertFile == "" {
			configuration.DeprecationWarnings = append(configuration.DeprecationWarnings,
				"plaintext Raft transport is enabled on a non-loopback network; controller metadata and credentials are exposed in transit")
		}
	}
	if configuration.NodeLifecycle.Enabled {
		if !configuration.Consensus.Enabled {
			return File{}, fmt.Errorf("node lifecycle execution requires Raft consensus")
		}
		if err := resolveNodeLifecycle(&configuration.NodeLifecycle); err != nil {
			return File{}, err
		}
	}
	if configuration.Fencing.Enabled {
		configuration.Fencing.ExecutablePath = strings.TrimSpace(configuration.Fencing.ExecutablePath)
		if !configuration.Consensus.Enabled {
			return File{}, fmt.Errorf("external fencing requires Raft consensus")
		}
		if configuration.Fencing.ExecutablePath == "" || !filepath.IsAbs(configuration.Fencing.ExecutablePath) {
			return File{}, fmt.Errorf("external fencing executable_path must be absolute")
		}
		if configuration.Fencing.TimeoutSeconds <= 0 {
			configuration.Fencing.TimeoutSeconds = 30
		}
	}
	if configuration.MySQL.AutomaticFailoverEnabled || configuration.PostgreSQL.AutomaticFailoverEnabled {
		if !configuration.Consensus.Enabled || !configuration.Agent.Enabled {
			return File{}, fmt.Errorf("automatic failover requires controller consensus and the restricted node agent")
		}
	}
	return configuration, nil
}

func validateConsensusPeerAPIs(configuration File) (bool, error) {
	insecure := false
	seen := make(map[string]model.ResourceID, len(configuration.Consensus.Peers))
	for _, peer := range configuration.Consensus.Peers {
		address := strings.TrimSpace(peer.APIAddress)
		if address == "" {
			if configuration.TLSCertFile != "" {
				continue
			}
			host, _, err := net.SplitHostPort(peer.Address)
			if err != nil {
				return false, fmt.Errorf("consensus peer API address cannot be derived")
			}
			if !networkHostIsLoopback(host) {
				if !configuration.AllowInsecureHTTP {
					return false, fmt.Errorf("consensus peer API requires HTTPS on a non-loopback network")
				}
				insecure = true
			}
			continue
		}
		parsed, _ := url.Parse(address)
		loopback := networkHostIsLoopback(parsed.Hostname())
		if parsed.Scheme == "http" && !loopback {
			if !configuration.AllowInsecureHTTP {
				return false, fmt.Errorf("consensus peer API requires HTTPS on a non-loopback network")
			}
			insecure = true
		}
		canonical := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
		if existing, found := seen[canonical]; found && existing != peer.ResourceID {
			return false, fmt.Errorf("consensus peer API address is duplicated")
		}
		seen[canonical] = peer.ResourceID
	}
	return insecure, nil
}

func networkHostIsLoopback(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func httpAddressIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	return networkHostIsLoopback(host)
}

func validateConsensus(configuration *Consensus) error {
	configuration.BindAddress = strings.TrimSpace(configuration.BindAddress)
	configuration.AdvertiseAddress = strings.TrimSpace(configuration.AdvertiseAddress)
	configuration.DataDirectory = strings.TrimSpace(configuration.DataDirectory)
	configuration.TLSCertFile = strings.TrimSpace(configuration.TLSCertFile)
	configuration.TLSKeyFile = strings.TrimSpace(configuration.TLSKeyFile)
	configuration.TLSCAFile = strings.TrimSpace(configuration.TLSCAFile)
	tlsPaths := []string{configuration.TLSCertFile, configuration.TLSKeyFile, configuration.TLSCAFile}
	configuredTLSPaths := 0
	for _, path := range tlsPaths {
		if path != "" {
			configuredTLSPaths++
			if !filepath.IsAbs(path) {
				return fmt.Errorf("Raft TLS certificate, key, and CA paths must be absolute")
			}
		}
	}
	if configuredTLSPaths != 0 && configuredTLSPaths != len(tlsPaths) {
		return fmt.Errorf("Raft TLS certificate, key, and CA must be configured together")
	}
	if !model.ValidResourceID(configuration.LocalID) || !filepath.IsAbs(configuration.DataDirectory) {
		return fmt.Errorf("consensus requires a valid local_id and absolute data_directory")
	}
	if _, _, err := net.SplitHostPort(configuration.BindAddress); err != nil {
		return fmt.Errorf("consensus bind_address is invalid")
	}
	if _, _, err := net.SplitHostPort(configuration.AdvertiseAddress); err != nil {
		return fmt.Errorf("consensus advertise_address is invalid")
	}
	if len(configuration.Peers) < 3 || len(configuration.Peers)%2 == 0 {
		return fmt.Errorf("consensus peers must be an odd set of at least three controllers")
	}
	ids := make(map[model.ResourceID]struct{}, len(configuration.Peers))
	addresses := make(map[string]struct{}, len(configuration.Peers))
	localFound := false
	for index := range configuration.Peers {
		peer := &configuration.Peers[index]
		peer.Address = strings.TrimSpace(peer.Address)
		peer.APIAddress = strings.TrimRight(strings.TrimSpace(peer.APIAddress), "/")
		if !model.ValidResourceID(peer.ResourceID) {
			return fmt.Errorf("consensus peer resource_id is invalid")
		}
		if _, _, err := net.SplitHostPort(peer.Address); err != nil {
			return fmt.Errorf("consensus peer address is invalid")
		}
		if peer.APIAddress != "" {
			parsed, err := url.Parse(peer.APIAddress)
			if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
				parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("consensus peer api_address is invalid")
			}
		}
		if _, duplicate := ids[peer.ResourceID]; duplicate {
			return fmt.Errorf("consensus peer resource_id is duplicated")
		}
		if _, duplicate := addresses[peer.Address]; duplicate {
			return fmt.Errorf("consensus peer address is duplicated")
		}
		ids[peer.ResourceID] = struct{}{}
		addresses[peer.Address] = struct{}{}
		if peer.ResourceID == configuration.LocalID {
			localFound = true
		}
	}
	if !localFound {
		return fmt.Errorf("consensus local controller is outside peers")
	}
	if configuredTLSPaths == 0 && consensusUsesExternalNetwork(*configuration) && !configuration.AllowInsecureTransport {
		return fmt.Errorf("Raft TLS is required for consensus transport on a non-loopback network")
	}
	if configuration.ApplyTimeoutSeconds <= 0 {
		configuration.ApplyTimeoutSeconds = 10
	}
	return nil
}

func consensusUsesExternalNetwork(configuration Consensus) bool {
	for _, address := range []string{configuration.BindAddress, configuration.AdvertiseAddress} {
		if !httpAddressIsLoopback(address) {
			return true
		}
	}
	for _, peer := range configuration.Peers {
		if !httpAddressIsLoopback(peer.Address) {
			return true
		}
	}
	return false
}

func resolveNodeLifecycle(configuration *NodeLifecycle) error {
	paths := map[string]*string{
		"executor_path":      &configuration.ExecutorPath,
		"package_repository": &configuration.PackageRepository,
		"known_hosts_file":   &configuration.KnownHostsFile,
		"jq_binary":          &configuration.JQBinary,
	}
	for name, value := range paths {
		*value = strings.TrimSpace(*value)
		if !filepath.IsAbs(*value) {
			return fmt.Errorf("node lifecycle %s must be an absolute path", name)
		}
	}
	for _, value := range []*string{
		&configuration.IdentityFile, &configuration.ControlJoinHelper, &configuration.CloneHelper, &configuration.XtraBackupHelper,
		&configuration.PostgreSQLInstallHelper, &configuration.PostgreSQLSyncHelper,
	} {
		*value = strings.TrimSpace(*value)
		if *value != "" && !filepath.IsAbs(*value) {
			return fmt.Errorf("node lifecycle helper paths must be absolute")
		}
	}
	configuration.SSHPasswordEnv = strings.TrimSpace(configuration.SSHPasswordEnv)
	if configuration.IdentityFile == "" && configuration.SSHPasswordEnv == "" {
		return fmt.Errorf("node lifecycle requires identity_file or ssh_password_env")
	}
	if configuration.SSHPasswordEnv != "" {
		configuration.SSHPassword = os.Getenv(configuration.SSHPasswordEnv)
		if configuration.SSHPassword == "" {
			return fmt.Errorf("node lifecycle SSH password environment variable %s is empty", configuration.SSHPasswordEnv)
		}
	}
	configuration.MySQLRootPasswordEnv = strings.TrimSpace(configuration.MySQLRootPasswordEnv)
	configuration.ReplicationPasswordEnv = strings.TrimSpace(configuration.ReplicationPasswordEnv)
	if configuration.MySQLRootPasswordEnv == "" || configuration.ReplicationPasswordEnv == "" {
		return fmt.Errorf("node lifecycle MySQL root and replication password environments are required")
	}
	configuration.MySQLRootPassword = os.Getenv(configuration.MySQLRootPasswordEnv)
	configuration.ReplicationPassword = os.Getenv(configuration.ReplicationPasswordEnv)
	if configuration.MySQLRootPassword == "" || configuration.ReplicationPassword == "" {
		return fmt.Errorf("node lifecycle MySQL credential environment is empty")
	}
	postgresqlConfigured := configuration.PostgreSQLBaseBackupAvailable || configuration.PostgreSQLRewindAvailable ||
		configuration.PostgreSQLInstallHelper != "" || configuration.PostgreSQLSyncHelper != "" ||
		strings.TrimSpace(configuration.PostgreSQLAdminPasswordEnv) != "" || strings.TrimSpace(configuration.PostgreSQLReplicationPasswordEnv) != ""
	if postgresqlConfigured {
		if configuration.PostgreSQLInstallHelper == "" || configuration.PostgreSQLSyncHelper == "" {
			return fmt.Errorf("node lifecycle PostgreSQL install and sync helper paths are required")
		}
		configuration.PostgreSQLAdminPasswordEnv = strings.TrimSpace(configuration.PostgreSQLAdminPasswordEnv)
		configuration.PostgreSQLReplicationPasswordEnv = strings.TrimSpace(configuration.PostgreSQLReplicationPasswordEnv)
		if configuration.PostgreSQLAdminPasswordEnv == "" || configuration.PostgreSQLReplicationPasswordEnv == "" {
			return fmt.Errorf("node lifecycle PostgreSQL administrator and replication password environments are required")
		}
		configuration.PostgreSQLAdminPassword = os.Getenv(configuration.PostgreSQLAdminPasswordEnv)
		configuration.PostgreSQLReplicationPassword = os.Getenv(configuration.PostgreSQLReplicationPasswordEnv)
		if strings.TrimSpace(configuration.PostgreSQLAdminPassword) == "" || strings.TrimSpace(configuration.PostgreSQLReplicationPassword) == "" {
			return fmt.Errorf("node lifecycle PostgreSQL credential environment is empty")
		}
	}
	if configuration.XtraBackupVersions == nil {
		configuration.XtraBackupVersions = map[string]bool{}
	}
	return nil
}

func resolveCredential(engine string, name string, credential *Credential) error {
	credential.Username = strings.TrimSpace(credential.Username)
	if credential.Username == "" {
		return fmt.Errorf("%s %s username is required when %s is enabled", engine, name, engine)
	}
	credential.Database = strings.TrimSpace(credential.Database)
	credential.PasswordEnv = strings.TrimSpace(credential.PasswordEnv)
	if credential.PasswordEnv == "" {
		return fmt.Errorf("%s %s password_env is required when %s is enabled", engine, name, engine)
	}
	credential.Password = os.Getenv(credential.PasswordEnv)
	if strings.TrimSpace(credential.Password) == "" {
		return fmt.Errorf("%s %s password environment variable %s is empty", engine, name, credential.PasswordEnv)
	}
	return nil
}

func credentialConfigured(credential Credential) bool {
	return strings.TrimSpace(credential.Username) != "" || strings.TrimSpace(credential.Database) != "" || strings.TrimSpace(credential.PasswordEnv) != ""
}
