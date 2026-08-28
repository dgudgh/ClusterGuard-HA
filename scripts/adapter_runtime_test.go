package scripts

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdapterRuntimeUsesManagedMySQLClientAndWritesReadinessMarker(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required")
	}
	root := t.TempDir()
	managed := filepath.Join(root, "managed", "mysql", "3384", "software", "bin", "mysql")
	writeExecutable(t, managed, "#!/usr/bin/env bash\necho 'mysql  Ver 8.4.10 for Linux on x86_64'\n")
	linkRoot := filepath.Join(root, "links")
	marker := filepath.Join(root, "state", "adapter-runtime-ready.json")
	payload := []byte(`{"request":{"engine":"mysql"},"target":{"mysql_version":"8.4.10"}}`)
	command := exec.Command("bash", "clusterguard-adapter-runtime-install.sh")
	command.Stdin = bytes.NewReader(payload)
	command.Env = append(os.Environ(),
		"CG_MANAGED_DATABASE_ROOT="+filepath.Join(root, "managed"),
		"CG_ADAPTER_RUNTIME_ROOT="+filepath.Join(root, "runtime"),
		"CG_ADAPTER_RUNTIME_LINK_ROOT="+linkRoot,
		"CG_ADAPTER_RUNTIME_MARKER="+marker,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare adapter runtime: %v\n%s", err, output)
	}
	linked, err := filepath.EvalSymlinks(filepath.Join(linkRoot, "mysql", "bin", "mysql"))
	wanted, wantedErr := filepath.EvalSymlinks(managed)
	if err != nil || wantedErr != nil || linked != wanted {
		t.Fatalf("mysql runtime link=%q err=%v, want %q (resolve err=%v)", linked, err, wanted, wantedErr)
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var readiness struct {
		Engine string `json:"engine"`
		Ready  bool   `json:"ready"`
		Binary string `json:"binary"`
	}
	if err := json.Unmarshal(contents, &readiness); err != nil {
		t.Fatalf("decode readiness marker: %v", err)
	}
	if readiness.Engine != "mysql" || !readiness.Ready || readiness.Binary == "" {
		t.Fatalf("unexpected readiness marker: %+v", readiness)
	}
}

func TestAdapterRuntimeFailsClosedWithoutRequiredClient(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("bash", "clusterguard-adapter-runtime-install.sh")
	command.Stdin = strings.NewReader(`{"request":{"engine":"mysql"},"target":{"mysql_version":"8.4.10"}}`)
	command.Env = []string{
		"PATH=/usr/bin:/bin",
		"CG_MANAGED_DATABASE_ROOT=" + filepath.Join(root, "missing"),
		"CG_ADAPTER_RUNTIME_ROOT=" + filepath.Join(root, "runtime"),
		"CG_ADAPTER_RUNTIME_LINK_ROOT=" + filepath.Join(root, "links"),
		"CG_ADAPTER_RUNTIME_MARKER=" + filepath.Join(root, "state", "ready.json"),
	}
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("missing adapter client was accepted: %s", output)
	}
}
