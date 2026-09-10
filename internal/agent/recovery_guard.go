package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type RecoveryPrimaryGuard interface {
	RecoveryGuardPrepare(context.Context, ClusterPolicy, model.ResourceID) error
	RecoveryGuardVerify(context.Context, ClusterPolicy, model.ResourceID) error
	RecoveryGuardRelease(context.Context, ClusterPolicy, model.ResourceID) error
}

type pgRecoveryGuard struct {
	TaskID      model.ResourceID `json:"task_id"`
	ClusterID   model.ResourceID `json:"cluster_id"`
	InstanceID  model.ResourceID `json:"instance_id"`
	Original    []byte           `json:"original"`
	Digest      string           `json:"digest"`
	InstalledAt time.Time        `json:"installed_at"`
	Released    bool             `json:"released"`
}

func recoveryFileDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func recoveryAtomicFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func recoveryRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("recovery state must be a bounded regular file")
	}
	return os.ReadFile(path)
}

func pgGuardPaths(p ClusterPolicy) (string, string, error) {
	dir, err := safePostgreSQLDataDirectory(p.PostgreSQLDataDirectory)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "pg_hba.conf"), filepath.Join(dir+".clusterguard-recovery", "guard.json"), nil
}

func pgGuardHBA(p ClusterPolicy) ([]byte, error) {
	if !postgresSQLNamePattern.MatchString(p.PostgreSQLUser) || !postgresSQLNamePattern.MatchString(p.PostgreSQLReplicationUser) {
		return nil, fmt.Errorf("recovery requires fixed administrative and replication users")
	}
	addresses := map[string]bool{"127.0.0.1/32": true, "::1/128": true}
	for _, peer := range p.PostgreSQLPeers {
		ip := net.ParseIP(peer.IPAddress)
		if ip == nil {
			return nil, fmt.Errorf("recovery peers require literal allowlisted IP addresses")
		}
		bits := "/128"
		if ip.To4() != nil {
			bits = "/32"
		}
		addresses[ip.String()+bits] = true
	}
	if len(p.PostgreSQLPeers) < 2 {
		return nil, fmt.Errorf("recovery guard requires the complete peer allowlist")
	}
	keys := make([]string, 0, len(addresses))
	for key := range addresses {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# ClusterGuard recovery-only access. Restored only after Recovery Commit.\n")
	fmt.Fprintf(&b, "local all %s peer\nlocal all all reject\n", p.PostgreSQLUser)
	for _, address := range keys {
		fmt.Fprintf(&b, "host all %s %s scram-sha-256\n", p.PostgreSQLUser, address)
		fmt.Fprintf(&b, "host replication %s %s scram-sha-256\n", p.PostgreSQLReplicationUser, address)
	}
	controllers := map[string]bool{}
	for _, raw := range p.RecoveryControllerAddresses {
		ip := net.ParseIP(raw)
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, fmt.Errorf("recovery controller address must be a pinned unicast IP")
		}
		bits := "/128"
		if ip.To4() != nil {
			bits = "/32"
		}
		address := ip.String() + bits
		if !addresses[address] {
			controllers[address] = true
		}
	}
	keys = keys[:0]
	for address := range controllers {
		keys = append(keys, address)
	}
	sort.Strings(keys)
	for _, address := range keys {
		fmt.Fprintf(&b, "host all %s %s scram-sha-256\n", p.PostgreSQLUser, address)
	}
	b.WriteString("host all all 0.0.0.0/0 reject\nhost all all ::0/0 reject\nhost replication all 0.0.0.0/0 reject\nhost replication all ::0/0 reject\n")
	return []byte(b.String()), nil
}

