package postgresql

import (
	"context"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
)

func TestParseJSONRowsPreservesTypedValuesWithoutInventingNulls(t *testing.T) {
	rows, err := parseJSONRows([]byte("{\"node_id\":\"11111111-1111-4111-8111-111111111111\",\"in_recovery\":true,\"lag_seconds\":2}\n{\"lag_seconds\":null,\"timeline_id\":7}\n"))
	if err != nil {
		t.Fatalf("parse JSON rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0]["in_recovery"] != "true" || rows[0]["lag_seconds"] != "2" {
		t.Fatalf("typed values were not normalized: %+v", rows[0])
	}
	if rows[1]["lag_seconds"] != "" || rows[1]["timeline_id"] != "7" {
		t.Fatalf("null/numeric values were not normalized: %+v", rows[1])
	}
}

func TestParseJSONRowsRejectsNonObjectAndTrailingGarbage(t *testing.T) {
	for _, input := range []string{"[]\n", "{\"ok\":true} trailing\n"} {
		if _, err := parseJSONRows([]byte(input)); err == nil {
			t.Fatalf("parseJSONRows(%q) unexpectedly succeeded", input)
		}
	}
}

func TestQueryCommandSpecKeepsPasswordOutOfArguments(t *testing.T) {
	credentials := adapter.Credentials{Username: "monitor", Password: "top-secret", Database: "postgres"}
	spec, err := queryCommandSpec(context.Background(), "psql-custom", adapter.Endpoint{Hostname: "pg-01", Port: 5432}, credentials, "SELECT 1")
	if err != nil {
		t.Fatalf("build command spec: %v", err)
	}
	if spec.Binary != "psql-custom" {
		t.Fatalf("binary = %q", spec.Binary)
	}
	if strings.Contains(strings.Join(spec.Arguments, " "), credentials.Password) {
		t.Fatalf("password leaked into arguments: %v", spec.Arguments)
	}
	joinedEnvironment := strings.Join(spec.Environment, "\n")
	for _, expected := range []string{"PGPASSWORD=top-secret", "PGDATABASE=postgres", "PGAPPNAME=clusterguard-discovery", "PGCONNECT_TIMEOUT=5"} {
		if !strings.Contains(joinedEnvironment, expected) {
			t.Fatalf("environment missing %q: %v", expected, spec.Environment)
		}
	}
	for _, expected := range []string{"--no-password", "--no-psqlrc", "--tuples-only", "--no-align", "--host", "pg-01", "--port", "5432", "--username", "monitor", "--command", "SELECT 1"} {
		if !containsArgument(spec.Arguments, expected) {
			t.Fatalf("arguments missing %q: %v", expected, spec.Arguments)
		}
	}
}

func TestQueryCommandSpecUsesIPAddressAndBoundedTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	spec, err := queryCommandSpec(ctx, "", adapter.Endpoint{IPAddress: "192.0.2.20", Port: 5433}, adapter.Credentials{Username: "cg"}, "SELECT 1")
	if err != nil {
		t.Fatalf("build command spec: %v", err)
	}
	if spec.Binary != "psql" || !containsArgument(spec.Arguments, "192.0.2.20") {
		t.Fatalf("unexpected command spec: %+v", spec)
	}
	if !strings.Contains(strings.Join(spec.Environment, "\n"), "PGCONNECT_TIMEOUT=1") {
		t.Fatalf("deadline was not bounded: %v", spec.Environment)
	}
}

func TestQueryCommandSpecPrefersRegisteredIPAddressOverHostname(t *testing.T) {
	spec, err := queryCommandSpec(
		context.Background(),
		"",
		adapter.Endpoint{Hostname: "renamed-host-not-in-dns", IPAddress: "192.0.2.25", Port: 5432},
		adapter.Credentials{Username: "cg"},
		"SELECT 1",
	)
	if err != nil {
		t.Fatalf("build command spec: %v", err)
	}
	for index, argument := range spec.Arguments {
		if argument == "--host" && index+1 < len(spec.Arguments) {
			if spec.Arguments[index+1] != "192.0.2.25" {
				t.Fatalf("PostgreSQL connected through mutable hostname %q instead of registered IP", spec.Arguments[index+1])
			}
			return
		}
	}
	t.Fatalf("PostgreSQL command is missing --host: %v", spec.Arguments)
}

func TestExecutionCommandSpecUsesDedicatedApplicationNameAndNoPasswordArgument(t *testing.T) {
	credentials := adapter.Credentials{Username: "operator", Password: "control-secret", Database: "postgres"}
	spec, err := postgresqlCommandSpec(context.Background(), "/usr/bin/psql", adapter.Endpoint{IPAddress: "192.0.2.30", Port: 5432}, credentials, "SELECT pg_reload_conf()", "clusterguard-control")
	if err != nil {
		t.Fatal(err)
	}
	if value := lastEnvironmentValue(spec.Environment, "PGAPPNAME"); value != "clusterguard-control" {
		t.Fatalf("PGAPPNAME=%q", value)
	}
	if strings.Contains(strings.Join(spec.Arguments, " "), credentials.Password) {
		t.Fatalf("password leaked into arguments: %v", spec.Arguments)
	}
}

func TestQueryCommandSpecClearsInheritedPGOptions(t *testing.T) {
	t.Setenv("PGOPTIONS", "-c clusterguard.node_id=00000000-0000-4000-8000-000000000000")
	spec, err := queryCommandSpec(context.Background(), "", adapter.Endpoint{Hostname: "pg-01", Port: 5432}, adapter.Credentials{Username: "cg"}, "SELECT 1")
	if err != nil {
		t.Fatalf("build command spec: %v", err)
	}
	if value := lastEnvironmentValue(spec.Environment, "PGOPTIONS"); value != "" {
		t.Fatalf("inherited PGOPTIONS can override native identity: %q", value)
	}
}

func TestQueryCommandSpecRequiresEndpointPortAndUsername(t *testing.T) {
	for _, testCase := range []struct {
		endpoint    adapter.Endpoint
		credentials adapter.Credentials
	}{
		{endpoint: adapter.Endpoint{Port: 5432}, credentials: adapter.Credentials{Username: "cg"}},
		{endpoint: adapter.Endpoint{Hostname: "pg-01"}, credentials: adapter.Credentials{Username: "cg"}},
		{endpoint: adapter.Endpoint{Hostname: "pg-01", Port: 5432}},
	} {
		if _, err := queryCommandSpec(context.Background(), "", testCase.endpoint, testCase.credentials, "SELECT 1"); err == nil {
			t.Fatalf("invalid command spec unexpectedly succeeded: %+v", testCase)
		}
	}
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func lastEnvironmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}
