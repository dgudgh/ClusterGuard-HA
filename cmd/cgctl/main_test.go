package main

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestForCommands(t *testing.T) {
	tests := []struct {
		arguments []string
		method    string
		path      string
	}{
		{[]string{"engines"}, http.MethodGet, "/api/v1/engines"},
		{[]string{"version"}, http.MethodGet, "/api/v1/platform/version"},
		{[]string{"status"}, http.MethodGet, "/api/v1/control-plane/status"},
		{[]string{"clusters"}, http.MethodGet, "/api/v1/clusters"},
		{[]string{"topology", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/topology"},
		{[]string{"health", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/health"},
		{[]string{"candidates", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/candidates"},
		{[]string{"metrics", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/metrics"},
		{[]string{"refresh", "cluster-id"}, http.MethodPost, "/api/v1/clusters/cluster-id/discover"},
		{[]string{"operation", "11111111-1111-4111-8111-111111111111"}, http.MethodGet, "/api/v1/operations/11111111-1111-4111-8111-111111111111"},
		{[]string{"approval", "issue", "--cluster", "cluster-id"}, http.MethodPost, "/api/v1/approvals"},
		{[]string{"approval", "list"}, http.MethodGet, "/api/v1/approvals"},
		{[]string{"approval", "show", "11111111-1111-4111-8111-111111111111"}, http.MethodGet, "/api/v1/approvals/11111111-1111-4111-8111-111111111111"},
	}
	for _, test := range tests {
		method, path, err := requestFor(test.arguments)
		if err != nil || method != test.method || path != test.path {
			t.Fatalf("requestFor(%v) = %s %s, %v; want %s %s", test.arguments, method, path, err, test.method, test.path)
		}
	}
}

func TestRunVersionPrintsPatchCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/platform/version" {
			t.Fatalf("version path=%s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok","result":{"product":"ClusterGuard HA","binary":"clusterguard","version":"2.2","release":"29","commit":"abc123","built_at":"2026-08-24T10:00:00Z","os":"linux","architecture":"amd64","rpm_architecture":"x86_64","state_format":1,"update_protocol":1}}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--server", server.URL, "version"}, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("version exit=%d stderr=%q", exitCode, stderr.String())
	}
	for _, expected := range []string{"ClusterGuard HA", "version=2.2-29", "arch=x86_64", "state_format=1", "update_protocol=1"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("version output missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunStatusPrintsEnterpriseControlPlaneSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/control-plane/status" {
			t.Fatalf("status path=%s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok","result":{"mode":"raft","local_controller_id":"11111111-1111-4111-8111-111111111111","role":"leader","leader_id":"11111111-1111-4111-8111-111111111111","leader_known":true,"voter_count":3,"quorum_confirmed":true,"mutation_authority":true,"snapshot_cas_active":true,"state_revision":42,"ready":true,"readiness_reason":"ready","uptime_seconds":600,"cluster_count":2,"active_operations":1,"indeterminate_operations":0,"active_lifecycle_tasks":1}}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--server", server.URL, "status"}, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("status exit=%d stderr=%q", exitCode, stderr.String())
	}
	for _, expected := range []string{"ready=yes", "mode=raft", "role=leader", "quorum=yes", "revision=42", "clusters=2", "active_operations=1", "active_lifecycle_tasks=1"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q: %s", expected, stdout.String())
		}
	}
}

func TestRunStatusTrustsConfiguredPrivateCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok","result":{"mode":"raft","role":"follower","leader_known":true,"voter_count":3,"snapshot_cas_active":true,"ready":true,"readiness_reason":"ready"}}`)
	}))
	defer server.Close()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS test server has no certificate")
	}
	caPath := filepath.Join(t.TempDir(), "controller-ca.crt")
	contents := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(caPath, contents, 0o600); err != nil {
		t.Fatalf("write private CA: %v", err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", server.URL, "--ca-file", caPath, "status"}, &stdout, &stderr, http.DefaultClient)
	if exitCode != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "ready=yes") {
		t.Fatalf("private CA status exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidPrivateCA(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "invalid-ca.crt")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write invalid CA: %v", err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", "https://127.0.0.1:3000", "--ca-file", caPath, "status"}, &stdout, &stderr, http.DefaultClient)
	if exitCode != 2 || !strings.Contains(stderr.String(), "CA") {
		t.Fatalf("invalid CA exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunApprovalIssueUsesAdministrativeCredentialAndPrintsSecretOnce(t *testing.T) {
	var method, path, authorization string
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method = request.Method
		path = request.URL.Path
		authorization = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode issue request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
  "status":"ok",
  "result":{
    "operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
    "grant":{
      "resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      "operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      "cluster_id":"11111111-1111-4111-8111-111111111111",
      "engine":"mysql",
      "operation_kind":"switchover",
      "target_id":"22222222-2222-4222-8222-222222222222",
      "issued_by":"platform-admin",
      "issued_at":"2026-07-16T10:00:00Z",
      "expires_at":"2026-07-16T10:05:00Z",
      "status":"active"
    },
    "approval_token":"cgag_bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.c2VjcmV0"
  }
}`)
	}))
	defer server.Close()

	t.Setenv("CG_CONTROL_TOKEN", "administrator-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--server", server.URL,
		"approval", "issue",
		"--cluster", "11111111-1111-4111-8111-111111111111",
		"--engine", "mysql",
		"--kind", "switchover",
		"--target", "22222222-2222-4222-8222-222222222222",
		"--issued-by", "platform-admin",
		"--ttl", "5m",
	}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	if method != http.MethodPost || path != "/api/v1/approvals" || authorization != "Bearer administrator-secret" {
		t.Fatalf("issue request=%s %s authorization=%q", method, path, authorization)
	}
	for name, expected := range map[string]interface{}{
		"cluster_id":     "11111111-1111-4111-8111-111111111111",
		"engine":         "mysql",
		"operation_kind": "switchover",
		"target_id":      "22222222-2222-4222-8222-222222222222",
		"issued_by":      "platform-admin",
		"ttl_seconds":    float64(300),
	} {
		if body[name] != expected {
			t.Fatalf("issue body %s=%v want %v; body=%v", name, body[name], expected, body)
		}
	}
	for _, expected := range []string{
		"Approval token is shown once and cannot be recovered.",
		"grant=bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"operation=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"cgag_bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.c2VjcmV0",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("issue output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunApprovalListAndShowAreReadOnlyAndSanitized(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		response string
		want     []string
	}{
		{
			name:    "list",
			command: []string{"approval", "list"},
			response: `{"status":"ok","result":[{
			  "resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			  "operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			  "cluster_id":"11111111-1111-4111-8111-111111111111",
			  "engine":"mysql","operation_kind":"switchover",
			  "target_id":"22222222-2222-4222-8222-222222222222",
			  "expires_at":"2026-07-16T10:05:00Z","status":"active"
			}]}`,
			want: []string{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "status=active", "kind=switchover"},
		},
		{
			name:    "show",
			command: []string{"approval", "show", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
			response: `{"status":"ok","result":{
			  "resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			  "operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			  "cluster_id":"11111111-1111-4111-8111-111111111111",
			  "engine":"mysql","operation_kind":"switchover",
			  "target_id":"22222222-2222-4222-8222-222222222222",
			  "expires_at":"2026-07-16T10:05:00Z","status":"consumed",
			  "consumed_by_operation_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			}}`,
			want: []string{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "status=consumed", "operation=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				authorization = request.Header.Get("Authorization")
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, test.response)
			}))
			defer server.Close()
			t.Setenv("CG_CONTROL_TOKEN", "must-not-be-sent")
			var stdout, stderr bytes.Buffer
			arguments := append([]string{"--server", server.URL}, test.command...)
			if exitCode := run(arguments, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
			}
			if authorization != "Bearer must-not-be-sent" {
				t.Fatalf("read-only approval request authorization=%q", authorization)
			}
			for _, expected := range test.want {
				if !strings.Contains(stdout.String(), expected) {
					t.Fatalf("output missing %q:\n%s", expected, stdout.String())
				}
			}
			if strings.Contains(stdout.String(), "token_hash") || strings.Contains(stdout.String(), "cgag_") {
				t.Fatalf("sanitized output exposed a token: %s", stdout.String())
			}
		})
	}
}

func TestRunReadRequestUsesConfiguredControlCredential(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok","result":[]}`)
	}))
	defer server.Close()
	t.Setenv("CG_CONTROL_TOKEN", "read-control-secret")
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--server", server.URL, "clusters"}, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("read request exit=%d stderr=%q", exitCode, stderr.String())
	}
	if authorization != "Bearer read-control-secret" {
		t.Fatalf("read request authorization=%q", authorization)
	}
}

