package mysql

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

// DisasterExecutor is used only after all members have been fenced and the
// durable recovery manager has selected an authoritative GTID history.
// Normal switchover/rejoin safety checks are deliberately left unchanged.
type DisasterExecutor struct {
	Runner           SQLRunner
	SemiSyncRequired bool
}

func (e DisasterExecutor) Prepare(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials, authorize, verifyGuard func() error) (err error) {
	if authorize == nil || verifyGuard == nil {
		return fmt.Errorf("MySQL preparation requires majority authority and an offline guard")
	}
	if err = authorize(); err != nil {
		return err
	}
	if err = verifyGuard(); err != nil {
		return err
	}
	if _, err = e.qualified(ctx, member, credentials); err != nil {
		return err
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		err = errors.Join(err, e.exec(restoreCtx, member, credentials, "SET PERSIST super_read_only=ON; SET PERSIST read_only=ON"))
	}()
	if err = e.exec(ctx, member, credentials, "SET GLOBAL super_read_only=OFF"); err != nil {
		return err
	}
	return e.ensureClone(ctx, member, credentials)
}

func (e DisasterExecutor) exec(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials, sql string) error {
	writer, ok := e.Runner.(SQLExecutor)
	if !ok {
		return fmt.Errorf("MySQL recovery requires a SQL executor")
	}
	return writer.Exec(ctx, instanceEndpoint(member), credentials, sql)
}

func (e DisasterExecutor) qualified(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials) (identityProbe, error) {
	p, err := probeIdentity(ctx, e.Runner, instanceEndpoint(member), credentials)
	if err != nil {
		return p, err
	}
	if p.serverUUID != member.EngineIdentity["server_uuid"] || !p.readOnly || !p.superReadOnly || p.gtidMode != "ON" || (p.logBin != "ON" && p.logBin != "1") {
		return p, fmt.Errorf("MySQL recovery requires the exact registered native identity, GTID logging, and both write fences")
	}
	if !strings.HasPrefix(p.version, "8.0.") && !strings.HasPrefix(p.version, "8.4.") {
		return p, fmt.Errorf("automatic MySQL physical recovery is qualified only for 8.0 and 8.4")
	}
	return p, nil
}

func (e DisasterExecutor) Preflight(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials) error {
	p, err := probeIdentity(ctx, e.Runner, instanceEndpoint(member), credentials)
	if err != nil {
		return err
	}
	if p.serverUUID != member.EngineIdentity["server_uuid"] || p.gtidMode != "ON" || (p.logBin != "ON" && p.logBin != "1") {
		return fmt.Errorf("MySQL recovery requires registered UUID and GTID/binlog evidence")
	}
	if !strings.HasPrefix(p.version, "8.0.") && !strings.HasPrefix(p.version, "8.4.") {
		return fmt.Errorf("automatic MySQL recovery requires a qualified 8.0 or 8.4 member")
	}
	return nil
}

func verifyFrozenGTID(expected, actual string) error {
	want, err := gtid.ParseGTIDSet(expected)
	if err != nil {
		return fmt.Errorf("invalid frozen MySQL GTID evidence")
	}
	got, err := gtid.ParseGTIDSet(actual)
	if err != nil {
		return fmt.Errorf("invalid current MySQL GTID evidence")
	}
	comparison, err := gtid.CompareGTIDSets(want, got)
	if err != nil || comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		return fmt.Errorf("selected primary history changed after recovery selection")
	}
	return nil
}

func (e DisasterExecutor) StartPrimary(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials, expectedGTID string, authorize func() error) error {
	if authorize == nil {
		return fmt.Errorf("MySQL recovery authority is unavailable")
	}
	if err := authorize(); err != nil {
		return err
	}
	p, err := e.qualified(ctx, member, credentials)
	if err != nil {
		return err
	}
	if err = verifyFrozenGTID(expectedGTID, p.gtidExecuted); err != nil {
		return err
	}
	dialect, err := dialectForVersion(p.version)
	if err != nil {
		return err
	}
	_, configured, err := probeReplication(ctx, e.Runner, instanceEndpoint(member), credentials)
	if err != nil {
		return err
	}
	if configured {
		if err = e.exec(ctx, member, credentials, dialect.StopReplication); err != nil {
			return err
		}
		if err = authorize(); err != nil {
			return err
		}
		if err = e.exec(ctx, member, credentials, dialect.ResetReplication); err != nil {
			return err
		}
	}
	_, configured, err = probeReplication(ctx, e.Runner, instanceEndpoint(member), credentials)
	if err != nil || configured {
		return fmt.Errorf("selected MySQL primary still has an upstream replication channel")
	}
	_, err = e.qualified(ctx, member, credentials)
	return err
}

