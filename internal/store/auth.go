package store

import (
	"sort"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	maximumPlatformUsernameLength     = 128
	maximumPlatformDisplayNameLength  = 256
	maximumSecurityEventMessageLength = 1024
	maximumSecurityEvents             = 10000
)

type ChangePlatformPasswordRequest struct {
	UserID                   model.ResourceID
	ExpectedMetadataRevision uint64
	PasswordHash             string
	ChangedAt                time.Time
}

func normalizePlatformUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func validatePlatformUser(user model.PlatformUser) error {
	username := normalizePlatformUsername(user.Username)
	if !model.ValidResourceID(user.ResourceID) {
		return validationError("platform user ID is invalid")
	}
	if user.MetadataRevision == 0 {
		return validationError("platform user metadata revision is required")
	}
	if username == "" || len(username) > maximumPlatformUsernameLength {
		return validationError("platform username is invalid")
	}
	if len(strings.TrimSpace(user.DisplayName)) > maximumPlatformDisplayNameLength {
		return validationError("platform user display name is invalid")
	}
	if !user.Role.Valid() {
		return validationError("platform user role is invalid")
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		return validationError("platform user password hash is required")
	}
	if user.AuthRevision == 0 {
		return validationError("platform user auth revision is required")
	}
	return nil
}

func validatePlatformSession(session model.PlatformSession) error {
	if !model.ValidResourceID(session.ResourceID) || !model.ValidResourceID(session.UserID) {
		return validationError("platform session identity is invalid")
	}
	if session.MetadataRevision == 0 || session.UserAuthRevision == 0 {
		return validationError("platform session revision is required")
	}
	if strings.TrimSpace(session.TokenHash) == "" || strings.TrimSpace(session.CSRFHash) == "" {
		return validationError("platform session hashes are required")
	}
	if session.IssuedAt.IsZero() || !session.ExpiresAt.After(session.IssuedAt) {
		return validationError("platform session validity window is invalid")
	}
	if !session.RevokedAt.IsZero() && session.RevokedAt.Before(session.IssuedAt) {
		return validationError("platform session revocation is invalid")
	}
	return nil
}

func validateSecurityEvent(event model.SecurityEvent) error {
	if !model.ValidResourceID(event.ResourceID) || event.MetadataRevision == 0 {
		return validationError("security event identity is invalid")
	}
	if event.UserID != "" && !model.ValidResourceID(event.UserID) {
		return validationError("security event user identity is invalid")
	}
	if strings.TrimSpace(event.Kind) == "" || strings.TrimSpace(event.Outcome) == "" {
		return validationError("security event kind and outcome are required")
	}
	message := strings.TrimSpace(event.Message)
	if message == "" || len(message) > maximumSecurityEventMessageLength {
		return validationError("security event message is invalid")
	}
	return nil
}

func (repository *Repository) CreatePlatformUser(user model.PlatformUser) (model.PlatformUser, error) {
	user.Username = normalizePlatformUsername(user.Username)
	user.DisplayName = strings.TrimSpace(user.DisplayName)
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	now := repository.now().UTC()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = user.CreatedAt
	}
	if err := validatePlatformUser(user); err != nil {
		return model.PlatformUser{}, err
	}

	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for _, existing := range repository.snapshot.PlatformUsers {
		if normalizePlatformUsername(existing.Username) == user.Username {
			return model.PlatformUser{}, conflictError("platform username already exists")
		}
	}
	if _, found := repository.snapshot.PlatformUsers[user.ResourceID]; found {
		return model.PlatformUser{}, conflictError("platform user already exists")
	}
	next := repository.snapshot
	next.PlatformUsers = clonePlatformUserMap(repository.snapshot.PlatformUsers)
	next.PlatformUsers[user.ResourceID] = user
	if err := repository.commitSnapshotLocked(next); err != nil {
		return model.PlatformUser{}, err
	}
	return user, nil
}

func (repository *Repository) PlatformUser(resourceID model.ResourceID) (model.PlatformUser, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	user, found := repository.snapshot.PlatformUsers[resourceID]
	return user, found
}

func (repository *Repository) PlatformUserByUsername(username string) (model.PlatformUser, bool) {
	username = normalizePlatformUsername(username)
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, user := range repository.snapshot.PlatformUsers {
		if normalizePlatformUsername(user.Username) == username {
			return user, true
		}
	}
	return model.PlatformUser{}, false
}

func (repository *Repository) PlatformUsers() []model.PlatformUser {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	users := make([]model.PlatformUser, 0, len(repository.snapshot.PlatformUsers))
	for _, user := range repository.snapshot.PlatformUsers {
		users = append(users, user)
	}
	sort.Slice(users, func(left, right int) bool {
		if users[left].Username == users[right].Username {
			return users[left].ResourceID < users[right].ResourceID
		}
		return users[left].Username < users[right].Username
	})
	return users
}

