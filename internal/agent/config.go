package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type ClusterPolicy struct {
	ClusterID         model.ResourceID `json:"cluster_id"`
	InstanceID        model.ResourceID `json:"instance_id"`
	VIP               string           `json:"vip"`
	Interface         string           `json:"interface"`
	Prefix            int              `json:"prefix"`
	MySQLPort         int              `json:"mysql_port"`
	MySQLDefaultsFile string           `json:"mysql_defaults_file"`
}

type Config struct {
	SharedSecret       string
	Clusters           map[model.ResourceID]ClusterPolicy
	IPBinary           string
	ARPingBinary       string
	MySQLBinary        string
	RoleStateDirectory string
}

type fileConfig struct {
	SharedSecretEnv    string          `json:"shared_secret_env"`
	IPBinary           string          `json:"ip_binary"`
	ARPingBinary       string          `json:"arping_binary"`
	MySQLBinary        string          `json:"mysql_binary"`
	RoleStateDirectory string          `json:"role_state_directory"`
	Clusters           []ClusterPolicy `json:"clusters"`
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
		IPBinary: strings.TrimSpace(file.IPBinary), ARPingBinary: strings.TrimSpace(file.ARPingBinary),
		MySQLBinary: strings.TrimSpace(file.MySQLBinary), RoleStateDirectory: strings.TrimSpace(file.RoleStateDirectory),
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
	if configuration.RoleStateDirectory == "" {
		configuration.RoleStateDirectory = "/var/lib/clusterguard-agent/roles"
	}
	for _, policy := range file.Clusters {
		if !model.ValidResourceID(policy.ClusterID) || !model.ValidResourceID(policy.InstanceID) {
			return Config{}, fmt.Errorf("agent cluster and instance UUIDs are required")
		}
		if _, exists := configuration.Clusters[policy.ClusterID]; exists {
			return Config{}, fmt.Errorf("duplicate agent cluster policy")
		}
		configuration.Clusters[policy.ClusterID] = policy
	}
	if len(configuration.Clusters) == 0 {
		return Config{}, fmt.Errorf("at least one agent cluster policy is required")
	}
	return configuration, nil
}
