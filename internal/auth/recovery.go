package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

const (
	DefaultAdminRecoveryFile     = "/var/lib/clusterguard/admin-recovery.json"
	DefaultBootstrapPasswordFile = "/var/lib/clusterguard/bootstrap-admin-password"
	adminRecoveryVersion         = 1
	adminRecoveryMaximumAge      = 24 * time.Hour
	adminRecoveryFutureSkew      = 5 * time.Minute
	adminRecoveryMaxBytes        = 64 << 10
	bootstrapPasswordMinBytes    = 12
	bootstrapPasswordMaxBytes    = 4096
	temporaryPasswordBytes       = 24
)

// requirePrivateParentDirectory guards the directory that holds a root-only
// credential. The file itself can only be trusted when nobody but its owner can
// put something in the directory in its place: any other writable directory
// lets a second account swap or redirect the artifact, which nothing checked on
// the file afterwards can undo. A world-writable directory that also carries the
// sticky bit (a shared temporary directory such as /tmp at mode 1777) still
// stops one account from replacing another's entries, so it is allowed.
//
// The owner is deliberately not compared with the current uid: that needs
// platform-specific stat structures and this package has to stay portable.
func requirePrivateParentDirectory(path string) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("credential directory %s must not be a symbolic link", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("credential parent %s is not a directory", directory)
	}
	permission := info.Mode().Perm()
	if permission&0o020 != 0 {
		return fmt.Errorf("credential directory %s must not be group-writable (mode %04o)", directory, permission)
	}
	if permission&0o002 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("credential directory %s must not be world-writable (mode %04o)", directory, permission)
	}
	return nil
}

// readPrivateCredential reads a root-only credential without ever following a
// symbolic link. The parent directory is vetted first, the path is inspected
// with Lstat so a link is rejected up front, and the descriptor is then
// re-checked with fstat: if anything replaced the path between those two steps
// the identity no longer matches and the opened file is refused. Reading through
// the descriptor also removes the window in which a swapped file would be read.
func readPrivateCredential(kind, path string, minimumSize, maximumSize int64) ([]byte, error) {
	if err := requirePrivateParentDirectory(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a private regular file with mode 0600", kind)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < minimumSize || info.Size() > maximumSize {
		return nil, fmt.Errorf("%s must be a private regular file with mode 0600", kind)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("%s was replaced while it was being opened", kind)
	}
	if !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%s must be a private regular file with mode 0600", kind)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	if int64(len(contents)) > maximumSize {
		return nil, fmt.Errorf("%s must be a private regular file with mode 0600", kind)
	}
	return contents, nil
}

// ReadOrCreateBootstrapPassword creates the initial administrator credential in
// a root-only file. It deliberately stores the plaintext only until the first
// password change; the platform metadata always stores an Argon2id hash.
func ReadOrCreateBootstrapPassword(path string, random io.Reader) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("bootstrap administrator password path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create bootstrap administrator password directory: %w", err)
	}
	read := func() (string, error) {
		contents, err := readPrivateCredential(
			"bootstrap administrator password file", path, bootstrapPasswordMinBytes, bootstrapPasswordMaxBytes,
		)
		if err != nil {
			return "", err
		}
		password := strings.TrimSpace(string(contents))
		if err := ValidateNewPassword(password); err != nil {
			return "", fmt.Errorf("bootstrap administrator password file is invalid: %w", err)
		}
		return password, nil
	}
	if password, err := read(); err == nil {
		return password, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	password, err := GenerateTemporaryPassword(random)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return read()
		}
		return "", fmt.Errorf("create bootstrap administrator password file: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, password+"\n"); err != nil {
		return "", fmt.Errorf("write bootstrap administrator password: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync bootstrap administrator password: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close bootstrap administrator password: %w", err)
	}
	remove = false
	return password, nil
}

func RemoveBootstrapPassword(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove bootstrap administrator password file: %w", err)
	}
	return nil
}

type AdminRecoveryArtifact struct {
	Version      int              `json:"version"`
	RecoveryID   model.ResourceID `json:"recovery_id"`
	Username     string           `json:"username"`
	PasswordHash string           `json:"password_hash"`
	CreatedAt    time.Time        `json:"created_at"`
}

