package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/model"
)

const (
	sessionSecretBytes = 32
	csrfSecretBytes    = 32
	sessionTokenPrefix = "cgs_"
	defaultSessionTTL  = 8 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUnauthenticated    = errors.New("authentication is required")
	ErrInvalidCSRF        = errors.New("CSRF validation failed")
)

type PasswordHasher interface {
	Hash(string) (string, error)
	Verify(string, string) bool
}

type Principal struct {
	UserID             model.ResourceID   `json:"resource_id"`
	SessionID          model.ResourceID   `json:"-"`
	Username           string             `json:"username"`
	DisplayName        string             `json:"display_name"`
	Role               model.PlatformRole `json:"role"`
	MustChangePassword bool               `json:"must_change_password"`
}

type LoginResult struct {
	Principal    Principal `json:"user"`
	SessionToken string    `json:"-"`
	CSRFToken    string    `json:"-"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Service struct {
	store      *store.Repository
	hasher     PasswordHasher
	random     io.Reader
	now        func() time.Time
	sessionTTL time.Duration
}

func New(repository *store.Repository, hasher PasswordHasher, random io.Reader, now func() time.Time, sessionTTL time.Duration) *Service {
	if random == nil {
		random = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	if sessionTTL <= 0 {
		sessionTTL = defaultSessionTTL
	}
	return &Service{store: repository, hasher: hasher, random: random, now: now, sessionTTL: sessionTTL}
}

func (service *Service) configured() bool {
	return service != nil && service.store != nil && service.hasher != nil && service.random != nil && service.now != nil && service.sessionTTL > 0
}

func (service *Service) EnsureBootstrapAdmin(ctx context.Context) (model.PlatformUser, error) {
	if err := ctx.Err(); err != nil {
		return model.PlatformUser{}, err
	}
	if !service.configured() {
		return model.PlatformUser{}, fmt.Errorf("authentication service is not configured")
	}
	if user, found := service.store.PlatformUserByUsername(DefaultAdminUsername); found {
		return user, nil
	}
	passwordHash, err := service.hasher.Hash(DefaultAdminPassword)
	if err != nil {
		return model.PlatformUser{}, fmt.Errorf("hash bootstrap administrator password: %w", err)
	}
	now := service.now().UTC()
	user := model.PlatformUser{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		Username:           DefaultAdminUsername,
		DisplayName:        "Administrator",
		Role:               model.PlatformRoleAdmin,
		PasswordHash:       passwordHash,
		MustChangePassword: true,
		AuthRevision:       1,
	}
	created, err := service.store.CreatePlatformUser(user)
	if err == nil {
		return created, nil
	}
	if errors.Is(err, store.ErrConflict) {
		if existing, found := service.store.PlatformUserByUsername(DefaultAdminUsername); found {
			return existing, nil
		}
	}
	return model.PlatformUser{}, err
}

func secretHash(secret []byte) string {
	digest := sha256.Sum256(secret)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func parseSecretHash(value string) []byte {
	return []byte(value)
}

func ParseSessionToken(token string) (model.ResourceID, []byte, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, sessionTokenPrefix) {
		return "", nil, ErrUnauthenticated
	}
	idPart, secretPart, found := strings.Cut(strings.TrimPrefix(token, sessionTokenPrefix), ".")
	sessionID := model.ResourceID(idPart)
	if !found || !model.ValidResourceID(sessionID) || secretPart == "" {
		return "", nil, ErrUnauthenticated
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretPart)
	if err != nil || len(secret) != sessionSecretBytes {
		return "", nil, ErrUnauthenticated
	}
	return sessionID, secret, nil
}

func principalFor(user model.PlatformUser, session model.PlatformSession) Principal {
	return Principal{
		UserID:             user.ResourceID,
		SessionID:          session.ResourceID,
		Username:           user.Username,
		DisplayName:        user.DisplayName,
		Role:               user.Role,
		MustChangePassword: user.MustChangePassword,
	}
}

func (service *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	if err := ctx.Err(); err != nil {
		return LoginResult{}, err
	}
	if !service.configured() {
		return LoginResult{}, fmt.Errorf("authentication service is not configured")
	}
	user, found := service.store.PlatformUserByUsername(username)
	if !found {
		if dummy, available := service.store.PlatformUserByUsername(DefaultAdminUsername); available {
			_ = service.hasher.Verify(dummy.PasswordHash, password)
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	passwordValid := service.hasher.Verify(user.PasswordHash, password)
	if user.Disabled || !passwordValid {
		return LoginResult{}, ErrInvalidCredentials
	}
	sessionSecret := make([]byte, sessionSecretBytes)
	if _, err := io.ReadFull(service.random, sessionSecret); err != nil {
		return LoginResult{}, fmt.Errorf("generate session secret: %w", err)
	}
	csrfSecret := make([]byte, csrfSecretBytes)
	if _, err := io.ReadFull(service.random, csrfSecret); err != nil {
		return LoginResult{}, fmt.Errorf("generate CSRF secret: %w", err)
	}
	now := service.now().UTC()
	session := model.PlatformSession{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
		UserID:           user.ResourceID,
		TokenHash:        secretHash(sessionSecret),
		CSRFHash:         secretHash(csrfSecret),
		UserAuthRevision: user.AuthRevision,
		IssuedAt:         now,
		ExpiresAt:        now.Add(service.sessionTTL),
	}
	if err := service.store.PutPlatformSession(session); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{
		Principal: principalFor(user, session),
		SessionToken: sessionTokenPrefix + string(session.ResourceID) + "." +
			base64.RawURLEncoding.EncodeToString(sessionSecret),
		CSRFToken: base64.RawURLEncoding.EncodeToString(csrfSecret),
		ExpiresAt: session.ExpiresAt,
	}, nil
}

func (service *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if !service.configured() {
		return Principal{}, ErrUnauthenticated
	}
	sessionID, secret, err := ParseSessionToken(token)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	session, found := service.store.PlatformSession(sessionID)
	hash := secretHash(secret)
	if !found || subtle.ConstantTimeCompare(parseSecretHash(session.TokenHash), parseSecretHash(hash)) != 1 {
		return Principal{}, ErrUnauthenticated
	}
	now := service.now().UTC()
	if !session.RevokedAt.IsZero() || !now.Before(session.ExpiresAt) {
		return Principal{}, ErrUnauthenticated
	}
	user, found := service.store.PlatformUser(session.UserID)
	if !found || user.Disabled || user.AuthRevision != session.UserAuthRevision {
		return Principal{}, ErrUnauthenticated
	}
	return principalFor(user, session), nil
}

func (service *Service) ValidateCSRF(ctx context.Context, principal Principal, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" || !model.ValidResourceID(principal.SessionID) {
		return ErrInvalidCSRF
	}
	secret, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || len(secret) != csrfSecretBytes {
		return ErrInvalidCSRF
	}
	session, found := service.store.PlatformSession(principal.SessionID)
	hash := secretHash(secret)
	if !found || subtle.ConstantTimeCompare(parseSecretHash(session.CSRFHash), parseSecretHash(hash)) != 1 {
		return ErrInvalidCSRF
	}
	return nil
}

func (service *Service) Logout(ctx context.Context, token string) error {
	principal, err := service.Authenticate(ctx, token)
	if err != nil {
		return err
	}
	_, err = service.store.RevokePlatformSession(principal.SessionID, service.now().UTC())
	return err
}

func (service *Service) ChangePassword(ctx context.Context, sessionToken, currentPassword, newPassword string) (model.PlatformUser, error) {
	principal, err := service.Authenticate(ctx, sessionToken)
	if err != nil {
		return model.PlatformUser{}, err
	}
	user, found := service.store.PlatformUser(principal.UserID)
	if !found || !service.hasher.Verify(user.PasswordHash, currentPassword) {
		return model.PlatformUser{}, ErrInvalidCredentials
	}
	if err := ValidateNewPassword(newPassword); err != nil || service.hasher.Verify(user.PasswordHash, newPassword) {
		return model.PlatformUser{}, ErrPasswordPolicy
	}
	passwordHash, err := service.hasher.Hash(newPassword)
	if err != nil {
		return model.PlatformUser{}, err
	}
	return service.store.ChangePlatformPassword(store.ChangePlatformPasswordRequest{
		UserID:                   user.ResourceID,
		ExpectedMetadataRevision: user.MetadataRevision,
		PasswordHash:             passwordHash,
		ChangedAt:                service.now().UTC(),
	})
}
