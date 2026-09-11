package mysql

import (
	"context"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/model"
)

type disasterIdentityRunner struct{ row Row }

func (r disasterIdentityRunner) Query(context.Context, adapter.Endpoint, adapter.Credentials, string) ([]Row, error) {
	return []Row{r.row}, nil
}

func TestDisasterIdentityUsesCanonicalUUIDWithoutRelaxingFences(t *testing.T) {
	const id = "14158b19-a162-11f1-9ab2-02420a210103"
	for _, tc := range []struct {
		name, id, readOnly, gtid string
		preflight, qualified     bool
	}{
		{"canonical", id, "1", "ON", true, true},
		{"case-space", "  " + strings.ToUpper(id) + "\n", "1", "ON", true, true},
		{"missing", "", "1", "ON", false, false},
		{"different", "14158b19-a162-11f1-9ab2-02420a210104", "1", "ON", false, false},
		{"writable", id, "0", "ON", true, false},
		{"no-gtid", id, "1", "OFF", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := DisasterExecutor{Runner: disasterIdentityRunner{row: Row{"server_uuid": id, "hostname": "mysql01", "port": "3306", "server_id": "1", "version": "8.0.44", "read_only": tc.readOnly, "super_read_only": tc.readOnly, "gtid_mode": tc.gtid, "log_bin": "ON"}}}
			member := model.DatabaseInstance{EngineIdentity: model.EngineIdentity{"server_uuid": tc.id}}
			if err := e.Preflight(context.Background(), member, adapter.Credentials{}); (err == nil) != tc.preflight {
				t.Errorf("preflight: %v", err)
			}
			if _, err := e.qualified(context.Background(), member, adapter.Credentials{}); (err == nil) != tc.qualified {
				t.Errorf("qualified: %v", err)
			}
		})
	}
}
