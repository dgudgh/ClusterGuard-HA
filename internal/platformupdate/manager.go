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

	"clusterguard.io/ha/internal/buildinfo"
)

const (
	DefaultRootDirectory      = "/var/lib/clusterguard/updates"
	DefaultPrivateRoot        = "/var/lib/clusterguard-update-private"
	DefaultTrustKeyPath       = "/etc/clusterguard/trust/patch-signing-public.pem"
	DefaultHelperSocketPath   = "/run/clusterguard/update-helper.sock"
	DefaultUpgradeBinaryPath  = "/usr/local/sbin/clusterguard-upgrade"
	DefaultMaximumUpload      = int64(2 << 30)
	PreferredPackageExtension = ".cgupgrade"
	LegacyPackageExtension    = ".cgpatch"

	// A stored uploaded_at further ahead of this node's clock than this cannot have been
	// written by a clock that agreed with it - the node was running ahead when the record
	// was created. The record is reported rather than quietly reordered: uploaded_at is
	// both the ordering key and the time the console shows, so an operator who is about to
	// pick an upgrade package has to know the time on that row is not trustworthy before
	// trusting the row order it produced.
	clockSkewTolerance = 5 * time.Minute

	// Package kinds as reported by the signed package inspector. A hotfix reuses
	// the legacy .cgpatch suffix but is applied by its own manifest-driven
	// installer instead of an RPM transaction.
	PackageKindUpgrade = "upgrade"
	PackageKindHotfix  = "hotfix"

	patchFileName    = "package.cgpatch"
	packageFileName  = "package.json"
	jobFileName      = "status.json"
	outputFileName   = "output.log"
	eventsFileName   = "events.jsonl"
	maximumTailLines = 200
	updateRootMode   = 0o750
	updateJobDirMode = 0o770
	updateFileMode   = 0o640
)

