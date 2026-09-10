package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/internal/disaster"
	"clusterguard.io/ha/pkg/model"
)

const recoveryWALByteLimit uint64 = 512 << 20

var emptyWALTail = regexp.MustCompile(`invalid record length at ([0-9A-Fa-f]+/[0-9A-Fa-f]+): (?:wanted 24|expected at least 24), got 0`)
var walRecordPosition = regexp.MustCompile(`\blsn: ([0-9A-Fa-f]+/[0-9A-Fa-f]+),`)

type recoveryPGTool func(context.Context, string, ...string) ([]byte, error)

func parseRecoveryControl(output []byte) (map[string]string, error) {
	if strings.Contains(string(output), "WARNING") || strings.Contains(string(output), "CRC") && strings.Contains(strings.ToLower(string(output)), "mismatch") {
		return nil, fmt.Errorf("PostgreSQL control file integrity check failed")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["Database system identifier"] == "" {
		return nil, fmt.Errorf("PostgreSQL control file is incomplete")
	}
	return values, nil
}

func readRecoveryHistory(directory string, timeline uint32) ([]model.RecoveryTimeline, error) {
	if timeline == 1 {
		return nil, nil
	}
	path := filepath.Join(directory, "pg_wal", fmt.Sprintf("%08X.history", timeline))
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("timeline history is missing: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid timeline history file")
	}
	var history []model.RecoveryTimeline
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid timeline history entry")
		}
		id, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("invalid ancestor timeline")
		}
		if _, err := disaster.LSN(fields[1]); err != nil {
			return nil, err
		}
		history = append(history, model.RecoveryTimeline{Timeline: uint32(id), SwitchLSN: fields[1]})
	}
	return history, scanner.Err()
}

