package model

import "testing"

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
