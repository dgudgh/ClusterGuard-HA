package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/store"
	"clusterguard.io/ha/pkg/adapter"
)

func configurationViewForTest() ConfigurationView {
	return ConfigurationView{
		Path:             "/etc/clusterguard/clusterguard.json",
		FilePresent:      true,
		FileModifiedAt:   time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC),
		ProcessStartedAt: time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC),
		ReloadSupported:  false,
		ReloadNote:       "本节点配置仅在进程启动时读取一次。",
		Sections: []ConfigurationSection{{
			Key: "engine:mysql", Label: "引擎：MySQL",
			Values: []ConfigurationValue{{
				Key: "automatic_failover_minimum_observations", Label: "故障证据观测次数",
				Value: "4", Source: ConfigurationSourceDefault, RestartRequired: true,
			}, {
				Key: "discovery.password_env", Label: "发现凭据", Value: "环境变量 CG_MYSQL_DISCOVERY_PASSWORD",
				Source: ConfigurationSourceFile, RestartRequired: true, CredentialRef: true,
			}},
		}},
	}
}

func TestConfigurationRouteReturnsEffectiveValuesAndSource(t *testing.T) {
	view := configurationViewForTest()
	provider := ConfigurationProviderFunc(func(context.Context) (ConfigurationView, error) { return view, nil })
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithConfiguration(provider))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/control-plane/configuration", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("configuration status=%d body=%s", response.Code, response.Body.String())
	}
	if cache := response.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("configuration response must not be cached: %q", cache)
	}
	var envelope struct {
		Status string            `json:"status"`
		Result ConfigurationView `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode configuration: %v", err)
	}
	if envelope.Status != "ok" || envelope.Result.Path != view.Path {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	if envelope.Result.ReloadSupported {
		t.Fatal("a configuration that is read once must not advertise hot reload")
	}
	if envelope.Result.ReloadNote == "" {
		t.Fatal("the view must explain why reload is unsupported")
	}
	if len(envelope.Result.Sections) != 1 || len(envelope.Result.Sections[0].Values) != 2 {
		t.Fatalf("unexpected sections: %+v", envelope.Result.Sections)
	}
	defaulted := envelope.Result.Sections[0].Values[0]
	if defaulted.Source != ConfigurationSourceDefault || !defaulted.RestartRequired {
		t.Fatalf("a value the platform supplied must be reported as a default: %+v", defaulted)
	}
	credential := envelope.Result.Sections[0].Values[1]
	if !credential.CredentialRef || credential.Source != ConfigurationSourceFile {
		t.Fatalf("credential reference must be reported as a file-sourced reference: %+v", credential)
	}
}

func TestConfigurationRouteReportsUnavailableProviderInsteadOfInventingValues(t *testing.T) {
	server := NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/control-plane/configuration", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("configuration without a provider status=%d body=%s", response.Code, response.Body.String())
	}

	failing := ConfigurationProviderFunc(func(context.Context) (ConfigurationView, error) {
		return ConfigurationView{}, errors.New("configuration unavailable")
	})
	server = NewServer(adapter.NewRegistry(), store.NewMemory(), nil, nil, WithConfiguration(failing))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("failing configuration status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "configuration unavailable") {
		t.Fatalf("internal failure detail must not leak to the client: %s", response.Body.String())
	}
}
