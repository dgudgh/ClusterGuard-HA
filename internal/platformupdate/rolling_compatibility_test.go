package platformupdate

import (
	"bytes"
	"clusterguard.io/ha/pkg/model"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type cpioFixtureEntry struct {
	name        string
	mode, links uint64
	body        []byte
}

func runtimeCPIO(entries []cpioFixtureEntry) []byte {
	var b bytes.Buffer
	for _, v := range append(entries, cpioFixtureEntry{name: "TRAILER!!!", links: 1}) {
		fmt.Fprintf(&b, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x", 1, v.mode, 0, 0, v.links, 0, len(v.body), 0, 0, 0, 0, len(v.name)+1, 0)
		b.WriteString(v.name)
		b.WriteByte(0)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
		b.Write(v.body)
		for b.Len()%4 != 0 {
			b.WriteByte(0)
		}
	}
	return b.Bytes()
}

// Minimal RPM fixture for the real system rpm2cpio decoder. It is signed by
// the ephemeral outer package key, never published or installed as an RPM.
func runtimeRPM(entries []cpioFixtureEntry) []byte {
	var b bytes.Buffer
	lead := make([]byte, 96)
	copy(lead, []byte{0xed, 0xab, 0xee, 0xdb, 3, 0})
	binary.BigEndian.PutUint16(lead[78:80], 5)
	b.Write(lead)
	header := func(n, size uint32) {
		b.Write([]byte{0x8e, 0xad, 0xe8, 1, 0, 0, 0, 0})
		binary.Write(&b, binary.BigEndian, n)
		binary.Write(&b, binary.BigEndian, size)
	}
	header(1, 4)
	for _, n := range []uint32{1000, 4, 0, 1, 0, 0} {
		binary.Write(&b, binary.BigEndian, n)
	} // signature size tag + 8-byte header padding
	header(2, 10)
	for _, v := range []struct{ tag, offset uint32 }{{1124, 0}, {1125, 5}} {
		for _, n := range []uint32{v.tag, 6, v.offset, 1} {
			binary.Write(&b, binary.BigEndian, n)
		}
	}
	b.WriteString("cpio\x00gzip\x00")
	w := gzip.NewWriter(&b)
	w.Write(runtimeCPIO(entries))
	w.Close()
	return b.Bytes()
}
func TestRuntimeCompatibilityCPIORejectsUnsafeOrMissingRuntimeEvidence(t *testing.T) {
	dest := "/usr/local/bin/clusterguard"
	for _, scenario := range []string{"regular", "symlink", "writable", "hardlink", "traversal", "duplicate", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			entry := cpioFixtureEntry{name: "." + dest, mode: 0100755, links: 1, body: runtimeProgram("clusterguard", true)}
			switch scenario {
			case "symlink":
				entry.mode = 0120777
			case "writable":
				entry.mode = 0100775
			case "hardlink":
				entry.links = 2
			case "traversal":
				entry.name = "../bin/clusterguard"
			}
			entries := []cpioFixtureEntry{entry}
			if scenario == "duplicate" {
				entries = append(entries, entry)
			}
			data := runtimeCPIO(entries)
			if scenario == "truncated" {
				data = data[:len(data)/2]
			}
			paths, e := readRuntimeCPIO(bytes.NewReader(data), t.TempDir(), map[string]string{dest: "clusterguard"})
			if scenario == "regular" {
				if e != nil || len(paths) != 1 {
					t.Fatalf("valid candidate rejected: %v", e)
				}
				if e = inspectRuntimeProgram(context.Background(), paths[dest], "clusterguard"); e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
}
func TestRuntimeCompatibilitySignedRPMTargets(t *testing.T) {
	if _, e := exec.LookPath("rpm2cpio"); e != nil {
		t.Skip("real Linux RPM decoder test; also run in root-isolated Linux acceptance")
	}
	for _, scenario := range []string{"compatible-upgrade", "compatible-rollback", "old-original", "wrong-package", "tampered-rpm", "missing-runtime-program"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRuntimeFixture(t)
			manifest := filepath.Join(f.root, "PATCH-MANIFEST.json")
			os.MkdirAll(filepath.Join(f.root, "payload"), 0700)
			source := []cpioFixtureEntry{}
			target := []cpioFixtureEntry{}
			for dest, binary := range f.programs {
				known := scenario != "old-original"
				source = append(source, cpioFixtureEntry{name: "." + dest, mode: 0100755, links: 1, body: runtimeProgram(binary, known)})
				target = append(target, cpioFixtureEntry{name: "." + dest, mode: 0100755, links: 1, body: runtimeProgram(binary, true)})
			}
			if scenario == "missing-runtime-program" {
				source = source[1:]
			}
			candidate := func(name string, entries []cpioFixtureEntry) map[string]string {
				data := runtimeRPM(entries)
				p := filepath.Join(f.root, "payload", name)
				os.WriteFile(p, data, 0600)
				sum := sha256.Sum256(data)
				return map[string]string{"rpm": name, "sha256": hex.EncodeToString(sum[:])}
			}
			id := "policy-patch"
			if scenario == "wrong-package" {
				id = "different"
			}
			body, _ := json.Marshal(map[string]interface{}{"patch_id": id, "source": candidate("clusterguard-ha-source.rpm", source), "target": candidate("clusterguard-ha-target.rpm", target)})
			os.WriteFile(manifest, body, 0600)
			sum := sha256.Sum256(body)
			sig, e := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
			if e != nil {
				t.Fatal(e)
			}
			os.WriteFile(strings.TrimSuffix(manifest, ".json")+".sig", sig, 0600)
			if scenario == "tampered-rpm" {
				os.WriteFile(filepath.Join(f.root, "payload", "clusterguard-ha-source.rpm"), []byte("different"), 0600)
			}
			mode := ModeRollback
			if scenario == "compatible-upgrade" {
				mode = ModeExecute
			}
			e = f.checker.checkManifest(context.Background(), manifest, mode, "policy-patch", model.PolicyCompatibilityFloor())
			want := strings.HasPrefix(scenario, "compatible-")
			expectedReason := map[string]string{"old-original": "incompatible or unknown", "wrong-package": "identity mismatch", "tampered-rpm": "digest mismatch", "missing-runtime-program": "omits required runtime program"}
			if reason := expectedReason[scenario]; reason != "" && (e == nil || !strings.Contains(e.Error(), reason)) {
				t.Fatalf("wrong rejection: wanted %s, got %v", reason, e)
			}
			if (e == nil) != want {
				var diagnostic bytes.Buffer
				cmd := exec.Command("rpm2cpio", filepath.Join(f.root, "payload", "clusterguard-ha-target.rpm"))
				cmd.Stdout = &bytes.Buffer{}
				cmd.Stderr = &diagnostic
				runErr := cmd.Run()
				t.Fatalf("accepted=%v want=%v error=%v decoder=%v stderr=%s", e == nil, want, e, runErr, diagnostic.String())
			}
		})
	}
}
