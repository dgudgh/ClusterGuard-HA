package agent

import (
	"errors"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestRecoverySlotIsNativeScopedAndRejectsConflicts(t *testing.T) {
	p := ClusterPolicy{RecoveryArchiveID: model.NewResourceID(), PostgreSQLNodeID: model.NewResourceID()}
	source := PostgreSQLPeer{NodeID: model.NewResourceID()}
	slot, err := recoveryReplicationSlot(p)
	if err != nil || slot != "cg_"+strings.ReplaceAll(string(p.PostgreSQLNodeID), "-", "") {
		t.Fatal("slot is not bound to the native UUID")
	}
	for _, tc := range []struct {
		name, row string
		bad       bool
		creates   int
		drops     int
	}{
		{name: "missing", creates: 1},
		{name: "reserved", row: `{"type":"physical","active":false,"temporary":false,"restart":"0/100","wal_status":"reserved"}`},
		{name: "lost", row: `{"type":"physical","active":false,"temporary":false,"restart":"0/100","wal_status":"lost"}`, creates: 1, drops: 1},
		{name: "active", row: `{"type":"physical","active":true,"temporary":false}`, bad: true},
		{name: "logical", row: `{"type":"logical","active":false,"temporary":false}`, bad: true},
		{name: "unknown", row: `{"type":"physical"}`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates, drops := 0, 0
			err := prepareRecoverySlot(p, source, func(sql string) ([]byte, error) {
				switch {
				case strings.Contains(sql, "current_setting"):
					return []byte(string(source.NodeID) + "\tfalse"), nil
				case strings.Contains(sql, "json_build_object"):
					return []byte(tc.row), nil
				case strings.Contains(sql, "pg_drop_replication_slot"):
					drops++
					return nil, nil
				case strings.Contains(sql, "pg_create_physical_replication_slot"):
					creates++
					if !strings.Contains(sql, ",true,false)") {
						t.Fatal("slot did not reserve persistent WAL")
					}
					return []byte(slot), nil
				}
				return nil, errors.New("unexpected slot query")
			})
			if (err != nil) != tc.bad || creates != tc.creates || drops != tc.drops {
				t.Fatalf("slot result: error=%v creates=%d drops=%d", err, creates, drops)
			}
		})
	}
	queries := 0
	if err := prepareRecoverySlot(p, source, func(string) ([]byte, error) { queries++; return []byte("different-native-primary\tfalse"), nil }); err == nil || queries != 1 {
		t.Fatal("slot mutation was allowed on a different primary")
	}
}

func TestPublicAgentErrorRetainsRootCauseAfterRedaction(t *testing.T) {
	err := errors.New(strings.Repeat("recovery wrapper: ", 25) + "FATAL: peer access rejected; password='not-for-diagnostics'")
	message := publicAgentError(err)
	if !strings.Contains(message, "FATAL: peer access rejected") || strings.Contains(message, "not-for-diagnostics") {
		t.Fatal("public recovery error lost its cause or disclosed a credential")
	}
}
