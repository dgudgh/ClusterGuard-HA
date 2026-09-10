package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"clusterguard.io/ha/pkg/model"
)

const reconcileSandboxDirectory = "/etc/systemd/system/clusterguard-agent-reconcile.service.d"

func recoveryPrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("recovery receipt directory must be a private regular directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(owner.Uid) != uint64(os.Geteuid()) {
		return fmt.Errorf("recovery receipt directory must be owned by the agent")
	}
	return nil
}

func sandboxPostgreSQLPaths(data []byte) ([]string, error) {
	var configuration struct {
		Clusters []ClusterPolicy `json:"clusters"`
	}
	if err := json.Unmarshal(data, &configuration); err != nil {
		return nil, fmt.Errorf("invalid agent sandbox configuration")
	}
	paths := map[string]bool{}
	for _, p := range configuration.Clusters {
		if p.Engine != model.EnginePostgreSQL {
			continue
		}
		if !model.ValidResourceID(p.ClusterID) || !model.ValidResourceID(p.InstanceID) {
			return nil, fmt.Errorf("PostgreSQL sandbox requires fixed cluster and instance identities")
		}
		dir, err := safePostgreSQLDataDirectory(p.PostgreSQLDataDirectory)
		if err != nil {
			return nil, err
		}
		if dir != p.PostgreSQLDataDirectory || strings.ContainsAny(dir, "\r\n\t%$\\\" ") || len(strings.Split(strings.Trim(dir, "/"), "/")) < 3 {
			return nil, fmt.Errorf("PostgreSQL sandbox requires a narrow literal data directory")
		}
		// systemd path specifiers and symlinked ancestors must not widen the
		// writable mount beyond the root-owned agent configuration.
		for path := dir; path != "/"; path = filepath.Dir(path) {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("PostgreSQL sandbox path contains a non-directory or symlink")
			}
		}
		paths[dir] = true
	}
	result := make([]string, 0, len(paths))
	for dir := range paths {
		result = append(result, dir)
	}
	sort.Strings(result)
	return result, nil
}

func configureReconcileSandbox(configPath, outputDirectory string) error {
	data, err := recoveryRegularFile(configPath)
	if err != nil {
		return err
	}
	paths, err := sandboxPostgreSQLPaths(data)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Generated from the local agent PostgreSQL policies.\n[Service]\n")
	if len(paths) > 0 {
		b.WriteString("CapabilityBoundingSet=CAP_DAC_OVERRIDE\nAmbientCapabilities=CAP_DAC_OVERRIDE\n")
	}
	for _, dir := range paths {
		stateDirectory := dir + ".clusterguard-recovery"
		if _, err := os.Stat(filepath.Dir(dir)); err == nil {
			if err = recoveryPrivateDirectory(stateDirectory); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(&b, "ReadWritePaths=-%s -%s\n", dir, stateDirectory)
	}
	if err = os.MkdirAll(outputDirectory, 0755); err != nil {
		return err
	}
	return recoveryAtomicFile(filepath.Join(outputDirectory, "20-postgresql-recovery.conf"), []byte(b.String()), 0644)
}

// ConfigureReconcileSandbox does not load credentials or change database state.
// Installers run it before daemon-reload, including RPM upgrades of frozen tasks.
func ConfigureReconcileSandbox(configPath string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("configuring the reconcile sandbox requires root")
	}
	return configureReconcileSandbox(configPath, reconcileSandboxDirectory)
}
