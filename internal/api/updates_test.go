package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clusterguard.io/ha/internal/platformupdate"
	"clusterguard.io/ha/internal/store"
)

type softwareUpdateManagerStub struct {
	snapshot     platformupdate.Snapshot
	packageValue platformupdate.Package
	job          platformupdate.Job
	uploadName   string
	uploadBody   string
	startedMode  platformupdate.Mode
	confirmation string
	err          error
}

func (stub *softwareUpdateManagerStub) Snapshot(context.Context) platformupdate.Snapshot {
	return stub.snapshot
}
func (stub *softwareUpdateManagerStub) Upload(_ context.Context, name string, source io.Reader) (platformupdate.Package, error) {
	contents, _ := io.ReadAll(source)
	stub.uploadName, stub.uploadBody = name, string(contents)
	return stub.packageValue, stub.err
}
func (stub *softwareUpdateManagerStub) Start(_ context.Context, mode platformupdate.Mode, patchID, confirmation string) (platformupdate.Job, error) {
	stub.startedMode, stub.confirmation = mode, confirmation
	stub.job.PatchID, stub.job.Mode = patchID, mode
	return stub.job, stub.err
}
func (stub *softwareUpdateManagerStub) Package(patchID string) (platformupdate.Package, bool) {
	return stub.packageValue, stub.packageValue.PatchID == patchID
}
func (stub *softwareUpdateManagerStub) Job(patchID string) (platformupdate.Job, bool) {
	return stub.job, stub.job.PatchID == patchID
}