func readPGGuard(p ClusterPolicy, taskID model.ResourceID) (pgRecoveryGuard, string, string, error) {
	var state pgRecoveryGuard
	hba, manifest, err := pgGuardPaths(p)
	if err != nil {
		return state, hba, manifest, err
	}
	b, err := recoveryRegularFile(manifest)
	// Upgrade an existing frozen task without mistaking its restrictive HBA
	// for the original. The legacy receipt remains an immutable backup.
	if os.IsNotExist(err) {
		b, err = recoveryRegularFile(filepath.Dir(hba) + ".clusterguard-recovery-guard.json")
	}
	if err != nil {
		return state, hba, manifest, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return state, hba, manifest, fmt.Errorf("invalid recovery guard state")
	}
	if !model.ValidResourceID(taskID) || state.TaskID != taskID || state.InstanceID != p.InstanceID || state.ClusterID != p.ClusterID || len(state.Original) == 0 {
		return state, hba, manifest, fmt.Errorf("recovery guard identity does not match the authorization")
	}
	return state, hba, manifest, nil
}

func preparePGGuard(ctx context.Context, p ClusterPolicy, taskID model.ResourceID, tool recoveryPGTool) error {
	if !model.ValidResourceID(taskID) {
		return fmt.Errorf("recovery task UUID is required")
	}
	hba, manifest, err := pgGuardPaths(p)
	if err != nil {
		return err
	}
	expected := hba
	if p.RuntimeKind == model.RuntimeDocker {
		expected = filepath.Join(p.DockerPostgreSQLDataDirectory, "pg_hba.conf")
	}
	configured, err := tool(ctx, "postgres", "-C", "hba_file")
	if err != nil || strings.TrimSpace(string(configured)) != expected {
		return fmt.Errorf("recovery cannot guard a non-default or unverified PostgreSQL HBA path")
	}
	guard, err := pgGuardHBA(p)
	if err != nil {
		return err
	}
	state, _, _, readErr := readPGGuard(p, taskID)
	if readErr != nil && state.Released && state.ClusterID == p.ClusterID && state.InstanceID == p.InstanceID {
		actual, err := recoveryRegularFile(hba)
		if err != nil || recoveryFileDigest(actual) != recoveryFileDigest(state.Original) {
			return fmt.Errorf("previous recovery guard restoration is not verified")
		}
		readErr = nil
	}
	if readErr == nil && !state.Released {
		if state.Digest != recoveryFileDigest(guard) {
			legacy := p
			legacy.RecoveryControllerAddresses = nil
			oldGuard, oldErr := pgGuardHBA(legacy)
			actual, actualErr := recoveryRegularFile(hba)
			if oldErr != nil || actualErr != nil || state.Digest != recoveryFileDigest(oldGuard) || recoveryFileDigest(actual) != state.Digest {
				return fmt.Errorf("recovery guard policy changed")
			}
			// Migrate only an exact legacy guard, while the node is stopped.
			// Preserve the original HBA and task ownership across the upgrade.
			state.Digest = recoveryFileDigest(guard)
		}
	} else {
		if !os.IsNotExist(readErr) && readErr != nil {
			return readErr
		}
		original, err := recoveryRegularFile(hba)
		if err != nil {
			return err
		}
		state = pgRecoveryGuard{TaskID: taskID, ClusterID: p.ClusterID, InstanceID: p.InstanceID, Original: original, Digest: recoveryFileDigest(guard), InstalledAt: time.Now().UTC()}
	}
	state.InstalledAt = time.Now().UTC()
	data, _ := json.Marshal(state)
	if err = recoveryPrivateDirectory(filepath.Dir(manifest)); err != nil {
		return err
	}
	// Persist the original before changing the active HBA; interruption can
	// only leave a stopped database or a restrictive, recoverable guard.
	if err = recoveryAtomicFile(manifest, data, 0600); err != nil {
		return err
	}
	return recoveryAtomicFile(hba, guard, 0644)
}

func verifyPGGuardFile(p ClusterPolicy, taskID model.ResourceID) (pgRecoveryGuard, error) {
	state, hba, _, err := readPGGuard(p, taskID)
	if err != nil {
		return state, err
	}
	actual, err := recoveryRegularFile(hba)
	if err != nil {
		return state, err
	}
	expected, err := pgGuardHBA(p)
	if err != nil {
		return state, err
	}
	if state.Released || state.Digest != recoveryFileDigest(expected) || recoveryFileDigest(actual) != state.Digest {
		return state, fmt.Errorf("recovery business-access guard is missing or changed")
	}
	return state, nil
}