func inspectStoppedPostgreSQL(ctx context.Context, policy ClusterPolicy, tool recoveryPGTool) (model.RecoveryEvidence, error) {
	e := model.RecoveryEvidence{InstanceID: policy.InstanceID, NativeID: string(policy.PostgreSQLNodeID), Engine: model.EnginePostgreSQL, ObservedAt: time.Now().UTC(), Fenced: true}
	directory, err := safePostgreSQLDataDirectory(policy.PostgreSQLDataDirectory)
	if err != nil {
		return e, err
	}
	output, err := tool(ctx, "pg_controldata")
	if err != nil {
		return e, err
	}
	control, err := parseRecoveryControl(output)
	if err != nil {
		return e, err
	}
	identity, err := tool(ctx, "postgres", "-C", "clusterguard.node_id")
	if err != nil || strings.TrimSpace(string(identity)) != string(policy.PostgreSQLNodeID) {
		return e, fmt.Errorf("offline PostgreSQL native identity does not match the Agent allowlist")
	}
	e.ControlDigest = disaster.Digest(string(output))
	e.SystemIdentifier = control["Database system identifier"]
	e.Checkpoint = control["Latest checkpoint location"]
	e.Redo = control["Latest checkpoint's REDO location"]
	e.ControlState = control["Database cluster state"]
	timeline, err := strconv.ParseUint(control["Latest checkpoint's TimeLineID"], 10, 32)
	if err != nil || timeline == 0 {
		return e, fmt.Errorf("invalid checkpoint timeline")
	}
	minimumTimeline, _ := strconv.ParseUint(control["Min recovery ending loc's timeline"], 10, 32)
	if minimumTimeline > timeline {
		timeline = minimumTimeline
	}
	e.Timeline = uint32(timeline)
	e.WALSegmentBytes, err = strconv.ParseUint(control["Bytes per WAL segment"], 10, 64)
	if err != nil || e.WALSegmentBytes < 1<<20 || e.WALSegmentBytes > 1<<30 || (e.WALSegmentBytes&(e.WALSegmentBytes-1)) != 0 {
		return e, fmt.Errorf("invalid WAL segment size")
	}
	if _, err = disaster.LSN(e.Checkpoint); err != nil {
		return e, err
	}
	if _, err = disaster.LSN(e.Redo); err != nil {
		return e, err
	}
	e.History, err = readRecoveryHistory(directory, e.Timeline)
	if err != nil {
		return e, err
	}
	// A promotion may advance minRecoveryPoint before the first checkpoint on
	// the new timeline. REDO can therefore still name an ancestor's WAL file.
	start := e.Redo
	var scanned strings.Builder
	remaining := 100000
	var previousTimeline uint32
	var previousFork uint64
	for _, ancestor := range e.History {
		fork, forkErr := disaster.LSN(ancestor.SwitchLSN)
		if forkErr != nil || fork == 0 || fork < previousFork || ancestor.Timeline <= previousTimeline || ancestor.Timeline >= e.Timeline || (previousTimeline == 0 && ancestor.Timeline != 1) {
			return e, fmt.Errorf("invalid PostgreSQL timeline ancestry")
		}
		previousTimeline, previousFork = ancestor.Timeline, fork
		position, _ := disaster.LSN(start)
		if fork <= position {
			continue
		}
		dump, dumpErr := tool(ctx, "pg_waldump", "--timeline", strconv.FormatUint(uint64(ancestor.Timeline), 10), "--start", start, "--end", ancestor.SwitchLSN, "--limit", strconv.Itoa(remaining))
		records := len(walRecordPosition.FindAll(dump, -1))
		if dumpErr != nil || records >= remaining {
			return e, fmt.Errorf("required ancestor WAL is missing, corrupt or exceeds bounded scan")
		}
		remaining -= records
		fmt.Fprintf(&scanned, "timeline=%d start=%s end=%s\n%s\n", ancestor.Timeline, start, ancestor.SwitchLSN, dump)
		start = ancestor.SwitchLSN
	}
	if e.Timeline != 1 && len(e.History) == 0 {
		return e, fmt.Errorf("PostgreSQL timeline ancestry is incomplete")
	}
	// PostgreSQL itself validates record CRCs and the available chain. Only an
	// all-zero end-of-WAL is a complete tail; missing files and corrupt records
	// are not interpreted as an empty branch.
	dump, dumpErr := tool(ctx, "pg_waldump", "--timeline", strconv.FormatUint(timeline, 10), "--start", start, "--limit", strconv.Itoa(remaining))
	tail := emptyWALTail.FindSubmatch(dump)
	if dumpErr == nil || len(tail) != 2 || len(walRecordPosition.FindAll(dump, -1)) >= remaining {
		return e, fmt.Errorf("WAL tail is incomplete, corrupt or exceeds bounded scan; preserve all data for manual review")
	}
	lines := strings.Split(strings.TrimSpace(string(dump)), "\n")
	if !emptyWALTail.MatchString(lines[len(lines)-1]) {
		return e, fmt.Errorf("unexpected error after WAL scan")
	}
	e.Position = string(tail[1])
	fmt.Fprintf(&scanned, "timeline=%d start=%s\n%s", e.Timeline, start, dump)
	e.WALTailDigest = disaster.Digest(scanned.String())
	end, _ := disaster.LSN(e.Position)
	checkpoint, _ := disaster.LSN(e.Checkpoint)
	if end <= checkpoint {
		return e, fmt.Errorf("WAL does not contain the latest checkpoint")
	}
	if min := control["Minimum recovery ending location"]; min != "" {
		position, err := disaster.LSN(min)
		if err != nil || position > end {
			return e, fmt.Errorf("WAL does not reach the minimum recovery point")
		}
	}
	if entries, readErr := os.ReadDir(filepath.Join(directory, "pg_twophase")); readErr != nil {
		return e, fmt.Errorf("prepared transaction state is unavailable")
	} else {
		e.PreparedTransactions = len(entries) > 0
	}
	// A crash can leave prepared transactions in WAL before pg_twophase was
	// checkpointed. Conservatively retain any PREPARE seen in the recovery tail.
	for _, line := range strings.Split(scanned.String(), "\n") {
		if strings.Contains(line, "rmgr: Transaction") && strings.Contains(line, "PREPARE") {
			e.PreparedTransactions = true
		}
	}
	e.Complete = true
	e.Fingerprint = disaster.EvidenceFingerprint(e)
	return e, nil
}