func (repository *Repository) PutPlatformSession(session model.PlatformSession) error {
	if err := validatePlatformSession(session); err != nil {
		return err
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	user, found := repository.snapshot.PlatformUsers[session.UserID]
	if !found || user.AuthRevision != session.UserAuthRevision {
		return validationError("platform session user revision is invalid")
	}
	if _, found := repository.snapshot.PlatformSessions[session.ResourceID]; found {
		return conflictError("platform session already exists")
	}
	next := repository.snapshot
	next.PlatformSessions = clonePlatformSessionMap(repository.snapshot.PlatformSessions)
	next.PlatformSessions[session.ResourceID] = session
	return repository.commitSnapshotLocked(next)
}

func (repository *Repository) PlatformSession(resourceID model.ResourceID) (model.PlatformSession, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	session, found := repository.snapshot.PlatformSessions[resourceID]
	return session, found
}

func (repository *Repository) PlatformSessions(userID model.ResourceID) []model.PlatformSession {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	sessions := make([]model.PlatformSession, 0, len(repository.snapshot.PlatformSessions))
	for _, session := range repository.snapshot.PlatformSessions {
		if userID == "" || session.UserID == userID {
			sessions = append(sessions, session)
		}
	}
	sort.Slice(sessions, func(left, right int) bool {
		if sessions[left].IssuedAt.Equal(sessions[right].IssuedAt) {
			return sessions[left].ResourceID < sessions[right].ResourceID
		}
		return sessions[left].IssuedAt.Before(sessions[right].IssuedAt)
	})
	return sessions
}

func (repository *Repository) RevokePlatformSession(resourceID model.ResourceID, revokedAt time.Time) (model.PlatformSession, error) {
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	session, found := repository.snapshot.PlatformSessions[resourceID]
	if !found {
		return model.PlatformSession{}, conflictError("platform session does not exist")
	}
	if !session.RevokedAt.IsZero() {
		return session, nil
	}
	if revokedAt.IsZero() {
		revokedAt = repository.now().UTC()
	}
	if revokedAt.Before(session.IssuedAt) {
		return model.PlatformSession{}, validationError("platform session revocation is invalid")
	}
	session.RevokedAt = revokedAt.UTC()
	session.UpdatedAt = session.RevokedAt
	session.MetadataRevision++
	next := repository.snapshot
	next.PlatformSessions = clonePlatformSessionMap(repository.snapshot.PlatformSessions)
	next.PlatformSessions[resourceID] = session
	if err := repository.commitSnapshotLocked(next); err != nil {
		return model.PlatformSession{}, err
	}
	return session, nil
}

func (repository *Repository) ChangePlatformPassword(request ChangePlatformPasswordRequest) (model.PlatformUser, error) {
	if !model.ValidResourceID(request.UserID) || request.ExpectedMetadataRevision == 0 ||
		strings.TrimSpace(request.PasswordHash) == "" || request.ChangedAt.IsZero() {
		return model.PlatformUser{}, validationError("platform password change request is invalid")
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	user, found := repository.snapshot.PlatformUsers[request.UserID]
	if !found {
		return model.PlatformUser{}, conflictError("platform user does not exist")
	}
	if user.MetadataRevision != request.ExpectedMetadataRevision {
		return model.PlatformUser{}, conflictError("platform user metadata revision changed")
	}
	changedAt := request.ChangedAt.UTC()
	user.PasswordHash = strings.TrimSpace(request.PasswordHash)
	user.MustChangePassword = false
	user.AuthRevision++
	user.PasswordChangedAt = changedAt
	user.UpdatedAt = changedAt
	user.MetadataRevision++
	if err := validatePlatformUser(user); err != nil {
		return model.PlatformUser{}, err
	}

	next := repository.snapshot
	next.PlatformUsers = clonePlatformUserMap(repository.snapshot.PlatformUsers)
	next.PlatformSessions = clonePlatformSessionMap(repository.snapshot.PlatformSessions)
	next.PlatformUsers[user.ResourceID] = user
	for resourceID, session := range next.PlatformSessions {
		if session.UserID != user.ResourceID || !session.RevokedAt.IsZero() {
			continue
		}
		session.RevokedAt = changedAt
		session.UpdatedAt = changedAt
		session.MetadataRevision++
		next.PlatformSessions[resourceID] = session
	}
	if err := repository.commitSnapshotLocked(next); err != nil {
		return model.PlatformUser{}, err
	}
	return user, nil
}

func (repository *Repository) RecordSecurityEvent(event model.SecurityEvent) error {
	now := repository.now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = event.CreatedAt
	}
	event.Username = normalizePlatformUsername(event.Username)
	event.Kind = strings.TrimSpace(event.Kind)
	event.Outcome = strings.TrimSpace(event.Outcome)
	event.Message = strings.TrimSpace(event.Message)
	if err := validateSecurityEvent(event); err != nil {
		return err
	}
	repository.mutationMu.Lock()
	defer repository.mutationMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	next := repository.snapshot
	next.SecurityEvents = append([]model.SecurityEvent{}, repository.snapshot.SecurityEvents...)
	next.SecurityEvents = append(next.SecurityEvents, event)
	if len(next.SecurityEvents) > maximumSecurityEvents {
		next.SecurityEvents = append([]model.SecurityEvent{}, next.SecurityEvents[len(next.SecurityEvents)-maximumSecurityEvents:]...)
	}
	return repository.commitSnapshotLocked(next)
}

func (repository *Repository) SecurityEvents() []model.SecurityEvent {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return append([]model.SecurityEvent{}, repository.snapshot.SecurityEvents...)
}
