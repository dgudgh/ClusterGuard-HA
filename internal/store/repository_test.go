package store

import (
	"path/filepath"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func mysqlInstance(clusterID model.ResourceID, hostname string, ipAddress string, port int) model.DatabaseInstance {
	return model.DatabaseInstance{
		ClusterID: clusterID,
		Engine:    model.EngineMySQL,
		EngineIdentity: model.EngineIdentity{
			"server_uuid": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		},
		DisplayName: hostname,
		Hostname:    hostname,
		IPAddress:   ipAddress,
		Port:        port,
		Role:        model.RoleReplica,
		Health:      model.Health{State: model.HealthHealthy},
	}
}

func TestReconcileInstanceKeepsResourceIDWhenMySQLHostnameChanges(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	first, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306))
	if err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	second, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-renamed", "192.0.2.20", 3310))
	if err != nil {
		t.Fatalf("second reconciliation: %v", err)
	}
	if second.Instance.ResourceID != first.Instance.ResourceID {
		t.Fatalf("same MySQL identity created a new resource: first=%s second=%s", first.Instance.ResourceID, second.Instance.ResourceID)
	}
	if second.Instance.MetadataRevision != first.Instance.MetadataRevision+1 {
		t.Fatalf("expected metadata revision increment: first=%d second=%d", first.Instance.MetadataRevision, second.Instance.MetadataRevision)
	}
	if second.Instance.Hostname != "mysql-renamed" || second.Instance.IPAddress != "192.0.2.20" || second.Instance.Port != 3310 {
		t.Fatalf("mutable endpoint was not updated: %+v", second.Instance)
	}
	aliases := second.Instance.Aliases
	if !contains(aliases, "mysql-a:3306") || !contains(aliases, "192.0.2.10:3306") {
		t.Fatalf("previous endpoint was not retained as aliases: %v", aliases)
	}
	instances := repository.Instances(clusterID)
	if len(instances) != 1 {
		t.Fatalf("same engine identity must leave one instance, got %d", len(instances))
	}
}

func TestFileRepositoryPersistsReconciledIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	clusterID := model.NewResourceID()
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	result, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload repository: %v", err)
	}
	instances := reloaded.Instances(clusterID)
	if len(instances) != 1 || instances[0].ResourceID != result.Instance.ResourceID {
		t.Fatalf("persisted identity was not restored: %+v", instances)
	}
}

func TestReconcileRejectsEndpointOwnedByDifferentIdentity(t *testing.T) {
	repository := NewMemory()
	clusterID := model.NewResourceID()
	if _, err := repository.ReconcileInstance(mysqlInstance(clusterID, "mysql-a", "192.0.2.10", 3306)); err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	conflict := mysqlInstance(clusterID, "mysql-b", "192.0.2.10", 3306)
	conflict.EngineIdentity["server_uuid"] = "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"
	if _, err := repository.ReconcileInstance(conflict); err == nil {
		t.Fatalf("endpoint collision with a distinct engine identity must fail")
	}
}

func TestRepositoryPersistsAuditAndReportResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	operationID := model.NewResourceID()
	repository.RecordAudit(model.AuditEvent{OperationID: operationID, Stage: model.StageExecute, Message: "executed"})
	repository.RecordReport(model.Report{OperationID: operationID, Title: "operation report", Summary: "verified"})
	reloaded, err := Open(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Audits()) != 1 || reloaded.Audits()[0].OperationID != operationID {
		t.Fatalf("audit was not persisted: %+v", reloaded.Audits())
	}
	if len(reloaded.Reports()) != 1 || reloaded.Reports()[0].OperationID != operationID {
		t.Fatalf("report was not persisted: %+v", reloaded.Reports())
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
