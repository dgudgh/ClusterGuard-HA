package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerRevalidatesSignedInstalledSupersedes(t *testing.T) {
	privateKey, publicKey := generatePatchSigningKey(t)
	predecessor, _ := signedHotfixPackage(t, hotfixFixtureOptions{hotfixID: "HF-OLD", privateKey: privateKey, publicKey: publicKey})
	for _, tc := range []struct {
		name, state string
		supersedes  []string
		tampered    bool
		expected    string
	}{
		{"signed installed successor", "installed", []string{"HF-OLD"}, false, "已被签名且已安装的 HF-NEW 替代"},
		{"unrelated installed successor", "installed", []string{"HF-UNRELATED"}, false, "热修补丁不支持 --resume"},
		{"rolled back successor", "rolled_back", []string{"HF-OLD"}, false, "热修补丁不支持 --resume"},
		{"tampered successor", "installed", []string{"HF-OLD"}, true, "无法重新验签已安装补丁 HF-NEW"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			successor, _ := signedHotfixPackage(t, hotfixFixtureOptions{hotfixID: "HF-NEW", supersedes: tc.supersedes, privateKey: privateKey, publicKey: publicKey, replaceApplyAfterSigning: tc.tampered})
			bytes, err := os.ReadFile(successor)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(root, "jobs", "HF-NEW", "package.cgpatch"), string(bytes), 0o600)
			writeFile(t, filepath.Join(root, "jobs", "HF-NEW", "deployment.json"), fmt.Sprintf(`{"package_id":"HF-NEW","state":%q}`, tc.state), 0o600)
			command := exec.Command("bash", "clusterguard-upgrade.sh", "--patch", predecessor, "--trust-key", publicKey, "--private-root", root, "--resume", "--execute", "--yes")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.expected) {
				t.Fatalf("wrong supersedes result: err=%v output=%s expected=%s", err, output, tc.expected)
			}
		})
	}
}

// The shape 2026-10-08 left behind. HF-2026-1008-01 was applied at 16:57 by an
// update runner that predated deployment.json, so the private tree holds no
// deployment record for it - and a guard reading only deployment records does not
// fail closed there, it never runs. The request it failed to stop was a rollback
// of HF-2026-0929-05, which HF-2026-1008-01 declares as superseded.
func TestRunnerBlocksSupersededPredecessorWithoutAnyDeploymentRecord(t *testing.T) {
	privateKey, publicKey := generatePatchSigningKey(t)
	predecessor, _ := signedHotfixPackage(t, hotfixFixtureOptions{hotfixID: "HF-OLD", privateKey: privateKey, publicKey: publicKey})
	root := t.TempDir()
	successor, _ := signedHotfixPackage(t, hotfixFixtureOptions{hotfixID: "HF-NEW", supersedes: []string{"HF-OLD"}, privateKey: privateKey, publicKey: publicKey})
	bytes, err := os.ReadFile(successor)
	if err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(root, "jobs", "HF-NEW")
	writeFile(t, filepath.Join(jobDir, "package.cgpatch"), string(bytes), 0o600)
	writeFile(t, filepath.Join(jobDir, "package.json"), `{"patch_id":"HF-NEW","kind":"hotfix"}`, 0o600)
	command := func() ([]byte, error) {
		return exec.Command("bash", "clusterguard-upgrade.sh", "--patch", predecessor, "--trust-key", publicKey, "--private-root", root, "--resume", "--execute", "--yes").CombinedOutput()
	}
	const expected = "已被签名且已安装的 HF-NEW 替代"
	writeFile(t, filepath.Join(jobDir, "status.json"), `{"patch_id":"HF-NEW","operation_id":"op-applied","mode":"execute","status":"succeeded"}`, 0o600)
	if output, err := command(); err == nil || !strings.Contains(string(output), expected) {
		t.Fatalf("an applied successor with no deployment record left the guard inert: err=%v output=%s", err, output)
	}
	// The same record, one step earlier in its life: a refused resume was written
	// over the attempt that applied the patch, so status says failed while the last
	// completed event still proves the payload is on disk.
	writeFile(t, filepath.Join(jobDir, "status.json"), `{"patch_id":"HF-NEW","operation_id":"op-refused","mode":"resume","status":"failed"}`, 0o600)
	writeFile(t, filepath.Join(jobDir, "events.jsonl"), `{"patch_id":"HF-NEW","status":"running"}`+"\n"+`{"patch_id":"HF-NEW","status":"succeeded"}`+"\n", 0o600)
	if output, err := command(); err == nil || !strings.Contains(string(output), expected) {
		t.Fatalf("a refused attempt over a verified success left the guard inert: err=%v output=%s", err, output)
	}
	// A successor that never applied must not block recovery.
	writeFile(t, filepath.Join(jobDir, "status.json"), `{"patch_id":"HF-NEW","operation_id":"op-failed","mode":"execute","status":"failed"}`, 0o600)
	writeFile(t, filepath.Join(jobDir, "events.jsonl"), `{"patch_id":"HF-NEW","status":"running"}`+"\n"+`{"patch_id":"HF-NEW","status":"failed"}`+"\n", 0o600)
	if output, err := command(); err == nil || strings.Contains(string(output), "已被签名且已安装") {
		t.Fatalf("a successor that never applied blocked recovery: err=%v output=%s", err, output)
	}
}
