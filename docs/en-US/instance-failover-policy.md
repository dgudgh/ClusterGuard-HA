# Cluster automatic failover and instance weights

[简体中文](../zh-CN/instance-failover-policy.md) · [Frontend](../development/frontend/settings/instance-failover.md) · [Backend](../development/backend/settings/instance-failover.md)

## Configure in the console

Select the database cluster at the top, then open **Settings → Runtime parameters → Automatic failover policy → Selected cluster automatic failover and instance weights**. Sign in as an administrator, unlock editing and save. Cluster changes, refresh, session changes and completed saves lock editing again. This policy affects the selected cluster independently of other clusters using the same engine.

- Enable cluster automation: a failed primary must satisfy the evidence window, candidate safety, Raft majority and old-primary fencing before automatic promotion.
- Disable cluster automation: no new automatic primary transition is started. Explicit manual operations remain subject to all existing safety checks.
- Instance participation binds to the database instance UUID. MySQL3306 and PostgreSQL5432 on one host have independent preferences. Disabling automatic promotion preserves discovery, monitoring, replication follow-up and manual switching.
- Weight is an integer from0 to1000, default100; higher values are preferred. Zero is the lowest weight; use the participation switch to exclude an instance.
- A recovered instance never preempts a healthy primary. Saving does not immediately switch the primary or move its writer endpoint.

The global engine maintenance pause remains authoritative. An enabled cluster still pauses while its engine is suppressed. Missing Agent/fencing, Raft, discovery or engine prerequisites cannot be bypassed by enabling this switch; the UI reports unavailable and the server rejects activation.

## Weight and data safety

Weight does not bypass replication health, maintenance, GTID, PG system identity/timeline/WAL, fencing or majority checks. MySQL retains risk and transaction-completeness precedence; PostgreSQL retains WAL-completeness precedence. Within an equal safety/data tier, weight takes precedence, with the original order and UUID resolving ties. An unsafe or less complete high-weight instance cannot displace a safer, more complete candidate.

Before an explicit policy exists, the startup switch remains the default, instances participate and have weight100. After saving a policy, newly discovered instance UUIDs not listed in it default to exclusion from automatic promotion until explicitly enabled. A replacement at the same IP does not inherit the old UUID's authorization. With prerequisites present, a startup-disabled cluster can be enabled on the next evaluation cycle without restarting or editing three configuration files.

## Upgrade, concurrency and failures

MySQL and PostgreSQL are supported. Native VIP and gateway providers share candidate policy. Upgrade every voting controller before configuring it: each save checks every current Raft member's protocol and identity. Old, unavailable, unknown-protocol or mismatched voters block saving. Do not downgrade to controllers that ignore an existing instance policy.

Saving uses a dedicated policy revision CAS, independent of frequent discovery revisions. Reload on conflict. Every preference must belong to the selected cluster; policy and audit commit atomically through Raft. Running cluster operations and upgrade/configuration maintenance block edits. Automatic execution also checks the revision used for candidate selection, preventing a stale selection after a policy change.

A transition already fencing/promoting a writer must finish under its existing safety protocol. This switch cannot kill an executor, remove leases or skip fencing. See the [implementation and acceptance record](../development/instance-failover-policy-2026-11-03.md) for actual test stages; isolated browser and unit checks are not live failover acceptance.

Before saving any instance policy, signed file rollback follows the existing process. After saving a policy, switching automation off does not remove the policy and does not make downgrade to unaware controllers safe.

Gateway database failover uses the signed native Agent isolation path: durable MySQL read-only/restart protection or a verified PostgreSQL service stop. Database hosts never acquire the gateway frontend VIP. An unreachable Agent or incomplete isolation still requires verifiable external fencing; weights cannot override this boundary. The native-VIP unreachable-Agent expiry fallback is not enabled for gateways.

The database-side Agent command path also requires an explicit no-VIP gateway policy: only MySQL/PG with empty VIP/interface/prefix. Status/release do not invoke local IP tools and VIP acquisition is always rejected. Native durable read-only or stopped-service evidence remains mandatory for database fencing.
