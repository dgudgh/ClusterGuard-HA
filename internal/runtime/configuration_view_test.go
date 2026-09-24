package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/internal/api"
	"clusterguard.io/ha/internal/config"
	"clusterguard.io/ha/internal/store"
)

// The value of the configuration view is not that it renders strings, but that
// it tells the truth about two things an operator cannot otherwise see: where
// each value came from, and that nothing in it is a secret. These tests pin
// exactly those two properties.

func writeConfigurationFileForTest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clusterguard.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return path
}

func configurationValueForTest(t *testing.T, view api.ConfigurationView, section, key string) api.ConfigurationValue {
	t.Helper()
	for _, entry := range view.Sections {
		if entry.Key != section {
			continue
		}
		for _, value := range entry.Values {
			if value.Key == key {
				return value
			}
		}
	}
	t.Fatalf("value %s/%s is missing from the view", section, key)
	return api.ConfigurationValue{}
}

func TestBuildConfigurationViewDistinguishesFileValuesFromPlatformDefaults(t *testing.T) {
	path := writeConfigurationFileForTest(t, `{
		"http_address": "0.0.0.0:3000",
		"metadata_path": "/var/lib/clusterguard",
		"mysql": {"enabled": true, "discovery_interval_seconds": 15}
	}`)
	configuration := config.File{
		HTTPAddress:  "0.0.0.0:3000",
		MetadataPath: "/var/lib/clusterguard",
		MySQL: config.MySQL{
			Enabled:                  true,
			DiscoveryIntervalSeconds: 15,
			DiscoveryTimeoutSeconds:  1,
		},
	}
	view := buildConfigurationView(configuration, path, time.Now().UTC(), nil)

	// Top-level keys live in the empty section; they must be answered from the
	// file just like nested ones, otherwise every top-level parameter would be
	// mislabelled as a platform default.
	if value := configurationValueForTest(t, view, "runtime", "http_address"); value.Source != api.ConfigurationSourceFile {
		t.Fatalf("http_address is present in the file: %+v", value)
	}
	if value := configurationValueForTest(t, view, "runtime", "metadata_path"); value.Source != api.ConfigurationSourceFile {
		t.Fatalf("metadata_path is present in the file: %+v", value)
	}
	if value := configurationValueForTest(t, view, "runtime", "tls_cert_file"); value.Source != api.ConfigurationSourceDefault {
		t.Fatalf("tls_cert_file is absent from the file: %+v", value)
	}
	if value := configurationValueForTest(t, view, "engine:mysql", "enabled"); value.Source != api.ConfigurationSourceFile {
		t.Fatalf("mysql.enabled is present in the file: %+v", value)
	}
	if value := configurationValueForTest(t, view, "engine:mysql", "discovery_interval_seconds"); value.Source != api.ConfigurationSourceFile {
		t.Fatalf("discovery_interval_seconds is present in the file: %+v", value)
	}
	// The file omitted the timeout while config.Load filled one in: that is a
	// platform default, and saying so is the whole point of the source column.
	if value := configurationValueForTest(t, view, "engine:mysql", "discovery_timeout_seconds"); value.Source != api.ConfigurationSourceDefault {
		t.Fatalf("discovery_timeout_seconds is absent from the file: %+v", value)
	}
}

func TestBuildConfigurationViewNeverReturnsCredentialValues(t *testing.T) {
	const secret = "super-secret-plaintext-value"
	t.Setenv("CG_TEST_MYSQL_DISCOVERY_PASSWORD", secret)
	configuration := config.File{
		MySQL: config.MySQL{
			Enabled: true,
			Discovery: config.Credential{
				Username:    "cg_discovery",
				PasswordEnv: "CG_TEST_MYSQL_DISCOVERY_PASSWORD",
				Password:    secret,
			},
		},
	}
	view := buildConfigurationView(configuration, filepath.Join(t.TempDir(), "missing.json"), time.Now().UTC(), nil)

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode view: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("the view leaked a credential value: %s", encoded)
	}
	value := configurationValueForTest(t, view, "engine:mysql", "discovery.password_env")
	if !value.CredentialRef {
		t.Fatalf("a credential must be flagged as a reference: %+v", value)
	}
	if value.Value != "环境变量 CG_TEST_MYSQL_DISCOVERY_PASSWORD" {
		t.Fatalf("a credential must be shown as the variable that holds it, got %q", value.Value)
	}
}

