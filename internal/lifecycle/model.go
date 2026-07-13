package lifecycle

import (
	"time"

	"clusterguard.io/ha/pkg/model"
)

type Action string

const (
	ActionAdd     Action = "add"
	ActionRebuild Action = "rebuild"
)

type SyncMethod string

const (
	SyncAuto        SyncMethod = "auto"
	SyncClone       SyncMethod = "clone"
	SyncXtraBackup  SyncMethod = "xtrabackup"
	SyncLogicalDump SyncMethod = "logical_dump"
)

type Target struct {
	NodeID       model.ResourceID `json:"node_id,omitempty"`
	NodeName     string           `json:"node_name"`
	Kind         model.NodeKind   `json:"kind"`
	Hostname     string           `json:"hostname"`
	IPAddress    string           `json:"ip_address,omitempty"`
	SSHPort      int              `json:"ssh_port,omitempty"`
	MySQLVersion string           `json:"mysql_version,omitempty"`
	MySQLPort    int              `json:"mysql_port,omitempty"`
	ServerID     uint64           `json:"server_id,omitempty"`
	PackageName  string           `json:"package_name,omitempty"`
	Rebuild      bool             `json:"rebuild,omitempty"`
}

type Request struct {
	ClusterID              model.ResourceID `json:"cluster_id"`
	Action                 Action           `json:"action"`
	Targets                []Target         `json:"targets"`
	SyncMethod             SyncMethod       `json:"sync_method"`
	CurrentControllerCount int              `json:"current_controller_count,omitempty"`
	RequestedBy            string           `json:"requested_by,omitempty"`
}

type ExecutionSecrets struct {
	SSHPassword         string `json:"-"`
	MySQLRootPassword   string `json:"-"`
	ReplicationPassword string `json:"-"`
}

type Capabilities struct {
	SourceVersion      string          `json:"source_version,omitempty"`
	CloneAvailable     bool            `json:"clone_available"`
	XtraBackupVersions map[string]bool `json:"xtrabackup_versions,omitempty"`
	LogicalDumpAllowed bool            `json:"logical_dump_allowed"`
}

type TargetPlan struct {
	Target
	SyncMethod     SyncMethod         `json:"sync_method,omitempty"`
	SyncReason     string             `json:"sync_reason,omitempty"`
	DatabaseRole   model.InstanceRole `json:"database_role,omitempty"`
	ReusesNodeSlot bool               `json:"reuses_node_slot"`
}

type Plan struct {
	ClusterID            model.ResourceID `json:"cluster_id"`
	Action               Action           `json:"action"`
	Targets              []TargetPlan     `json:"targets"`
	FinalControllerCount int              `json:"final_controller_count,omitempty"`
	Checks               []model.Check    `json:"checks"`
	Blocked              bool             `json:"blocked"`
	CreatedAt            time.Time        `json:"created_at"`
}
