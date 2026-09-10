package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type RecoveryReplicaRebuilder interface {
	RecoveryRebuild(context.Context, ClusterPolicy, model.ResourceID, string, PostgreSQLPeer, func() error) error
	RecoveryVerifyReplica(context.Context, ClusterPolicy, model.ResourceID, PostgreSQLPeer) error
}

func verifyRecoveryReplica(ctx context.Context, p ClusterPolicy, id model.ResourceID, source PostgreSQLPeer, c PostgreSQLController, verify func(context.Context, ClusterPolicy, PostgreSQLPeer, string) error) error {
	state, _, _, err := readPGGuard(p, id)
	if err != nil || !state.Released {
		return fmt.Errorf("reconstructed replica access policy is not verified")
	}
	running, standby, err := c.Status(ctx, p)
	if err != nil || !running || !standby {
		return fmt.Errorf("reconstructed member is not an active standby")
	}
	connection, err := postgresqlSourceURI(p, source)
	if err != nil {
		return err
	}
	return verify(ctx, p, source, connection)
}

func (c *PostgreSQLLocalController) RecoveryVerifyReplica(ctx context.Context, p ClusterPolicy, id model.ResourceID, source PostgreSQLPeer) error {
	return verifyRecoveryReplica(ctx, p, id, source, c, c.verifyPostgreSQLSource)
}
func (c *DockerPostgreSQLController) RecoveryVerifyReplica(ctx context.Context, p ClusterPolicy, id model.ResourceID, source PostgreSQLPeer) error {
	return verifyRecoveryReplica(ctx, p, id, source, c, c.waitForPostgreSQLSource)
}
func (c *RuntimePostgreSQLController) RecoveryVerifyReplica(ctx context.Context, p ClusterPolicy, id model.ResourceID, source PostgreSQLPeer) error {
	selected, err := c.selected(p)
	if err != nil {
		return err
	}
	controller, ok := selected.(RecoveryReplicaRebuilder)
	if !ok {
		return fmt.Errorf("runtime lacks replica verification")
	}
	return controller.RecoveryVerifyReplica(ctx, p, id, source)
}

func (s *Service) requireRecoveryPreparation(ctx context.Context, p ClusterPolicy, request Request) error {
	if s.recoveryDecisions == nil {
		return fmt.Errorf("fresh recovery preparation authorization is unavailable")
	}
	decision, err := s.recoveryDecisions.Decision(ctx, p)
	if err != nil {
		return err
	}
	if decision.ClusterID != p.ClusterID || decision.InstanceID != p.InstanceID || (decision.Action != ReconcileRecoveryPrepare && decision.Action != ReconcileRecoveryReplica) || decision.RecoveryTaskID != request.RecoveryTaskID || decision.LeaseID != request.LeaseID || !decision.ValidUntil.After(s.now().UTC()) {
		return fmt.Errorf("current majority does not authorize this fenced restart")
	}
	return nil
}

func (s *Service) requireRecoveryReplica(ctx context.Context, p ClusterPolicy, request Request) error {
	if s.recoveryDecisions == nil {
		return fmt.Errorf("fresh replica recovery authorization is unavailable")
	}
	decision, err := s.recoveryDecisions.Decision(ctx, p)
	if err != nil {
		return err
	}
	if decision.ClusterID != p.ClusterID || decision.InstanceID != p.InstanceID || decision.Action != ReconcileRecoveryReplica || decision.RecoveryTaskID != request.RecoveryTaskID || decision.LeaseID != request.LeaseID || !decision.ValidUntil.After(s.now().UTC()) {
		return fmt.Errorf("current majority does not authorize this replica reconstruction")
	}
	return nil
}

func recoveryPostgreSQLDirectories(p ClusterPolicy, directory string) (string, string, error) {
	suffix := ""
	if p.RecoveryArchiveID != "" {
		if !model.ValidResourceID(p.RecoveryArchiveID) {
			return "", "", fmt.Errorf("recovery quarantine requires a task UUID")
		}
		suffix = "-" + string(p.RecoveryArchiveID)
	}
	return directory + ".clusterguard-stage" + suffix, directory + ".clusterguard-backup" + suffix, nil
}