func TestRunApprovalIssueRejectsInvalidTTLBeforeRequest(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requested = true
	}))
	defer server.Close()
	t.Setenv("CG_CONTROL_TOKEN", "administrator-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--server", server.URL,
		"approval", "issue",
		"--cluster", "11111111-1111-4111-8111-111111111111",
		"--engine", "mysql",
		"--kind", "switchover",
		"--target", "22222222-2222-4222-8222-222222222222",
		"--issued-by", "platform-admin",
		"--ttl", "16m",
	}, &stdout, &stderr, server.Client())
	if exitCode != 2 || requested || !strings.Contains(stderr.String(), "TTL") {
		t.Fatalf("invalid TTL exit=%d requested=%t stderr=%q", exitCode, requested, stderr.String())
	}
}

func TestRequestForRejectsInvalidCommands(t *testing.T) {
	for _, arguments := range [][]string{nil, {"unknown"}, {"topology"}, {"clusters", "extra"}, {"refresh", ""}} {
		if _, _, err := requestFor(arguments); err == nil {
			t.Fatalf("requestFor(%v) succeeded", arguments)
		}
	}
}

func TestRunRefreshPostsExactEmptyJSONObject(t *testing.T) {
	var method, path, contentType, authorization, body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		method = request.Method
		path = request.URL.Path
		contentType = request.Header.Get("Content-Type")
		authorization = request.Header.Get("Authorization")
		contents, _ := io.ReadAll(request.Body)
		body = string(contents)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"ok","result":{"cluster_id":"11111111-1111-4111-8111-111111111111","instances":[],"links":[],"probes":[],"health":{"state":"healthy"},"observed_at":"2026-07-11T01:02:03Z"}}`)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	t.Setenv("CG_CONTROL_TOKEN", "control-secret")
	exitCode := run([]string{"--server", server.URL, "refresh", "11111111-1111-4111-8111-111111111111"}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	if method != http.MethodPost || path != "/api/v1/clusters/11111111-1111-4111-8111-111111111111/discover" || contentType != "application/json" || authorization != "Bearer control-secret" || body != "{}" {
		t.Fatalf("refresh request = %s %s content-type=%q authorization=%q body=%q", method, path, contentType, authorization, body)
	}
	if !strings.Contains(stdout.String(), "11111111-1111-4111-8111-111111111111") || !strings.Contains(stdout.String(), "refreshed") {
		t.Fatalf("refresh output does not acknowledge the cluster: %q", stdout.String())
	}
}

func TestRunRefreshFailsBeforeRequestWhenControlTokenEnvironmentIsEmpty(t *testing.T) {
	server := testAPIServer(t, `{"status":"ok","result":{}}`)
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--server", server.URL, "refresh", "cluster-id"}, &stdout, &stderr, server.Client()); exitCode != 2 || !strings.Contains(stderr.String(), "control token environment variable") {
		t.Fatalf("missing control token exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunJSONPrintsIndentedRawAPIResponse(t *testing.T) {
	server := testAPIServer(t, `{"status":"ok","result":[{"resource_id":"11111111-1111-4111-8111-111111111111","display_name":"orders","engine":"mysql"}]}`)
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", server.URL, "--json", "clusters"}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	want := "{\n  \"status\": \"ok\",\n  \"result\": ["
	if !strings.Contains(stdout.String(), want) || !strings.Contains(stdout.String(), `"display_name": "orders"`) {
		t.Fatalf("JSON output is not indented raw response:\n%s", stdout.String())
	}
}

func TestRunHumanOutputIsConciseAndCommandSpecific(t *testing.T) {
	tests := []struct {
		name     string
		command  []string
		response string
		want     []string
	}{
		{
			name:     "topology",
			command:  []string{"topology", "cluster-id"},
			response: `{"status":"ok","result":{"cluster_id":"cluster-id","instances":[{"resource_id":"instance-1","display_name":"mysql-a","hostname":"mysql-a","ip_address":"192.0.2.10","port":3306,"role":"primary","health":{"state":"healthy"},"replication":{"lag_seconds":0}}]}}`,
			want:     []string{"instance-1", "mysql-a (192.0.2.10:3306)", "role=primary", "health=healthy", "lag=0s"},
		},
		{
			name:     "candidates",
			command:  []string{"candidates", "cluster-id"},
			response: `{"status":"ok","result":[{"instance_id":"instance-2","eligible":true,"rank":1,"risk_level":"low","data_loss_risk":"none"}]}`,
			want:     []string{"instance-2", "rank=1", "eligible=yes", "risk=low", "data_loss=none"},
		},
		{
			name:     "metrics",
			command:  []string{"metrics", "cluster-id"},
			response: `{"status":"ok","result":{"cluster_id":"cluster-id","instances":[{"instance_id":"instance-1","values":{"qps":12.5,"replication_lag_seconds":0}}]}}`,
			want:     []string{"instance-1", "qps=12.5", "replication_lag_seconds=0"},
		},
		{
			name:    "operation",
			command: []string{"operation", "11111111-1111-4111-8111-111111111111"},
			response: `{"status":"ok","result":{"resource_id":"11111111-1111-4111-8111-111111111111","operation":{"engine":"mysql","kind":"switchover"},` +
				`"target_id":"22222222-2222-4222-8222-222222222222","stage":"report","status":"succeeded","message":"operation completed and verified"}}`,
			want: []string{"11111111-1111-4111-8111-111111111111", "mysql", "switchover", "target=22222222-2222-4222-8222-222222222222", "stage=report", "status=succeeded"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testAPIServer(t, test.response)
			defer server.Close()
			arguments := append([]string{"--server", server.URL}, test.command...)
			var stdout, stderr bytes.Buffer
			if exitCode := run(arguments, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
			}
			for _, want := range test.want {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("human output missing %q:\n%s", want, stdout.String())
				}
			}
			if strings.Contains(stdout.String(), "password") || strings.Contains(stdout.String(), "credential") {
				t.Fatalf("human output exposed credentials: %s", stdout.String())
			}
		})
	}
}

func TestRunReturnsClearUsageAndAPIErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"topology"}, &stdout, &stderr, http.DefaultClient); exitCode != 2 || !strings.Contains(stderr.String(), "requires a platform cluster UUID") {
		t.Fatalf("usage error exit=%d stderr=%q", exitCode, stderr.String())
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(writer, `{"status":"error","message":"cluster has no persisted topology observation"}`)
	}))
	defer server.Close()
	stdout.Reset()
	stderr.Reset()
	if exitCode := run([]string{"--server", server.URL, "topology", "cluster-id"}, &stdout, &stderr, server.Client()); exitCode != 1 || !strings.Contains(stderr.String(), "cluster has no persisted topology observation") {
		t.Fatalf("API error exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func testAPIServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, response)
	}))
}

func TestRunClusterShutdownRequiresClusterAndValidMode(t *testing.T) {
	t.Setenv("CG_SHUTDOWN_SCRIPT", "/nonexistent")
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"cluster", "shutdown"}, &stdout, &stderr, http.DefaultClient); exitCode != 2 || !strings.Contains(stderr.String(), "--cluster") {
		t.Fatalf("missing cluster exit=%d stderr=%q", exitCode, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := run([]string{"cluster", "shutdown", "--cluster", "lab", "--mode", "reboot"}, &stdout, &stderr, http.DefaultClient); exitCode != 2 || !strings.Contains(stderr.String(), "service or poweroff") {
		t.Fatalf("invalid mode exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunClusterShutdownUsesUnifiedPowerLifecycle(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/clusters":
			_, _ = io.WriteString(writer, powerTestClusterList())
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/power/precheck"):
			_, _ = io.WriteString(writer, `{"status":"ok","result":{"power_operation":{"state":"prechecking","mode":"service"},"blocking_reasons":[],"risk":"low"}}`)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/power/plan"):
			_, _ = io.WriteString(writer, `{"status":"ok","result":{"power_operation":{"state":"shutdown_planned","mode":"service"}}}`)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/power/execute"):
			_, _ = io.WriteString(writer, `{"status":"ok","result":{"power_operation":{"state":"power_off","mode":"service"},"protection":{"recovery_freeze":true,"instances_in_maintenance":[]}}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("CG_CONTROL_TOKEN", "control-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", server.URL, "cluster", "shutdown", "--cluster", "lab-cluster", "--mode", "service", "--approval-token", "one-time"}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("shutdown exit=%d stderr=%q", exitCode, stderr.String())
	}
	want := []string{
		"GET /api/v1/clusters",
		"POST /api/v1/clusters/11111111-1111-4111-8111-111111111111/power/precheck",
		"POST /api/v1/clusters/11111111-1111-4111-8111-111111111111/power/plan",
		"POST /api/v1/clusters/11111111-1111-4111-8111-111111111111/power/execute",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("power lifecycle paths=%v, want %v", paths, want)
	}
	if !strings.Contains(stdout.String(), "state\tpower_off") {
		t.Fatalf("shutdown output missing terminal state: %q", stdout.String())
	}
}

