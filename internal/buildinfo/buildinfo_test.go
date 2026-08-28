package buildinfo

import (
	"runtime"
	"testing"
)

func TestCurrentExposesStableUpgradeCompatibilityContract(t *testing.T) {
	info := Current("clusterguard")
	if info.Product != "ClusterGuard HA" || info.Binary != "clusterguard" {
		t.Fatalf("identity=%+v", info)
	}
	if info.Version == "" || info.Release == "" || info.Commit == "" || info.BuiltAt == "" {
		t.Fatalf("build metadata is incomplete: %+v", info)
	}
	if info.OS != runtime.GOOS || info.Architecture != runtime.GOARCH {
		t.Fatalf("runtime identity=%+v", info)
	}
	if info.StateFormat < 1 || info.UpdateProtocol < 1 {
		t.Fatalf("upgrade compatibility contract is invalid: %+v", info)
	}
}

func TestRPMArchitectureUsesPackageNaming(t *testing.T) {
	tests := map[string]string{"amd64": "x86_64", "arm64": "aarch64", "386": "i386"}
	for input, want := range tests {
		if got := RPMArchitecture(input); got != want {
			t.Fatalf("RPMArchitecture(%q)=%q want %q", input, got, want)
		}
	}
}
