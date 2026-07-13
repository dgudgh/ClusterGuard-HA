// Package config loads the standalone ClusterGuard HA runtime configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type Credential struct {
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
	Password    string `json:"-"`
}

type MySQL struct {
	Enabled     bool       `json:"enabled"`
	Discovery   Credential `json:"discovery"`
	Operation   Credential `json:"operation"`
	Replication Credential `json:"replication"`
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

type File struct {
	HTTPAddress      string `json:"http_address"`
	MetadataPath     string `json:"metadata_path"`
	ControlTokenEnv  string `json:"control_token_env"`
	ControlToken     string `json:"-"`
	ApprovalTokenEnv string `json:"approval_token_env"`
	ApprovalToken    string `json:"-"`
	MySQL            MySQL  `json:"mysql"`
	Agent            Agent  `json:"agent"`
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
	if environment := strings.TrimSpace(configuration.ApprovalTokenEnv); environment != "" {
		configuration.ApprovalToken = os.Getenv(environment)
		if configuration.ApprovalToken == "" {
			return File{}, fmt.Errorf("approval token environment variable %s is empty", environment)
		}
	}
	if configuration.MySQL.Enabled {
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
	return configuration, nil
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
