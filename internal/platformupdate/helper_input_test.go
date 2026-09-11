package platformupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperRequiresOneCompleteBoundedRequest(t *testing.T) {
	for _, mode := range []Mode{ModePlan, ModeExecute, ModeResume, ModeRollback} {
		for _, suffix := range []struct {
			name  string
			body  string
			valid bool
		}{
			{"eof", "", true},
			{"whitespace", " \n\t\r", true},
			{"second-object", `{}`, false},
			{"second-null", ` null`, false},
			{"garbage", ` secret-must-not-be-echoed`, false},
			{"oversized-tail", strings.Repeat(" ", 4096), false},
		} {
			t.Run(string(mode)+"/"+suffix.name, func(t *testing.T) {
				root := helperTestRoot(t)
				patchID := "cg-2.2-1-to-2.2-2"
				directory := filepath.Join(root, patchID)
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				patchPath := filepath.Join(directory, patchFileName)
				if err := os.WriteFile(patchPath, []byte("test patch"), 0o600); err != nil {
					t.Fatal(err)
				}
				launcher := &launcherStub{}
				response := httptest.NewRecorder()
				body := `{"mode":"` + string(mode) + `","patch_id":"` + patchID + `"}` + suffix.body
				NewHelperHandler(root, launcher).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/jobs", strings.NewReader(body)))
				if suffix.valid {
					if response.Code != http.StatusAccepted || launcher.mode != mode || launcher.patchID != patchID {
						t.Fatalf("valid request: status=%d launcher=%+v", response.Code, launcher)
					}
					launcher.done(nil)
					return
				}
				if response.Code != http.StatusBadRequest || launcher.done != nil {
					t.Errorf("invalid request reached launcher: status=%d started=%v", response.Code, launcher.done != nil)
				}
				for path, permission := range map[string]os.FileMode{directory: 0o700, patchPath: 0o600} {
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != permission {
						t.Errorf("invalid request changed permission: got=%o want=%o", info.Mode().Perm(), permission)
					}
				}
				if strings.Contains(response.Body.String(), "secret-must-not-be-echoed") {
					t.Fatal("response exposed request contents")
				}
			})
		}
	}
}
