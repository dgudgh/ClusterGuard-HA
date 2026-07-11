package main

import "testing"

func TestDefaultConfigurationUsesOfficialClusterGuardPath(t *testing.T) {
	if defaultConfigPath != "/etc/clusterguard/clusterguard.json" {
		t.Fatalf("default config path = %q", defaultConfigPath)
	}
}
