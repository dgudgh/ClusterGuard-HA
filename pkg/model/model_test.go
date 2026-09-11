package model

import (
	"net"
	"reflect"
	"testing"
)

func TestEndpointAddressPreservesLegacyAndFormatsIPv6(t *testing.T) {
	for _, test := range []struct {
		name, host, ip string
		port           int
		want           []string
	}{
		{"dns and IPv4", " DB01 ", " 192.0.2.1 ", 5432, []string{"DB01:5432", "192.0.2.1:5432"}},
		{"IPv6", "", "2001:0db8:0:0:0:0:0:1", 5432, []string{"[2001:db8::1]:5432"}},
		{"bracketed IPv6", "[::1]", "", 3306, []string{"[::1]:3306"}},
		{"scoped IPv6", "", "fe80::1%en0", 5432, []string{"[fe80::1%en0]:5432"}},
		{"empty", " ", "", 5432, []string{}},
		{"zero port", "db01", "::1", 0, []string{}},
		{"negative port", "db01", "::1", -1, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := EndpointAddress(test.host, test.ip, test.port)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("addresses=%v want=%v", got, test.want)
			}
			for _, address := range got {
				if _, _, err := net.SplitHostPort(address); err != nil {
					t.Fatalf("invalid host:port %q: %v", address, err)
				}
			}
		})
	}
}

func TestNewResourceIDProducesStableUUIDShape(t *testing.T) {
	id := NewResourceID()
	if !ValidResourceID(id) {
		t.Fatalf("generated resource ID is invalid: %q", id)
	}
	if id == NewResourceID() {
		t.Fatalf("resource IDs must be unique")
	}
}

func TestSupportedEnginesAreStable(t *testing.T) {
	want := []Engine{EngineMySQL, EnginePostgreSQL, EngineOracle, EngineSQLServer}
	got := SupportedEngines()
	if len(got) != len(want) {
		t.Fatalf("unexpected engines: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("engine %d: got %q want %q", i, got[i], want[i])
		}
	}
}
