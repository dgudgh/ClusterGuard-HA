# ClusterGuard HA

ClusterGuard HA is an independent enterprise high-availability control platform
for MySQL, PostgreSQL, Oracle, and SQL Server.

The first phase provides a clean control kernel with stable platform UUIDs,
engine adapters, metadata reconciliation, guarded workflows, versioned HTTP
APIs, a compact web console, and a read-only MySQL discovery path.

## Development

```bash
export CG_APPROVAL_TOKEN='replace-with-a-local-secret'
export CG_MYSQL_DISCOVERY_PASSWORD='replace-with-the-discovery-account-secret'
go run ./cmd/clusterguardd --config configs/clusterguard.example.json
go run ./cmd/cgctl -- clusters
```

The default console is served at `http://127.0.0.1:8088/`.

See [operations.md](docs/operations.md) for API usage, identity reconciliation,
current capability limits, and the next adapter milestones.