func TestBuildConfigurationViewReportsAnUnreadableFileAndStillShowsEffectiveValues(t *testing.T) {
	view := buildConfigurationView(config.File{HTTPAddress: "127.0.0.1:8088"},
		filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	if view.FilePresent {
		t.Fatal("a missing configuration file must be reported as missing")
	}
	if len(view.Warnings) == 0 {
		t.Fatal("a missing configuration file must warn that the values are the ones already in effect")
	}
	if len(view.Sections) == 0 {
		t.Fatal("the view must still describe what the process is running with")
	}
}

func TestBuildConfigurationViewDoesNotInventADiscoveryTimeoutForADisabledEngine(t *testing.T) {
	view := buildConfigurationView(config.File{}, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	value := configurationValueForTest(t, view, "engine:oracle", "discovery_timeout_seconds")
	if !strings.Contains(value.Value, "未启用") {
		t.Fatalf("a disabled engine has no discovery timeout, got %q", value.Value)
	}
	// The per-engine fallback differs (MySQL/PostgreSQL one second, Oracle and
	// SQL Server ten), so printing any number here would be a fabrication.
	if strings.Contains(value.Value, "秒") {
		t.Fatalf("a disabled engine must not be given an invented timeout, got %q", value.Value)
	}

	enabled := config.File{Oracle: config.Oracle{Enabled: true, DiscoveryTimeoutSeconds: 10}}
	view = buildConfigurationView(enabled, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	value = configurationValueForTest(t, view, "engine:oracle", "discovery_timeout_seconds")
	if value.Value != "10 秒" {
		t.Fatalf("an enabled engine must report the timeout in effect, got %q", value.Value)
	}
}

func TestBuildConfigurationViewStatesThatReloadIsUnsupported(t *testing.T) {
	view := buildConfigurationView(config.File{}, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	if view.ReloadSupported {
		t.Fatal("the platform reads its configuration once; the view must not advertise a reload")
	}
	if !strings.Contains(view.ReloadNote, "重启") {
		t.Fatalf("the reload note must tell the operator what to do instead: %q", view.ReloadNote)
	}
}

func TestBuildConfigurationViewKeepsClusterPolicySectionHonest(t *testing.T) {
	view := buildConfigurationView(config.File{}, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	value := configurationValueForTest(t, view, "cluster_policy", "cluster_policy")
	if !strings.Contains(value.Value, "未设置") {
		t.Fatalf("an unset policy must say so rather than showing invented overrides: %q", value.Value)
	}
	if value.RestartRequired {
		t.Fatal("the policy record itself is hot; only the configuration file needs a restart")
	}

	// A stored policy must be shown as replicated, hot, and attributed.
	view = buildConfigurationView(config.File{}, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(),
		func() store.ClusterPolicy {
			return store.ClusterPolicy{
				Engines: map[string]store.ClusterEnginePolicy{
					"mysql": {AutomaticFailoverMinimumObservations: 6, AutomaticFailoverSuppressed: true},
				},
				UpdatedBy: "ops@example", UpdatedAt: time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC),
			}
		})
	override := configurationValueForTest(t, view, "cluster_policy", "engine:mysql")
	if override.Source != api.ConfigurationSourcePolicy {
		t.Fatalf("a replicated override must not be reported as a file or default: %+v", override)
	}
	if override.RestartRequired {
		t.Fatalf("a policy override takes effect on the next round: %+v", override)
	}
	if !strings.Contains(override.Value, "观测次数 6") || !strings.Contains(override.Value, "维护抑制") {
		t.Fatalf("the override must describe what it changes: %q", override.Value)
	}
	if actor := configurationValueForTest(t, view, "cluster_policy", "updated_by"); !strings.Contains(actor.Value, "ops@example") {
		t.Fatalf("the view must say who changed failover behaviour: %q", actor.Value)
	}
}

func TestNewConfigurationViewProviderHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := newConfigurationViewProvider(config.File{}, filepath.Join(t.TempDir(), "absent.json"), time.Now().UTC(), nil)
	if _, err := provider.Configuration(ctx); err == nil {
		t.Fatal("a cancelled request must not be answered with a view")
	}
}
