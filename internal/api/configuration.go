package api

import (
	"context"
	"net/http"
	"time"
)

// ConfigurationValue is one effective parameter exactly as the console should
// show it. It carries no secret material: a credential is reported as the name
// of the environment variable that holds it, never as its value.
type ConfigurationValue struct {
	Key             string `json:"key"`
	Label           string `json:"label"`
	Value           string `json:"value"`
	Source          string `json:"source"`
	RestartRequired bool   `json:"restart_required"`
	CredentialRef   bool   `json:"credential_ref,omitempty"`
	Note            string `json:"note,omitempty"`
}

// ConfigurationValue sources. A value is "file" when the key is present in the
// configuration file - even when it repeats the platform default - and
// "default" when the platform supplied it because the key was absent.
const (
	ConfigurationSourceFile    = "file"
	ConfigurationSourceDefault = "default"
	// ConfigurationSourcePolicy marks a value that came from the replicated
	// cluster policy. It is neither node-local nor a platform default: it is
	// cluster-wide, changeable from the console, audited, and applied by the
	// next round without a restart.
	ConfigurationSourcePolicy = "policy"
)

type ConfigurationSection struct {
	Key    string               `json:"key"`
	Label  string               `json:"label"`
	Note   string               `json:"note,omitempty"`
	Values []ConfigurationValue `json:"values"`
}

// ConfigurationView answers "what is actually in effect on this node, and where
// did each value come from". It states ReloadSupported explicitly because the
// platform reads its configuration once at start-up: without that flag a console
// "refresh" button would imply a reload that does not exist.
type ConfigurationView struct {
	Path             string                 `json:"path"`
	FilePresent      bool                   `json:"file_present"`
	FileModifiedAt   time.Time              `json:"file_modified_at,omitempty"`
	ProcessStartedAt time.Time              `json:"process_started_at"`
	ReloadSupported  bool                   `json:"reload_supported"`
	ReloadNote       string                 `json:"reload_note"`
	Sections         []ConfigurationSection `json:"sections"`
	Warnings         []string               `json:"warnings,omitempty"`
}

// ConfigurationProvider renders the effective configuration of one node.
type ConfigurationProvider interface {
	Configuration(context.Context) (ConfigurationView, error)
}

type ConfigurationProviderFunc func(context.Context) (ConfigurationView, error)

func (provider ConfigurationProviderFunc) Configuration(ctx context.Context) (ConfigurationView, error) {
	return provider(ctx)
}

func (server *Server) configurationRoute(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if server.configuration == nil {
		writeError(writer, http.StatusServiceUnavailable, "configuration view is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	view, err := server.configuration.Configuration(ctx)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "configuration view is temporarily unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]interface{}{"status": "ok", "result": view})
}
