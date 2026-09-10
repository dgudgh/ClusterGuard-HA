package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type RecoveryRestartPreparer interface {
	RecoveryPrepareRestart(context.Context, ClusterPolicy) error
}

// MySQL applies mysqld-auto.cnf after ordinary option files. Updating only the
// host fence can therefore restart a former primary writable. This narrowly
// updates the two persisted roles while the service is proven stopped, retaining
// the exact original and all unrelated settings.
func fenceMySQLPersistedVariables(dataDirectory string) error {
	path := filepath.Join(dataDirectory, "mysqld-auto.cnf")
	original, err := recoveryRegularFile(path)
	if err != nil {
		return fmt.Errorf("qualified stopped MySQL recovery requires its persisted-variable file: %w", err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(original, &document); err != nil {
		return fmt.Errorf("invalid MySQL persisted-variable document")
	}
	var version int
	if err = json.Unmarshal(document["Version"], &version); err != nil || version != 2 {
		return fmt.Errorf("unqualified MySQL persisted-variable format")
	}
	var dynamic map[string]map[string]json.RawMessage
	if err = json.Unmarshal(document["mysql_dynamic_variables"], &dynamic); err != nil || dynamic == nil {
		return fmt.Errorf("MySQL dynamic persisted variables are unavailable")
	}
	changed := false
	for _, name := range []string{"read_only", "super_read_only"} {
		entry, found := dynamic[name]
		if !found {
			return fmt.Errorf("MySQL persisted role evidence is incomplete")
		}
		var value string
		if err = json.Unmarshal(entry["Value"], &value); err != nil {
			return fmt.Errorf("invalid persisted MySQL role")
		}
		on, err := mysqlBoolean(value)
		if err != nil {
			return err
		}
		if !on {
			entry["Value"] = json.RawMessage(`"ON"`)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	backup := path + ".clusterguard-before-" + recoveryFileDigest(original)[:16]
	if data, err := recoveryRegularFile(backup); err == nil {
		if string(data) != string(original) {
			return fmt.Errorf("MySQL persisted-role backup conflicts")
		}
	} else if os.IsNotExist(err) {
		if err = recoveryAtomicFile(backup, original, 0600); err != nil {
			return err
		}
	} else {
		return err
	}
	document["mysql_dynamic_variables"], err = json.Marshal(dynamic)
	if err != nil {
		return err
	}
	contents, err := json.Marshal(document)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("persisted-role ownership cannot be verified")
	}
	f, err := os.CreateTemp(dataDirectory, ".recovery-mysql-role-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(info.Mode().Perm() & 0600); err != nil {
		return err
	}
	if err = f.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
		return err
	}
	if _, err = f.Write(contents); err != nil {
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
	dir, err := os.Open(dataDirectory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func parseMySQLStartupSettings(output []byte) (map[string]string, error) {
	settings := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		switch fields[0] {
		case "datadir", "persisted-globals-load", "init-file", "skip-grant-tables":
			settings[fields[0]] = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		}
	}
	load, err := mysqlBoolean(settings["persisted-globals-load"])
	if err != nil || !load {
		return settings, fmt.Errorf("stopped MySQL recovery requires persisted globals to load")
	}
	initFile := settings["init-file"]
	if initFile != "" && initFile != "(No default value)" {
		return settings, fmt.Errorf("an unreviewed MySQL init-file prevents automatic recovery")
	}
	if skip := settings["skip-grant-tables"]; skip != "" {
		on, err := mysqlBoolean(skip)
		if err != nil || on {
			return settings, fmt.Errorf("MySQL authentication bypass prevents automatic recovery")
		}
	}
	return settings, nil
}

func (c *MySQLRoleController) RecoveryPrepareRestart(ctx context.Context, p ClusterPolicy) error {
	if p.MySQLServerBinary == "" || p.MySQLServerDefaultsFile == "" {
		return fmt.Errorf("native MySQL stopped recovery requires pinned server binary and defaults file")
	}
	output, err := c.runner.Run(ctx, p.MySQLServerBinary, "--defaults-file="+p.MySQLServerDefaultsFile, "--verbose", "--help")
	if err != nil {
		return err
	}
	settings, err := parseMySQLStartupSettings(output)
	if err != nil {
		return err
	}
	directory := filepath.Clean(settings["datadir"])
	if !filepath.IsAbs(directory) || directory == "/" {
		return fmt.Errorf("native MySQL data directory is not verified")
	}
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return fmt.Errorf("native MySQL data directory must be canonical")
	}
	if err = fenceMySQLPersistedVariables(directory); err != nil {
		return err
	}
	return c.persistState(p, true)
}

func (c *DockerMySQLRoleController) RecoveryPrepareRestart(ctx context.Context, p ClusterPolicy) error {
	var service struct {
		ID   string
		Spec struct {
			Mode         struct{ Replicated *struct{ Replicas *uint64 } }
			TaskTemplate struct {
				ContainerSpec struct {
					Image   string
					Command []string
					Configs []json.RawMessage
					Secrets []struct{ File struct{ Name string } }
					Args    []string
					Labels  map[string]string
					Mounts  []struct {
						Type, Source, Target string
						ReadOnly             bool
					}
				}
			}
		}
	}
	output, err := c.runDocker(ctx, "service", "inspect", "--format", "{{json .}}", p.DockerSwarmService)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(output, &service); err != nil {
		return fmt.Errorf("invalid MySQL Swarm service evidence")
	}
	if p.DockerSwarmServiceID == "" || service.ID != p.DockerSwarmServiceID || service.Spec.Mode.Replicated == nil || service.Spec.Mode.Replicated.Replicas == nil || *service.Spec.Mode.Replicated.Replicas != 0 {
		return fmt.Errorf("MySQL service must be stopped with an immutable allowlisted identity")
	}
	spec := service.Spec.TaskTemplate.ContainerSpec
	if len(spec.Command) > 0 || len(spec.Configs) > 0 {
		return fmt.Errorf("custom MySQL entrypoints or Swarm configs require startup qualification")
	}
	for _, secret := range spec.Secrets {
		if strings.Contains(secret.File.Name, "/") {
			return fmt.Errorf("custom secret mount path requires MySQL startup qualification")
		}
	}
	if spec.Labels["clusterguard.cluster_id"] != string(p.ClusterID) || spec.Labels["clusterguard.instance_id"] != string(p.InstanceID) || !safeDockerImageReference(spec.Image) {
		return fmt.Errorf("MySQL service labels or image are outside the allowlist")
	}
	args := []string{"run", "--rm", "--pull=never", "--network=none", "--read-only", "--user=mysql"}
	dataDirectory := ""
	fenceMounted := false
	for _, mount := range spec.Mounts {
		if mount.Type != "bind" {
			return fmt.Errorf("stopped MySQL recovery currently requires inspectable bind mounts")
		}
		if !filepath.IsAbs(mount.Source) || !filepath.IsAbs(mount.Target) || strings.ContainsAny(mount.Source+mount.Target, ",\r\n") {
			return fmt.Errorf("MySQL service has an unsafe mount")
		}
		if mount.Target == "/var/lib/mysql" {
			dataDirectory = mount.Source
		}
		if mount.Source == p.DockerFenceFile && strings.HasPrefix(mount.Target, "/etc/mysql/conf.d/") && mount.ReadOnly {
			fenceMounted = true
		}
		if mount.Source == filepath.Dir(p.DockerFenceFile) && mount.Target == "/etc/mysql/conf.d" && mount.ReadOnly {
			fenceMounted = true
		}
		args = append(args, "--mount", "type=bind,src="+mount.Source+",dst="+mount.Target+",readonly")
	}
	if dataDirectory == "" || !fenceMounted {
		return fmt.Errorf("MySQL recovery requires the pinned data and read-only restart fence mounts")
	}
	if resolved, err := filepath.EvalSymlinks(dataDirectory); err != nil || resolved != filepath.Clean(dataDirectory) {
		return fmt.Errorf("MySQL data directory must be canonical")
	}
	if _, running, err := c.runningContainer(ctx, p); err != nil || running {
		return fmt.Errorf("MySQL process absence is not verified")
	}
	if err = writeDockerRestartFence(p.DockerFenceFile, true); err != nil {
		return err
	}
	args = append(args, "--entrypoint", "mysqld", spec.Image)
	args = append(args, spec.Args...)
	args = append(args, "--verbose", "--help")
	output, err = c.runDocker(ctx, args...)
	if err != nil {
		return fmt.Errorf("inspect effective MySQL startup settings: %w", err)
	}
	settings, err := parseMySQLStartupSettings(output)
	if err != nil {
		return err
	}
	if filepath.Clean(settings["datadir"]) != "/var/lib/mysql" {
		return fmt.Errorf("effective MySQL datadir differs from the pinned mount")
	}
	if err = fenceMySQLPersistedVariables(dataDirectory); err != nil {
		return err
	}
	return c.state.persistState(p, true)
}

func (c *RuntimeRoleController) RecoveryPrepareRestart(ctx context.Context, p ClusterPolicy) error {
	selected, err := c.selected(p)
	if err != nil {
		return err
	}
	preparer, ok := selected.(RecoveryRestartPreparer)
	if !ok {
		return fmt.Errorf("runtime cannot verify a fenced MySQL restart")
	}
	return preparer.RecoveryPrepareRestart(ctx, p)
}
