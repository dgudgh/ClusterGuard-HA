# ClusterGuard HA Console Benchmark and Superiority Design

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/specs/2026-07-15-console-benchmark-and-superiority-design.md)
<!-- /LANGUAGE-SWITCH -->

**Date:** 2026-07-15
**Scope:** Only refactor the ClusterGuard HA Web Console, no new database execution capabilities are added, and the existing API semantics are not changed.

## Objective

Upgrade the current "functionally available" console into an enterprise-grade workstation for DBA daily operations. The page must simultaneously satisfy:

1. The first screen can determine whether the entire database fleet is healthy.
2. Target switching, impact scope, and execution status have no ambiguity.
3. Only show success after backend validation passes.
4. Complex evidence is collapsed by default, but any operation can be traced.
5. Hostnames, IPs, and port changes do not obscure immutable resource identities.
6. Desktop, narrow screen, and mobile views do not rearrange into uncontrollable long pages.

## Benchmark Audit

| Dimension | Old Prototype Advantages | Old Prototype Issues | ClusterGuard HA Superiority Approach |
| --- | --- | --- | --- |
| Capability Coverage | Topology, VIP, Switching, Recovery, Nodes, Audit are complete | Check, Plan, Gatekeeping, and Execute buttons are mixed in the same block | Users only select scenarios and targets; the system automatically displays workflow stages |
| Overview | Can see the current cluster status | Easy to mistake the selected cluster as the global status | Fleet-level statistics, cluster directory, anomaly prioritization, and observation freshness |
| Topology | Master-slave relationships are intuitive | Cards are too large, connections are easily misaligned, and identity information hierarchy is confusing | Compact node alignment, stable CSS grid connections, and identity detail tables |
| Switching | Can execute a real coordinated primary and VIP switch | "Request Accepted" was once misunderstood as success, and gatekeeping concepts were exposed too much | Five-stage real-time progress; only declare completion after VERIFY passes |
| Recovery | Old master reattachment and replication recovery entries are complete | Candidate sources and reasons for inexecutable actions are not intuitive enough | Only display recoverable objects within the cluster and directly explain blocking reasons |
| Node Lifecycle | Installation, synchronization, and recovery have a unified entry | Large forms are expanded at once, mixing daily browsing and high-risk inputs | List priority, action forms grouped, capabilities and constraints explained on-site |
| Metadata | Supports correction of hostnames, IPs, and ports | Mutable endpoints and immutable identities are easily confused | Pop-up clearly partitioned, identity is read-only, and changes only affect endpoints |
| Logs | Has raw returns | Raw JSON dominates the visual, and filtering capabilities are insufficient | Human-readable summary, status filtering, and raw evidence is collapsed by default |
| Visual | High functional density | Red and green thick borders, large buttons, card nesting, and misaligned seams | Light borders, 8px small rounded corners, unified 40px controls and 8px grid |
| Internationalization | Chinese and English entries exist | Only navigation is translated, while body content mixes languages | All user-visible static text and dynamic states have a unified translation entry |

## Solution Comparison

### Solution A: Only Change Color and Spacing

Lowest risk, but cannot resolve operational status ambiguity, overview misguidance, and complex form issues, and cannot achieve "superiority." Not adopted.

### Solution B: Redesign the Frontend and Introduce a Build Toolchain

Best for long-term modularity, but will immediately increase Node build, static resource caching, and offline installation complexity. Not adopted at this stage.

### Solution C: Retain Embedded Single-Page Delivery, Refactor Information Architecture and State Model

Retain the current single-binary, no CDN, no external resources delivery advantages, and establish clear layout components, state semantics, and rendering boundaries within the existing self-contained page. Risk is controllable, and can quickly improve real operations experience. Adopted at this stage.

## Information Architecture

### 1. Overview

- Fleet Summary: Clusters, health, anomalies, instances, control nodes, pending operations.
- Cluster Directory: Cluster name, engine, master, number of instances, risk, observation time.
- Supports keyword and health status filtering.
- Clicking on a cluster row selects the cluster and enters the topology, without relying on a hidden current selection.

### 2. Topology

- Top displays cluster name, engine, observation time, and number of anomalies.
- Master on the left, replication nodes on the right, using stable grids and row-by-row connection lines.
- Node cards only retain three lines: fixed node name/endpoint/delay; host/IP/port; version/role.
- Master card displays VIP; candidate card displays "Preferred Candidate."
- Complete resource_id, native identity, and endpoint information remain in the identity table below.
- Metadata modification entry is fixed in the top-right corner of the topology, with pop-up distinguishing immutable identity and mutable endpoint.

