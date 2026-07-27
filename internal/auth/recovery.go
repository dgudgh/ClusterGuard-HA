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
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

const (
	DefaultAdminRecoveryFile = "/var/lib/clusterguard/admin-recovery.json"
	adminRecoveryVersion     = 1
	adminRecoveryMaximumAge  = 24 * time.Hour
	adminRecoveryFutureSkew  = 5 * time.Minute
	adminRecoveryMaxBytes    = 64 << 10
	temporaryPasswordBytes   = 24
)

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
	info, err := os.Stat(path)
	if err != nil {
		return AdminRecoveryArtifact{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > adminRecoveryMaxBytes {
		return AdminRecoveryArtifact{}, fmt.Errorf("administrator recovery artifact must be a private regular file with mode 0600")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return AdminRecoveryArtifact{}, fmt.Errorf("read administrator recovery artifact: %w", err)
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
