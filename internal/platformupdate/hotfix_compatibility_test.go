package platformupdate

import (
	"archive/tar"
	"clusterguard.io/ha/pkg/model"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type runtimeFixture struct {
	checker                RuntimeCompatibilityChecker
	root, manifest, backup string
	key                    *rsa.PrivateKey
	programs               map[string]string
	files                  []map[string]string
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	root := helperTestRoot(t)
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pubPath := filepath.Join(root, "public.pem")
	os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0600)
	f := &runtimeFixture{root: root, key: key, manifest: filepath.Join(root, "HOTFIX-MANIFEST.json"), backup: filepath.Join(root, "backups"), programs: map[string]string{}}
	os.MkdirAll(f.backup, 0700)
	for _, name := range []string{"clusterguard", "clusterguard-update-helper", "clusterguard-upgrade", "clusterguard-update-job"} {
		dest := filepath.Join(root, "installed", name)
		os.MkdirAll(filepath.Dir(dest), 0700)
		body := runtimeProgram(name, true)
		os.WriteFile(dest, body, 0755)
		f.programs[dest] = name
		artifact := "payload/" + name
		os.MkdirAll(filepath.Join(root, "payload"), 0700)
		os.WriteFile(filepath.Join(root, artifact), body, 0700)
		sum := sha256.Sum256(body)
		f.files = append(f.files, map[string]string{"artifact": artifact, "install_path": dest, "sha256": hex.EncodeToString(sum[:])})
	}
	f.checker = RuntimeCompatibilityChecker{TrustKeyPath: pubPath, BackupDirectory: f.backup, requirements: func() (model.RuntimeCompatibilityFloor, error) { return model.PolicyCompatibilityFloor(), nil }, programs: f.programs}
	f.sign(t)
	f.backupPrograms(t, "001", true)
	return f
}
func runtimeProgram(name string, known bool) []byte {
	protocol := 1
	if !known {
		protocol = 0
	}
	data, _ := json.Marshal(map[string]interface{}{"binary": name, "runtime_compatibility_protocol": protocol, "instance_failover_policy_protocol": 1})
	return []byte("#!/bin/sh\nprintf '%s\\n' '" + string(data) + "'\n")
}
func (f *runtimeFixture) sign(t *testing.T) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"hotfix_id": "policy-patch", "files": f.files})
	if e := os.WriteFile(f.manifest, body, 0600); e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(body)
	sig, e := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, hash[:])
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(f.root, "HOTFIX-MANIFEST.sig"), sig, 0600)
}
func (f *runtimeFixture) backupPrograms(t *testing.T, stamp string, known bool) {
	t.Helper()
	lines := []string{"# package_id=policy-patch"}
	for dest, name := range f.programs {
		saved := dest + ".bak-" + stamp
		os.WriteFile(saved, runtimeProgram(name, known), 0755)
		lines = append(lines, dest+"\t"+saved)
	}
	os.WriteFile(filepath.Join(f.backup, "backup-policy-patch-"+stamp+".txt"), []byte(strings.Join(lines, "\n")+"\n"), 0600)
}
func TestRuntimeCompatibilityChecksActualCandidatesAndOriginalBackups(t *testing.T) {
	for _, scenario := range []string{"compatible-forward", "compatible-rollback", "old-original-not-newest", "wrong-backup-id", "missing-backup", "symlink-backup", "tampered-payload", "unknown-current", "missing-current", "unknown-floor"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRuntimeFixture(t)
			mode := ModeRollback
			want := false
			switch scenario {
			case "compatible-forward":
				mode = ModeExecute
				want = true
			case "compatible-rollback":
				want = true
			case "old-original-not-newest":
				f.backupPrograms(t, "001", false)
				f.backupPrograms(t, "999", true)
			case "wrong-backup-id":
				os.WriteFile(filepath.Join(f.backup, "backup-policy-patch-001.txt"), []byte("# package_id=other\n"), 0600)
			case "missing-backup":
				for dest := range f.programs {
					os.Remove(dest + ".bak-001")
					break
				}
			case "symlink-backup":
				for dest := range f.programs {
					saved := dest + ".bak-001"
					os.Remove(saved)
					os.Symlink(dest, saved)
					break
				}
			case "tampered-payload":
				mode = ModeExecute
				os.WriteFile(filepath.Join(f.root, f.files[0]["artifact"]), []byte("tampered"), 0700)
			case "unknown-current":
				f.files = nil
				f.sign(t)
				for dest, name := range f.programs {
					os.WriteFile(dest, runtimeProgram(name, false), 0755)
					break
				}
			case "missing-current":
				f.files = nil
				f.sign(t)
				for dest := range f.programs {
					os.Remove(dest)
					break
				}
			case "unknown-floor":
				f.checker.requirements = func() (model.RuntimeCompatibilityFloor, error) {
					return model.RuntimeCompatibilityFloor{Protocol: 2, InstanceFailoverPolicy: 1}, nil
				}
			}
			err := f.checker.checkManifest(context.Background(), f.manifest, mode, "policy-patch", model.RuntimeCompatibilityFloor{})
			if (err == nil) != want {
				t.Fatalf("allowed=%t want=%t error=%v", err == nil, want, err)
			}
		})
	}
}
func TestRuntimeCompatibilityEmptyFloorAndRequestCannotLowerLocalFloor(t *testing.T) {
	f := newRuntimeFixture(t)
	f.backupPrograms(t, "001", false)
	if e := f.checker.checkManifest(context.Background(), f.manifest, ModeRollback, "policy-patch", model.RuntimeCompatibilityFloor{}); e == nil {
		t.Fatal("caller empty floor bypassed local policy")
	}
	f.checker.requirements = func() (model.RuntimeCompatibilityFloor, error) { return model.RuntimeCompatibilityFloor{}, nil }
	if e := f.checker.checkManifest(context.Background(), f.manifest, ModeRollback, "policy-patch", model.PolicyCompatibilityFloor()); e == nil {
		t.Fatal("passed cluster floor ignored")
	}
	if e := f.checker.checkManifest(context.Background(), f.manifest, ModeRollback, "policy-patch", model.RuntimeCompatibilityFloor{}); e != nil {
		t.Fatal("never-saved control blocked", e)
	}
}

