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

func TestProductVersionDoesNotChangeCompatibilityBaseline(t *testing.T) {
	oldVersion, oldRelease, oldProduct := Version, Release, ProductVersion
	defer func() { Version, Release, ProductVersion = oldVersion, oldRelease, oldProduct }()
	Version, Release = "2.2", "105"
	for _, value := range []string{"3.1.1.1", "", "3.1.1", "3.1.1.0", "3.1.1.000", "3.1.1.1\ninjected"} {
		ProductVersion = value
		info := Current("clusterguard")
		want := "2.2-105"
		if value == "3.1.1.1" {
			want = value
		} else if info.ProductVersion != "" {
			t.Fatalf("invalid version exposed: %+v", info)
		}
		if info.DisplayVersion() != want || info.Version != "2.2" || info.Release != "105" {
			t.Fatalf("version %q: %+v display=%s", value, info, info.DisplayVersion())
		}
	}
}
