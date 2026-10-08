package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the generated pre-mutation guard with real signatures and the compiled
// contract consumer. A legacy installed Helper is not required: the signed
// new Helper must load v2 before apply/rollback can replace any file.
func TestGeneratedHotfixLoadsOnlyVerifiedContractConsumer(t *testing.T) {
	node := requireNode(t)
	source, err := os.ReadFile("build-hotfix-patch.sh")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(source), "cat >\"${render_js}\" <<'RENDER_JS'\n", 2)
	if len(parts) != 2 {
		t.Fatal("render-scripts heredoc missing")
	}
	renderer := strings.SplitN(parts[1], "\nRENDER_JS", 2)[0]
	for _, scenario := range []string{"valid", "payload changed", "tool changed", "signature changed", "contract refused"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			helperPath := filepath.Join(dir, "payload/bin/clusterguard-update-helper")
			if err := os.MkdirAll(filepath.Dir(helperPath), 0o700); err != nil {
				t.Fatal(err)
			}
			helper, err := os.ReadFile(os.Getenv("CG_UPDATE_CONTRACT_HELPER"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "contract refused" {
				helper = []byte("#!/bin/sh\nexit 1\n")
			}
			writeFile(t, helperPath, string(helper), 0o700)
			manifest := map[string]any{
				"hotfix_id": "HF-TEST", "build_commit": "1234567890abcdef", "fix_commits": []string{},
				"source": map[string]string{"version": "2.2", "release": "105"}, "verification": []string{},
				"files": []map[string]string{{"artifact": "payload/bin/clusterguard-update-helper", "install_path": "/usr/local/libexec/clusterguard-update-helper", "mode": "0755", "owner": "root", "group": "root", "sha256": sha256OfFile(t, helperPath)}},
			}
			manifestPath := filepath.Join(dir, "HOTFIX-MANIFEST.json")
			data, _ := json.Marshal(manifest)
			writeFile(t, manifestPath, string(data), 0o600)
			renderPath := filepath.Join(dir, "render.cjs")
			writeFile(t, renderPath, renderer, 0o600)
			applyPath, rollbackPath := filepath.Join(dir, "apply.sh"), filepath.Join(dir, "rollback.sh")
			if output, err := exec.Command(node, renderPath, manifestPath, "", applyPath, rollbackPath).CombinedOutput(); err != nil {
				t.Fatalf("render: %v: %s", err, output)
			}
			manifest["tooling"] = map[string]any{
				"apply":    map[string]string{"sha256": sha256OfFile(t, applyPath)},
				"rollback": map[string]string{"sha256": sha256OfFile(t, rollbackPath)},
			}
			data, _ = json.Marshal(manifest)
			writeFile(t, manifestPath, string(data), 0o600)
			private, public := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
			for _, args := range [][]string{
				{"genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", private},
				{"pkey", "-in", private, "-pubout", "-out", public},
				{"dgst", "-sha256", "-sign", private, "-out", filepath.Join(dir, "HOTFIX-MANIFEST.sig"), manifestPath},
			} {
				if output, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
					t.Fatalf("openssl: %v: %s", err, output)
				}
			}
			apply, _ := os.ReadFile(applyPath)
			start := strings.Index(string(apply), "here=\"")
			end := strings.Index(string(apply), "\nsha() {\n")
			if start < 0 || end <= start {
				t.Fatal("pre-mutation verification block missing")
			}
			guard := filepath.Join(dir, "guard.sh")
			writeFile(t, guard, "#!/bin/bash\nset -euo pipefail\n"+string(apply)[start:end]+"\nprintf 'contract guard passed\\n'\n", 0o700)
			switch scenario {
			case "payload changed":
				writeFile(t, helperPath, "#!/bin/sh\nexit 0\n", 0o700)
			case "tool changed":
				writeFile(t, rollbackPath, "#!/bin/sh\nexit 0\n", 0o700)
			case "signature changed":
				writeFile(t, filepath.Join(dir, "HOTFIX-MANIFEST.sig"), "wrong", 0o600)
			}
			command := exec.Command("bash", guard)
			command.Env = append(os.Environ(), "CG_HOTFIX_TRUST_KEY="+public)
			output, err := command.CombinedOutput()
			if scenario == "valid" {
				if err != nil || !strings.Contains(string(output), "contract guard passed") {
					t.Fatalf("signed new Helper must work before installation: %v: %s", err, output)
				}
			} else if err == nil || strings.Contains(string(output), "contract guard passed") {
				t.Fatalf("unsafe guard accepted %s: %v: %s", scenario, err, output)
			}
		})
	}
}
