// Package config loads the standalone ClusterGuard HA runtime configuration.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type MySQL struct {
	Enabled     bool   `json:"enabled"`
	Username    string `json:"username"`
	PasswordEnv string `json:"password_env"`
	Password    string `json:"-"`
}

type File struct {
	HTTPAddress      string `json:"http_address"`
	MetadataPath     string `json:"metadata_path"`
	ApprovalTokenEnv string `json:"approval_token_env"`
	ApprovalToken    string `json:"-"`
	MySQL            MySQL  `json:"mysql"`
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
	if environment := strings.TrimSpace(configuration.ApprovalTokenEnv); environment != "" {
		configuration.ApprovalToken = os.Getenv(environment)
		if configuration.ApprovalToken == "" {
			return File{}, fmt.Errorf("approval token environment variable %s is empty", environment)
		}
	}
	if configuration.MySQL.Enabled && strings.TrimSpace(configuration.MySQL.PasswordEnv) != "" {
		configuration.MySQL.Password = os.Getenv(configuration.MySQL.PasswordEnv)
		if configuration.MySQL.Password == "" {
			return File{}, fmt.Errorf("MySQL password environment variable %s is empty", configuration.MySQL.PasswordEnv)
		}
	}
	return configuration, nil
}