func rebuildRecoveryReplica(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, source PostgreSQLPeer, authorize func() error, c interface {
	PostgreSQLController
	RecoveryEvidenceInspector
	RecoveryPrimaryGuard
}, query pgGuardQuery, slot func(context.Context, ClusterPolicy, PostgreSQLPeer) error) (err error) {
	if authorize == nil {
		return fmt.Errorf("replica recovery requires a live authorization check")
	}
	if err = authorize(); err != nil {
		return err
	}
	evidence, err := c.RecoveryInspect(ctx, p)
	if err != nil {
		return err
	}
	if evidence.Fingerprint != fingerprint {
		return fmt.Errorf("PostgreSQL replica evidence changed before reconstruction")
	}
	if err = c.RecoveryGuardPrepare(ctx, p, id); err != nil {
		return err
	}
	p.RecoveryArchiveID = id
	if err = slot(ctx, p, source); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, c.Stop(stopCtx, p))
		}
	}()
	if rewindErr := c.Rewind(ctx, p, source); rewindErr != nil {
		if err = authorize(); err != nil {
			return err
		}
		if err = c.BaseBackup(ctx, p, source); err != nil {
			return fmt.Errorf("PostgreSQL rewind and retained-data base backup failed: %w", errors.Join(rewindErr, err))
		}
	}
	if err = authorize(); err != nil {
		return err
	}
	running, standby, statusErr := c.Status(ctx, p)
	if statusErr != nil || !running || !standby {
		return errors.Join(statusErr, fmt.Errorf("reconstructed PostgreSQL member is not a standby"))
	}
	expectedSlot, err := recoveryReplicationSlot(p)
	if err != nil {
		return err
	}
	receiver, err := query(ctx, p, "SELECT current_setting('primary_slot_name') || E'\\t' || COALESCE((SELECT slot_name FROM pg_stat_wal_receiver WHERE status='streaming'), '')")
	if err != nil || strings.TrimSpace(string(receiver)) != expectedSlot+"\t"+expectedSlot {
		return fmt.Errorf("reconstructed standby is not streaming through its reserved physical slot")
	}
	// Rewind/basebackup legitimately replace HBA with the donor's file. Once
	// native standby identity is verified, restore this member's own original
	// access policy instead of retaining the donor's recovery-only policy.
	state, hba, manifest, err := readPGGuard(p, id)
	if err != nil {
		return err
	}
	if err = recoveryAtomicFile(hba, state.Original, 0644); err != nil {
		return err
	}
	output, err := query(ctx, p, "SELECT pg_reload_conf()")
	if err != nil || strings.TrimSpace(string(output)) != "t" {
		return fmt.Errorf("restore reconstructed standby access: %w", errors.Join(err, fmt.Errorf("HBA reload not verified")))
	}
	state.Released = true
	b, _ := json.Marshal(state)
	return recoveryAtomicFile(manifest, b, 0600)
}

func (c *PostgreSQLLocalController) RecoveryRebuild(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, source PostgreSQLPeer, authorize func() error) error {
	return rebuildRecoveryReplica(ctx, p, id, fingerprint, source, authorize, c, c.psql, c.recoverySlot)
}
func (c *DockerPostgreSQLController) RecoveryRebuild(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, source PostgreSQLPeer, authorize func() error) error {
	return rebuildRecoveryReplica(ctx, p, id, fingerprint, source, authorize, c, c.psql, c.recoverySlot)
}
func (c *RuntimePostgreSQLController) RecoveryRebuild(ctx context.Context, p ClusterPolicy, id model.ResourceID, fingerprint string, source PostgreSQLPeer, authorize func() error) error {
	selected, e := c.selected(p)
	if e != nil {
		return e
	}
	controller, ok := selected.(RecoveryReplicaRebuilder)
	if !ok {
		return fmt.Errorf("runtime lacks PostgreSQL replica reconstruction")
	}
	return controller.RecoveryRebuild(ctx, p, id, fingerprint, source, authorize)
}
