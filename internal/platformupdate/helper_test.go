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
	"time"
)

type launcherStub struct {
	mode    Mode
	patchID string
	done    func(error)
	err     error
}

func helperTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func (stub *launcherStub) Start(mode Mode, patchID, _ string, done func(error)) error {
	stub.mode, stub.patchID, stub.done = mode, patchID, done
	return stub.err
}

func TestHelperHandlerAcceptsOnlyKnownPackageAndAllowedMode(t *testing.T) {
	root := helperTestRoot(t)
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
	root := helperTestRoot(t)
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

func TestHelperPreservesRunnerTerminalStatusAndPublishesFallbackReadably(t *testing.T) {
	for _, test := range []struct {
		name        string
		initial     Job
		wantMessage string
	}{
		{
			name: "runner terminal status is authoritative",
			initial: Job{PatchID: "cg-2.2-1-to-2.2-2", Mode: ModeExecute, Status: StatusRolledBack,
				Message: "升级未完成，已自动回退全部节点并释放维护门禁"},
			wantMessage: "升级未完成，已自动回退全部节点并释放维护门禁",
		},
		{
			name:        "missing runner terminal status gets helper fallback",
			initial:     Job{PatchID: "cg-2.2-1-to-2.2-2", Mode: ModeExecute, Status: StatusRunning},
			wantMessage: "特权更新任务启动或执行失败：exit status 1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := helperTestRoot(t)
			directory := filepath.Join(root, test.initial.PatchID)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, patchFileName), []byte("patch"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONAtomic(filepath.Join(directory, jobFileName), test.initial); err != nil {
				t.Fatal(err)
			}
			launcher := &launcherStub{}
			handler := NewHelperHandler(root, launcher)
			body, _ := json.Marshal(helperRequest{Mode: ModeExecute, PatchID: test.initial.PatchID})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewReader(body)))
			if response.Code != http.StatusAccepted {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			launcher.done(errors.New("exit status 1"))
			job := Job{}
			if err := readJSONFile(filepath.Join(directory, jobFileName), &job); err != nil {
				t.Fatal(err)
			}
			if job.Message != test.wantMessage {
				t.Fatalf("message=%q want=%q", job.Message, test.wantMessage)
			}
			statusInfo, err := os.Stat(filepath.Join(directory, jobFileName))
			if err != nil {
				t.Fatal(err)
			}
			if statusInfo.Mode().Perm() != updateFileMode {
				t.Fatalf("status mode=%#o want=%#o", statusInfo.Mode().Perm(), updateFileMode)
			}
			directoryInfo, err := os.Stat(directory)
			if err != nil {
				t.Fatal(err)
			}
			if directoryInfo.Mode().Perm() != updateJobDirMode {
				t.Fatalf("directory mode=%#o want=%#o", directoryInfo.Mode().Perm(), updateJobDirMode)
			}
		})
	}
}

func TestCommandLauncherPublishesGroupReadableOutput(t *testing.T) {
	root := helperTestRoot(t)
	runner := filepath.Join(root, "runner.sh")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nprintf 'planned\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "output.log")
	done := make(chan error, 1)
	launcher := CommandLauncher{RunnerPath: runner}
	if err := launcher.Start(ModePlan, "patch", outputPath, func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not finish")
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("output mode=%#o, want 0640", info.Mode().Perm())
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil || string(contents) != "planned\n" {
		t.Fatalf("output=%q err=%v", contents, err)
	}
}

// The runner is told what to do by argv, so the string that reaches it is the
// whole contract between the control plane and the updater: resume picks up the
// rolling journal, execute starts over, and a hotfix is refused a resume
// outright. Pinning argv here keeps "the console asked for a resume" and "the
// updater was told to resume" from drifting apart without anyone noticing.
func TestCommandLauncherPassesTheModeItWasAskedFor(t *testing.T) {
	root := helperTestRoot(t)
	capture := filepath.Join(root, "argv.txt")
	runner := filepath.Join(root, "runner.sh")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+capture+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const patchID = "HF-2026-0930-01"
	for _, testCase := range []struct {
		mode Mode
		want string
	}{
		{ModePlan, "--mode\nplan\n--patch-id\n" + patchID + "\n"},
		{ModeExecute, "--mode\nexecute\n--patch-id\n" + patchID + "\n"},
		{ModeRetry, "--mode\nretry\n--patch-id\n" + patchID + "\n"},
		{ModeResume, "--mode\nresume\n--patch-id\n" + patchID + "\n"},
		{ModeRollback, "--mode\nrollback\n--patch-id\n" + patchID + "\n"},
	} {
		t.Run(string(testCase.mode), func(t *testing.T) {
			if err := os.Remove(capture); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			output := filepath.Join(root, "output-"+string(testCase.mode)+".log")
			if err := (CommandLauncher{RunnerPath: runner}).Start(testCase.mode, patchID, output, func(err error) { done <- err }); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not finish")
			}
			contents, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(contents); got != testCase.want {
				t.Fatalf("argv=%q, want %q", got, testCase.want)
			}
		})
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
