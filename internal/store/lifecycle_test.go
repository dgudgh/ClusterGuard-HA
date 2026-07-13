package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

func TestLifecycleTaskIsDurableAndContainsNoExecutionSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	task := lifecycle.Task{
		ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(),
		Status: lifecycle.TaskQueued, Request: lifecycle.Request{ClusterID: model.NewResourceID(), Action: lifecycle.ActionAdd, RequestedBy: "dba"},
		LogTail: []string{"credentials accepted without persistence"},
	}
	stored, err := repository.PutLifecycleTask(task)
	if err != nil {
		t.Fatalf("store lifecycle task: %v", err)
	}
	if stored.MetadataRevision != 1 {
		t.Fatalf("stored task=%+v", stored)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	got, found := reopened.LifecycleTask(task.ResourceID)
	if !found || got.ResourceID != task.ResourceID || got.Status != lifecycle.TaskQueued {
		t.Fatalf("durable task=%+v found=%t", got, found)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	for _, secret := range []string{"ssh-secret", "mysql-root-secret", "replication-secret"} {
		if strings.Contains(string(contents), secret) {
			t.Fatalf("metadata persisted execution secret %q", secret)
		}
	}
}

func TestLifecycleTasksAreListedNewestFirst(t *testing.T) {
	repository := NewMemory()
	first, err := repository.PutLifecycleTask(lifecycle.Task{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Status: lifecycle.TaskPlanned})
	if err != nil {
		t.Fatalf("store first task: %v", err)
	}
	second, err := repository.PutLifecycleTask(lifecycle.Task{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, ClusterID: model.NewResourceID(), Status: lifecycle.TaskQueued})
	if err != nil {
		t.Fatalf("store second task: %v", err)
	}
	tasks := repository.LifecycleTasks()
	if len(tasks) != 2 || tasks[0].ResourceID != second.ResourceID || tasks[1].ResourceID != first.ResourceID {
		t.Fatalf("task order=%+v", tasks)
	}
}
