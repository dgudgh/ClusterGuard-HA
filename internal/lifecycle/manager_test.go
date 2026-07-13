package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type taskStoreStub struct {
	tasks      map[model.ResourceID]Task
	history    []Task
	audits     []model.AuditEvent
	reports    []model.Report
	auditErrAt model.WorkflowStage
	reportErr  error
}

func (store *taskStoreStub) PutLifecycleTask(task Task) (Task, error) {
	if task.ResourceID == "" {
		task.ResourceID = model.NewResourceID()
	}
	if task.MetadataRevision == 0 {
		task.MetadataRevision = 1
	} else {
		task.MetadataRevision++
	}
	store.tasks[task.ResourceID] = task
	store.history = append(store.history, task)
	return task, nil
}
func (store *taskStoreStub) LifecycleTask(resourceID model.ResourceID) (Task, bool) {
	task, found := store.tasks[resourceID]
	return task, found
}
func (store *taskStoreStub) LifecycleTasks() []Task {
	result := make([]Task, 0, len(store.tasks))
	for _, task := range store.tasks {
		result = append(result, task)
	}
	return result
}
func (store *taskStoreStub) RecordAudit(event model.AuditEvent) error {
	if event.Stage == store.auditErrAt {
		return errors.New("audit unavailable")
	}
	store.audits = append(store.audits, event)
	return nil
}
func (store *taskStoreStub) RecordReport(report model.Report) error {
	if store.reportErr != nil {
		return store.reportErr
	}
	store.reports = append(store.reports, report)
	return nil
}

type lifecycleAuthorityStub struct{ err error }

func (stub lifecycleAuthorityStub) RequireMutationAuthority(context.Context) error { return stub.err }

type lifecycleSafetyStub struct{ err error }

func (stub lifecycleSafetyStub) EvaluateLifecycle(context.Context, Request, Plan) error {
	return stub.err
}

type lifecycleApprovalStub struct {
	err      error
	received string
}

func (stub *lifecycleApprovalStub) ValidateLifecycle(_ context.Context, _ Request, _ Plan, token string) error {
	stub.received = token
	return stub.err
}

type lifecycleLockStub struct{ held bool }

func (lock *lifecycleLockStub) AcquireCluster(context.Context, model.ResourceID) (func(), error) {
	if lock.held {
		return nil, errors.New("locked")
	}
	lock.held = true
	return func() { lock.held = false }, nil
}

type lifecycleExecutorStub struct {
	events   []Event
	result   ExecutionResult
	err      error
	received ExecutionSecrets
	calls    int
}

func (executor *lifecycleExecutorStub) Execute(_ context.Context, _ Request, _ Plan, secrets ExecutionSecrets, emit func(Event)) (ExecutionResult, error) {
	executor.calls++
	executor.received = secrets
	for _, event := range executor.events {
		emit(event)
	}
	return executor.result, executor.err
}

type lifecycleCommitterStub struct {
	calls int
	task  Task
}

func (committer *lifecycleCommitterStub) Commit(_ context.Context, task Task, _ ExecutionResult) error {
	committer.calls++
	committer.task = task
	return nil
}

func executableLifecyclePlan() (Request, Plan) {
	request := Request{ClusterID: model.NewResourceID(), Action: ActionAdd, RequestedBy: "dba", SyncMethod: SyncAuto, Targets: []Target{lifecycleTarget("cg-data-0001", model.NodeData, "8.0.44")}}
	plan := BuildPlan(request, Capabilities{SourceVersion: "8.0.44", CloneAvailable: true})
	return request, plan
}

func TestManagerCommitsMetadataOnlyAfterVerification(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}}
	executor := &lifecycleExecutorStub{events: []Event{
		{Stage: StageInstall, Status: StageRunning, Message: "installing with ssh-secret"},
		{Stage: StageSynchronize, Status: StageSucceeded, Message: "replication-secret copied"},
	}, result: ExecutionResult{Verified: true, Message: "mysql-root-secret verification completed", Instances: []model.DatabaseInstance{{ResourceMeta: model.ResourceMeta{ResourceID: model.NewResourceID()}, Role: model.RoleReplica}}}}
	committer := &lifecycleCommitterStub{}
	approval := &lifecycleApprovalStub{}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{}, &lifecycleLockStub{}, approval, executor, committer, func() time.Time { return time.Date(2026, time.July, 13, 19, 0, 0, 0, time.UTC) })
	secrets := ExecutionSecrets{SSHPassword: "ssh-secret", MySQLRootPassword: "mysql-root-secret", ReplicationPassword: "replication-secret"}
	task, err := manager.Execute(context.Background(), request, plan, secrets, "approval-secret")
	if err != nil || task.Status != TaskSucceeded || committer.calls != 1 {
		t.Fatalf("execute task=%+v commit_calls=%d err=%v", task, committer.calls, err)
	}
	if executor.received != secrets {
		t.Fatal("executor did not receive transient secrets")
	}
	if approval.received != "approval-secret" {
		t.Fatalf("approval token was not checked: %q", approval.received)
	}
	for _, line := range task.LogTail {
		if strings.Contains(line, "ssh-secret") || strings.Contains(line, "mysql-root-secret") || strings.Contains(line, "replication-secret") {
			t.Fatalf("task persisted secret in log: %q", line)
		}
	}
	for _, persisted := range store.history {
		if strings.Contains(persisted.Message, "ssh-secret") || strings.Contains(persisted.Message, "mysql-root-secret") || strings.Contains(persisted.Message, "replication-secret") {
			t.Fatalf("task history persisted secret: %+v", persisted)
		}
	}
	if committer.task.Status != TaskVerifying {
		t.Fatalf("metadata committed outside verified stage: %+v", committer.task)
	}
	wantStages := []model.WorkflowStage{model.StagePrecheck, model.StageSafetyGuard, model.StageLock, model.StageApprove, model.StageExecute, model.StageVerify, model.StageAudit, model.StageReport}
	if len(store.audits) != len(wantStages) {
		t.Fatalf("lifecycle audits=%+v", store.audits)
	}
	for index, stage := range wantStages {
		if store.audits[index].Stage != stage || store.audits[index].OperationID != task.OperationID || store.audits[index].Actor != "dba" {
			t.Fatalf("lifecycle audit %d=%+v want stage=%s", index, store.audits[index], stage)
		}
	}
	if len(store.reports) != 1 || store.reports[0].OperationID != task.OperationID || store.reports[0].Status != model.OperationSucceeded || task.ReportID != store.reports[0].ResourceID {
		t.Fatalf("lifecycle report task=%+v reports=%+v", task, store.reports)
	}
}