func verifyRecoveryWAL(ctx context.Context, policy ClusterPolicy, tool recoveryPGTool, request model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	result := model.RecoveryWALResult{}
	e, err := inspectStoppedPostgreSQL(ctx, policy, tool)
	if err != nil {
		return result, err
	}
	if e.Fingerprint != request.Fingerprint {
		return result, fmt.Errorf("PostgreSQL storage changed after recovery inspection")
	}
	start, err := disaster.LSN(request.Start)
	if err != nil {
		return result, err
	}
	end, err := disaster.LSN(request.End)
	if err != nil {
		return result, err
	}
	if request.Timeline == 0 || start >= end || end-start > recoveryWALByteLimit {
		return result, fmt.Errorf("WAL proof range is invalid or too large")
	}
	allowed := false
	bound, _ := disaster.LSN(e.Position)
	var branchStart uint64
	for _, ancestor := range e.History {
		limit, _ := disaster.LSN(ancestor.SwitchLSN)
		if ancestor.Timeline == request.Timeline {
			allowed = start >= branchStart && end <= limit
		}
		branchStart = limit
	}
	if request.Timeline == e.Timeline {
		allowed = start >= branchStart && end <= bound
	}
	if !allowed {
		return result, fmt.Errorf("WAL proof is outside this member's history")
	}
	output, err := tool(ctx, "pg_waldump", "--timeline", strconv.FormatUint(uint64(request.Timeline), 10), "--start", request.Start, "--end", request.End)
	if err != nil {
		return result, fmt.Errorf("required WAL range cannot be completely decoded")
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "rmgr: Transaction") && (strings.Contains(line, "COMMIT") || strings.Contains(line, "PREPARE")) {
			result.Transactions = true
		}
	}
	digest := sha256.New()
	directory := filepath.Join(policy.PostgreSQLDataDirectory, "pg_wal")
	for position := start; position < end; {
		segment := position / e.WALSegmentBytes
		perLog := (uint64(1) << 32) / e.WALSegmentBytes
		name := fmt.Sprintf("%08X%08X%08X", request.Timeline, segment/perLog, segment%perLog)
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != e.WALSegmentBytes {
			return result, fmt.Errorf("required WAL segment is missing or invalid")
		}
		file, err := os.Open(path)
		if err != nil {
			return result, err
		}
		offset := position % e.WALSegmentBytes
		length := min(end-position, e.WALSegmentBytes-offset)
		_, err = io.CopyN(digest, io.NewSectionReader(file, int64(offset), int64(length)), int64(length))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return result, fmt.Errorf("cannot hash complete WAL range")
		}
		position += length
	}
	result.Digest = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	after, err := inspectStoppedPostgreSQL(ctx, policy, tool)
	if err != nil || after.Fingerprint != e.Fingerprint {
		return model.RecoveryWALResult{}, fmt.Errorf("PostgreSQL evidence changed during WAL verification")
	}
	return result, nil
}

func (c *PostgreSQLLocalController) recoveryTool(policy ClusterPolicy) recoveryPGTool {
	reader := *c
	reader.runner = orderedOutputRunner{c.runner}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "postgres" {
			args = append([]string{"-D", policy.PostgreSQLDataDirectory}, args...)
		} else if name == "pg_controldata" {
			args = append([]string{policy.PostgreSQLDataDirectory}, args...)
		} else {
			args = append([]string{"--path", filepath.Join(policy.PostgreSQLDataDirectory, "pg_wal")}, args...)
		}
		return reader.runAsPostgreSQLUser(ctx, policy, filepath.Join(policy.PostgreSQLBinaryDirectory, name), args...)
	}
}

func (c *PostgreSQLLocalController) RecoveryInspect(ctx context.Context, policy ClusterPolicy) (model.RecoveryEvidence, error) {
	if err := c.recoveryStopped(ctx, policy); err != nil {
		return model.RecoveryEvidence{}, fmt.Errorf("offline PostgreSQL evidence requires a stopped service")
	}
	return inspectStoppedPostgreSQL(ctx, policy, c.recoveryTool(policy))
}

func (c *PostgreSQLLocalController) RecoveryWAL(ctx context.Context, policy ClusterPolicy, request model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	if err := c.recoveryStopped(ctx, policy); err != nil {
		return model.RecoveryWALResult{}, fmt.Errorf("WAL comparison requires a stopped service")
	}
	return verifyRecoveryWAL(ctx, policy, c.recoveryTool(policy), request)
}

