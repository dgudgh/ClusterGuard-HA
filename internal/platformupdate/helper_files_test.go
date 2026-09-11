//go:build linux || darwin

package platformupdate

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func helperBoundaryRoot(t *testing.T) string {
	t.Helper()
	return helperTestRoot(t)
}

func helperBoundaryRequest(handler *HelperHandler) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"mode":"plan","patch_id":"patch"}`)))
	return response
}

func assertHelperFileUnchanged(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != contents {
		t.Errorf("file changed: contents=%q err=%v", actual, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("file permissions changed: %o want %o", info.Mode().Perm(), mode)
	}
}

func TestHelperRejectsLinkedPackagePaths(t *testing.T) {
	for _, kind := range []string{"job-directory", "root-directory", "ancestor-directory", "package-symlink", "package-hardlink"} {
		t.Run(kind, func(t *testing.T) {
			base, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
			root := filepath.Join(base, "updates")
			if err := os.MkdirAll(filepath.Join(root, "patch"), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(external, patchFileName)
			if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "job-directory":
				if err := os.Remove(filepath.Join(root, "patch")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(root, "patch")); err != nil {
					t.Fatal(err)
				}
			case "root-directory", "ancestor-directory":
				if err := os.Mkdir(filepath.Join(external, "patch"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(outside, filepath.Join(external, "patch", patchFileName)); err != nil {
					t.Fatal(err)
				}
				outside = filepath.Join(external, "patch", patchFileName)
				link := filepath.Join(base, "linked")
				if err := os.Symlink(external, link); err != nil {
					t.Fatal(err)
				}
				root = link
				if kind == "ancestor-directory" {
					if err := os.Mkdir(filepath.Join(external, "updates"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(filepath.Join(external, "patch"), filepath.Join(external, "updates", "patch")); err != nil {
						t.Fatal(err)
					}
					root = filepath.Join(link, "updates")
					outside = filepath.Join(external, "updates", "patch", patchFileName)
				}
			case "package-symlink", "package-hardlink":
				link := os.Symlink
				if kind == "package-hardlink" {
					link = os.Link
				}
				if err := link(outside, filepath.Join(root, "patch", patchFileName)); err != nil {
					t.Fatal(err)
				}
			}
			launcher := &launcherStub{}
			response := helperBoundaryRequest(NewHelperHandler(root, launcher))
			if response.Code != http.StatusNotFound || launcher.done != nil {
				t.Errorf("linked package accepted: status=%d started=%v", response.Code, launcher.done != nil)
			}
			assertHelperFileUnchanged(t, outside, "outside", 0o600)
		})
	}
}

func TestCommandLauncherRejectsLinkedOutput(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
			outside := filepath.Join(external, outputFileName)
			if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, outputFileName)
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, output); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, output); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
				output = filepath.Join(root, "linked", outputFileName)
			}
			runner := filepath.Join(root, "runner.sh")
			if err := os.WriteFile(runner, []byte("#!/bin/sh\nprintf 'should-not-run\\n'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			err := (CommandLauncher{RunnerPath: runner}).Start(ModePlan, "patch", output, func(err error) { done <- err })
			if err == nil {
				t.Error("linked output accepted")
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("runner did not finish")
				}
			}
			assertHelperFileUnchanged(t, outside, "outside\n", 0o600)
		})
	}
}

func TestHelperFailureDoesNotFollowReplacedDirectory(t *testing.T) {
	root, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
	directory := filepath.Join(root, "patch")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, patchFileName), []byte("patch"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(external, jobFileName)
	contents := `{"status":"running","message":"outside"}`
	if err := os.WriteFile(outside, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &launcherStub{}
	if response := helperBoundaryRequest(NewHelperHandler(root, launcher)); response.Code != http.StatusAccepted {
		t.Fatalf("job not accepted: %d %s", response.Code, response.Body.String())
	}
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, directory); err != nil {
		t.Fatal(err)
	}
	launcher.done(errors.New("runner failed"))
	assertHelperFileUnchanged(t, outside, contents, 0o600)
	job := Job{}
	if err := readJSONFile(filepath.Join(directory+"-original", jobFileName), &job); err != nil || job.Status != StatusFailed {
		t.Fatalf("failure was not published in original directory: job=%+v err=%v", job, err)
	}
}

func TestHelperSpecialFilesAreRejectedWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"fifo", "directory", "socket"} {
		t.Run(kind, func(t *testing.T) {
			root := helperBoundaryRoot(t)
			if kind == "socket" {
				short, err := os.MkdirTemp("", "cg-socket-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(short) })
				root, err = filepath.EvalSymlinks(short)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "file")
			switch kind {
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			directory, err := openHelperDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			for _, flags := range []int{os.O_RDONLY, os.O_CREATE | os.O_APPEND | os.O_WRONLY} {
				result := make(chan error, 1)
				go func() {
					file, err := openHelperFile(directory, "file", flags, 0o600)
					if file != nil {
						_ = file.Close()
					}
					result <- err
				}()
				select {
				case err := <-result:
					if err == nil {
						t.Fatalf("special file accepted: flags=%d", flags)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("special file blocked helper")
				}
			}
		})
	}
}

func TestHelperFailureRejectsLinkedStatusAndPreservesAllTerminalStates(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", string(StatusPlanned), string(StatusSucceeded), string(StatusFailed), string(StatusRolledBack)} {
		t.Run(kind, func(t *testing.T) {
			root, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
			path := filepath.Join(root, jobFileName)
			contents := `{"status":"` + kind + `","message":"authoritative"}`
			if kind == "symlink" || kind == "hardlink" {
				outside := filepath.Join(external, jobFileName)
				contents = `{"status":"running","message":"must-not-be-copied"}`
				if err := os.WriteFile(outside, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(outside, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			directory, err := openHelperDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			NewHelperHandler(root, nil).recordLaunchFailure(directory, "patch", ModeExecute, errors.New("runner failed"))
			assertHelperFileUnchanged(t, path, contents, 0o600)
		})
	}
}

type helperLauncherFunc func(Mode, string, string, func(error)) error

func (launch helperLauncherFunc) Start(mode Mode, id, output string, done func(error)) error {
	return launch(mode, id, output, done)
}

func TestHelperRejectsDirectoryReplacementBetweenValidationAndLaunch(t *testing.T) {
	root, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
	directory := filepath.Join(root, "patch")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, patchFileName), []byte("patch"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(external, outputFileName)
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := helperLauncherFunc(func(mode Mode, id, output string, done func(error)) error {
		if err := os.Rename(directory, directory+"-original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, directory); err != nil {
			t.Fatal(err)
		}
		return (CommandLauncher{RunnerPath: "/bin/true"}).Start(mode, id, output, done)
	})
	handler := NewHelperHandler(root, launcher)
	response := helperBoundaryRequest(handler)
	if response.Code != http.StatusInternalServerError || handler.active {
		t.Fatalf("replacement accepted or slot leaked: status=%d active=%v", response.Code, handler.active)
	}
	assertHelperFileUnchanged(t, outside, "outside", 0o600)
}

func TestHelperPinnedOutputAndAtomicStatusSurviveParentReplacement(t *testing.T) {
	root, external := helperBoundaryRoot(t), helperBoundaryRoot(t)
	path := filepath.Join(root, "job")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := openHelperDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	output, err := openHelperFile(directory, outputFileName, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if _, err := output.WriteString("original\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeHelperJob(directory, Job{PatchID: "patch", Status: StatusFailed}); err != nil {
		t.Fatal(err)
	}
	assertHelperFileUnchanged(t, filepath.Join(path+"-original", outputFileName), "original\n", 0o600)
	job := Job{}
	if err := readJSONFile(filepath.Join(path+"-original", jobFileName), &job); err != nil || job.Status != StatusFailed {
		t.Fatalf("status not published in pinned directory: job=%+v err=%v", job, err)
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory modified: entries=%v err=%v", entries, err)
	}
	entries, err = os.ReadDir(path + "-original")
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files leaked: entries=%v err=%v", entries, err)
	}
}

func TestHelperFailureDoesNotTrustPartiallyDecodedTerminalStatus(t *testing.T) {
	for _, contents := range []string{
		`{"status":"succeeded","maintenance_active":"invalid"}`,
		`{"status":"rolled_back","updated_at":"not-a-time"}`,
	} {
		t.Run(contents, func(t *testing.T) {
			root := helperBoundaryRoot(t)
			path := filepath.Join(root, jobFileName)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			directory, err := openHelperDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			NewHelperHandler(root, nil).recordLaunchFailure(directory, "patch", ModeExecute, errors.New("runner failed"))
			job := Job{}
			if err := readJSONFile(path, &job); err != nil || job.Status != StatusFailed || job.PatchID != "patch" {
				t.Fatalf("invalid terminal record preserved: job=%+v err=%v", job, err)
			}
		})
	}
}
