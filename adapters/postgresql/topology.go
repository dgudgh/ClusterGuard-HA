package postgresql

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"clusterguard.io/ha/pkg/adapter"
	"clusterguard.io/ha/pkg/identity"
	"clusterguard.io/ha/pkg/model"
)

type walSenderEvidence struct {
	ApplicationName string `json:"application_name"`
	State           string `json:"state"`
	ClientAddress   string `json:"client_addr"`
	ReplayLSN       string `json:"replay_lsn"`
}

type streamingEvidence struct {
	SenderHost string
	SenderPort int
	Senders    []walSenderEvidence
}

func parseStreamingEvidence(row Row) (*streamingEvidence, error) {
	evidence := &streamingEvidence{SenderHost: strings.TrimSpace(row["receiver_sender_host"])}
	if port := strings.TrimSpace(row["receiver_sender_port"]); port != "" {
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed <= 0 || parsed > 65535 {
			return nil, fmt.Errorf("invalid PostgreSQL receiver sender port")
		}
		evidence.SenderPort = parsed
	}
	if senders := strings.TrimSpace(row["replication_senders"]); senders != "" {
		if err := json.Unmarshal([]byte(senders), &evidence.Senders); err != nil {
			return nil, fmt.Errorf("invalid PostgreSQL WAL sender evidence")
		}
	}
	return evidence, nil
}

// ResolveTopologyObservations uses both ends of the live replication connection.
// Bootstrap GUCs cannot override or substitute for this same-round evidence.
func (a *Adapter) ResolveTopologyObservations(observations []adapter.TopologyObservation) {
	endpointsByIdentity := make(map[string]map[string]struct{})
	primaryKeys := make(map[string]struct{})
	for _, observation := range observations {
		instance := observation.Discovery.Instance
		if key, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity); err == nil {
			if endpointsByIdentity[key] == nil {
				endpointsByIdentity[key] = make(map[string]struct{})
			}
			endpointsByIdentity[key][observedEndpointKey(observation.Endpoint)] = struct{}{}
			if instance.Role == model.RolePrimary {
				primaryKeys[key] = struct{}{}
			}
		}
	}
	// Different registered network endpoints are not proven aliases merely because
	// cloned configurations report the same UUID. Keep that ambiguity fail-closed.
	for index := range observations {
		instance := &observations[index].Discovery.Instance
		key, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity)
		if err == nil && len(endpointsByIdentity[key]) > 1 {
			instance.Health.State = model.HealthDegraded
			instance.Health.Summary = "PostgreSQL native identity is shared by distinct endpoints"
			instance.PromotionEligible = false
			if instance.EngineMetadata == nil {
				instance.EngineMetadata = make(map[string]string)
			}
			instance.EngineMetadata["topology_identity_conflict"] = "duplicate_native_identity"
		}
	}
	for index := range observations {
		observation := &observations[index]
		instance := &observation.Discovery.Instance
		observation.Topology.Links = []adapter.TopologyLink{}
		if instance.Role != model.RoleStandby {
			continue
		}
		instance.Replication.SourceIdentity = nil
		instance.PromotionEligible = false
		instance.Health.State = model.HealthDegraded
		instance.Health.Summary = "PostgreSQL standby lacks verified upstream streaming identity"
		if instance.EngineMetadata["topology_identity_conflict"] != "" {
			instance.Health.Summary = "PostgreSQL native identity is shared by distinct endpoints"
			continue
		}
		evidence, ok := observation.Discovery.TopologyEvidence.(*streamingEvidence)
		if !ok || evidence == nil || len(primaryKeys) != 1 || !safeStreamingStandby(*instance) {
			continue
		}
		targetKey, err := identity.InstanceKey(instance.Engine, instance.EngineIdentity)
		if err != nil {
			continue
		}
		var source *model.DatabaseInstance
		sourceKey := ""
		ambiguous := false
		for sourceIndex := range observations {
			candidate := &observations[sourceIndex]
			primary := &candidate.Discovery.Instance
			if primary.Role != model.RolePrimary || primary.Health.State != model.HealthHealthy ||
				primary.EngineMetadata["in_recovery"] != "false" || primary.EngineMetadata["transaction_read_only"] != "false" ||
				primary.ClusterID != instance.ClusterID || primary.EngineIdentity["system_identifier"] != instance.EngineIdentity["system_identifier"] ||
				primary.EngineMetadata["timeline_id"] != instance.EngineMetadata["timeline_id"] {
				continue
			}
			key, err := identity.InstanceKey(primary.Engine, primary.EngineIdentity)
			if err != nil || key == targetKey || !receiverMatchesEndpoint(evidence, candidate.Endpoint) {
				continue
			}
			upstream, ok := candidate.Discovery.TopologyEvidence.(*streamingEvidence)
			if !ok || upstream == nil || !uniqueStreamingSender(upstream.Senders, instance.EngineIdentity["resource_id"]) {
				continue
			}
			if source != nil && sourceKey != key {
				ambiguous = true
				break
			}
			source, sourceKey = primary, key
		}
		if source == nil || ambiguous {
			continue
		}
		instance.Replication.SourceIdentity = source.EngineIdentity.Clone()
		instance.Health.State = model.HealthHealthy
		instance.Health.Summary = "PostgreSQL standby upstream identity and streaming verified at both peers"
		instance.PromotionEligible = true
		observation.Topology.Links = []adapter.TopologyLink{{
			SourceIdentity: source.EngineIdentity.Clone(), TargetIdentity: instance.EngineIdentity.Clone(),
			Healthy: true, LagSeconds: cloneInt64(instance.Replication.LagSeconds),
		}}
	}
}