func (c *DockerPostgreSQLController) recoveryTool(policy ClusterPolicy) recoveryPGTool {
	reader := *c
	reader.runner = orderedOutputRunner{c.runner}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		image, err := reader.serviceImage(ctx, policy)
		if err != nil {
			return nil, err
		}
		if name == "postgres" {
			args = append([]string{"-D", policy.DockerPostgreSQLDataDirectory}, args...)
		} else if name == "pg_controldata" {
			args = append([]string{policy.DockerPostgreSQLDataDirectory}, args...)
		} else {
			args = append([]string{"--path", filepath.Join(policy.DockerPostgreSQLDataDirectory, "pg_wal")}, args...)
		}
		base := []string{"run", "--rm", "--network", "none", "--read-only", "--user", policy.PostgreSQLUser, "--env", "LC_ALL=C", "--mount", "type=bind,src=" + policy.PostgreSQLDataDirectory + ",dst=" + policy.DockerPostgreSQLDataDirectory + ",readonly", "--entrypoint", filepath.Join(policy.PostgreSQLBinaryDirectory, name), image}
		return reader.runDocker(ctx, append(base, args...)...)
	}
}

func (c *DockerPostgreSQLController) RecoveryInspect(ctx context.Context, policy ClusterPolicy) (model.RecoveryEvidence, error) {
	if err := c.recoveryStopped(ctx, policy); err != nil {
		return model.RecoveryEvidence{}, fmt.Errorf("offline PostgreSQL evidence requires a stopped Swarm service")
	}
	return inspectStoppedPostgreSQL(ctx, policy, c.recoveryTool(policy))
}

func (c *DockerPostgreSQLController) RecoveryWAL(ctx context.Context, policy ClusterPolicy, request model.RecoveryWALRequest) (model.RecoveryWALResult, error) {
	if err := c.recoveryStopped(ctx, policy); err != nil {
		return model.RecoveryWALResult{}, fmt.Errorf("WAL comparison requires a stopped Swarm service")
	}
	return verifyRecoveryWAL(ctx, policy, c.recoveryTool(policy), request)
}

func (c *PostgreSQLLocalController) recoveryStopped(ctx context.Context, p ClusterPolicy) error {
	state, err := c.serviceState(ctx, p)
	if err != nil || (state != "inactive" && state != "failed" && state != "dead") {
		return fmt.Errorf("PostgreSQL service is not stopped")
	}
	output, err := c.runAsPostgreSQLUser(ctx, p, filepath.Join(p.PostgreSQLBinaryDirectory, "pg_ctl"), "-D", p.PostgreSQLDataDirectory, "status")
	if err == nil || !strings.Contains(string(output), "no server running") {
		return fmt.Errorf("PostgreSQL process absence is not verified")
	}
	return nil
}

func (c *DockerPostgreSQLController) recoveryStopped(ctx context.Context, p ClusterPolicy) error {
	if _, err := c.verifyService(ctx, p); err != nil {
		return err
	}
	output, err := c.runDocker(ctx, "service", "inspect", "--format", "{{json .Spec}}", p.DockerSwarmService)
	if err != nil {
		return err
	}
	var spec struct {
		Mode         struct{ Replicated *struct{ Replicas *uint64 } }
		TaskTemplate struct {
			ContainerSpec struct {
				Mounts []struct{ Type, Source, Target string }
			}
		}
	}
	if json.Unmarshal(output, &spec) != nil || spec.Mode.Replicated == nil || spec.Mode.Replicated.Replicas == nil || *spec.Mode.Replicated.Replicas != 0 {
		return fmt.Errorf("offline recovery requires verified zero desired replicas")
	}
	matched := false
	for _, mount := range spec.TaskTemplate.ContainerSpec.Mounts {
		if mount.Target == p.DockerPostgreSQLDataDirectory {
			matched = mount.Type == "bind" && mount.Source == p.PostgreSQLDataDirectory
		}
	}
	if !matched {
		return fmt.Errorf("offline recovery data mount does not match the Agent allowlist")
	}
	output, err = c.runDocker(ctx, "service", "ps", "--no-trunc", "--format", "{{.CurrentState}}", p.DockerSwarmService)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" && !strings.HasPrefix(line, "Shutdown ") && !strings.HasPrefix(line, "Failed ") && !strings.HasPrefix(line, "Rejected ") && !strings.HasPrefix(line, "Complete ") {
			return fmt.Errorf("a Swarm task is still active or its state is uncertain")
		}
	}
	return nil
}
