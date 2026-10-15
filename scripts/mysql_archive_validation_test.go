package scripts

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the production validation block with actual tar streams. Only the tar
// wrapper counts reads; it delegates every read to the native tar executable.
func TestMySQLArchiveValidationReadsOnceAndRejectsUnsafeContent(t *testing.T) {
	source, err := os.ReadFile("clusterguard-mysql-install.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(source)
	start := strings.Index(s, "\n[[ -n \"${package_path}\" && -f \"${package_path}\" ]]")
	end := strings.Index(s, "\nquiesce_managed_mysql_service() {")
	if start < 0 || end <= start {
		t.Fatal("cannot locate production archive validation")
	}
	program := s[start:end]
	for _, tc := range []struct {
		name    string
		paths   []string
		corrupt bool
		cut     bool
		ok      bool
	}{
		{name: "valid", paths: []string{"mysql/", "mysql/bin/", "mysql/bin/mysql"}, ok: true},
		{name: "valid-leading-dot", paths: []string{"./mysql/", "./mysql/bin/", "./mysql/bin/mysql"}, ok: true},
		{name: "unreadable-stream", corrupt: true},
		{name: "truncated-valid-stream", paths: []string{"mysql/", "mysql/bin/mysql"}, cut: true},
		{name: "absolute-path", paths: []string{"/mysql/", "/mysql/bin/mysql"}},
		{name: "parent-traversal", paths: []string{"mysql/", "mysql/../sentinel"}},
		{name: "multiple-roots", paths: []string{"mysql/", "other/bin/mysql"}},
		{name: "loose-top-level-file", paths: []string{"mysql"}},
		{name: "dot-path-alias", paths: []string{"mysql/", "mysql/./bin/mysql"}},
		{name: "ownership-marker", paths: []string{"mysql/", "mysql/.clusterguard-managed"}},
		{name: "ownership-marker-child", paths: []string{"mysql/", "mysql/.clusterguard-managed/alias"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "mysql.tar.gz")
			if tc.corrupt {
				writeFile(t, archive, "not a tar archive", 0600)
			} else {
				f, err := os.Create(archive)
				if err != nil {
					t.Fatal(err)
				}
				gz := gzip.NewWriter(f)
				tw := tar.NewWriter(gz)
				for _, name := range tc.paths {
					h := &tar.Header{Name: name, Mode: 0644, Size: 1}
					if strings.HasSuffix(name, "/") {
						h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0755, 0
					}
					if err := tw.WriteHeader(h); err != nil {
						t.Fatal(err)
					}
					if h.Size != 0 {
						if _, err := tw.Write([]byte("x")); err != nil {
							t.Fatal(err)
						}
					}
				}
				for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
					if err := close(); err != nil {
						t.Fatal(err)
					}
				}
				if tc.cut {
					info, _ := os.Stat(archive)
					if err := os.Truncate(archive, info.Size()-8); err != nil {
						t.Fatal(err)
					}
				}
			}
			writeFile(t, filepath.Join(root, "sentinel"), "original database bytes", 0600)
			command := exec.Command("bash", "-c", `set -euo pipefail
package_path="$CG_TEST_ROOT/mysql.tar.gz"
port=3306
archive_listing=""
trap 'if [[ -n "$archive_listing" ]]; then printf "%s\n" "$archive_listing" > "$CG_TEST_ROOT/temporary-path"; rm -f "$archive_listing"; fi' EXIT
tar() { printf '%s\n' "$*" >> "$CG_TEST_ROOT/tar-calls"; command tar "$@"; }
`+program+`
touch "$CG_TEST_ROOT/mutation-reached"
`)
			command.Env = append(os.Environ(), "CG_TEST_ROOT="+root)
			output, runErr := command.CombinedOutput()
			if (runErr == nil) != tc.ok {
				t.Fatalf("success=%v want=%v: %s", runErr == nil, tc.ok, output)
			}
			calls, err := os.ReadFile(filepath.Join(root, "tar-calls"))
			if err != nil || strings.Count(strings.TrimSpace(string(calls)), "\n") != 0 {
				t.Fatalf("expected exactly one native archive read, got %q (%v)", calls, err)
			}
			_, mutated := os.Stat(filepath.Join(root, "mutation-reached"))
			if (mutated == nil) != tc.ok {
				t.Fatal("unsafe validation reached the later mutation boundary")
			}
			if original, _ := os.ReadFile(filepath.Join(root, "sentinel")); string(original) != "original database bytes" {
				t.Fatal("validation changed existing data")
			}
			if !tc.ok {
				if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
					t.Fatalf("wrong failure status: %v (%s)", runErr, output)
				}
				path, err := os.ReadFile(filepath.Join(root, "temporary-path"))
				if err != nil {
					t.Fatal("missing cleanup evidence", err)
				}
				if _, err := os.Stat(strings.TrimSpace(string(path))); !os.IsNotExist(err) {
					t.Fatal("private archive listing survived failure")
				}
			}
		})
	}
}
