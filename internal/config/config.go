// Package config loads the standalone ClusterGuard HA runtime configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type Credential struct {
	Username    string `json:"username"`
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

type Agent struct {
	Enabled         bool   `json:"enabled"`
	User            string `json:"user"`
	IdentityFile    string `json:"identity_file"`
	KnownHostsFile  string `json:"known_hosts_file"`
	SSHBinary       string `json:"ssh_binary,omitempty"`
	AgentBinary     string `json:"agent_binary,omitempty"`
	AgentConfigPath string `json:"agent_config_path,omitempty"`
	SharedSecretEnv string `json:"shared_secret_env"`
	SharedSecret    string `json:"-"`
}

type ConsensusPeer struct {
	ResourceID model.ResourceID `json:"resource_id"`
	Address    string           `json:"address"`
}

type Consensus struct {
	Enabled             bool             `json:"enabled"`
	LocalID             model.ResourceID `json:"local_id"`
	BindAddress         string           `json:"bind_address"`
	AdvertiseAddress    string           `json:"advertise_address"`
	DataDirectory       string           `json:"data_directory"`
	Bootstrap           bool             `json:"bootstrap"`
	ApplyTimeoutSeconds int              `json:"apply_timeout_seconds,omitempty"`
	Peers               []ConsensusPeer  `json:"peers"`
}

type NodeLifecycle struct {
	Enabled                bool            `json:"enabled"`
	ExecutorPath           string          `json:"executor_path"`
	PackageRepository      string          `json:"package_repository"`
	KnownHostsFile         string          `json:"known_hosts_file"`
	IdentityFile           string          `json:"identity_file,omitempty"`
	JQBinary               string          `json:"jq_binary"`
	ControlJoinHelper      string          `json:"control_join_helper,omitempty"`
	CloneHelper            string          `json:"clone_helper,omitempty"`
	XtraBackupHelper       string          `json:"xtrabackup_helper,omitempty"`
	SSHPasswordEnv         string          `json:"ssh_password_env,omitempty"`
	MySQLRootPasswordEnv   string          `json:"mysql_root_password_env"`
	ReplicationPasswordEnv string          `json:"replication_password_env"`
	SSHPassword            string          `json:"-"`
	MySQLRootPassword      string          `json:"-"`
	ReplicationPassword    string          `json:"-"`
	CloneAvailable         bool            `json:"clone_available"`
	XtraBackupVersions     map[string]bool `json:"xtrabackup_versions,omitempty"`
	LogicalDumpAllowed     bool            `json:"logical_dump_allowed"`
}

type File struct {
	HTTPAddress        string        `json:"http_address"`
	TLSCertFile        string        `json:"tls_cert_file,omitempty"`
	TLSKeyFile         string        `json:"tls_key_file,omitempty"`
	MetadataPath       string        `json:"metadata_path"`
	ControlTokenEnv    string        `json:"control_token_env"`
	ControlToken       string        `json:"-"`
	MonitoringTokenEnv string        `json:"monitoring_token_env"`
	MonitoringToken    string        `json:"-"`
	ApprovalTokenEnv   string        `json:"approval_token_env"`
	ApprovalToken      string        `json:"-"`
	MySQL              MySQL         `json:"mysql"`
	Agent              Agent         `json:"agent"`
	Consensus          Consensus     `json:"consensus"`
	NodeLifecycle      NodeLifecycle `json:"node_lifecycle"`
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
	configuration.TLSCertFile = strings.TrimSpace(configuration.TLSCertFile)
	configuration.TLSKeyFile = strings.TrimSpace(configuration.TLSKeyFile)
	if (configuration.TLSCertFile == "") != (configuration.TLSKeyFile == "") {
		return File{}, fmt.Errorf("tls_cert_file and tls_key_file must be configured together")
	}
	if configuration.TLSCertFile != "" && (!filepath.IsAbs(configuration.TLSCertFile) || !filepath.IsAbs(configuration.TLSKeyFile)) {
		return File{}, fmt.Errorf("TLS certificate and key paths must be absolute")
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
		configuration.ApprovalToken = os.Getenv(environment)
		if configuration.ApprovalToken == "" {
			return File{}, fmt.Errorf("approval token environment variable %s is empty", environment)
		}
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
			if err := resolveCredential(name, credential); err != nil {
				return File{}, err
			}
		}
	}
	if configuration.Agent.Enabled {
		configuration.Agent.User = strings.TrimSpace(configuration.Agent.User)
		configuration.Agent.IdentityFile = strings.TrimSpace(configuration.Agent.IdentityFile)
		configuration.Agent.KnownHostsFile = strings.TrimSpace(configuration.Agent.KnownHostsFile)
		configuration.Agent.SharedSecretEnv = strings.TrimSpace(configuration.Agent.SharedSecretEnv)
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
	}
	if configuration.NodeLifecycle.Enabled {
		if !configuration.Consensus.Enabled {
			return File{}, fmt.Errorf("node lifecycle execution requires Raft consensus")
		}
		if err := resolveNodeLifecycle(&configuration.NodeLifecycle); err != nil {
			return File{}, err
		}
	}
	if configuration.MySQL.AutomaticFailoverEnabled {
		if !configuration.Consensus.Enabled || !configuration.Agent.Enabled || strings.TrimSpace(configuration.ApprovalToken) == "" {
			return File{}, fmt.Errorf("automatic failover requires controller consensus, the restricted node agent, and an approval token")
		}
	}
	return configuration, nil
}

func validateConsensus(configuration *Consensus) error {
	configuration.BindAddress = strings.TrimSpace(configuration.BindAddress)
	configuration.AdvertiseAddress = strings.TrimSpace(configuration.AdvertiseAddress)
	configuration.DataDirectory = strings.TrimSpace(configuration.DataDirectory)
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
		if !model.ValidResourceID(peer.ResourceID) {
			return fmt.Errorf("consensus peer resource_id is invalid")
		}
		if _, _, err := net.SplitHostPort(peer.Address); err != nil {
			return fmt.Errorf("consensus peer address is invalid")
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
	if configuration.ApplyTimeoutSeconds <= 0 {
		configuration.ApplyTimeoutSeconds = 10
	}
	return nil
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
	for _, value := range []*string{&configuration.IdentityFile, &configuration.ControlJoinHelper, &configuration.CloneHelper, &configuration.XtraBackupHelper} {
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
	if configuration.XtraBackupVersions == nil {
		configuration.XtraBackupVersions = map[string]bool{}
	}
	return nil
}

func resolveCredential(name string, credential *Credential) error {
	credential.Username = strings.TrimSpace(credential.Username)
	if credential.Username == "" {
		return fmt.Errorf("MySQL %s username is required when MySQL is enabled", name)
	}
	credential.PasswordEnv = strings.TrimSpace(credential.PasswordEnv)
	if credential.PasswordEnv == "" {
		return fmt.Errorf("MySQL %s password_env is required when MySQL is enabled", name)
	}
	credential.Password = os.Getenv(credential.PasswordEnv)
	if strings.TrimSpace(credential.Password) == "" {
		return fmt.Errorf("MySQL %s password environment variable %s is empty", name, credential.PasswordEnv)
	}
	return nil
}
