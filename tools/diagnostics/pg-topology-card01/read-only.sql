-- Card 01: SELECT-only PostgreSQL 16 diagnostics. Run on each registered endpoint.
-- Do not print primary_conninfo, pg_settings, environment variables or passfiles.
-- Preserve the session's normal transaction_read_only setting for this observation.
SELECT clock_timestamp() AS observed_at,
       inet_server_addr() AS server_addr,
       inet_server_port() AS server_port,
       current_setting('server_version') AS server_version,
       current_setting('clusterguard.node_id', true) AS native_node_id,
       current_setting('clusterguard.primary_node_id', true) AS native_primary_node_id,
       (pg_control_system()).system_identifier::text AS system_identifier,
       (pg_control_checkpoint()).timeline_id AS checkpoint_timeline,
       pg_is_in_recovery() AS in_recovery,
       current_setting('transaction_read_only') AS transaction_read_only;

SELECT status, sender_host, sender_port, received_tli, latest_end_lsn,
       pg_last_wal_receive_lsn() AS receive_lsn,
       pg_last_wal_replay_lsn() AS replay_lsn
FROM pg_stat_wal_receiver;

SELECT application_name, client_addr, state, sync_state,
       sent_lsn, write_lsn, flush_lsn, replay_lsn
FROM pg_stat_replication
ORDER BY application_name;

SELECT slot_name, slot_type, active, restart_lsn
FROM pg_replication_slots
ORDER BY slot_name;
