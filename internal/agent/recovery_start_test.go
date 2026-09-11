package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

type recoveryStartFenceFixture struct {
	PostgreSQLController
	RecoveryPrimaryGuard
	fingerprint string
	guardErr    error
	started     bool
	stopped     bool
	promoted    bool
	inRecovery  bool
}

func (f *recoveryStartFenceFixture) RecoveryInspect(context.Context, ClusterPolicy) (model.RecoveryEvidence, error) {
	return model.RecoveryEvidence{Fingerprint: f.fingerprint}, nil
}
func (f *recoveryStartFenceFixture) RecoveryGuardVerify(context.Context, ClusterPolicy, model.ResourceID) error {
	return f.guardErr
}
func (f *recoveryStartFenceFixture) Start(context.Context, ClusterPolicy) error {
	f.started = true
	return nil
}
func (f *recoveryStartFenceFixture) Stop(context.Context, ClusterPolicy) error {
	f.stopped = true
	return nil
}
func (f *recoveryStartFenceFixture) Promote(context.Context, ClusterPolicy) error {
	f.promoted = true
	f.inRecovery = false
	return nil
}
func (f *recoveryStartFenceFixture) Status(context.Context, ClusterPolicy) (bool, bool, error) {
	return f.started, f.inRecovery, nil
}

func TestRecoveryPrimarySQLFenceRequiresGuardAndCurrentAuthorization(t *testing.T) {
	for _, scenario := range []string{"authorized primary", "authorized standby", "no authorization", "stale evidence", "missing guard", "revoked before start", "revoked after start"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			p := ClusterPolicy{PostgreSQLDataDirectory: t.TempDir(), PostgreSQLNodeID: model.NewResourceID()}
			path := filepath.Join(p.PostgreSQLDataDirectory, "postgresql.auto.conf")
			original := "default_transaction_read_only = 'on'\n"
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			f := &recoveryStartFenceFixture{fingerprint: "verified", inRecovery: scenario == "authorized standby"}
			if scenario == "stale evidence" {
				f.fingerprint = "changed"
			}
			if scenario == "missing guard" {
				f.guardErr = errors.New("missing guard")
			}
			calls := 0
			authorize := func() error {
				calls++
				if scenario == "no authorization" || scenario == "revoked before start" && calls == 2 || scenario == "revoked after start" && calls == 3 {
					return errors.New("no majority authorization")
				}
				return nil
			}
			err := startRecoveryPrimary(ctx, p, model.NewResourceID(), "verified", f, authorize)
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.HasPrefix(scenario, "authorized") {
				if err != nil || !f.started || !strings.Contains(string(data), "default_transaction_read_only = 'off'") {
					t.Fatalf("guarded primary not writable: %v", err)
				}
				if (scenario == "authorized standby") != f.promoted {
					t.Fatal("unexpected promotion")
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			if scenario == "revoked after start" {
				if !f.started || !f.stopped {
					t.Fatal("lost authorization did not stop the guarded primary")
				}
			} else if f.started {
				t.Fatal("unauthorized start")
			}
			if scenario == "no authorization" || scenario == "stale evidence" || scenario == "missing guard" {
				if string(data) != original {
					t.Fatal("fence changed before safety checks passed")
				}
			}
		})
	}
}