### 3. Operations

- Top context only displays: cluster, current master, candidate master, VIP, delay.
- Local anti-misoperation buttons only unlock the UI, not impersonating backend Operation Lock.
- Master switch has only one execute button.
- After clicking, automatically display: pre-check, plan, safety gatekeeping, execution, and verification five stages.
- Only show "Switch Successful" after backend returns `succeeded` and verification is complete.
- `blocked`, `unsupported`, `indeterminate`, and network interruption are displayed separately, not uniformly packaged as failure or success.
- Old master reattachment and replication recovery are independent, and candidates are strictly limited to the current cluster.

### 4. Nodes

- Node list prioritizes displaying fixed node name, resource ID, type, endpoint, and status.
- Adding and rebuilding share a single process, but actions, fixed identity, connection information, and database parameters are grouped.
- Rebuilding must reuse resource ID and fixed node name.
- Control node final count must be an odd number not less than 3; data node count is unrestricted.
- Task progress is expressed as a timeline, with reports and failure stages visible.

### 5. Metrics

- Only display real-time API sampling, not fabricated historical curves.
- Summary and instance details use the same metric names and units.
- Clearly indicate observation time; missing sampling displays "No Data," not 0.

### 6. Operation Logs

- Provide operation type, status, and keyword filtering.
- Default display time, cluster, source node, target node, type, and status.
- Original workflow objects are collapsed by default; report links and stage timelines appear when data is present.

### 7. Settings and About

- Control tokens and approval tokens only exist in page memory.
- Language and refresh interval are current session preferences, not written to browser persistent storage.
- The About page clearly states the four first-class database adapters and their real capabilities.

## State Semantics

| State | UI Semantics |
| --- | --- |
| idle | Not executed yet |
| running | Request in progress, button disabled |
| succeeded | Backend execution and VERIFY both passed |
| blocked | Blocked by Safety Guard, Lock, or Approval, reason provided |
| unsupported | Adapter not implemented, no fake execution provided |
| indeterminate | Execution result is uncertain, automatic retry of high-risk actions prohibited |
| failed | Clear failure evidence present |

High-risk switching only allows limited rebuild plan retries for "outdated plans"; network interruption or uncertain results must not be automatically repeated.

## Visual System

- 8px base spacing; maximum card corner radius 8px.
- Sidebar 224px, content maximum width 1600px.
- Main operation color uses dark green; danger is only used for badges and clear failure states.
- No gradients, decorative balls, thick red/green card outlines, or large colored buttons.
- Titles, tables, and work areas use compact font sizes, no marketing-style large titles.
- All dynamic long identities use ellipsis or safe line breaks, not pushing layout width.

## Responsive Rules

- `>= 1280px`: Full sidebar, dual-column workstation, master-slave horizontal topology.
- `900px - 1279px`: Status bar line wrap, operation cards still keep clear grouping.
- `< 900px`: Sidebar becomes horizontal navigation, topology becomes vertical and hides decorative connections.
- `< 680px`: Summary two columns, tables placed in their own horizontal scrolling containers, page itself does not horizontally scroll.

## Security and Accessibility

- API return content is only written to DOM through `textContent`.
- Credentials do not enter URL, logs, DOM dataset, or persistent storage.
- Action results use `aria-live`; all buttons, dropdowns, and details support keyboard focus.
- Unlock is a one-time state, automatically locked back after switch success or cluster change.
- Cluster changes cancel stale request rendering, preventing data from one cluster from appearing in another cluster's view.

## Acceptable Superiority Criteria

1. Overview does not render topology and can aggregate all registered clusters.
2. Switching area has only one real switching entry and displays five-stage progress.
3. Uncertain results do not display success and do not automatically repeat high-risk execution.
4. Topology connection lines and node cards have definite layout rules at desktop and mobile breakpoints.
5. All dynamic cluster data includes observation time or clearly states "No Evidence."
6. Operation logs can be filtered and original evidence is collapsed by default.
7. Nodes, metadata, metrics, and logs all use real APIs, no simulated actions are provided.
8. Page has no old product names, no external frontend dependencies, and no browser persistent secrets.