func (e DisasterExecutor) Rebuild(ctx context.Context, target, primary model.DatabaseInstance, credentials adapter.OperationCredentials, authorize, verifyGuard, restart func() error) error {
	if authorize == nil || verifyGuard == nil || restart == nil {
		return fmt.Errorf("MySQL reconstruction requires authority, offline guard and fenced restart callbacks")
	}
	if err := authorize(); err != nil {
		return err
	}
	source, err := e.qualified(ctx, primary, credentials.Administrative)
	if err != nil {
		return err
	}
	recipient, err := e.qualified(ctx, target, credentials.Administrative)
	if err != nil {
		return err
	}
	sourceGTID, err := gtid.ParseGTIDSet(source.gtidExecuted)
	if err != nil {
		return err
	}
	purged, err := gtid.ParseGTIDSet(source.gtidPurged)
	if err != nil {
		return err
	}
	targetGTID, err := gtid.ParseGTIDSet(recipient.gtidExecuted)
	if err != nil {
		return err
	}
	assessment, err := gtid.AssessGTIDRecovery(sourceGTID, purged, targetGTID)
	if err != nil || assessment.ErrantTransactions != 0 {
		return fmt.Errorf("replica history diverged after recovery selection")
	}
	dialect, err := dialectForVersion(recipient.version)
	if err != nil {
		return err
	}
	_, configured, err := probeReplication(ctx, e.Runner, instanceEndpoint(target), credentials.Administrative)
	if err != nil {
		return err
	}
	if configured {
		if err = e.exec(ctx, target, credentials.Administrative, dialect.StopReplication); err != nil {
			return err
		}
	}
	if !assessment.FastRejoinSafe {
		if source.version != recipient.version {
			return fmt.Errorf("physical MySQL clone requires identical qualified source and recipient versions")
		}
		if err = e.clone(ctx, target, primary, credentials.Administrative, authorize, verifyGuard, restart); err != nil {
			return err
		}
	}
	if err = authorize(); err != nil {
		return err
	}
	if err = e.exec(ctx, target, credentials.Administrative, "SET PERSIST super_read_only=ON; SET PERSIST read_only=ON"); err != nil {
		return err
	}
	if err = e.exec(ctx, target, credentials.Administrative, dialect.ResetReplication); err != nil {
		return err
	}
	statement, err := buildChangeSourceStatement(recipient.version, primary, credentials.Replication)
	if err != nil {
		return err
	}
	if err = e.exec(ctx, target, credentials.Administrative, statement); err != nil {
		return err
	}
	if err = e.exec(ctx, target, credentials.Administrative, dialect.StartReplication); err != nil {
		return err
	}
	if _, err = waitForFollowerReplicationHealthy(ctx, e.Runner, instanceEndpoint(target), credentials.Administrative, source.serverUUID, 0); err != nil {
		return err
	}
	rows, err := e.Runner.Query(ctx, instanceEndpoint(target), credentials.Administrative, "SELECT WAIT_FOR_EXECUTED_GTID_SET("+mysqlStringLiteral(source.gtidExecuted)+",60) AS caught_up")
	if err != nil || len(rows) != 1 || rows[0]["caught_up"] != "0" {
		return fmt.Errorf("reconstructed replica did not apply the authoritative GTID set")
	}
	after, err := e.qualified(ctx, target, credentials.Administrative)
	if err != nil {
		return err
	}
	afterSet, err := gtid.ParseGTIDSet(after.gtidExecuted)
	if err != nil {
		return err
	}
	comparison, err := gtid.CompareGTIDSets(sourceGTID, afterSet)
	if err != nil || comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		return fmt.Errorf("reconstructed replica GTID history does not exactly match the fenced primary")
	}
	return authorize()
}

func (e DisasterExecutor) ensureClone(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials) error {
	rows, err := e.Runner.Query(ctx, instanceEndpoint(member), credentials, "SELECT PLUGIN_STATUS AS state FROM INFORMATION_SCHEMA.PLUGINS WHERE PLUGIN_NAME='clone'")
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if err = e.exec(ctx, member, credentials, "SET SESSION sql_log_bin=0; INSTALL PLUGIN clone SONAME 'mysql_clone.so'"); err != nil {
			return fmt.Errorf("install the qualified MySQL clone plugin behind the write fence: %w", err)
		}
		rows, err = e.Runner.Query(ctx, instanceEndpoint(member), credentials, "SELECT PLUGIN_STATUS AS state FROM INFORMATION_SCHEMA.PLUGINS WHERE PLUGIN_NAME='clone'")
	}
	if err != nil || len(rows) != 1 || rows[0]["state"] != "ACTIVE" {
		return fmt.Errorf("MySQL clone plugin is not active")
	}
	return nil
}

