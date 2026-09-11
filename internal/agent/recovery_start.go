package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type RecoveryPrimaryStarter interface {
	RecoveryPrimaryGuard
	RecoveryPreparePrimary(context.Context, ClusterPolicy, model.ResourceID, string) error
	RecoveryStartPrimary(context.Context, ClusterPolicy, model.ResourceID, string, func() error) error
}

func (s *Service) requireRecoveryPrimary(ctx context.Context, p ClusterPolicy, request Request) error {
	if s.recoveryDecisions == nil {
		return fmt.Errorf("fresh recovery authorization is unavailable")
	}
	decision, err := s.recoveryDecisions.Decision(ctx, p)
	if err != nil {
		return err
	}
	if decision.ClusterID != p.ClusterID || decision.InstanceID != p.InstanceID || decision.Action != ReconcileRecoveryPrimary || decision.RecoveryTaskID != request.RecoveryTaskID || decision.LeaseID != request.LeaseID || !decision.ValidUntil.After(s.now().UTC()) {
		return fmt.Errorf("current majority does not authorize this recovery primary and operation lease")
	}
	return nil
}

func prepareRecoveryPrimary(ctx context.Context, p ClusterPolicy, taskID model.ResourceID, fingerprint string, controller interface {
	RecoveryEvidenceInspector
	RecoveryPrimaryGuard
}, tool recoveryPGTool) error {
	evidence, err := controller.RecoveryInspect(ctx, p)
	if err != nil {
		return err
	}
	if evidence.Fingerprint != fingerprint {
		return fmt.Errorf("selected PostgreSQL evidence changed before guarded startup")
	}
	if _, err = os.Lstat(filepath.Join(p.PostgreSQLDataDirectory, "recovery.signal")); !os.IsNotExist(err) {
		return fmt.Errorf("PITR recovery.signal requires manual recovery review")
	}
	restore, err := tool(ctx, "postgres", "-C", "restore_command")
	if err != nil || strings.TrimSpace(string(restore)) != "" {
		return fmt.Errorf("unverified external WAL restore prevents automatic primary startup")
	}
	return controller.RecoveryGuardPrepare(ctx, p, taskID)
}

func startRecoveryPrimary(ctx context.Context, p ClusterPolicy, taskID model.ResourceID, fingerprint string, controller interface {
	PostgreSQLController
	RecoveryEvidenceInspector
	RecoveryPrimaryGuard
}, authorize func() error) (err error) {
	if authorize == nil {
		return fmt.Errorf("recovery startup requires a live authorization check")
	}
	if err = authorize(); err != nil {
		return err
	}
	evidence, err := controller.RecoveryInspect(ctx, p)
	if err != nil {
		return err
	}
	if evidence.Fingerprint != fingerprint {
		return fmt.Errorf("selected PostgreSQL evidence changed before startup")
	}
	if err = controller.RecoveryGuardVerify(ctx, p, taskID); err != nil {
		return err
	}
	configuration := filepath.Join(p.PostgreSQLDataDirectory, "postgresql.auto.conf")
	if _, err = recoveryRegularFile(configuration); err != nil {
		return err
	}
	f, err := os.OpenFile(configuration, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	// The verified HBA guard still blocks business access. Clear the old SQL
	// fence only for this authorized primary, or strict topology verification
	// can never succeed and reach Recovery Commit.
	_, err = fmt.Fprintf(f, "\n# ClusterGuard recovery: upstream is selected from frozen WAL evidence.\nprimary_conninfo = ''\nprimary_slot_name = ''\nclusterguard.primary_node_id = '%s'\ndefault_transaction_read_only = 'off'\n", p.PostgreSQLNodeID)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = authorize(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, controller.Stop(stopCtx, p))
		}
	}()
	if err = controller.Start(ctx, p); err != nil {
		return err
	}
	if err = authorize(); err != nil {
		return err
	}
	if err = controller.RecoveryGuardVerify(ctx, p, taskID); err != nil {
		return err
	}
	running, inRecovery, statusErr := controller.Status(ctx, p)
	if statusErr != nil || !running {
		return errors.Join(statusErr, fmt.Errorf("guarded PostgreSQL primary did not start"))
	}
	if inRecovery {
		if err = controller.Promote(ctx, p); err != nil {
			return err
		}
	}
	return controller.RecoveryGuardVerify(ctx, p, taskID)
}

func (c *PostgreSQLLocalController) RecoveryPreparePrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string) error {
	return prepareRecoveryPrimary(ctx, p, id, fingerprint, c, c.recoveryTool(p))
}
func (c *DockerPostgreSQLController) RecoveryPreparePrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string) error {
	return prepareRecoveryPrimary(ctx, p, id, fingerprint, c, c.recoveryTool(p))
}
func (c *PostgreSQLLocalController) RecoveryStartPrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, authorize func() error) error {
	return startRecoveryPrimary(ctx, p, id, fingerprint, c, authorize)
}
func (c *DockerPostgreSQLController) RecoveryStartPrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, authorize func() error) error {
	return startRecoveryPrimary(ctx, p, id, fingerprint, c, authorize)
}

func (c *RuntimePostgreSQLController) recoveryController(p ClusterPolicy) (RecoveryPrimaryStarter, error) {
	selected, err := c.selected(p)
	if err != nil {
		return nil, err
	}
	guard, ok := selected.(RecoveryPrimaryStarter)
	if !ok {
		return nil, fmt.Errorf("selected PostgreSQL runtime has no guarded recovery controller")
	}
	return guard, nil
}
func (c *RuntimePostgreSQLController) RecoveryGuardPrepare(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryController(p)
	if e != nil {
		return e
	}
	return g.RecoveryGuardPrepare(ctx, p, id)
}
func (c *RuntimePostgreSQLController) RecoveryGuardVerify(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryController(p)
	if e != nil {
		return e
	}
	return g.RecoveryGuardVerify(ctx, p, id)
}
func (c *RuntimePostgreSQLController) RecoveryGuardRelease(ctx context.Context, p ClusterPolicy, id model.ResourceID) error {
	g, e := c.recoveryController(p)
	if e != nil {
		return e
	}
	return g.RecoveryGuardRelease(ctx, p, id)
}
func (c *RuntimePostgreSQLController) RecoveryPreparePrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string) error {
	g, e := c.recoveryController(p)
	if e != nil {
		return e
	}
	return g.RecoveryPreparePrimary(ctx, p, id, fingerprint)
}
func (c *RuntimePostgreSQLController) RecoveryStartPrimary(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, authorize func() error) error {
	g, e := c.recoveryController(p)
	if e != nil {
		return e
	}
	return g.RecoveryStartPrimary(ctx, p, id, fingerprint, authorize)
}
