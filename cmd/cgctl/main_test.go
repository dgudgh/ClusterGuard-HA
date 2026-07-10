package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
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
		{[]string{"clusters"}, http.MethodGet, "/api/v1/clusters"},
		{[]string{"topology", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/topology"},
		{[]string{"health", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/health"},
		{[]string{"candidates", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/candidates"},
		{[]string{"metrics", "cluster-id"}, http.MethodGet, "/api/v1/clusters/cluster-id/metrics"},
		{[]string{"refresh", "cluster-id"}, http.MethodPost, "/api/v1/clusters/cluster-id/discover"},
	}
	for _, test := range tests {
		method, path, err := requestFor(test.arguments)
		if err != nil || method != test.method || path != test.path {
			t.Fatalf("requestFor(%v) = %s %s, %v; want %s %s", test.arguments, method, path, err, test.method, test.path)
		}
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
