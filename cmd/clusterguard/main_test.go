package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestDefaultConfigurationUsesOfficialClusterGuardPath(t *testing.T) {
	if defaultConfigPath != "/etc/clusterguard/clusterguard.json" {
		t.Fatalf("default config path = %q", defaultConfigPath)
	}
}

func TestLogConfigurationWarningsPrintsDeprecations(t *testing.T) {
	var output bytes.Buffer
	logConfigurationWarnings(log.New(&output, "", 0), []string{
		"CG_APPROVAL_TOKEN is deprecated for database operations",
	})
	if !strings.Contains(output.String(), "configuration warning: CG_APPROVAL_TOKEN is deprecated") {
		t.Fatalf("warning output=%q", output.String())
	}
}
