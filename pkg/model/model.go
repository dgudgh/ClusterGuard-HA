package model

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

type ResourceID string

func NewResourceID() ResourceID {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("generate resource ID: %v", err))
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return ResourceID(fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]))
}

func ValidResourceID(id ResourceID) bool {
	value := string(id)
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

type Engine string

const (
	EngineMySQL      Engine = "mysql"
	EnginePostgreSQL Engine = "postgresql"
	EngineOracle     Engine = "oracle"
	EngineSQLServer  Engine = "sqlserver"
)

func SupportedEngines() []Engine {
	return []Engine{EngineMySQL, EnginePostgreSQL, EngineOracle, EngineSQLServer}
}

func (engine Engine) Valid() bool {
	for _, candidate := range SupportedEngines() {
		if engine == candidate {
			return true
		}
	}
	return false
}

type EngineIdentity map[string]string

func (identity EngineIdentity) Clone() EngineIdentity {
	result := make(EngineIdentity, len(identity))
	for key, value := range identity {
		result[key] = value
	}
	return result
}

type ResourceMeta struct {
	ResourceID       ResourceID `json:"resource_id"`
	MetadataRevision uint64     `json:"metadata_revision"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Platform struct {
	ResourceMeta
	DisplayName string `json:"display_name"`
}

type Controller struct {
	ResourceMeta
	PlatformID  ResourceID `json:"platform_id"`
	DisplayName string     `json:"display_name"`
	EndpointID  ResourceID `json:"endpoint_id,omitempty"`
	Healthy     bool       `json:"healthy"`
}

type DatabaseCluster struct {
	ResourceMeta
	PlatformID     ResourceID     `json:"platform_id"`
	Engine         Engine         `json:"engine"`
	EngineIdentity EngineIdentity `json:"engine_identity"`
	DisplayName    string         `json:"display_name"`
	Health         Health         `json:"health"`
	RecoveryFreeze bool           `json:"recovery_freeze"`
}

type NodeKind string

const (
	NodeData       NodeKind = "data"
	NodeController NodeKind = "controller"
	NodeMixed      NodeKind = "mixed"
)

func (kind NodeKind) Valid() bool {
	return kind == NodeData || kind == NodeController || kind == NodeMixed
}

type DatabaseNode struct {
	ResourceMeta
	PlatformID  ResourceID `json:"platform_id,omitempty"`
	NodeName    string     `json:"node_name"`
	DisplayName string     `json:"display_name"`
	Hostname    string     `json:"hostname,omitempty"`
	IPAddress   string     `json:"ip_address,omitempty"`
	Aliases     []string   `json:"aliases,omitempty"`
	Kind        NodeKind   `json:"kind"`
	HostClass   string     `json:"host_class,omitempty"`
	Active      bool       `json:"active"`
}

type RuntimeKind string

const (
	RuntimeLinux      RuntimeKind = "linux"
	RuntimeDocker     RuntimeKind = "docker"
	RuntimeKubernetes RuntimeKind = "kubernetes"
)

func (kind RuntimeKind) Valid() bool {
	return kind == RuntimeLinux || kind == RuntimeDocker || kind == RuntimeKubernetes
}

// RuntimeTarget identifies a control boundary such as a Linux host, Docker
// engine, or Kubernetes API. Credentials are referenced, never embedded.
type RuntimeTarget struct {
	ResourceMeta
	DisplayName   string            `json:"display_name"`
	Kind          RuntimeKind       `json:"kind"`
	Endpoint      string            `json:"endpoint,omitempty"`
	CredentialRef string            `json:"credential_ref,omitempty"`
	TLSProfile    string            `json:"tls_profile,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Active        bool              `json:"active"`
}

type DockerWorkloadRef struct {
	SwarmServiceID   string `json:"swarm_service_id,omitempty"`
	SwarmServiceName string `json:"swarm_service_name"`
	NodeID           string `json:"node_id,omitempty"`
	ContainerName    string `json:"container_name,omitempty"`
	ContainerID      string `json:"container_id,omitempty"`
	VolumeIdentity   string `json:"volume_identity,omitempty"`
}

type KubernetesWorkloadRef struct {
	ClusterName string `json:"cluster_name"`
	Namespace   string `json:"namespace"`
	StatefulSet string `json:"stateful_set"`
	Ordinal     int    `json:"ordinal"`
	PodName     string `json:"pod_name,omitempty"`
	PodUID      string `json:"pod_uid,omitempty"`
	NodeName    string `json:"node_name,omitempty"`
	PVCUID      string `json:"pvc_uid,omitempty"`
}

// WorkloadBinding keeps an immutable database resource separate from its
// replaceable process/container/pod placement.
type WorkloadBinding struct {
	ResourceMeta
	InstanceID        ResourceID             `json:"instance_id"`
	RuntimeTargetID   ResourceID             `json:"runtime_target_id"`
	RuntimeKind       RuntimeKind            `json:"runtime_kind"`
	HostNodeID        ResourceID             `json:"host_node_id,omitempty"`
	Docker            *DockerWorkloadRef     `json:"docker,omitempty"`
	Kubernetes        *KubernetesWorkloadRef `json:"kubernetes,omitempty"`
	ObservedRuntimeID string                 `json:"observed_runtime_id,omitempty"`
	Generation        uint64                 `json:"generation"`
	ObservedAt        time.Time              `json:"observed_at,omitempty"`
	Active            bool                   `json:"active"`
}

type InstanceRole string

const (
	RoleUnknown InstanceRole = "unknown"
	RolePrimary InstanceRole = "primary"
	RoleReplica InstanceRole = "replica"
	RoleStandby InstanceRole = "standby"
)

type HealthState string

const (
	HealthUnknown   HealthState = "unknown"
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

type Health struct {
	State       HealthState `json:"state"`
	Summary     string      `json:"summary,omitempty"`
	ObservedAt  time.Time   `json:"observed_at,omitempty"`
	LatencyMS   int64       `json:"latency_ms,omitempty"`
	Replication string      `json:"replication,omitempty"`
}

type DatabaseInstance struct {
	ResourceMeta
	ClusterID         ResourceID        `json:"cluster_id"`
	NodeID            ResourceID        `json:"node_id,omitempty"`
	Engine            Engine            `json:"engine"`
	EngineIdentity    EngineIdentity    `json:"engine_identity"`
	DisplayName       string            `json:"display_name"`
	Hostname          string            `json:"hostname"`
	IPAddress         string            `json:"ip_address"`
	Port              int               `json:"port"`
	Aliases           []string          `json:"aliases,omitempty"`
	Role              InstanceRole      `json:"role"`
	Health            Health            `json:"health"`
	Replication       ReplicationStatus `json:"replication"`
	Maintenance       bool              `json:"maintenance"`
	PromotionEligible bool              `json:"promotion_eligible"`
	EngineMetadata    map[string]string `json:"engine_metadata"`
}

type EndpointKind string

const (
	EndpointDatabase EndpointKind = "database"
	EndpointVIP      EndpointKind = "vip"
	EndpointListener EndpointKind = "listener"
	EndpointService  EndpointKind = "service"
)

type EndpointProviderKind string

const (
	EndpointProviderLinuxVIP          EndpointProviderKind = "linux_vip"
	EndpointProviderKubernetesService EndpointProviderKind = "kubernetes_service"
)

func (kind EndpointProviderKind) Valid() bool {
	return kind == EndpointProviderLinuxVIP || kind == EndpointProviderKubernetesService
}

type Endpoint struct {
	ResourceMeta
	ClusterID  ResourceID   `json:"cluster_id"`
	InstanceID ResourceID   `json:"instance_id,omitempty"`
	Kind       EndpointKind `json:"kind"`
	Hostname   string       `json:"hostname,omitempty"`
	IPAddress  string       `json:"ip_address,omitempty"`
	Port       int          `json:"port,omitempty"`
	Active     bool         `json:"active"`
}

type EndpointAlias struct {
	ResourceMeta
	EndpointID ResourceID `json:"endpoint_id"`
	Alias      string     `json:"alias"`
	RetiredAt  time.Time  `json:"retired_at,omitempty"`
}

type ReplicationLink struct {
	ResourceMeta
	ClusterID        ResourceID `json:"cluster_id"`
	SourceInstanceID ResourceID `json:"source_instance_id"`
	TargetInstanceID ResourceID `json:"target_instance_id"`
	Healthy          bool       `json:"healthy"`
	LagSeconds       *int64     `json:"lag_seconds,omitempty"`
}

type HAEndpoint struct {
	ResourceMeta
	ClusterID   ResourceID           `json:"cluster_id"`
	EndpointID  ResourceID           `json:"endpoint_id"`
	Kind        EndpointKind         `json:"kind"`
	DesiredRole InstanceRole         `json:"desired_role"`
	OwnerID     ResourceID           `json:"owner_id,omitempty"`
	Interface   string               `json:"interface,omitempty"`
	Prefix      int                  `json:"prefix,omitempty"`
	Provider    EndpointProviderKind `json:"provider,omitempty"`
	ProviderRef string               `json:"provider_ref,omitempty"`
	Healthy     bool                 `json:"healthy"`
}

func EndpointAddress(hostname string, ipAddress string, port int) []string {
	values := make([]string, 0, 2)
	if hostname = strings.TrimSpace(hostname); hostname != "" && port > 0 {
		values = append(values, fmt.Sprintf("%s:%d", hostname, port))
	}
	if ipAddress = strings.TrimSpace(ipAddress); ipAddress != "" && port > 0 {
		values = append(values, fmt.Sprintf("%s:%d", ipAddress, port))
	}
	return values
}