func TestManagerDoesNotCommitMetadataWhenVerificationFails(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}}
	committer := &lifecycleCommitterStub{}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{}, &lifecycleLockStub{}, &lifecycleApprovalStub{}, &lifecycleExecutorStub{result: ExecutionResult{Verified: false}}, committer, time.Now)
	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{}, "approved")
	if err == nil || task.Status != TaskIndeterminate || committer.calls != 0 {
		t.Fatalf("unverified task=%+v commit_calls=%d err=%v", task, committer.calls, err)
	}
}

func TestManagerInterruptsBeforeMutationWithoutLeaderQuorum(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}}
	executor := &lifecycleExecutorStub{}
	manager := NewManager(store, lifecycleAuthorityStub{err: errors.New("no quorum")}, lifecycleSafetyStub{}, &lifecycleLockStub{}, &lifecycleApprovalStub{}, executor, &lifecycleCommitterStub{}, time.Now)
	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{}, "approved")
	if err == nil || task.Status != TaskInterrupted || len(executor.received.SSHPassword) != 0 {
		t.Fatalf("authority failure task=%+v err=%v", task, err)
	}
}

func TestManagerBlocksExecutorWhenSafetyGuardRejectsPlan(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}}
	executor := &lifecycleExecutorStub{}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{err: errors.New("unsafe plan")}, &lifecycleLockStub{}, &lifecycleApprovalStub{}, executor, &lifecycleCommitterStub{}, time.Now)

	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{SSHPassword: "must-not-run"}, "approved")
	if err == nil || task.Status != TaskFailed || executor.received.SSHPassword != "" || !strings.Contains(task.Message, "safety") {
		t.Fatalf("unsafe execution task=%+v executor=%+v err=%v", task, executor.received, err)
	}
}

func TestManagerBlocksExecutorWhenApprovalIsMissing(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}}
	executor := &lifecycleExecutorStub{}
	approval := &lifecycleApprovalStub{err: errors.New("approval required")}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{}, &lifecycleLockStub{}, approval, executor, &lifecycleCommitterStub{}, time.Now)

	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{SSHPassword: "must-not-run"}, "")
	if err == nil || task.Status != TaskFailed || executor.received.SSHPassword != "" || approval.received != "" || !strings.Contains(task.Message, "approval") {
		t.Fatalf("unapproved execution task=%+v executor=%+v approval=%q err=%v", task, executor.received, approval.received, err)
	}
}

func TestManagerFailsClosedWhenPreMutationAuditCannotBePersisted(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}, auditErrAt: model.StagePrecheck}
	executor := &lifecycleExecutorStub{}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{}, &lifecycleLockStub{}, &lifecycleApprovalStub{}, executor, &lifecycleCommitterStub{}, time.Now)

	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{SSHPassword: "must-not-run"}, "approved")
	if err == nil || task.Status != TaskFailed || executor.calls != 0 {
		t.Fatalf("pre-mutation audit failure task=%+v executor_calls=%d err=%v", task, executor.calls, err)
	}
}

func TestManagerMarksTaskIndeterminateWhenReportPersistenceFailsAfterMutation(t *testing.T) {
	request, plan := executableLifecyclePlan()
	store := &taskStoreStub{tasks: map[model.ResourceID]Task{}, reportErr: errors.New("report unavailable")}
	committer := &lifecycleCommitterStub{}
	manager := NewManager(store, lifecycleAuthorityStub{}, lifecycleSafetyStub{}, &lifecycleLockStub{}, &lifecycleApprovalStub{}, &lifecycleExecutorStub{result: ExecutionResult{Verified: true}}, committer, time.Now)

	task, err := manager.Execute(context.Background(), request, plan, ExecutionSecrets{}, "approved")
	if err == nil || task.Status != TaskIndeterminate || committer.calls != 1 || task.ReportID == "" {
		t.Fatalf("post-mutation report failure task=%+v commit_calls=%d err=%v", task, committer.calls, err)
	}
}
