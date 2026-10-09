package platformupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Deployment records verified installation independently of the latest attempt.
type Deployment struct {
	PackageID               string    `json:"package_id"`
	State                   string    `json:"state"`
	OperationID             string    `json:"operation_id,omitempty"`
	LastVerifiedState       string    `json:"last_verified_state,omitempty"`
	LastVerifiedOperationID string    `json:"last_verified_operation_id,omitempty"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func appendOperation(directory, name string, job Job) error {
	// Resolve the configured root (macOS /var is a system symlink), while
	// retaining NOFOLLOW on the package directory and history file themselves.
	root, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return err
	}
	pinned, err := openHelperDirectory(filepath.Join(root, filepath.Base(directory)))
	if err != nil {
		return err
	}
	defer pinned.Close()
	contents, err := json.Marshal(job)
	if err != nil {
		return err
	}
	file, err := openHelperFile(pinned, name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, updateFileMode)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(contents, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// lastCompletedEvent returns the newest event that ended an attempt: the runner
// writes several progress events per node before it writes the terminal one.
func lastCompletedEvent(job Job) *Event {
	for index := len(job.Events) - 1; index >= 0; index-- {
		event := job.Events[index]
		if event.Status != "succeeded" && event.Status != "failed" && event.Status != "rolled_back" && event.Status != "rollback_failed" {
			continue
		}
		return &job.Events[index]
	}
	return nil
}

// payloadInstalled reports whether an operation left the package's payload on
// disk, reading the operation record alone. A success is the direct answer. A
// failure is not necessarily the opposite of one: an attempt refused before it
// touched a node - the updater rejects a hotfix resume on its first line - is
// written over the same status.json as the run that actually applied and
// verified the patch, and that rejected attempt is what a site is left holding
// (2026-09-30, HF-2026-0929-05). The last completed attempt is the evidence
// there, exactly as it is when the deployment record is written.
func payloadInstalled(job Job) bool {
	if job.Status == StatusSucceeded {
		return true
	}
	if job.Status != StatusFailed || job.MaintenanceActive {
		return false
	}
	event := lastCompletedEvent(job)
	return event != nil && event.Status == "succeeded"
}

func (manager *Manager) preservePreviousOperation(job Job) error {
	directory := filepath.Join(manager.config.RootDirectory, job.PatchID)
	if job.OperationID == "" {
		contents, err := json.Marshal(job)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		job.OperationID = "legacy-" + hex.EncodeToString(digest[:16])
	}
	if err := appendOperation(directory, "requests.jsonl", job); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(directory, "deployment.json")); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		if _, valid := manager.Deployment(job.PatchID); !valid {
			return ErrInvalidPatch
		}
		return nil
	}
	state := "not_installed"
	switch job.Status {
	case StatusSucceeded:
		state = "installed"
	case StatusRolledBack:
		state = "rolled_back"
	case StatusFailed:
		if job.MaintenanceActive {
			state = "recovery_required"
		}
	}
	deployment := Deployment{PackageID: job.PatchID, State: state, UpdatedAt: job.UpdatedAt}
	// Migrate the original incident: a rejected legacy attempt overwrote
	// status.json while the runner's last completed event still proved success.
	if job.Status == StatusFailed && !job.MaintenanceActive {
		if event := lastCompletedEvent(job); event != nil && event.Status == "succeeded" {
			verified := job
			verified.Status, verified.Mode, verified.UpdatedAt = StatusSucceeded, ModeExecute, event.UpdatedAt
			verified.OperationID = event.OperationID
			if verified.OperationID == "" {
				verified.OperationID = event.ExecutionID
			}
			if verified.OperationID == "" {
				contents, _ := json.Marshal(event)
				digest := sha256.Sum256(contents)
				verified.OperationID = "legacy-success-" + hex.EncodeToString(digest[:16])
			}
			if err := appendOperation(directory, "requests.jsonl", verified); err != nil {
				return err
			}
			state, deployment.State, job.OperationID = "installed", "installed", verified.OperationID
		}
	}
	if state == "installed" || state == "rolled_back" {
		deployment.OperationID = job.OperationID
		deployment.LastVerifiedState = state
		deployment.LastVerifiedOperationID = job.OperationID
	}
	return writeJSONAtomic(filepath.Join(directory, "deployment.json"), deployment)
}

func (manager *Manager) Deployment(patchID string) (*Deployment, bool) {
	if !validPatchID(patchID) {
		return nil, false
	}
	var deployment Deployment
	if err := readJSONFile(filepath.Join(manager.config.RootDirectory, patchID, "deployment.json"), &deployment); err != nil || deployment.PackageID != patchID {
		return nil, false
	}
	switch deployment.State {
	case "not_installed", "applying", "installed", "rollbacking", "rolled_back", "recovery_required":
		return &deployment, true
	}
	return nil, false
}

// Operations folds append-only snapshots by identity. Events from one attempt
// cannot update another attempt, even when clocks move backwards.
func (manager *Manager) Operations(patchID string) []Job {
	if !validPatchID(patchID) {
		return nil
	}
	byID := map[string]Job{}
	for _, name := range []string{"requests.jsonl", "operations.jsonl"} {
		file, err := os.Open(filepath.Join(manager.config.RootDirectory, patchID, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var job Job
			if json.Unmarshal(scanner.Bytes(), &job) != nil || job.PatchID != patchID || job.OperationID == "" {
				continue
			}
			if previous, ok := byID[job.OperationID]; ok && terminalOperation(previous.Status) && !terminalOperation(job.Status) {
				continue
			}
			if previous, ok := byID[job.OperationID]; ok {
				preserveOperationVersions(previous, &job)
			}
			byID[job.OperationID] = job
		}
		file.Close()
	}
	jobs := make([]Job, 0, len(byID))
	for _, job := range byID {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	return jobs
}

func terminalOperation(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusRolledBack
}

type historyValidationCache struct {
	mu      sync.Mutex
	entries map[string]historyValidationEntry
}
type historyValidationEntry struct {
	files []os.FileInfo
	err   error
}

func (manager *Manager) validateHistory(patchID string) error {
	return manager.checkHistory(patchID, false)
}
func (manager *Manager) validateHistoryFresh(patchID string) error {
	return manager.checkHistory(patchID, true)
}

// Polling does not rescan unchanged append-only logs. An action always requests
// a fresh validation; inode identity detects atomic replacement even if size
// and modification time happen to stay the same.
func (manager *Manager) checkHistory(patchID string, fresh bool) error {
	manager.historyValidation.mu.Lock()
	defer manager.historyValidation.mu.Unlock()
	files := make([]os.FileInfo, 0, 3)
	for _, name := range []string{"deployment.json", "requests.jsonl", "operations.jsonl"} {
		info, err := os.Lstat(filepath.Join(manager.config.RootDirectory, patchID, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		files = append(files, info)
	}
	same := func(previous []os.FileInfo) bool {
		if len(previous) != len(files) {
			return false
		}
		for index, info := range files {
			old := previous[index]
			if info == nil || old == nil {
				if info != old {
					return false
				}
				continue
			}
			if !os.SameFile(info, old) || info.Size() != old.Size() || !info.ModTime().Equal(old.ModTime()) || info.Mode() != old.Mode() {
				return false
			}
		}
		return true
	}
	if entry, ok := manager.historyValidation.entries[patchID]; ok && !fresh && same(entry.files) {
		return entry.err
	}
	err := manager.validateHistoryFiles(patchID)
	if manager.historyValidation.entries == nil {
		manager.historyValidation.entries = make(map[string]historyValidationEntry)
	}
	manager.historyValidation.entries[patchID] = historyValidationEntry{files: files, err: err}
	return err
}

func (manager *Manager) validateHistoryFiles(patchID string) error {
	directory := filepath.Join(manager.config.RootDirectory, patchID)
	if _, err := os.Lstat(filepath.Join(directory, "deployment.json")); err == nil {
		if _, ok := manager.Deployment(patchID); !ok {
			return fmt.Errorf("CG_HISTORY_OVERWRITE_FORBIDDEN: invalid deployment record")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, name := range []string{"requests.jsonl", "operations.jsonl"} {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrInvalidPatch
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var job Job
			if err := json.Unmarshal(scanner.Bytes(), &job); err != nil || job.PatchID != patchID || !validPatchID(job.OperationID) {
				file.Close()
				return fmt.Errorf("CG_HISTORY_OVERWRITE_FORBIDDEN: invalid operation history")
			}
		}
		err = scanner.Err()
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) verifyStoredDigest(record Package) error {
	root, err := filepath.EvalSymlinks(manager.config.RootDirectory)
	if err != nil {
		return err
	}
	directory, err := openHelperDirectory(filepath.Join(root, record.PatchID))
	if err != nil {
		return err
	}
	defer directory.Close()
	file, err := openHelperFile(directory, patchFileName, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != record.SHA256 {
		return fmt.Errorf("%w: CG_RELEASED_ARTIFACT_MUTATED", ErrPackageConflict)
	}
	return nil
}

// Old wrappers drop unknown fields when publishing their terminal snapshot.
// Preserve the Runner's observations for that same operation, never a neighbour.
func preserveOperationVersions(previous Job, job *Job) {
	if previous.OperationID == "" || previous.OperationID != job.OperationID || previous.PatchID != job.PatchID || previous.Mode != job.Mode {
		return
	}
	if len(job.FromNodeVersions) != 0 || job.FromVersion != "" || job.ToVersion != "" {
		return
	}
	job.FromVersion, job.FromNodeVersions, job.ToVersion = previous.FromVersion, previous.FromNodeVersions, previous.ToVersion
}