func GenerateTemporaryPassword(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	secret := make([]byte, temporaryPasswordBytes)
	if _, err := io.ReadFull(random, secret); err != nil {
		return "", fmt.Errorf("generate temporary password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

func NewAdminRecoveryArtifact(password string, hasher PasswordHasher, now func() time.Time) (AdminRecoveryArtifact, error) {
	if err := ValidateNewPassword(password); err != nil {
		return AdminRecoveryArtifact{}, err
	}
	if hasher == nil {
		return AdminRecoveryArtifact{}, fmt.Errorf("password hasher is required")
	}
	if now == nil {
		now = time.Now
	}
	passwordHash, err := hasher.Hash(password)
	if err != nil {
		return AdminRecoveryArtifact{}, fmt.Errorf("hash recovery password: %w", err)
	}
	artifact := AdminRecoveryArtifact{
		Version: adminRecoveryVersion, RecoveryID: model.NewResourceID(),
		Username: DefaultAdminUsername, PasswordHash: passwordHash, CreatedAt: now().UTC(),
	}
	if err := artifact.Validate(artifact.CreatedAt); err != nil {
		return AdminRecoveryArtifact{}, err
	}
	return artifact, nil
}

func (artifact AdminRecoveryArtifact) Validate(now time.Time) error {
	if artifact.Version != adminRecoveryVersion || !model.ValidResourceID(artifact.RecoveryID) ||
		strings.ToLower(strings.TrimSpace(artifact.Username)) != DefaultAdminUsername || artifact.CreatedAt.IsZero() {
		return fmt.Errorf("administrator recovery artifact identity is invalid")
	}
	if _, _, _, ok := parseArgon2Hash(strings.TrimSpace(artifact.PasswordHash)); !ok {
		return fmt.Errorf("administrator recovery password hash is invalid")
	}
	now = now.UTC()
	createdAt := artifact.CreatedAt.UTC()
	if createdAt.After(now.Add(adminRecoveryFutureSkew)) || now.Sub(createdAt) > adminRecoveryMaximumAge {
		return fmt.Errorf("administrator recovery artifact is outside its validity window")
	}
	return nil
}

func WriteAdminRecoveryArtifact(path string, artifact AdminRecoveryArtifact) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("administrator recovery artifact path is required")
	}
	if err := artifact.Validate(artifact.CreatedAt.UTC()); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("encode administrator recovery artifact: %w", err)
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("administrator recovery artifact already exists")
		}
		return fmt.Errorf("create administrator recovery artifact: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write administrator recovery artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync administrator recovery artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close administrator recovery artifact: %w", err)
	}
	remove = false
	return nil
}

func ReadAdminRecoveryArtifact(path string, now time.Time) (AdminRecoveryArtifact, error) {
	contents, err := readPrivateCredential("administrator recovery artifact", path, 1, adminRecoveryMaxBytes)
	if err != nil {
		return AdminRecoveryArtifact{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	artifact := AdminRecoveryArtifact{}
	if err := decoder.Decode(&artifact); err != nil {
		return AdminRecoveryArtifact{}, fmt.Errorf("decode administrator recovery artifact: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return AdminRecoveryArtifact{}, fmt.Errorf("administrator recovery artifact contains multiple JSON values")
	}
	if err := artifact.Validate(now); err != nil {
		return AdminRecoveryArtifact{}, err
	}
	return artifact, nil
}

func (service *Service) ApplyAdminRecoveryArtifact(ctx context.Context, artifact AdminRecoveryArtifact) (model.PlatformUser, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.PlatformUser{}, false, err
	}
	if !service.configured() {
		return model.PlatformUser{}, false, fmt.Errorf("authentication service is not configured")
	}
	now := service.now().UTC()
	if err := artifact.Validate(now); err != nil {
		return model.PlatformUser{}, false, err
	}
	return service.store.ApplyPlatformAdminRecovery(store.PlatformAdminRecoveryRequest{
		RecoveryID: artifact.RecoveryID, Username: artifact.Username,
		PasswordHash: artifact.PasswordHash, ArtifactCreatedAt: artifact.CreatedAt, RecoveredAt: now,
	})
}
