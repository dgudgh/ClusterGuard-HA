//go:build linux || darwin

package platformupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrivateEarlyFailurePublishesOnlyMatchingPinnedPublicJob(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned private workspace: also run the Linux test binary as root with a private TMPDIR")
	}
	for _, scenario := range []string{"matching", "newer-public", "replaced-parent"} {
		t.Run(scenario, func(t *testing.T) {
			root, private := helperTestRoot(t), helperTestRoot(t)
			dir := filepath.Join(root, "patch")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(dir, 0, 65534); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, patchFileName), []byte("patch"), 0600); err != nil {
				t.Fatal(err)
			}
			queued := Job{PatchID: "patch", OperationID: "new-execute", Mode: ModeExecute, Status: StatusQueued}
			if err := writeJSONAtomic(filepath.Join(dir, jobFileName), queued); err != nil {
				t.Fatal(err)
			}
			launcher := &launcherStub{}
			h := NewHelperHandlerWithPrivateRoot(root, private, launcher)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(`{"patch_id":"patch","mode":"execute","operation_id":"new-execute"}`)))
			if response.Code != http.StatusAccepted {
				t.Fatalf("accept: %d %s", response.Code, response.Body.String())
			}
			if scenario == "newer-public" {
				queued.OperationID = "another-execute"
				if err := writeJSONAtomic(filepath.Join(dir, jobFileName), queued); err != nil {
					t.Fatal(err)
				}
			}
			var outside string
			if scenario == "replaced-parent" {
				outside = helperTestRoot(t)
				if err := os.Rename(dir, dir+"-pinned"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
				dir += "-pinned"
			}
			launcher.done(errors.New("failed before wrapper trap"))
			var job Job
			if err := readJSONFile(filepath.Join(dir, jobFileName), &job); err != nil {
				t.Fatal(err)
			}
			if scenario == "newer-public" {
				if job.OperationID != "another-execute" || job.Status != StatusQueued {
					t.Fatalf("overwrote another operation: %+v", job)
				}
			} else if job.Status != StatusFailed || job.OperationID != "new-execute" || job.PatchID != "patch" {
				t.Fatalf("early failure left stale public state: %+v", job)
			}
			if scenario != "newer-public" {
				var stat unix.Stat_t
				if err := unix.Stat(filepath.Join(dir, jobFileName), &stat); err != nil {
					t.Fatal(err)
				}
				if stat.Gid != 65534 {
					t.Fatal("published status lost console group readability")
				}
			}
			if outside != "" {
				xs, err := os.ReadDir(outside)
				if err != nil || len(xs) != 0 {
					t.Fatalf("followed replaced parent: %v %v", xs, err)
				}
			}
			if h.active {
				t.Fatal("action slot retained")
			}
		})
	}
}
