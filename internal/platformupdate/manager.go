package platformupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRootDirectory      = "/var/lib/clusterguard/updates"
	DefaultTrustKeyPath       = "/etc/clusterguard/trust/patch-signing-public.pem"
	DefaultHelperSocketPath   = "/run/clusterguard/update-helper.sock"
	DefaultUpgradeBinaryPath  = "/usr/local/sbin/clusterguard-upgrade"
	DefaultMaximumUpload      = int64(2 << 30)
	PreferredPackageExtension = ".cgupgrade"
	LegacyPackageExtension    = ".cgpatch"

	patchFileName    = "package.cgpatch"
	packageFileName  = "package.json"
	jobFileName      = "status.json"
	outputFileName   = "output.log"
	eventsFileName   = "events.jsonl"
	maximumTailLines = 200
)

var (
	ErrUploadTooLarge       = errors.New("software update package exceeds maximum size")
	ErrInvalidPatch         = errors.New("software update package is invalid")
	ErrPackageConflict      = errors.New("software update package ID already exists with different content")
	ErrPackageNotFound      = errors.New("software update package was not found")
	ErrPlanRequired         = errors.New("a successful update plan is required before execution")
	ErrConfirmationRequired = errors.New("typed update package confirmation is required")
	ErrJobActive            = errors.New("another software update job is already active")
	patchIDPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

const AutomaticFailoverWarning = "系统升级期间无法进行自动切换，请注意关注。"

func SupportedPackageFileName(fileName string) bool {
	name := strings.ToLower(strings.TrimSpace(filepath.Base(fileName)))
	return strings.HasSuffix(name, PreferredPackageExtension) || strings.HasSuffix(name, LegacyPackageExtension)
}

type Mode string

const (
	ModePlan     Mode = "plan"
	ModeExecute  Mode = "execute"
	ModeResume   Mode = "resume"
	ModeRollback Mode = "rollback"
)

type Status string

const (
	StatusUploaded   Status = "uploaded"
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusPlanned    Status = "planned"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusRolledBack Status = "rolled_back"
)

type Package struct {
	PatchID           string    `json:"patch_id"`
	FileName          string    `json:"file_name"`
	SizeBytes         int64     `json:"size_bytes"`
	SHA256            string    `json:"sha256"`
	SourceVersion     string    `json:"source_version"`
	TargetVersion     string    `json:"target_version"`
	Architecture      string    `json:"architecture"`
	SignatureVerified bool      `json:"signature_verified"`
	RollbackAvailable bool      `json:"rollback_available"`
	Rolling           bool      `json:"rolling"`
	DatabaseMutation  bool      `json:"database_mutation"`
	UploadedAt        time.Time `json:"uploaded_at"`
}

type Event struct {
	PatchID   string    `json:"patch_id,omitempty"`
	Status    string    `json:"status"`
	Node      string    `json:"node,omitempty"`
	Message   string    `json:"message,omitempty"`
	Source    string    `json:"source,omitempty"`
	Target    string    `json:"target,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Job struct {
	PatchID                    string    `json:"patch_id"`
	Mode                       Mode      `json:"mode"`
	Status                     Status    `json:"status"`
	Node                       string    `json:"node,omitempty"`
	Message                    string    `json:"message,omitempty"`
	Warning                    string    `json:"warning,omitempty"`
	MaintenanceActive          bool      `json:"maintenance_active"`
	AutomaticFailoverAvailable bool      `json:"automatic_failover_available"`
	StartedAt                  time.Time `json:"started_at,omitempty"`
	UpdatedAt                  time.Time `json:"updated_at,omitempty"`
	FinishedAt                 time.Time `json:"finished_at,omitempty"`
	Events                     []Event   `json:"events,omitempty"`
	OutputTail                 []string  `json:"output_tail,omitempty"`
}

type PackageStatus struct {
	Package Package `json:"package"`
	Job     *Job    `json:"job,omitempty"`
}

type Snapshot struct {
	Available          bool            `json:"available"`
	Reason             string          `json:"reason,omitempty"`
	Warning            string          `json:"warning"`
	MaximumUploadBytes int64           `json:"maximum_upload_bytes"`
	Packages           []PackageStatus `json:"packages"`
}

type Config struct {
	RootDirectory      string
	TrustKeyPath       string
	UpgradeBinaryPath  string
	HelperSocketPath   string
	MaximumUploadBytes int64
}

type Inspector interface {
	Inspect(context.Context, string, string) (Package, error)
}

type Helper interface {
	Ready(context.Context) error
	Start(context.Context, Mode, string) error
}

type Manager struct {
	config    Config
	inspector Inspector
	helper    Helper
	now       func() time.Time
	mu        sync.Mutex
}

type Option func(*Manager)

func WithInspector(inspector Inspector) Option {
	return func(manager *Manager) { manager.inspector = inspector }
}
func WithHelper(helper Helper) Option         { return func(manager *Manager) { manager.helper = helper } }
func WithClock(clock func() time.Time) Option { return func(manager *Manager) { manager.now = clock } }

func NewManager(config Config, options ...Option) *Manager {
	if strings.TrimSpace(config.RootDirectory) == "" {
		config.RootDirectory = DefaultRootDirectory
	}
	if strings.TrimSpace(config.TrustKeyPath) == "" {
		config.TrustKeyPath = DefaultTrustKeyPath
	}
	if strings.TrimSpace(config.UpgradeBinaryPath) == "" {
		config.UpgradeBinaryPath = DefaultUpgradeBinaryPath
	}
	if strings.TrimSpace(config.HelperSocketPath) == "" {
		config.HelperSocketPath = DefaultHelperSocketPath
	}
	if config.MaximumUploadBytes <= 0 {
		config.MaximumUploadBytes = DefaultMaximumUpload
	}
	manager := &Manager{config: config, now: time.Now}
	manager.inspector = CommandInspector{UpgradeBinaryPath: config.UpgradeBinaryPath}
	manager.helper = NewUnixHelperClient(config.HelperSocketPath)
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

func (manager *Manager) Snapshot(ctx context.Context) Snapshot {
	result := Snapshot{Warning: AutomaticFailoverWarning, MaximumUploadBytes: manager.config.MaximumUploadBytes, Packages: []PackageStatus{}}
	if err := manager.readiness(ctx); err != nil {
		result.Reason = err.Error()
	} else {
		result.Available = true
	}
	entries, err := os.ReadDir(manager.config.RootDirectory)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && result.Reason == "" {
			result.Reason = err.Error()
			result.Available = false
		}
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validPatchID(entry.Name()) {
			continue
		}
		candidate, found := manager.Package(entry.Name())
		if !found {
			continue
		}
		status := PackageStatus{Package: candidate}
		if job, jobFound := manager.Job(candidate.PatchID); jobFound {
			status.Job = &job
		}
		result.Packages = append(result.Packages, status)
	}
	sort.Slice(result.Packages, func(left, right int) bool {
		return result.Packages[left].Package.UploadedAt.After(result.Packages[right].Package.UploadedAt)
	})
	return result
}

func (manager *Manager) Upload(ctx context.Context, fileName string, source io.Reader) (Package, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.readiness(ctx); err != nil {
		return Package{}, err
	}
	if source == nil || !SupportedPackageFileName(fileName) {
		return Package{}, ErrInvalidPatch
	}
	if err := os.MkdirAll(manager.config.RootDirectory, 0o750); err != nil {
		return Package{}, fmt.Errorf("create software update directory: %w", err)
	}
	temporary, err := os.CreateTemp(manager.config.RootDirectory, ".upload-*"+PreferredPackageExtension)
	if err != nil {
		return Package{}, fmt.Errorf("create software update staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_ = temporary.Chmod(0o600)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, manager.config.MaximumUploadBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return Package{}, fmt.Errorf("stage software update package: %w", copyErr)
	}
	if closeErr != nil {
		return Package{}, fmt.Errorf("close software update package: %w", closeErr)
	}
	if written > manager.config.MaximumUploadBytes {
		return Package{}, ErrUploadTooLarge
	}
	inspected, err := manager.inspector.Inspect(ctx, temporaryPath, manager.config.TrustKeyPath)
	if err != nil {
		return Package{}, fmt.Errorf("verify software update package: %w", err)
	}
	if !validPatchID(inspected.PatchID) || !inspected.SignatureVerified || !inspected.Rolling ||
		!inspected.RollbackAvailable || inspected.DatabaseMutation || strings.TrimSpace(inspected.TargetVersion) == "" {
		return Package{}, ErrInvalidPatch
	}
	destinationDirectory := filepath.Join(manager.config.RootDirectory, inspected.PatchID)
	if info, statErr := os.Lstat(destinationDirectory); statErr == nil && !info.IsDir() {
		return Package{}, ErrInvalidPatch
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Package{}, statErr
	}
	if err := os.MkdirAll(destinationDirectory, 0o750); err != nil {
		return Package{}, fmt.Errorf("create patch directory: %w", err)
	}
	inspected.FileName = filepath.Base(fileName)
	inspected.SizeBytes = written
	inspected.SHA256 = hex.EncodeToString(hash.Sum(nil))
	inspected.UploadedAt = manager.now().UTC()
	if existing, found := manager.Package(inspected.PatchID); found {
		if existing.SHA256 == inspected.SHA256 && existing.TargetVersion == inspected.TargetVersion && existing.SourceVersion == inspected.SourceVersion {
			return existing, nil
		}
		return Package{}, ErrPackageConflict
	}
	if err := os.Rename(temporaryPath, filepath.Join(destinationDirectory, patchFileName)); err != nil {
		return Package{}, fmt.Errorf("publish software update package: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(destinationDirectory, packageFileName), inspected); err != nil {
		_ = os.Remove(filepath.Join(destinationDirectory, patchFileName))
		return Package{}, err
	}
	return inspected, nil
}

func (manager *Manager) Start(ctx context.Context, mode Mode, patchID, confirmation string) (Job, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if err := manager.readiness(ctx); err != nil {
		return Job{}, err
	}
	if !validPatchID(patchID) {
		return Job{}, ErrPackageNotFound
	}
	if _, found := manager.Package(patchID); !found {
		return Job{}, ErrPackageNotFound
	}
	current, found := manager.Job(patchID)
	if found && (current.Status == StatusQueued || current.Status == StatusRunning) {
		return Job{}, ErrJobActive
	}
	if mode == ModeExecute && (!found || current.Status != StatusPlanned) {
		return Job{}, ErrPlanRequired
	}
	if mode != ModePlan && strings.TrimSpace(confirmation) != patchID {
		return Job{}, ErrConfirmationRequired
	}
	switch mode {
	case ModePlan, ModeExecute, ModeResume, ModeRollback:
	default:
		return Job{}, ErrInvalidPatch
	}
	now := manager.now().UTC()
	job := Job{
		PatchID: patchID, Mode: mode, Status: StatusQueued, StartedAt: now, UpdatedAt: now,
		AutomaticFailoverAvailable: mode == ModePlan,
	}
	if mode != ModePlan {
		job.Warning = AutomaticFailoverWarning
		job.Message = "已进入升级队列，维护门禁建立后自动故障切换将暂停"
	} else {
		job.Message = "正在生成只读滚动升级计划"
	}
	jobPath := filepath.Join(manager.config.RootDirectory, patchID, jobFileName)
	if err := writeJSONAtomic(jobPath, job); err != nil {
		return Job{}, err
	}
	if err := manager.helper.Start(ctx, mode, patchID); err != nil {
		job.Status = StatusFailed
		job.Message = err.Error()
		job.FinishedAt = manager.now().UTC()
		job.UpdatedAt = job.FinishedAt
		_ = writeJSONAtomic(jobPath, job)
		return Job{}, fmt.Errorf("start software update helper: %w", err)
	}
	return job, nil
}

func (manager *Manager) Package(patchID string) (Package, bool) {
	if !validPatchID(patchID) {
		return Package{}, false
	}
	result := Package{}
	if err := readJSONFile(filepath.Join(manager.config.RootDirectory, patchID, packageFileName), &result); err != nil || result.PatchID != patchID {
		return Package{}, false
	}
	return result, true
}

func (manager *Manager) Job(patchID string) (Job, bool) {
	if !validPatchID(patchID) {
		return Job{}, false
	}
	directory := filepath.Join(manager.config.RootDirectory, patchID)
	result := Job{}
	if err := readJSONFile(filepath.Join(directory, jobFileName), &result); err != nil || result.PatchID != patchID {
		return Job{}, false
	}
	result.OutputTail = readTail(filepath.Join(directory, outputFileName), maximumTailLines)
	eventsPath := filepath.Join(directory, eventsFileName)
	if _, err := os.Stat(eventsPath); errors.Is(err, os.ErrNotExist) {
		eventsPath = filepath.Join(directory, "clusterguard-update-"+patchID+".events.jsonl")
	}
	result.Events = readEvents(eventsPath, maximumTailLines)
	return result, true
}

func (manager *Manager) readiness(ctx context.Context) error {
	info, err := os.Stat(manager.config.TrustKeyPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("可信补丁签名公钥未配置：%s", manager.config.TrustKeyPath)
	}
	if manager.inspector == nil {
		return errors.New("补丁签名校验器未配置")
	}
	if manager.helper == nil {
		return errors.New("特权更新 Helper 未配置")
	}
	if err := manager.helper.Ready(ctx); err != nil {
		return fmt.Errorf("特权更新 Helper 不可用：%w", err)
	}
	return nil
}

func validPatchID(value string) bool { return patchIDPattern.MatchString(strings.TrimSpace(value)) }

func marshalJSON(value interface{}) ([]byte, error) {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(contents, '\n'), nil
}

func writeJSONAtomic(path string, value interface{}) error {
	contents, err := marshalJSON(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_ = temporary.Chmod(0o600)
	if _, err = temporary.Write(contents); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readJSONFile(path string, destination interface{}) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidPatch
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, destination)
}

func readTail(path string, limit int) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	result := make([]string, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result = append(result, line)
		if len(result) > limit {
			result = result[len(result)-limit:]
		}
	}
	return result
}

func readEvents(path string, limit int) []Event {
	lines := readTail(path, limit)
	result := make([]Event, 0, len(lines))
	for _, line := range lines {
		event := Event{}
		if json.Unmarshal([]byte(line), &event) == nil {
			result = append(result, event)
		}
	}
	return result
}