var (
	ErrUploadTooLarge       = errors.New("software update package exceeds maximum size")
	ErrInvalidPatch         = errors.New("software update package is invalid")
	ErrPackageConflict      = errors.New("software update package ID already exists with different content")
	ErrPackageNotFound      = errors.New("software update package was not found")
	ErrPackagePruned        = errors.New("安装包已按保留策略清理，历史日志仍保留；请重新上传签名升级包")
	ErrPlanRequired         = errors.New("a successful update plan is required before execution")
	ErrConfirmationRequired = errors.New("typed update package confirmation is required")
	ErrJobActive            = errors.New("another software update job is already active")
	ErrBootstrapRequired    = errors.New("signed .cgupgrade package does not contain a verified bootstrap upgrader")
	// ErrPackageBaselineMismatch reports a package that is cryptographically
	// sound but belongs to a release line this cluster is not running. The
	// console used to accept such a package and only discover it was unusable
	// when its plan ran the updater's own baseline guard - by which point the
	// record already owned the single action slot and could never release it.
	ErrPackageBaselineMismatch = errors.New("软件更新包与当前集群基线不一致")
	patchIDPattern             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

const AutomaticFailoverWarning = "系统升级期间无法进行自动切换，请注意关注。"

func SupportedPackageFileName(fileName string) bool {
	name := strings.ToLower(strings.TrimSpace(filepath.Base(fileName)))
	return strings.HasSuffix(name, PreferredPackageExtension) || strings.HasSuffix(name, LegacyPackageExtension)
}

// packageBaselineFault reports why a signed package cannot ever be applied to a
// cluster running current, or "" when it can. This is deliberately the single
// implementation of the rule: the upload path rejects on it and the snapshot
// annotates on it, so the two can never drift apart.
//
// The rule is not invented here - it mirrors the baseline guard in the updater
// (scripts/clusterguard-upgrade.sh, run_hotfix_update and run_rolling_update),
// which is the only other place that decides it:
//
//   - A hotfix replaces files inside one installed release, so the updater
//     requires the installed release to equal the patch baseline exactly, and
//     it never changes the RPM release, so the baseline stays valid afterwards.
//   - A rolling package is applied node by node and is resumable, so the updater
//     accepts a node that already reached the target as well as one still on the
//     source. Both ends are therefore legitimate baselines.
//
// An unknown running release (a build with no release metadata linked in, or an
// unreadable one) abstains rather than blocking legitimate uploads.
func packageBaselineFault(kind string, sourceVersion string, targetVersion string, current string) string {
	current = strings.TrimSpace(current)
	sourceVersion = strings.TrimSpace(sourceVersion)
	if current == "" || sourceVersion == "" || sourceVersion == current {
		return ""
	}
	if kind == PackageKindHotfix {
		return fmt.Sprintf("热修补丁属于 %s 发布线，本集群运行 %s；请先完成到 %s 的滚动升级再上传。", sourceVersion, current, sourceVersion)
	}
	if strings.TrimSpace(targetVersion) == current {
		return ""
	}
	return fmt.Sprintf("滚动升级包适用于 %s 到 %s，本集群运行 %s；请确认集群版本或改用匹配的升级包。", sourceVersion, targetVersion, current)
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
	ArtifactsPruned bool   `json:"artifacts_pruned,omitempty"`
	PatchID         string `json:"patch_id"`
	// Kind distinguishes a rolling RPM upgrade from a hotfix patch. The console
	// accepts both and applies them through the same helper, but the two are not
	// interchangeable: a hotfix deliberately leaves the installed RPM release
	// alone and only replaces the files its signed manifest names.
	Kind               string    `json:"kind,omitempty"`
	FileName           string    `json:"file_name"`
	SizeBytes          int64     `json:"size_bytes"`
	SHA256             string    `json:"sha256"`
	SourceVersion      string    `json:"source_version"`
	TargetVersion      string    `json:"target_version"`
	Architecture       string    `json:"architecture"`
	SignatureVerified  bool      `json:"signature_verified"`
	RollbackAvailable  bool      `json:"rollback_available"`
	Rolling            bool      `json:"rolling"`
	DatabaseMutation   bool      `json:"database_mutation"`
	BootstrapAvailable bool      `json:"bootstrap_available"`
	BootstrapProtocol  int       `json:"bootstrap_protocol,omitempty"`
	UploadedAt         time.Time `json:"uploaded_at"`
}

type Event struct {
	PatchID   string    `json:"patch_id,omitempty"`
	Mode      Mode      `json:"mode,omitempty"`
	Status    string    `json:"status"`
	Node      string    `json:"node,omitempty"`
	Message   string    `json:"message,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Current   int       `json:"current,omitempty"`
	Total     int       `json:"total,omitempty"`
	Source    string    `json:"source,omitempty"`
	Target    string    `json:"target,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Progress struct {
	Phase          string   `json:"phase,omitempty"`
	Current        int      `json:"current"`
	Total          int      `json:"total"`
	Percent        int      `json:"percent"`
	CompletedNodes []string `json:"completed_nodes,omitempty"`
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
	VerificationRequired       bool      `json:"verification_required,omitempty"`
	StartedAt                  time.Time `json:"started_at,omitempty"`
	UpdatedAt                  time.Time `json:"updated_at,omitempty"`
	FinishedAt                 time.Time `json:"finished_at,omitempty"`
	Events                     []Event   `json:"events,omitempty"`
	OutputTail                 []string  `json:"output_tail,omitempty"`
	Progress                   Progress  `json:"progress"`
}

type PackageStatus struct {
	Package Package `json:"package"`
	Job     *Job    `json:"job,omitempty"`
	// ClockSkew marks a record whose uploaded_at lies in the future, i.e. the node clock
	// was running ahead when the record was written. It travels with the row so the
	// console can say the time - and therefore the position - is not trustworthy.
	ClockSkew bool `json:"clock_skew,omitempty"`
	// Incompatible marks a record whose source_version does not describe this
	// cluster's release line, i.e. a package that can never be applied here
	// regardless of how often it is retried. It travels with the row for the same
	// reason ClockSkew does: the console has to know before it picks a target, and
	// it must not re-derive the answer from a second copy of the rule living in
	// JavaScript. IncompatibleReason carries the operator-facing explanation.
	Incompatible       bool   `json:"incompatible,omitempty"`
	IncompatibleReason string `json:"incompatible_reason,omitempty"`
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
	// CurrentRelease is the release line this control plane is running, written
	// in the same "<version>-<release>" form a signed package reports as its
	// source_version and the updater compares against the installed RPM release
	// (scripts/clusterguard-upgrade.sh, run_hotfix_update). It comes from the
	// binary's own build metadata rather than an rpm query so the control plane
	// needs no package database access; the hotfix builder stamps the plain
	// release, so the value survives a hotfix exactly like the RPM release does.
	// Empty disables the baseline guard, which is what a development build that
	// was never linked with release metadata should do.
	CurrentRelease string
}

// CurrentRelease reports the release line the running control plane belongs to,
// in the form signed packages and the updater both use (for example "2.2-105").
//
// A binary that was never linked with release metadata reports the development
// sentinel (see internal/buildinfo), which names no release line, so this returns
// the empty string and the baseline guard abstains. Guessing a line here would
// turn a development build into a cluster that refuses every package.
func CurrentRelease() string {
	if buildinfo.Release == buildinfo.DevRelease {
		return ""
	}
	return buildinfo.Version + "-" + buildinfo.Release
}

type Inspector interface {
	Inspect(context.Context, string, string) (Package, error)
}

type Helper interface {
	Ready(context.Context) error
	Start(context.Context, Mode, string) error
}

type Manager struct {
	config             Config
	inspector          Inspector
	helper             Helper
	now                func() time.Time
	verifiedBootstraps map[string]struct{}
	mu                 sync.Mutex
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
	manager := &Manager{config: config, now: time.Now, verifiedBootstraps: make(map[string]struct{})}
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
	manager.mu.Lock()
	defer manager.mu.Unlock()
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
		if reconciled, reconcileErr := manager.reconcileStoredBootstrap(ctx, candidate); reconcileErr == nil {
			candidate = reconciled
		}
		status := PackageStatus{Package: candidate}
		if skew := candidate.UploadedAt.Sub(manager.now()); skew > clockSkewTolerance {
			status.ClockSkew = true
		}
		// A record can only carry a foreign baseline if it predates the upload
		// guard or was accepted while the cluster sat on another release line.
		// It is annotated rather than hidden: the operator still has to see why
		// the package is on the list but is not the one the console will run.
		if fault := packageBaselineFault(candidate.Kind, candidate.SourceVersion, candidate.TargetVersion, manager.config.CurrentRelease); fault != "" {
			status.Incompatible = true
			status.IncompatibleReason = fault
		}
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
	if err := ensureDirectoryMode(manager.config.RootDirectory, updateRootMode); err != nil {
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
	if strings.HasSuffix(strings.ToLower(filepath.Base(fileName)), PreferredPackageExtension) &&
		(!inspected.BootstrapAvailable || inspected.BootstrapProtocol != 1) {
		return Package{}, ErrBootstrapRequired
	}
	// A signature proves where a package came from, not that this cluster can
	// run it. Rejecting here is the only place that can still say no cheaply:
	// once the record exists the console has a single action slot, orders it by
	// upload time, and treats a failed job as retryable - so a package built for
	// a release line this cluster is not on would hold that slot forever.
	if fault := packageBaselineFault(inspected.Kind, inspected.SourceVersion, inspected.TargetVersion, manager.config.CurrentRelease); fault != "" {
		return Package{}, fmt.Errorf("%w：%s", ErrPackageBaselineMismatch, fault)
	}
	destinationDirectory := filepath.Join(manager.config.RootDirectory, inspected.PatchID)
	if info, statErr := os.Lstat(destinationDirectory); statErr == nil && !info.IsDir() {
		return Package{}, ErrInvalidPatch
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Package{}, statErr
	}
	if err := ensureDirectoryMode(destinationDirectory, updateJobDirMode); err != nil {
		return Package{}, fmt.Errorf("create patch directory: %w", err)
	}
	inspected.FileName = filepath.Base(fileName)
	inspected.SizeBytes = written
	inspected.SHA256 = hex.EncodeToString(hash.Sum(nil))
	inspected.UploadedAt = manager.now().UTC()
	if existing, found := manager.Package(inspected.PatchID); found {
		if existing.SHA256 == inspected.SHA256 && existing.TargetVersion == inspected.TargetVersion && existing.SourceVersion == inspected.SourceVersion {
			if !existing.ArtifactsPruned {
				return existing, nil
			}
			inspected.UploadedAt = existing.UploadedAt
		} else {
			return Package{}, ErrPackageConflict
		}
	}
	if err := os.Chmod(temporaryPath, updateFileMode); err != nil {
		return Package{}, fmt.Errorf("publish software update package permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(destinationDirectory, patchFileName)); err != nil {
		return Package{}, fmt.Errorf("publish software update package: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(destinationDirectory, packageFileName), inspected); err != nil {
		_ = os.Remove(filepath.Join(destinationDirectory, patchFileName))
		return Package{}, err
	}
	if err := os.Remove(filepath.Join(destinationDirectory, "artifacts-pruned")); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	softwarePackage, found := manager.Package(patchID)
	if !found {
		return Job{}, ErrPackageNotFound
	}
	if softwarePackage.ArtifactsPruned {
		return Job{}, ErrPackagePruned
	}
	softwarePackage, err := manager.reconcileStoredBootstrap(ctx, softwarePackage)
	if err != nil {
		return Job{}, err
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
	if err := ensureDirectoryMode(filepath.Dir(jobPath), updateJobDirMode); err != nil {
		return Job{}, fmt.Errorf("prepare software update job directory: %w", err)
	}
	if err := writeJSONAtomic(jobPath, job); err != nil {
		return Job{}, err
	}
	if err := manager.helper.Start(ctx, mode, patchID); err != nil {
		job.Status = StatusFailed
		job.Message = err.Error()
		job.FinishedAt = manager.now().UTC()
		job.UpdatedAt = job.FinishedAt
		if !jobFileHasTerminalStatus(jobPath) {
			_ = writeJSONAtomic(jobPath, job)
		}
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
	if info, err := os.Lstat(filepath.Join(manager.config.RootDirectory, patchID, "artifacts-pruned")); err == nil && info.Mode().IsRegular() {
		result.ArtifactsPruned = true
	}
	return result, true
}

func (manager *Manager) reconcileStoredBootstrap(ctx context.Context, softwarePackage Package) (Package, error) {
	if softwarePackage.ArtifactsPruned {
		return softwarePackage, nil
	}
	if !strings.HasSuffix(strings.ToLower(filepath.Base(softwarePackage.FileName)), PreferredPackageExtension) {
		return softwarePackage, nil
	}
	if softwarePackage.BootstrapAvailable && softwarePackage.BootstrapProtocol == 1 {
		return softwarePackage, nil
	}
	cacheKey := softwarePackage.PatchID + "\x00" + softwarePackage.SHA256
	if _, verified := manager.verifiedBootstraps[cacheKey]; verified {
		softwarePackage.BootstrapAvailable = true
		softwarePackage.BootstrapProtocol = 1
		return softwarePackage, nil
	}
	patchPath := filepath.Join(manager.config.RootDirectory, softwarePackage.PatchID, patchFileName)
	inspected, err := manager.inspector.Inspect(ctx, patchPath, manager.config.TrustKeyPath)
	if err != nil || inspected.PatchID != softwarePackage.PatchID ||
		inspected.SourceVersion != softwarePackage.SourceVersion || inspected.TargetVersion != softwarePackage.TargetVersion ||
		inspected.Architecture != softwarePackage.Architecture || !inspected.SignatureVerified ||
		!inspected.RollbackAvailable || !inspected.Rolling || inspected.DatabaseMutation ||
		!inspected.BootstrapAvailable || inspected.BootstrapProtocol != 1 {
		return softwarePackage, ErrBootstrapRequired
	}
	softwarePackage.BootstrapAvailable = true
	softwarePackage.BootstrapProtocol = inspected.BootstrapProtocol
	manager.verifiedBootstraps[cacheKey] = struct{}{}
	// Root owns completed update history so the unprivileged API cannot forge it.
	// Persist when the upload directory is still API-owned, otherwise retain the
	// verified contract in memory and re-verify it once after a process restart.
	_ = writeJSONAtomic(filepath.Join(manager.config.RootDirectory, softwarePackage.PatchID, packageFileName), softwarePackage)
	return softwarePackage, nil
}

func (manager *Manager) Job(patchID string) (Job, bool) {
	if !validPatchID(patchID) {
		return Job{}, false
	}
	directory := filepath.Join(manager.config.RootDirectory, patchID)
	jobPath := filepath.Join(directory, jobFileName)
	result := Job{}
	readErr := readJSONFile(jobPath, &result)
	if readErr == nil && result.PatchID != patchID {
		readErr = errors.New("任务记录中的升级包 ID 与目录不一致")
	}
	if readErr != nil {
		info, statErr := os.Lstat(jobPath)
		if errors.Is(statErr, os.ErrNotExist) {
			return Job{}, false
		}
		updatedAt := manager.now().UTC()
		if statErr == nil {
			updatedAt = info.ModTime().UTC()
		}
		message := "升级任务状态存在但不可读取或解析，操作结果需要验证"
		if detail := strings.TrimSpace(readErr.Error()); detail != "" {
			if len(detail) > 256 {
				detail = detail[:256]
			}
			message += "：" + detail
		}
		result = Job{
			PatchID: patchID, Mode: ModeExecute, Status: StatusFailed, Message: message,
			Warning: AutomaticFailoverWarning, VerificationRequired: true,
			AutomaticFailoverAvailable: false, UpdatedAt: updatedAt,
		}
		deriveJobProgress(&result)
		return result, true
	}
	result.OutputTail = readTail(filepath.Join(directory, outputFileName), maximumTailLines)
	eventsPath := filepath.Join(directory, eventsFileName)
	if _, err := os.Stat(eventsPath); errors.Is(err, os.ErrNotExist) {
		eventsPath = filepath.Join(directory, "clusterguard-update-"+patchID+".events.jsonl")
	}
	result.Events = readEvents(eventsPath, maximumTailLines)
	deriveJobProgress(&result)
	return result, true
}

func deriveJobProgress(job *Job) {
	if job == nil {
		return
	}
	progress := job.Progress
	completed := append([]string(nil), progress.CompletedNodes...)
	seen := make(map[string]struct{})
	for _, node := range completed {
		seen[node] = struct{}{}
	}
	for _, event := range job.Events {
		if strings.TrimSpace(event.Node) != "" {
			job.Node = event.Node
		}
		if event.Total > progress.Total {
			progress.Total = event.Total
		}
		if event.Current > progress.Current {
			progress.Current = event.Current
		}
		if strings.TrimSpace(event.Phase) != "" {
			progress.Phase = event.Phase
		}
		if event.Status == "verified" && strings.TrimSpace(event.Node) != "" {
			if _, found := seen[event.Node]; !found {
				seen[event.Node] = struct{}{}
				completed = append(completed, event.Node)
			}
		}
	}
	if len(job.Events) > 0 {
		latest := job.Events[len(job.Events)-1]
		if !latest.UpdatedAt.IsZero() && (job.UpdatedAt.IsZero() || latest.UpdatedAt.After(job.UpdatedAt)) {
			job.UpdatedAt = latest.UpdatedAt
			if latest.Mode != "" {
				job.Mode = latest.Mode
			}
			if status, found := jobStatusForEvent(latest.Status); found {
				job.Status = status
				job.MaintenanceActive = status == StatusRunning || status == StatusQueued || status == StatusFailed
				job.AutomaticFailoverAvailable = !job.MaintenanceActive
			}
		}
	}
	progress.CompletedNodes = completed
	if progress.Current < len(completed) {
		progress.Current = len(completed)
	}
	if progress.Total > 0 && progress.Current > progress.Total {
		progress.Current = progress.Total
	}
	if progress.Phase == "" {
		progress.Phase = progressPhaseForStatus(job.Status)
	}
	progress.Percent = progressPercent(job.Status, progress)
	job.Progress = progress
}

func jobStatusForEvent(status string) (Status, bool) {
	switch status {
	case "running", "updating", "verified", "finalizing", "rolling_back", "rollback_verified":
		return StatusRunning, true
	case "succeeded":
		return StatusSucceeded, true
	case "rolled_back":
		return StatusRolledBack, true
	case "failed", "rollback_failed", "rollback_lock_release_failed":
		return StatusFailed, true
	default:
		return "", false
	}
}

func progressPhaseForStatus(status Status) string {
	switch status {
	case StatusQueued:
		return "queued"
	case StatusRunning:
		return "preparing"
	case StatusSucceeded:
		return "completed"
	case StatusRolledBack:
		return "rolled_back"
	case StatusFailed:
		return "failed"
	default:
		return string(status)
	}
}

func progressPercent(status Status, progress Progress) int {
	switch status {
	case StatusQueued:
		return 3
	case StatusSucceeded, StatusRolledBack:
		return 100
	}
	switch progress.Phase {
	case "queued":
		return 3
	case "preparing", "locking":
		return 8
	case "updating":
		if progress.Total > 0 {
			return 15 + (progress.Current * 70 / progress.Total)
		}
		return 15
	case "verifying", "finalizing":
		return 92
	case "completed", "rolled_back":
		return 100
	case "rollback":
		return 85
	}
	if status == StatusFailed {
		if progress.Total > 0 {
			return 15 + (progress.Current * 70 / progress.Total)
		}
		return 15
	}
	return 0
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
	if err = temporary.Chmod(updateFileMode); err != nil {
		_ = temporary.Close()
		return err
	}
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

func ensureDirectoryMode(path string, mode os.FileMode) error {
	return ensureDirectoryModeWithChmod(path, mode, os.Chmod)
}

func ensureDirectoryModeWithChmod(path string, mode os.FileMode, chmod func(string, os.FileMode) error) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidPatch
	}
	if info.Mode().Perm() == mode.Perm() {
		return nil
	}
	return chmod(path, mode)
}

func jobFileHasTerminalStatus(path string) bool {
	job := Job{}
	if err := readJSONFile(path, &job); err != nil {
		return false
	}
	switch job.Status {
	case StatusPlanned, StatusSucceeded, StatusFailed, StatusRolledBack:
		return true
	default:
		return false
	}
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