func TestRunClusterRestoreStatusReportsSnapshotAndFreezeState(t *testing.T) {
	snapshot := `{"cluster_id":"11111111-1111-4111-8111-111111111111","cluster":{"cluster_name":"lab","primary":{"host":"orch-mysql01","port":3306,"instance_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}}`
	snapshotPath := filepath.Join(t.TempDir(), "cluster-topology.json")
	if err := os.WriteFile(snapshotPath, []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(request.URL.Path, "/topology"):
			_, _ = io.WriteString(writer, `{"status":"ok","result":{"cluster_id":"11111111-1111-4111-8111-111111111111","instances":[{"resource_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","hostname":"orch-mysql01","ip_address":"192.168.102.152","port":3306,"role":"primary","health":{"state":"healthy"},"replication":{"lag_seconds":null}}],"probes":[],"health":{"state":"healthy"},"observed_at":"2026-08-06T00:00:00Z"}}`)
		default:
			_, _ = io.WriteString(writer, `{"status":"ok","result":{"cluster":{"display_name":"lab","recovery_freeze":true},"instances":[]}}`)
		}
	}))
	defer server.Close()
	t.Setenv("CLUSTER_SNAPSHOT_PATH", snapshotPath)
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"--server", server.URL, "cluster", "restore-status"}, &stdout, &stderr, server.Client()); exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("restore-status exit=%d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "recovery_freeze\tfrozen") || !strings.Contains(stdout.String(), "advice") {
		t.Fatalf("restore-status output missing freeze/advice: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "role=primary") || !strings.Contains(stdout.String(), "health=healthy") {
		t.Fatalf("restore-status output missing instance state: %q", stdout.String())
	}
}

