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