func (e DisasterExecutor) clone(ctx context.Context, target, primary model.DatabaseInstance, credentials adapter.Credentials, authorize, verifyGuard, restart func() error) error {
	for _, member := range []model.DatabaseInstance{primary, target} {
		if err := e.ensureClone(ctx, member, credentials); err != nil {
			return err
		}
	}
	before, err := e.Runner.Query(ctx, instanceEndpoint(target), credentials, "SELECT ID, BEGIN_TIME, STATE FROM performance_schema.clone_status")
	if err != nil {
		return err
	}
	if err = verifyGuard(); err != nil {
		return err
	}
	if err = authorize(); err != nil {
		return err
	}
	donor := primary.IPAddress
	if donor == "" {
		donor = primary.Hostname
	}
	if donor == "" || primary.Port < 1 || primary.Port > 65535 {
		return fmt.Errorf("clone donor address is unavailable")
	}
	if err = e.exec(ctx, target, credentials, "SET GLOBAL clone_valid_donor_list="+mysqlStringLiteral(donor+":"+strconv.Itoa(primary.Port))); err != nil {
		return err
	}
	if err = e.exec(ctx, target, credentials, "SET GLOBAL super_read_only=OFF"); err != nil {
		return err
	}
	statement := "CLONE INSTANCE FROM " + mysqlStringLiteral(credentials.Username) + "@" + mysqlStringLiteral(donor) + ":" + strconv.Itoa(primary.Port) + " IDENTIFIED BY " + mysqlStringLiteral(credentials.Password) + " REQUIRE SSL"
	// A successful clone disconnects/restarts the recipient. Treat neither
	// command success nor a disconnect as completion; verify the new receipt.
	cloneErr := e.exec(ctx, target, credentials, mysqlLiteralStatement(statement, credentials.Username, credentials.Password, donor))
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err = authorize(); err != nil {
			return err
		}
		rows, queryErr := e.Runner.Query(ctx, instanceEndpoint(target), credentials, "SELECT ID, BEGIN_TIME, STATE, ERROR_NO FROM performance_schema.clone_status")
		if queryErr == nil && len(rows) == 1 {
			changed := len(before) == 0 || rows[0]["ID"] != before[0]["ID"] || rows[0]["BEGIN_TIME"] != before[0]["BEGIN_TIME"]
			if changed && rows[0]["STATE"] == "Completed" && rows[0]["ERROR_NO"] == "0" {
				return verifyGuard()
			}
			if changed && rows[0]["STATE"] == "Failed" {
				return fmt.Errorf("MySQL physical clone failed with error code %s", rows[0]["ERROR_NO"])
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MySQL physical clone did not produce a fresh completed receipt: %v", cloneErr)
		}
		if queryErr != nil {
			if err = restart(); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// VerifyGuardedPrimary proves recovery readiness without changing the normal
// health evaluator's definition of a writable primary.
func (e DisasterExecutor) VerifyGuardedPrimary(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials, expectedGTID string) (model.DatabaseInstance, error) {
	p, err := e.qualified(ctx, member, credentials)
	if err != nil {
		return model.DatabaseInstance{}, err
	}
	if err = verifyFrozenGTID(expectedGTID, p.gtidExecuted); err != nil {
		return model.DatabaseInstance{}, err
	}
	_, configured, err := probeReplication(ctx, e.Runner, instanceEndpoint(member), credentials)
	if err != nil || configured {
		return model.DatabaseInstance{}, fmt.Errorf("guarded primary still has a replication source")
	}
	if e.SemiSyncRequired {
		semiSync, err := probeSemiSync(ctx, e.Runner, instanceEndpoint(member), credentials)
		if err != nil || !semiSync.sourceReady() {
			return model.DatabaseInstance{}, fmt.Errorf("guarded primary lacks required semi-sync acknowledgement evidence")
		}
	}
	member.Role = model.RolePrimary
	member.Health = model.Health{State: model.HealthHealthy, ObservedAt: time.Now().UTC(), Summary: "authoritative MySQL primary is ready behind the recovery write fence"}
	member.EngineMetadata = map[string]string{"read_only": "true", "super_read_only": "true", "gtid_executed": p.gtidExecuted, "gtid_purged": p.gtidPurged, "gtid_mode": p.gtidMode, "version": p.version}
	return member, nil
}