func TestRunClusterRestoreStatusWithoutSnapshotAndClusterReportsMissing(t *testing.T) {
	t.Setenv("CLUSTER_SNAPSHOT_PATH", filepath.Join(t.TempDir(), "absent.json"))
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"cluster", "restore-status"}, &stdout, &stderr, http.DefaultClient); exitCode != 0 || !strings.Contains(stdout.String(), "no cluster UUID available") {
		t.Fatalf("missing-snapshot exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func powerTestClusterList() string {
	return `{"status":"ok","result":[{
		"resource_id":"11111111-1111-4111-8111-111111111111",
		"display_name":"lab-cluster","engine":"mysql"
	}]}`
}

func TestRunPowerPrecheckResolvesNameAndPostsMode(t *testing.T) {
	var seen []struct{ method, path, authorization string }
	var body map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, struct{ method, path, authorization string }{
			request.Method, request.URL.Path, request.Header.Get("Authorization"),
		})
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/clusters":
			_, _ = io.WriteString(writer, powerTestClusterList())
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/clusters/11111111-1111-4111-8111-111111111111/power/precheck":
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode precheck body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"status":"ok","result":{
				"power_operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","cluster_id":"11111111-1111-4111-8111-111111111111","engine":"mysql","operation_type":"service","state":"prechecking","mode":"service","requested_by":"tester","auto_recovery":true,"started_at":"2026-08-06T10:00:00Z"},
				"blocking_reasons":[],"risk":"low"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	t.Setenv("CG_CONTROL_TOKEN", "administrator-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"--server", server.URL,
		"power", "precheck", "--cluster", "lab-cluster", "--mode", "service",
	}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run exit=%d stderr=%q", exitCode, stderr.String())
	}
	if len(seen) != 2 || seen[0].method != http.MethodGet || seen[1].method != http.MethodPost {
		t.Fatalf("expected inventory lookup then precheck, got %+v", seen)
	}
	if body["mode"] != "service" {
		t.Fatalf("precheck mode=%v, want service", body["mode"])
	}
	for _, expected := range []string{
		"state\tprechecking",
		"mode\tservice",
		"risk\tlow",
		"blocking_reasons\t<none>",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("precheck output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunPowerPlanAndExecutePostToken(t *testing.T) {
	var paths []string
	var executeBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/clusters":
			_, _ = io.WriteString(writer, powerTestClusterList())
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/power/plan"):
			_, _ = io.WriteString(writer, `{"status":"ok","result":{
				"power_operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","cluster_id":"11111111-1111-4111-8111-111111111111","engine":"mysql","operation_type":"service","state":"shutdown_planned","mode":"service","requested_by":"tester","auto_recovery":true,"started_at":"2026-08-06T10:00:00Z"},
				"snapshot":{"cluster_id":"11111111-1111-4111-8111-111111111111","primary":{"instance_id":"22222222-2222-4222-8222-222222222222","hostname":"mysql-a","ip_address":"192.0.2.10","port":3306},"replicas":[],"captured_at":"2026-08-06T10:00:00Z"},
				"plan_steps":[]}}`)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/power/execute"):
			if err := json.NewDecoder(request.Body).Decode(&executeBody); err != nil {
				t.Fatalf("decode execute body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"status":"ok","result":{
				"operation_record":{"resource_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","status":"succeeded"},
				"power_operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","cluster_id":"11111111-1111-4111-8111-111111111111","engine":"mysql","operation_type":"service","state":"power_off","mode":"service","requested_by":"tester","auto_recovery":true,"started_at":"2026-08-06T10:00:00Z"},
				"protection":{"recovery_freeze":true,"instances_in_maintenance":["22222222-2222-4222-8222-222222222222"]}}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	t.Setenv("CG_CONTROL_TOKEN", "administrator-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", server.URL, "power", "plan", "--cluster", "11111111-1111-4111-8111-111111111111"}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("plan exit=%d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "state\tshutdown_planned") {
		t.Fatalf("plan output missing state:\n%s", stdout.String())
	}

	stdout.Reset()
	// Execute accepts the cluster UUID directly and forwards the approval token.
	exitCode = run([]string{
		"--server", server.URL,
		"power", "execute", "--cluster", "11111111-1111-4111-8111-111111111111", "--approval-token", "cgag_secret",
	}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("execute exit=%d stderr=%q", exitCode, stderr.String())
	}
	if executeBody["approval_token"] != "cgag_secret" {
		t.Fatalf("execute token=%v, want cgag_secret", executeBody["approval_token"])
	}
	for _, expected := range []string{
		"state\tpower_off",
		"recovery_freeze\tyes",
		"instances_in_maintenance\t22222222-2222-4222-8222-222222222222",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("execute output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunPowerStatusIsReadOnly(t *testing.T) {
	var seenMethod, seenPath, seenAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenMethod = request.Method
		seenPath = request.URL.Path
		seenAuthorization = request.Header.Get("Authorization")
		_ = seenAuthorization
		_, _ = io.WriteString(writer, `{"status":"ok","result":{
			"cluster":{"resource_id":"11111111-1111-4111-8111-111111111111","display_name":"lab-cluster","engine":"mysql"},
			"power_operation":{"resource_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","cluster_id":"11111111-1111-4111-8111-111111111111","engine":"mysql","operation_type":"service","state":"power_off","mode":"service","requested_by":"tester","auto_recovery":true,"started_at":"2026-08-06T10:00:00Z"},
			"protected":true,"protected_state":"power_off","recovery_freeze":true,
			"topology":{},"has_topology":true,"operation_history":[]}}`)
	}))
	defer server.Close()

	t.Setenv("CG_CONTROL_TOKEN", "monitoring-secret")
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"--server", server.URL, "power", "status", "--cluster", "11111111-1111-4111-8111-111111111111"}, &stdout, &stderr, server.Client())
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("status exit=%d stderr=%q", exitCode, stderr.String())
	}
	if seenMethod != http.MethodGet || seenPath != "/api/v1/clusters/11111111-1111-4111-8111-111111111111/power/status" {
		t.Fatalf("status request=%s %s", seenMethod, seenPath)
	}
	for _, expected := range []string{
		"cluster\t11111111-1111-4111-8111-111111111111\tdisplay=lab-cluster\tengine=mysql",
		"state\tpower_off",
		"protected\tyes",
		"recovery_freeze\tyes",
		"started_at\t2026-08-06T10:00:00Z",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestRunPowerRejectsInvalidInvocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	}))
	defer server.Close()

	for _, arguments := range [][]string{
		{"power"},
		{"power", "frobnicate", "--cluster", "x"},
		{"power", "execute", "--cluster", "x"},
		{"power", "precheck", "--cluster", "x", "--mode", "sideways"},
	} {
		var stdout, stderr bytes.Buffer
		if exitCode := run(append([]string{"--server", server.URL}, arguments...), &stdout, &stderr, server.Client()); exitCode != 2 {
			t.Fatalf("run %v exit=%d, want 2 (stderr=%q)", arguments, exitCode, stderr.String())
		}
	}
}
