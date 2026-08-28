package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestAdminRecoveryArtifactContainsOnlyHashAndRequiresPrivateFile(t *testing.T) {
	now := time.Date(2026, time.July, 17, 2, 30, 0, 0, time.UTC)
	password := "Recovery-temporary-password-123"
	artifact, err := NewAdminRecoveryArtifact(
		password, testArgon2Hasher(), func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	if !model.ValidResourceID(artifact.RecoveryID) || artifact.Username != DefaultAdminUsername ||
		artifact.PasswordHash == "" || strings.Contains(artifact.PasswordHash, password) {
		t.Fatalf("unsafe recovery artifact=%+v", artifact)
	}

	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := WriteAdminRecoveryArtifact(path, artifact); err != nil {
		t.Fatalf("write recovery artifact: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovery artifact: %v", err)
	}
	if bytes.Contains(contents, []byte(password)) {
		t.Fatal("recovery artifact contains plaintext password")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery artifact mode=%v err=%v", info.Mode().Perm(), err)
	}
	loaded, err := ReadAdminRecoveryArtifact(path, now.Add(time.Minute))
	if err != nil || loaded.RecoveryID != artifact.RecoveryID || loaded.PasswordHash != artifact.PasswordHash {
		t.Fatalf("loaded recovery artifact=%+v err=%v", loaded, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("weaken recovery artifact permissions: %v", err)
	}
	if _, err := ReadAdminRecoveryArtifact(path, now.Add(time.Minute)); err == nil {
		t.Fatal("world-readable recovery artifact was accepted")
	}
}

func TestApplyAdminRecoveryArtifactRevokesSessionsAndForcesChange(t *testing.T) {
	service, repository, now := newTestService(t)
	user, err := service.EnsureBootstrapAdmin(context.Background(), testBootstrapPassword)
	if err != nil {
		t.Fatalf("ensure bootstrap admin: %v", err)
	}
	login, err := service.Login(context.Background(), user.Username, testBootstrapPassword)
	if err != nil {
		t.Fatalf("login bootstrap admin: %v", err)
	}
	changed, err := service.ChangePassword(
		context.Background(), login.SessionToken, testBootstrapPassword, "Original-secure-password-123",
	)
	if err != nil {
		t.Fatalf("change bootstrap password: %v", err)
	}
	active, err := service.Login(context.Background(), changed.Username, "Original-secure-password-123")
	if err != nil {
		t.Fatalf("login changed admin: %v", err)
	}

	password := "Recovery-temporary-password-123"
	artifact, err := NewAdminRecoveryArtifact(password, testArgon2Hasher(), func() time.Time { return *now })
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	recovered, applied, err := service.ApplyAdminRecoveryArtifact(context.Background(), artifact)
	if err != nil || !applied || !recovered.MustChangePassword || recovered.AuthRevision != changed.AuthRevision+1 {
		t.Fatalf("recover administrator user=%+v applied=%t err=%v", recovered, applied, err)
	}
	if _, err := service.Authenticate(context.Background(), active.SessionToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("pre-recovery session survived: %v", err)
	}
	if _, err := service.Login(context.Background(), recovered.Username, "Original-secure-password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	recoveryLogin, err := service.Login(context.Background(), recovered.Username, password)
	if err != nil || !recoveryLogin.Principal.MustChangePassword {
		t.Fatalf("recovery login=%+v err=%v", recoveryLogin, err)
	}
	if events := repository.SecurityEvents(); len(events) != 1 || events[0].ResourceID != artifact.RecoveryID {
		t.Fatalf("recovery security events=%+v", events)
	}
}

func TestAdminRecoveryArtifactRejectsWeakPasswordAndStaleArtifact(t *testing.T) {
	now := time.Date(2026, time.July, 17, 2, 30, 0, 0, time.UTC)
	if _, err := NewAdminRecoveryArtifact("short", testArgon2Hasher(), func() time.Time { return now }); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("weak recovery password error=%v", err)
	}
	artifact, err := NewAdminRecoveryArtifact("Recovery-temporary-password-123", testArgon2Hasher(), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new recovery artifact: %v", err)
	}
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := WriteAdminRecoveryArtifact(path, artifact); err != nil {
		t.Fatalf("write recovery artifact: %v", err)
	}
	if _, err := ReadAdminRecoveryArtifact(path, now.Add(25*time.Hour)); err == nil {
		t.Fatal("stale recovery artifact was accepted")
	}
}

func TestBootstrapPasswordArtifactIsPrivateIdempotentAndRemovedAfterUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "bootstrap-password")
	first, err := ReadOrCreateBootstrapPassword(path, bytes.NewReader(bytes.Repeat([]byte{0x6e}, 128)))
	if err != nil {
		t.Fatalf("create bootstrap password: %v", err)
	}
	if err := ValidateNewPassword(first); err != nil {
		t.Fatalf("generated bootstrap password rejected: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bootstrap password mode=%v err=%v", info.Mode().Perm(), err)
	}
	second, err := ReadOrCreateBootstrapPassword(path, bytes.NewReader(bytes.Repeat([]byte{0x2a}, 128)))
	if err != nil || second != first {
		t.Fatalf("bootstrap password was not idempotent: first=%q second=%q err=%v", first, second, err)
	}
	if err := RemoveBootstrapPassword(path); err != nil {
		t.Fatalf("remove bootstrap password: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap password was not removed: %v", err)
	}
}

func TestBootstrapPasswordArtifactRejectsUnsafePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(path, []byte(testBootstrapPassword+"\n"), 0o644); err != nil {
		t.Fatalf("write unsafe bootstrap artifact: %v", err)
	}
	if _, err := ReadOrCreateBootstrapPassword(path, nil); err == nil {
		t.Fatal("world-readable bootstrap password was accepted")
	}
}
