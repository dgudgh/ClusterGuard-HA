package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
	"clusterguard.io/ha/pkg/model"
)

const (
	sessionCookieName = "clusterguard_session"
	csrfCookieName    = "clusterguard_csrf"
)

type requestAuthenticationState struct {
	principal    platformauth.Principal
	sessionToken string
	viaSession   bool
}

type requestAuthenticationKey struct{}

func withRequestAuthentication(request *http.Request, state requestAuthenticationState) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), requestAuthenticationKey{}, state))
}

func requestAuthentication(request *http.Request) (requestAuthenticationState, bool) {
	state, found := request.Context().Value(requestAuthenticationKey{}).(requestAuthenticationState)
	return state, found
}

type loginPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordChangePayload struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func requestIsSecure(request *http.Request) bool {
	return request != nil && request.TLS != nil
}

func sessionCookie(token string, expires time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

func csrfCookie(token string, expires time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name: csrfCookieName, Value: token, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

func clearAuthenticationCookies(writer http.ResponseWriter, secure bool) {
	expired := time.Unix(1, 0).UTC()
	for _, cookie := range []*http.Cookie{
		{Name: sessionCookieName, Path: "/", Expires: expired, MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode},
		{Name: csrfCookieName, Path: "/", Expires: expired, MaxAge: -1, Secure: secure, SameSite: http.SameSiteStrictMode},
	} {
		http.SetCookie(writer, cookie)
	}
}

func (server *Server) recordSecurityEvent(principal platformauth.Principal, username, kind, outcome, message string) {
	if server.store == nil {
		return
	}
	_ = server.store.RecordSecurityEvent(model.SecurityEvent{
		ResourceMeta: model.ResourceMeta{
			ResourceID:       model.NewResourceID(),
			MetadataRevision: 1,
			CreatedAt:        time.Now().UTC(),
			UpdatedAt:        time.Now().UTC(),
		},
		UserID: principal.UserID, Username: strings.ToLower(strings.TrimSpace(username)),
		Kind: kind, Outcome: outcome, Message: message,
	})
}

func (server *Server) authRoute(writer http.ResponseWriter, request *http.Request, path string) {
	if server.authentication == nil {
		writeError(writer, http.StatusServiceUnavailable, "platform authentication is not configured")
		return
	}
	switch path {
	case "/api/v1/auth/login":
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !server.authorizeMutation(writer, request) {
			return
		}
		payload := loginPayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid login request")
			return
		}
		result, err := server.authentication.Login(request.Context(), payload.Username, payload.Password)
		if err != nil {
			server.recordSecurityEvent(platformauth.Principal{}, payload.Username, "login_failed", "failure", "platform login failed")
			writeError(writer, http.StatusUnauthorized, platformauth.ErrInvalidCredentials.Error())
			return
		}
		server.recordSecurityEvent(result.Principal, result.Principal.Username, "login_success", "success", "platform login succeeded")
		http.SetCookie(writer, sessionCookie(result.SessionToken, result.ExpiresAt, requestIsSecure(request)))
		http.SetCookie(writer, csrfCookie(result.CSRFToken, result.ExpiresAt, requestIsSecure(request)))
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{
			"user": result.Principal, "expires_at": result.ExpiresAt,
		}})
		return
	case "/api/v1/auth/me":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		state, ok := server.requireSession(writer, request, false)
		if !ok {
			return
		}
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"user": state.principal}})
		return
	case "/api/v1/auth/logout":
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !server.authorizeMutation(writer, request) {
			return
		}
		state, ok := server.requireSession(writer, request, true)
		if !ok {
			return
		}
		if err := server.authentication.Logout(request.Context(), state.sessionToken); err != nil {
			clearAuthenticationCookies(writer, requestIsSecure(request))
			writeError(writer, http.StatusUnauthorized, platformauth.ErrUnauthenticated.Error())
			return
		}
		server.recordSecurityEvent(state.principal, state.principal.Username, "logout", "success", "platform session logged out")
		clearAuthenticationCookies(writer, requestIsSecure(request))
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok"})
		return
	case "/api/v1/auth/password":
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !server.authorizeMutation(writer, request) {
			return
		}
		state, ok := server.requireSession(writer, request, true)
		if !ok {
			return
		}
		payload := passwordChangePayload{}
		if err := decode(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid password change request")
			return
		}
		changed, err := server.authentication.ChangePassword(request.Context(), state.sessionToken, payload.CurrentPassword, payload.NewPassword)
		if err != nil {
			switch {
			case errors.Is(err, platformauth.ErrInvalidCredentials):
				writeError(writer, http.StatusUnauthorized, "current password is invalid")
			case errors.Is(err, platformauth.ErrPasswordPolicy):
				writeError(writer, http.StatusBadRequest, platformauth.ErrPasswordPolicy.Error())
			default:
				writeError(writer, http.StatusConflict, "password change failed")
			}
			return
		}
		server.recordSecurityEvent(state.principal, changed.Username, "password_changed", "success", "platform password changed")
		clearAuthenticationCookies(writer, requestIsSecure(request))
		writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": map[string]interface{}{"password_changed": true}})
		return
	default:
		writeError(writer, http.StatusNotFound, "authentication route not found")
	}
}