func TestSoftwareUpdateUploadRequiresControlAuthenticationAndWritesAudit(t *testing.T) {
	repository := store.NewMemory()
	manager := &softwareUpdateManagerStub{packageValue: platformupdate.Package{
		PatchID: "cgpatch-2.2-1-to-2.2-2", TargetVersion: "2.2-2", SignatureVerified: true,
	}}
	server := NewServer(nil, repository, nil, nil, WithControlToken(testControlToken), WithSoftwareUpdates(manager))
	body, contentType := updateUploadBody(t, "release.cgupgrade", "signed package")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || manager.uploadName != "release.cgupgrade" || manager.uploadBody != "signed package" {
		t.Fatalf("upload status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
	if audits := repository.Audits(); len(audits) != 2 || !strings.Contains(audits[1].Message, manager.packageValue.PatchID) {
		t.Fatalf("software update audits=%+v", audits)
	}
}

func TestSoftwareUpdateUploadRejectsFullOfflineInstaller(t *testing.T) {
	manager := &softwareUpdateManagerStub{}
	server := NewServer(nil, store.NewMemory(), nil, nil, WithControlToken(testControlToken), WithSoftwareUpdates(manager))
	body, contentType := updateUploadBody(t, "clusterguard-ha-2.2-39-offline-linux-x86_64.tar.gz", "installer")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || manager.uploadName != "" {
		t.Fatalf("offline installer must not enter rolling update workflow: status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
}

func TestSoftwareUpdateActionForwardsConfirmationAndSurfacesWarning(t *testing.T) {
	manager := &softwareUpdateManagerStub{
		packageValue: platformupdate.Package{PatchID: "cgpatch-2.2-1-to-2.2-2"},
		job:          platformupdate.Job{Status: platformupdate.StatusQueued},
	}
	server := NewServer(nil, store.NewMemory(), nil, nil, WithControlToken(testControlToken), WithSoftwareUpdates(manager))
	payload, _ := json.Marshal(softwareUpdateActionPayload{Confirmation: manager.packageValue.PatchID})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates/"+manager.packageValue.PatchID+"/execute", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || manager.startedMode != platformupdate.ModeExecute || manager.confirmation != manager.packageValue.PatchID {
		t.Fatalf("action status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
	if !strings.Contains(response.Body.String(), platformupdate.AutomaticFailoverWarning) {
		t.Fatalf("missing automatic failover warning: %s", response.Body.String())
	}
}

func TestSoftwareUpdateMaintenanceAllowsOnlyControlledUpdateActions(t *testing.T) {
	for _, test := range []struct {
		action string
		want   int
	}{
		{action: "plan", want: http.StatusAccepted},
		{action: "execute", want: http.StatusAccepted},
		{action: "resume", want: http.StatusAccepted},
		{action: "rollback", want: http.StatusAccepted},
	} {
		t.Run(test.action, func(t *testing.T) {
			manager := &softwareUpdateManagerStub{
				packageValue: platformupdate.Package{PatchID: "cgpatch-2.2-1-to-2.2-2"},
				job:          platformupdate.Job{Status: platformupdate.StatusQueued},
			}
			server := NewServer(nil, store.NewMemory(), nil, nil,
				WithControlToken(testControlToken), WithSoftwareUpdates(manager),
				WithMutationMaintenance(mutationMaintenanceStub{err: context.DeadlineExceeded}),
			)
			payload := `{"confirmation":"` + manager.packageValue.PatchID + `"}`
			request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates/"+manager.packageValue.PatchID+"/"+test.action, strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+testControlToken)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}

	manager := &softwareUpdateManagerStub{packageValue: platformupdate.Package{
		PatchID: "cgpatch-2.2-1-to-2.2-2", TargetVersion: "2.2-2", SignatureVerified: true,
	}}
	server := NewServer(nil, store.NewMemory(), nil, nil,
		WithControlToken(testControlToken), WithSoftwareUpdates(manager),
		WithMutationMaintenance(mutationMaintenanceStub{err: context.DeadlineExceeded}),
	)
	body, contentType := updateUploadBody(t, "recovery.cgupgrade", "signed recovery package")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || manager.uploadName != "recovery.cgupgrade" {
		t.Fatalf("maintenance recovery upload status=%d body=%s manager=%+v", response.Code, response.Body.String(), manager)
	}
}

func TestSoftwareUpdateReplicatedGateUsesExecutionOwnershipDuringMaintenance(t *testing.T) {
	repository := store.NewMemory()
	manager := &softwareUpdateManagerStub{}
	server := NewServer(nil, repository, nil, nil,
		WithControlToken(testControlToken), WithSoftwareUpdates(manager),
		WithMutationMaintenance(mutationMaintenanceStub{err: context.DeadlineExceeded}),
	)
	call := func(action, executionID string) *httptest.ResponseRecorder {
		t.Helper()
		payload := `{"patch_id":"cgupgrade-2.2-68-to-2.2-72-x86_64","execution_id":"` + executionID + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/platform/updates/gate/"+action, strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+testControlToken)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}

	if response := call("acquire", "execution-1"); response.Code != http.StatusOK || !repository.SoftwareUpdateMaintenanceActive() {
		t.Fatalf("acquire replicated gate: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call("release", "execution-2"); response.Code != http.StatusConflict || !repository.SoftwareUpdateMaintenanceActive() {
		t.Fatalf("foreign release: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call("release", "execution-1"); response.Code != http.StatusOK || repository.SoftwareUpdateMaintenanceActive() {
		t.Fatalf("owned release: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSoftwareUpdateSnapshotIncludesMandatoryWarning(t *testing.T) {
	manager := &softwareUpdateManagerStub{snapshot: platformupdate.Snapshot{
		Available: true, Warning: platformupdate.AutomaticFailoverWarning,
	}}
	server := NewServer(nil, store.NewMemory(), nil, nil, WithSoftwareUpdates(manager))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/platform/updates", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), platformupdate.AutomaticFailoverWarning) {
		t.Fatalf("snapshot status=%d body=%s", response.Code, response.Body.String())
	}
}

func updateUploadBody(t *testing.T, name, contents string) ([]byte, string) {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	part, err := writer.CreateFormFile("package", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}