type pgGuardQuery func(context.Context, ClusterPolicy, string) ([]byte, error)

func verifyPGGuardLoaded(ctx context.Context, p ClusterPolicy, taskID model.ResourceID, query pgGuardQuery) error {
	state, err := verifyPGGuardFile(p, taskID)
	if err != nil {
		return err
	}
	// HBA reload does not terminate existing sessions. Guard installation is
	// restricted to a stopped database, so its process must postdate the guard.
	expected := filepath.Join(p.PostgreSQLDataDirectory, "pg_hba.conf")
	if p.RuntimeKind == model.RuntimeDocker {
		expected = filepath.Join(p.DockerPostgreSQLDataDirectory, "pg_hba.conf")
	}
	q := fmt.Sprintf("SELECT (pg_postmaster_start_time() >= '%s'::timestamptz AND current_setting('hba_file') = '%s' AND NOT EXISTS (SELECT 1 FROM pg_hba_file_rules WHERE error IS NOT NULL))", state.InstalledAt.Format(time.RFC3339Nano), strings.ReplaceAll(expected, "'", "''"))
	output, err := query(ctx, p, q)
	if err != nil && time.Since(state.InstalledAt) >= 0 && time.Since(state.InstalledAt) < 30*time.Second {
		return nil
	}
	if err != nil || strings.TrimSpace(string(output)) != "t" {
		return fmt.Errorf("running PostgreSQL has not proven recovery-only access")
	}
	return nil
}

func releasePGGuard(ctx context.Context, p ClusterPolicy, taskID model.ResourceID, query pgGuardQuery) error {
	state, hba, manifest, err := readPGGuard(p, taskID)
	if err != nil {
		return err
	}
	if err = recoveryPrivateDirectory(filepath.Dir(manifest)); err != nil {
		return err
	}
	actual, err := recoveryRegularFile(hba)
	if err != nil {
		return err
	}
	// After a crash between restoring HBA and recording the receipt, only the
	// exact preserved original is accepted for idempotent activation.
	if recoveryFileDigest(actual) != state.Digest && recoveryFileDigest(actual) != recoveryFileDigest(state.Original) {
		return fmt.Errorf("refusing to replace an independently changed HBA file")
	}
	if err = recoveryAtomicFile(hba, state.Original, 0644); err != nil {
		return err
	}
	output, err := query(ctx, p, "SELECT pg_reload_conf()")
	if err != nil || strings.TrimSpace(string(output)) != "t" {
		return fmt.Errorf("restored PostgreSQL access could not be reloaded")
	}
	state.Released = true
	data, _ := json.Marshal(state)
	return recoveryAtomicFile(manifest, data, 0600)
}

func (c *PostgreSQLLocalController) RecoveryGuardPrepare(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	if err := c.recoveryStopped(ctx, p); err != nil {
		return err
	}
	return preparePGGuard(ctx, p, taskID, c.recoveryTool(p))
}
func (c *DockerPostgreSQLController) RecoveryGuardPrepare(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	if err := c.recoveryStopped(ctx, p); err != nil {
		return err
	}
	return preparePGGuard(ctx, p, taskID, c.recoveryTool(p))
}
func (c *PostgreSQLLocalController) RecoveryGuardVerify(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	if err := c.recoveryStopped(ctx, p); err == nil {
		_, err = verifyPGGuardFile(p, taskID)
		return err
	}
	return verifyPGGuardLoaded(ctx, p, taskID, c.psql)
}
func (c *DockerPostgreSQLController) RecoveryGuardVerify(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	if err := c.recoveryStopped(ctx, p); err == nil {
		_, err = verifyPGGuardFile(p, taskID)
		return err
	}
	return verifyPGGuardLoaded(ctx, p, taskID, c.psql)
}
func (c *PostgreSQLLocalController) RecoveryGuardRelease(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	return releasePGGuard(ctx, p, taskID, c.psql)
}
func (c *DockerPostgreSQLController) RecoveryGuardRelease(ctx context.Context, p ClusterPolicy, taskID model.ResourceID) error {
	return releasePGGuard(ctx, p, taskID, c.psql)
}
