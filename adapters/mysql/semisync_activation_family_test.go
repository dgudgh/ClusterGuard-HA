package mysql

import (
	"clusterguard.io/ha/pkg/adapter"
	"context"
	"fmt"
	"strings"
	"testing"
)

type nativeSemiSyncFamilyClient struct {
	modern     bool
	mode       string
	active     bool
	statements []string
}

func (c *nativeSemiSyncFamilyClient) Query(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, q string) ([]Row, error) {
	v, s := readySemiSyncRows(c.modern)
	if c.mode == "incomplete" {
		v = v[:len(v)-1]
	}
	if c.mode == "ambiguous" {
		v = append(v, Row{"Variable_name": "rpl_semi_sync_master_enabled", "Value": "ON"})
	}
	if !c.active {
		s[0]["Value"] = "OFF"
	}
	if q == semiSyncVariablesQuery {
		return v, nil
	}
	if q == semiSyncStatusQuery {
		return s, nil
	}
	return nil, fmt.Errorf("unexpected query")
}
func (c *nativeSemiSyncFamilyClient) Exec(_ context.Context, _ adapter.Endpoint, _ adapter.Credentials, q string) error {
	c.statements = append(c.statements, q)
	name := "rpl_semi_sync_master_enabled"
	if c.modern {
		name = "rpl_semi_sync_source_enabled"
	}
	if !strings.Contains(q, name) {
		return fmt.Errorf("MySQL error 1193: unknown source plugin variable")
	}
	if strings.HasSuffix(q, "ON") && c.mode == "enable-error" {
		return fmt.Errorf("activation denied")
	}
	c.active = strings.HasSuffix(q, "ON")
	return nil
}
func TestSemiSyncActivationUsesObservedPluginFamily(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			c := &nativeSemiSyncFamilyClient{modern: modern}
			if err := activateSemiSyncSource(context.Background(), c, c, adapter.Endpoint{}, adapter.Credentials{}, "8.0.44"); err != nil {
				t.Fatal(err)
			}
			want := []string{disableSemiSyncMaster, enableSemiSyncMaster}
			if modern {
				want = []string{disableSemiSyncSource, enableSemiSyncSource}
			}
			if fmt.Sprint(c.statements) != fmt.Sprint(want) {
				t.Fatalf("statements=%v", c.statements)
			}
		})
	}
	for _, mode := range []string{"incomplete", "ambiguous", "enable-error"} {
		t.Run(mode, func(t *testing.T) {
			c := &nativeSemiSyncFamilyClient{modern: true, mode: mode}
			if err := activateSemiSyncSource(context.Background(), c, c, adapter.Endpoint{}, adapter.Credentials{}, "8.0.44"); err == nil {
				t.Fatal("unsafe activation accepted")
			}
			if mode != "enable-error" && len(c.statements) != 0 {
				t.Fatalf("incomplete/ambiguous plugin was mutated: %v", c.statements)
			}
			if c.active {
				t.Fatal("failed activation reported active")
			}
		})
	}
}
