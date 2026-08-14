# ClusterGuard HA Product Tour

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/product-tour.md)
<!-- /LANGUAGE-SWITCH -->

This guide introduces the primary ClusterGuard HA console surfaces with screenshots from the laboratory environment. Cluster names, node addresses, and runtime states are examples only. Production behavior must be qualified against the actual topology, database build, and site safety policy.

## High-Availability Operations Workbench

The Operations page is scoped to the selected cluster. Its context bar keeps the current primary, candidate primary, VIP, and replication lag visible. Controlled switchover, former-primary recovery, safety gates, and verification are stages of one backend workflow; a button click alone is never reported as success.

![ClusterGuard HA high-availability operations workbench](../assets/screenshots/ha-operation-workbench.png)

The workbench provides:

- candidate selection restricted to the active cluster inventory;
- coupled database-role and VIP-ownership transition;
- identity, replication, Raft majority, operation-lock, and safety checks before mutation;
- post-operation rediscovery and verification of the primary, replicas, VIP, and replication links;
- incremental rejoin or full rebuild for a recovered former primary.

## Cluster Topology

The Topology page is organized by immutable resource identity. Hostname, IP address, and port are correctable endpoints rather than resource keys. Node cards show role, health, version, endpoint, and VIP ownership. Status must come from current probe evidence; an unreachable node is shown as unknown or unavailable instead of inheriting a stale healthy state.

![ClusterGuard HA cluster topology](../assets/screenshots/cluster-topology.png)

The topology answers four operational questions:

1. Which instance is the currently observed primary?
2. Which instances are replicating, and in which direction?
3. Which node currently owns the VIP?
4. Which nodes are unreachable, degraded, or require attention?

## Node Lifecycle

The Nodes page separates inventory from lifecycle tasks. Add, replace, and rebuild parameters are collected in a modal and then submitted as an auditable task, keeping long installation forms out of the main workspace.

![ClusterGuard HA node lifecycle](../assets/screenshots/node-lifecycle.png)

Lifecycle tasks cover:

- data, controller, and mixed node roles;
- fixed node names and immutable resource IDs;
- SSH access, database media, port, and data directory;
- synchronization selection, replication restoration, and completion verification;
- odd controller-count enforcement and Raft membership checks.

## Operation Log

The Operation Log separates concise events from raw evidence. The default view shows time, cluster, source primary, target node, operation type, and final status. Operators can expand an entry to inspect raw output, operation UUID, workflow stages, checks, and report links.

![ClusterGuard HA operation log](../assets/screenshots/operation-log.png)

A successful event requires both execution and post-operation verification. If a database role changed but verification did not complete, the result must remain review-required or failed rather than being inferred from the command exit code alone.

## Screenshot Maintenance Policy

- Use real product screens; never present a design mockup as a delivered capability.
- Never expose passwords, tokens, private keys, cookies, or database connection secrets.
- Label screenshots as laboratory examples, not production acceptance evidence.
- Update both language indexes and screenshots when the console structure changes materially.
- Reuse each screenshot across both languages so the documentation cannot show conflicting product states.