func TestPrivilegedCompatibilityAdmissionDoesNotTrustCallerOrLaunchRejectedJobs(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			f := newRuntimeFixture(t)
			if !compatible {
				f.backupPrograms(t, "001", false)
			}
			root := filepath.Join(f.root, "staging")
			dir := filepath.Join(root, "policy-patch")
			os.MkdirAll(dir, 0700)
			archive := filepath.Join(dir, patchFileName)
			createCompatibilityTar(t, f, archive)
			f.checker.RootDirectory = root
			launcher := &launcherStub{}
			h := NewHelperHandler(root, launcher)
			h.compatibilityGuard = f.checker.Check
			for _, path := range []string{"/v1/compatibility", "/v1/jobs"} {
				req := httptest.NewRequest("POST", path, strings.NewReader(`{"mode":"rollback","patch_id":"policy-patch","requirements":{"protocol":0,"instance_failover_policy":0}}`))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				expected := 409
				if compatible {
					expected = 200
					if path == "/v1/jobs" {
						expected = 202
					}
				}
				if w.Code != expected {
					t.Fatalf("path=%s status=%d expected=%d body=%s", path, w.Code, expected, w.Body.String())
				}
			}
			if !compatible && launcher.patchID != "" {
				t.Fatal("rejected direct Helper request launched a job")
			}
		})
	}
}
func createCompatibilityTar(t *testing.T, f *runtimeFixture, destination string) {
	t.Helper()
	w, e := os.Create(destination)
	if e != nil {
		t.Fatal(e)
	}
	g := gzip.NewWriter(w)
	a := tar.NewWriter(g)
	names := []string{"HOTFIX-MANIFEST.json", "HOTFIX-MANIFEST.sig"}
	for _, file := range f.files {
		names = append(names, file["artifact"])
	}
	for _, name := range names {
		b, e := os.ReadFile(filepath.Join(f.root, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = a.WriteHeader(&tar.Header{Name: "clusterguard-hotfix/" + name, Mode: 0700, Typeflag: tar.TypeReg, Size: int64(len(b))}); e != nil {
			t.Fatal(e)
		}
		if _, e = a.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	a.Close()
	g.Close()
	w.Close()
}

type compatibilityRoundTripper func(*http.Request) (*http.Response, error)

func (f compatibilityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestManagerAndPrivilegedHelperValidateSignedTargetsBeforeWritingJob(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(fmt.Sprint(compatible), func(t *testing.T) {
			f := newRuntimeFixture(t)
			if !compatible {
				f.backupPrograms(t, "001", false)
			}
			root := filepath.Join(f.root, "staging")
			dir := filepath.Join(root, "policy-patch")
			os.MkdirAll(dir, 0700)
			archive := filepath.Join(f.root, "fixture.cgpatch")
			createCompatibilityTar(t, f, archive)
			f.checker.RootDirectory = root
			launcher := &launcherStub{}
			handler := NewHelperHandler(root, launcher)
			handler.compatibilityGuard = f.checker.Check
			server := httptest.NewServer(handler)
			defer server.Close()
			origin, _ := url.Parse(server.URL)
			helper := NewUnixHelperClient("isolated")
			helper.client = &http.Client{Transport: compatibilityRoundTripper(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				u := *r.URL
				u.Scheme, u.Host = origin.Scheme, origin.Host
				clone.URL = &u
				return http.DefaultTransport.RoundTrip(clone)
			})}
			manager := NewManager(Config{RootDirectory: root, TrustKeyPath: f.checker.TrustKeyPath}, WithHelper(helper), WithInspector(inspectorStub{result: Package{PatchID: "policy-patch", Kind: PackageKindHotfix, SourceVersion: "2.2-113", TargetVersion: "2.2-113+policy", SignatureVerified: true, RollbackAvailable: true, Rolling: true}}))
			file, e := os.Open(archive)
			if e != nil {
				t.Fatal(e)
			}
			defer file.Close()
			if _, e = manager.Upload(context.Background(), "fixture.cgpatch", file); e != nil {
				t.Fatal(e)
			}
			old := Job{OperationID: "original-execute", PatchID: "policy-patch", Mode: ModeExecute, Status: StatusSucceeded}
			writeJobForTest(t, root, old)
			manager.SetRuntimeCompatibilityRequirements(func() model.RuntimeCompatibilityFloor { return model.PolicyCompatibilityFloor() })
			_, e = manager.StartWithOperationID(context.Background(), ModeRollback, "policy-patch", "policy-patch", "fresh-rollback")
			if compatible {
				if e != nil || launcher.patchID != "policy-patch" {
					t.Fatal("compatible target failed", e, launcher.patchID)
				}
			} else {
				if !errors.Is(e, ErrRuntimeIncompatible) || launcher.patchID != "" {
					t.Fatal("old target reached launcher", e, launcher.patchID)
				}
				current, _ := manager.Job("policy-patch")
				if current.OperationID != old.OperationID || current.Status != old.Status {
					t.Fatal("rejection overwrote original operation", current)
				}
				if _, e = os.Stat(filepath.Join(dir, "requests.jsonl")); !os.IsNotExist(e) {
					t.Fatal("rejected target wrote a new request")
				}
			}
		})
	}
}
