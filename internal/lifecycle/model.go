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
	SyncAuto                 SyncMethod = "auto"
	SyncClone                SyncMethod = "clone"
	SyncXtraBackup           SyncMethod = "xtrabackup"
	SyncLogicalDump          SyncMethod = "logical_dump"
	SyncPostgreSQLRewind     SyncMethod = "pg_rewind"
	SyncPostgreSQLBaseBackup SyncMethod = "pg_basebackup"
)

type Target struct {
	NodeID                  model.ResourceID `json:"node_id,omitempty"`
	NodeName                string           `json:"node_name"`
	Kind                    model.NodeKind   `json:"kind"`
	Hostname                string           `json:"hostname"`
	IPAddress               string           `json:"ip_address,omitempty"`
	SSHUser                 string           `json:"ssh_user,omitempty"`
	SSHPort                 int              `json:"ssh_port,omitempty"`
	MySQLVersion            string           `json:"mysql_version,omitempty"`
	MySQLPort               int              `json:"mysql_port,omitempty"`
	PostgreSQLVersion       string           `json:"postgresql_version,omitempty"`
	PostgreSQLPort          int              `json:"postgresql_port,omitempty"`
	PostgreSQLService       string           `json:"postgresql_service,omitempty"`
	PostgreSQLDataDirectory string           `json:"postgresql_data_directory,omitempty"`
	ServerID                uint64           `json:"server_id,omitempty"`
	PackageName             string           `json:"package_name,omitempty"`
	Rebuild                 bool             `json:"rebuild,omitempty"`
}

type Donor struct {
	InstanceID       model.ResourceID `json:"instance_id,omitempty"`
	NativeResourceID model.ResourceID `json:"native_resource_id,omitempty"`
	Hostname         string           `json:"hostname,omitempty"`
	IPAddress        string           `json:"ip_address,omitempty"`
	Port             int              `json:"port,omitempty"`
	ServerUUID       string           `json:"server_uuid,omitempty"`
	SystemIdentifier string           `json:"system_identifier,omitempty"`
	Version          string           `json:"version,omitempty"`
}

type Request struct {
	ClusterID              model.ResourceID `json:"cluster_id"`
	Engine                 model.Engine     `json:"engine,omitempty"`
	Action                 Action           `json:"action"`
	Targets                []Target         `json:"targets"`
	Donor                  Donor            `json:"donor,omitempty"`
	VIP                    string           `json:"vip,omitempty"`
	SyncMethod             SyncMethod       `json:"sync_method"`
	CurrentControllerCount int              `json:"current_controller_count,omitempty"`
	RequestedBy            string           `json:"requested_by,omitempty"`
}

type ExecutionSecrets struct {
	SSHPassword                   string `json:"-"`
	MySQLRootPassword             string `json:"-"`
	MySQLDiscoveryUsername        string `json:"-"`
	MySQLDiscoveryPassword        string `json:"-"`
	MySQLOperationUsername        string `json:"-"`
	MySQLOperationPassword        string `json:"-"`
	MySQLReplicationUsername      string `json:"-"`
	ReplicationPassword           string `json:"-"`
	PostgreSQLAdminPassword       string `json:"-"`
	PostgreSQLReplicationPassword string `json:"-"`
}

type Capabilities struct {
	SourceVersion                 string          `json:"source_version,omitempty"`
	CloneAvailable                bool            `json:"clone_available"`
	XtraBackupVersions            map[string]bool `json:"xtrabackup_versions,omitempty"`
	LogicalDumpAllowed            bool            `json:"logical_dump_allowed"`
	PostgreSQLBaseBackupAvailable bool            `json:"postgresql_basebackup_available"`
	PostgreSQLRewindAvailable     bool            `json:"postgresql_rewind_available"`
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

type TaskStatus string

const (
	TaskPlanned       TaskStatus = "planned"
	TaskQueued        TaskStatus = "queued"
	TaskRunning       TaskStatus = "running"
	TaskVerifying     TaskStatus = "verifying"
	TaskSucceeded     TaskStatus = "succeeded"
	TaskFailed        TaskStatus = "failed"
	TaskInterrupted   TaskStatus = "interrupted"
	TaskIndeterminate TaskStatus = "indeterminate"
)

func (status TaskStatus) Valid() bool {
	switch status {
	case TaskPlanned, TaskQueued, TaskRunning, TaskVerifying, TaskSucceeded, TaskFailed, TaskInterrupted, TaskIndeterminate:
		return true
	default:
		return false
	}
}

type Stage string

const (
	StagePreflight   Stage = "preflight"
	StageInstall     Stage = "install"
	StageSynchronize Stage = "synchronize"
	StageConfigure   Stage = "configure_replication"
	StageVerify      Stage = "verify"
	StageCommit      Stage = "metadata_commit"
)

type StageStatus string

const (
	StagePending   StageStatus = "pending"
	StageRunning   StageStatus = "running"
	StageSucceeded StageStatus = "succeeded"
	StageFailed    StageStatus = "failed"
)

func (status StageStatus) Valid() bool {
	return status == StagePending || status == StageRunning || status == StageSucceeded || status == StageFailed
}

func (stage Stage) Valid() bool {
	switch stage {
	case StagePreflight, StageInstall, StageSynchronize, StageConfigure, StageVerify, StageCommit:
		return true
	default:
		return false
	}
}

type StageState struct {
	Stage     Stage       `json:"stage"`
	Status    StageStatus `json:"status"`
	Message   string      `json:"message,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type Event struct {
	Stage   Stage       `json:"stage"`
	Status  StageStatus `json:"status"`
	Message string      `json:"message,omitempty"`
}

type ExecutionResult struct {
	Verified  bool                     `json:"verified"`
	Instances []model.DatabaseInstance `json:"instances,omitempty"`
	Checks    []model.Check            `json:"checks,omitempty"`
	Message   string                   `json:"message,omitempty"`
}

type Task struct {
	model.ResourceMeta
	ClusterID    model.ResourceID `json:"cluster_id"`
	OperationID  model.ResourceID `json:"operation_id,omitempty"`
	Request      Request          `json:"request"`
	Plan         Plan             `json:"plan"`
	Status       TaskStatus       `json:"status"`
	CurrentStage Stage            `json:"current_stage,omitempty"`
	Stages       []StageState     `json:"stages,omitempty"`
	Checks       []model.Check    `json:"checks,omitempty"`
	LogTail      []string         `json:"log_tail,omitempty"`
	Message      string           `json:"message,omitempty"`
	ReportID     model.ResourceID `json:"report_id,omitempty"`
}