func (server *Server) requireSession(writer http.ResponseWriter, request *http.Request, csrfRequired bool) (requestAuthenticationState, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		clearAuthenticationCookies(writer, requestIsSecure(request))
		writer.Header().Set("WWW-Authenticate", `Session realm="clusterguard-platform"`)
		writeError(writer, http.StatusUnauthorized, platformauth.ErrUnauthenticated.Error())
		return requestAuthenticationState{}, false
	}
	principal, err := server.authentication.Authenticate(request.Context(), cookie.Value)
	if err != nil {
		clearAuthenticationCookies(writer, requestIsSecure(request))
		writer.Header().Set("WWW-Authenticate", `Session realm="clusterguard-platform"`)
		writeError(writer, http.StatusUnauthorized, platformauth.ErrUnauthenticated.Error())
		return requestAuthenticationState{}, false
	}
	state := requestAuthenticationState{principal: principal, sessionToken: cookie.Value, viaSession: true}
	if csrfRequired && !server.validateRequestCSRF(request, principal) {
		writeError(writer, http.StatusForbidden, platformauth.ErrInvalidCSRF.Error())
		return requestAuthenticationState{}, false
	}
	return state, true
}

func (server *Server) validateRequestCSRF(request *http.Request, principal platformauth.Principal) bool {
	header := strings.TrimSpace(request.Header.Get("X-CSRF-Token"))
	cookie, err := request.Cookie(csrfCookieName)
	if err != nil || header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
		return false
	}
	return server.authentication.ValidateCSRF(request.Context(), principal, header) == nil
}

func (server *Server) authenticatePlatformRequest(writer http.ResponseWriter, request *http.Request) (*http.Request, bool) {
	if cookie, err := request.Cookie(sessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
		principal, authErr := server.authentication.Authenticate(request.Context(), cookie.Value)
		if authErr == nil {
			if principal.MustChangePassword {
				writeJSON(writer, http.StatusForbidden, map[string]interface{}{
					"status": "blocked", "message": "password change is required",
					"password_change_required": true,
				})
				return nil, false
			}
			return withRequestAuthentication(request, requestAuthenticationState{
				principal: principal, sessionToken: cookie.Value, viaSession: true,
			}), true
		}
	}
	if server.validControlBearer(request) {
		return withRequestAuthentication(request, requestAuthenticationState{
			principal: platformauth.Principal{Username: "service-api", DisplayName: "Service API", Role: model.PlatformRoleAdmin},
		}), true
	}
	clearAuthenticationCookies(writer, requestIsSecure(request))
	writer.Header().Set("WWW-Authenticate", `Session realm="clusterguard-platform"`)
	writeError(writer, http.StatusUnauthorized, platformauth.ErrUnauthenticated.Error())
	return nil, false
}

func (server *Server) authorizeSessionMutation(writer http.ResponseWriter, request *http.Request, principal platformauth.Principal, path string) bool {
	if principal.MustChangePassword {
		writeJSON(writer, http.StatusForbidden, map[string]interface{}{
			"status": "blocked", "message": "password change is required",
			"password_change_required": true,
		})
		return false
	}
	allowed := principal.Role == model.PlatformRoleAdmin
	if principal.Role == model.PlatformRoleOperator {
		allowed = strings.HasPrefix(path, "/api/v1/operations") ||
			(strings.HasPrefix(path, "/api/v1/clusters/") && strings.HasSuffix(path, "/discover"))
	}
	if !allowed {
		server.recordSecurityEvent(principal, principal.Username, "authorization_denied", "failure", "platform mutation was denied")
		writeError(writer, http.StatusForbidden, "insufficient platform role")
		return false
	}
	if !server.validateRequestCSRF(request, principal) {
		writeError(writer, http.StatusForbidden, platformauth.ErrInvalidCSRF.Error())
		return false
	}
	return true
}