func observedEndpointKey(endpoint adapter.Endpoint) string {
	host := strings.TrimSpace(endpoint.IPAddress)
	if host == "" {
		host = strings.TrimSpace(endpoint.Hostname)
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(strings.ToLower(strings.TrimSuffix(host, ".")), strconv.Itoa(endpoint.Port))
}

func safeStreamingStandby(instance model.DatabaseInstance) bool {
	if instance.EngineMetadata["in_recovery"] != "true" || instance.EngineMetadata["transaction_read_only"] != "true" ||
		instance.EngineMetadata["replay_paused"] != "false" || instance.EngineMetadata["wal_receiver_status"] != "streaming" ||
		instance.Replication.IOThread != model.ThreadRunning || instance.Replication.SQLThread != model.ThreadRunning {
		return false
	}
	timeline, err := strconv.ParseUint(instance.EngineMetadata["timeline_id"], 10, 32)
	if err != nil || timeline == 0 || instance.Replication.ExecutedPosition == "" {
		return false
	}
	_, err = parseLSN(instance.Replication.ExecutedPosition)
	return err == nil
}

func receiverMatchesEndpoint(receiver *streamingEvidence, endpoint adapter.Endpoint) bool {
	if receiver.SenderPort != endpoint.Port || receiver.SenderHost == "" {
		return false
	}
	for _, host := range []string{endpoint.IPAddress, endpoint.Hostname} {
		if host == "" {
			continue
		}
		if ip, peer := net.ParseIP(receiver.SenderHost), net.ParseIP(host); ip != nil && peer != nil && ip.Equal(peer) {
			return true
		}
		if strings.EqualFold(strings.TrimSuffix(receiver.SenderHost, "."), strings.TrimSuffix(host, ".")) {
			return true
		}
	}
	return false
}

func uniqueStreamingSender(senders []walSenderEvidence, nativeID string) bool {
	matches := 0
	streaming := false
	for _, sender := range senders {
		if !strings.EqualFold(strings.TrimSpace(sender.ApplicationName), nativeID) {
			continue
		}
		matches++
		_, err := parseLSN(sender.ReplayLSN)
		streaming = sender.State == "streaming" && sender.ReplayLSN != "" && err == nil
	}
	return matches == 1 && streaming
}
