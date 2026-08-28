package platformupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type launcherStub struct {
	mode    Mode
	patchID string
	done    func(error)
	err     error
}

func (stub *launcherStub) Start(mode Mode, patchID, _ string, done func(error)) error {
	stub.mode, stub.patchID, stub.done = mode, patchID, done
	return stub.err
}

func TestHelperHandlerAcceptsOnlyKnownPackageAndAllowedMode(t *testing.T) {
	root := t.TempDir()
	patchID := "cg-2.2-1-to-2.2-2"
	directory := filepath.Join(root, patchID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, patchFileName), []byte("patch"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &launcherStub{}
	handler := NewHelperHandler(root, launcher)

	body, _ := json.Marshal(helperRequest{Mode: ModeExecute, PatchID: patchID})
	request := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || launcher.patchID != patchID || launcher.mode != ModeExecute {
		t.Fatalf("status=%d body=%s launch=%+v", response.Code, response.Body.String(), launcher)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(`{"mode":"execute","patch_id":"../escape"}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe patch status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHelperHandlerSerializesJobsAndReleasesSlotOnCompletion(t *testing.T) {
	root := t.TempDir()
	patchID := "cg-2.2-1-to-2.2-2"
	directory := filepath.Join(root, patchID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, patchFileName), []byte("patch"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &launcherStub{}
	handler := NewHelperHandler(root, launcher)
	requestBody := `{"mode":"plan","patch_id":"` + patchID + `"}`

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(requestBody)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(requestBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("parallel status=%d body=%s", response.Code, response.Body.String())
	}
	launcher.done(nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(requestBody)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("released status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUnixHelperClientSurfacesHelperErrors(t *testing.T) {
	client := NewUnixHelperClient(filepath.Join(t.TempDir(), "missing.sock"))
	if err := client.Ready(context.Background()); err == nil {
		t.Fatal("missing helper socket was accepted")
	}
	client.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	if err := client.Start(context.Background(), ModePlan, "patch"); err == nil {
		t.Fatal("helper transport failure was ignored")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
