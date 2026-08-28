package mysql

import (
	"context"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
)

const replicationCredentialProbeQuery = "SELECT 1 AS credential_ready"

// verifyTargetReplicationCredentials proves that the selected target already
// owns a usable local replication account. The check runs before fencing or
// promotion so a missing account cannot strand the topology after handoff.
func verifyTargetReplicationCredentials(ctx context.Context, runner SQLRunner, target adapter.Endpoint, credentials adapter.Credentials) error {
	if strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
		return fmt.Errorf("target replication credentials are incomplete")
	}
	rows, err := runner.Query(ctx, target, credentials, replicationCredentialProbeQuery)
	if err != nil {
		return fmt.Errorf("target replication credentials are not usable: %w", err)
	}
	if len(rows) != 1 || strings.TrimSpace(rows[0]["credential_ready"]) != "1" {
		return fmt.Errorf("target replication credential probe returned invalid evidence")
	}
	return nil
}
