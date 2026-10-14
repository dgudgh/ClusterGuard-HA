package mysql

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

var recoveryBinlogName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func recoveryBinlogStatus(ctx context.Context, runner SQLRunner, member model.DatabaseInstance, credentials adapter.Credentials, version, expectedGTID string) (string, error) {
	statement := "SHOW MASTER STATUS"
	if strings.HasPrefix(version, "8.4.") {
		statement = "SHOW BINARY LOG STATUS"
	}
	rows, err := runner.Query(ctx, instanceEndpoint(member), credentials, statement)
	if err != nil {
		return "", fmt.Errorf("read recovery binlog status: %w", err)
	}
	if len(rows) != 1 || !recoveryBinlogName.MatchString(rows[0]["File"]) {
		return "", fmt.Errorf("recovery binlog identity is unavailable")
	}
	position, err := strconv.ParseUint(rows[0]["Position"], 10, 64)
	history, found := rows[0]["Executed_Gtid_Set"]
	if err != nil || position < 4 || !found {
		return "", fmt.Errorf("recovery binlog position or GTID evidence is unavailable")
	}
	if err := verifyFrozenGTID(expectedGTID, history); err != nil {
		return "", err
	}
	return rows[0]["File"], nil
}

func recoveryBinlogPrevious(ctx context.Context, runner SQLRunner, member model.DatabaseInstance, credentials adapter.Credentials, name string) (string, error) {
	if !recoveryBinlogName.MatchString(name) {
		return "", fmt.Errorf("invalid native recovery binlog name")
	}
	rows, err := runner.Query(ctx, instanceEndpoint(member), credentials, "SHOW BINLOG EVENTS IN "+mysqlStringLiteral(name)+" LIMIT 2")
	if err != nil {
		return "", err
	}
	if len(rows) != 2 || rows[0]["Event_type"] != "Format_desc" || rows[1]["Event_type"] != "Previous_gtids" ||
		rows[0]["Log_name"] != name || rows[1]["Log_name"] != name {
		return "", fmt.Errorf("recovery binlog lacks a complete native GTID header")
	}
	history, found := rows[1]["Info"]
	if !found {
		return "", fmt.Errorf("recovery binlog previous GTID evidence is absent")
	}
	if _, err := gtid.ParseGTIDSet(history); err != nil {
		return "", fmt.Errorf("invalid native binlog previous GTID evidence")
	}
	return history, nil
}

func (e DisasterExecutor) refreshRecoveryBinlog(ctx context.Context, member model.DatabaseInstance, credentials adapter.Credentials, expectedGTID string, authorize func() error) error {
	p, err := e.qualified(ctx, member, credentials)
	if err != nil {
		return err
	}
	if err := verifyFrozenGTID(expectedGTID, p.gtidExecuted); err != nil {
		return err
	}
	before, err := recoveryBinlogStatus(ctx, e.Runner, member, credentials, p.version, expectedGTID)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := authorize(); err != nil {
			return err
		}
		if err := e.exec(ctx, member, credentials, "FLUSH BINARY LOGS"); err != nil {
			return fmt.Errorf("establish recovery binlog boundary: %w", err)
		}
		current, err := recoveryBinlogStatus(ctx, e.Runner, member, credentials, p.version, expectedGTID)
		if err != nil {
			return err
		}
		// After physical deletion MySQL can reuse the missing filename on the
		// first rotation. Rotate once more; never edit its index or reset GTIDs.
		if current == before {
			continue
		}
		previous, err := recoveryBinlogPrevious(ctx, e.Runner, member, credentials, current)
		if err != nil {
			return fmt.Errorf("read newly established recovery binlog: %w", err)
		}
		if err := verifyFrozenGTID(expectedGTID, previous); err != nil {
			return fmt.Errorf("new recovery binlog boundary differs from frozen history: %w", err)
		}
		after, err := e.qualified(ctx, member, credentials)
		if err != nil {
			return err
		}
		if err := verifyFrozenGTID(expectedGTID, after.gtidExecuted); err != nil {
			return err
		}
		return authorize()
	}
	return fmt.Errorf("recovery could not establish a distinct readable binlog boundary")
}

// This is additional evidence for disaster recovery only. GTID purge metadata
// cannot establish that an externally deleted or recreated log still contains
// a replica's missing transactions.
func recoveryIncrementalAvailable(ctx context.Context, runner SQLRunner, member model.DatabaseInstance, credentials adapter.Credentials, executed, target gtid.GTIDSet) (bool, error) {
	rows, err := runner.Query(ctx, instanceEndpoint(member), credentials, "SHOW BINARY LOGS")
	if err != nil {
		return false, fmt.Errorf("inspect recovery source binlogs: %w", err)
	}
	if len(rows) == 0 {
		return false, fmt.Errorf("recovery source binlog inventory is absent")
	}
	seen := map[string]bool{}
	var prior gtid.GTIDSet
	first := true
	for _, row := range rows {
		name := row["Log_name"]
		if !recoveryBinlogName.MatchString(name) {
			return false, fmt.Errorf("invalid recovery source binlog inventory")
		}
		if seen[name] {
			continue
		} // Native rotation can retain duplicate index entries after deletion.
		seen[name] = true
		previous, err := recoveryBinlogPrevious(ctx, runner, member, credentials, name)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			var query *QueryError
			// Native ERROR 29 + ENOENT for this specific trusted log is evidence
			// of loss. Authorization/transport/parser failures are never fallback.
			if errors.As(err, &query) && query.Code == 29 && strings.Contains(query.Output, name+"'") && strings.Contains(query.Output, "OS errno 2 ") {
				return false, nil
			}
			return false, fmt.Errorf("inspect recovery source binlog header: %w", err)
		}
		boundary, err := gtid.ParseGTIDSet(previous)
		if err != nil {
			return false, err
		}
		assessment, err := gtid.AssessGTIDRecovery(executed, boundary, target)
		if err != nil {
			return false, fmt.Errorf("validate actual recovery binlog boundary: %w", err)
		}
		if first {
			if !assessment.FastRejoinSafe {
				return false, nil
			}
			first = false
		} else {
			comparison, err := gtid.CompareGTIDSets(boundary, prior)
			if err != nil || comparison.ErrantTransactions != 0 {
				return false, fmt.Errorf("recovery binlog history ordering is inconsistent")
			}
		}
		prior = boundary
	}
	return !first, nil
}
