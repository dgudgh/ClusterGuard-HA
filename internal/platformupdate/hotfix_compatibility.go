package platformupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"clusterguard.io/ha/internal/runtimecompat"
	"clusterguard.io/ha/pkg/model"
)

const defaultMetadataConfig = "/etc/clusterguard/clusterguard.json"
const defaultBackupDirectory = "/var/lib/clusterguard/hotfix"

var criticalRuntimePrograms = map[string]string{
	"/usr/local/bin/clusterguard":                   "clusterguard",
	"/usr/local/libexec/clusterguard-update-helper": "clusterguard-update-helper",
	"/usr/local/sbin/clusterguard-upgrade":          "clusterguard-upgrade",
	"/usr/local/libexec/clusterguard-update-job.sh": "clusterguard-update-job",
}

type RuntimeCompatibilityChecker struct {
	RootDirectory, MetadataConfigPath, TrustKeyPath, BackupDirectory string
	// Internal injection points keep tests on isolated files, never live binaries.
	requirements   func() (model.RuntimeCompatibilityFloor, error)
	programs       map[string]string
	inspectProgram func(context.Context, string, string) error
}

func (c RuntimeCompatibilityChecker) floor(required model.RuntimeCompatibilityFloor) (model.RuntimeCompatibilityFloor, error) {
	var local model.RuntimeCompatibilityFloor
	var err error
	if c.requirements != nil {
		local, err = c.requirements()
	} else {
		path := c.MetadataConfigPath
		if path == "" {
			path = defaultMetadataConfig
		}
		local, err = runtimecompat.ReadFromConfig(path)
	}
	if err != nil && os.IsNotExist(err) {
		// Data-only nodes do not hold Raft metadata. They may consume the
		// controller floor, but cannot pretend to be controllers with missing state.
		if b, e := os.ReadFile("/etc/clusterguard/node.json"); e == nil && trustedRegularFile("/etc/clusterguard/node.json", false) == nil {
			var node struct {
				Role string           `json:"role"`
				ID   model.ResourceID `json:"resource_id"`
			}
			if json.Unmarshal(b, &node) == nil && node.Role == "data" && model.ValidResourceID(node.ID) {
				err = nil
			}
		}
	}
	if err != nil {
		return local, fmt.Errorf("%w: current metadata cannot be verified: %v", ErrRuntimeIncompatible, err)
	}
	if !local.Valid() || !required.Valid() {
		return local, fmt.Errorf("%w: unknown minimum protocol", ErrRuntimeIncompatible)
	}
	if required.Required() {
		local = required
	}
	return local, nil
}
func (c RuntimeCompatibilityChecker) Check(ctx context.Context, mode Mode, id string, required model.RuntimeCompatibilityFloor) error {
	if mode == ModePlan {
		return nil
	}
	if !allowedMode(mode) || !validPatchID(id) {
		return ErrInvalidPatch
	}
	floor, err := c.floor(required)
	if err != nil {
		return err
	}
	if !floor.Required() {
		return nil
	}
	root := c.RootDirectory
	if root == "" {
		root = DefaultRootDirectory
	}
	archive := filepath.Join(root, id, patchFileName)
	temp, err := os.MkdirTemp("", "cg-runtime-compat-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = extractCompatibilityArchive(archive, temp); err != nil {
		return fmt.Errorf("%w: %v", ErrRuntimeIncompatible, err)
	}
	manifest := filepath.Join(temp, "clusterguard-hotfix", "HOTFIX-MANIFEST.json")
	if _, e := os.Stat(manifest); os.IsNotExist(e) {
		manifest = filepath.Join(temp, "clusterguard-patch", "PATCH-MANIFEST.json")
	}
	return c.checkManifest(ctx, manifest, mode, id, floor)
}

type compatibilityManifest struct {
	ID    string `json:"hotfix_id"`
	Files []struct {
		Artifact string `json:"artifact"`
		Path     string `json:"install_path"`
		SHA256   string `json:"sha256"`
	} `json:"files"`
}

func (c RuntimeCompatibilityChecker) trustKey() (string, error) {
	if c.TrustKeyPath != "" {
		return c.TrustKeyPath, nil
	}
	b, e := os.ReadFile("/etc/clusterguard/update.json")
	if e != nil {
		return "", e
	}
	var cfg struct {
		Key string `json:"trust_key"`
	}
	if e = json.Unmarshal(b, &cfg); e != nil || !filepath.IsAbs(cfg.Key) {
		return "", fmt.Errorf("trusted update key unavailable")
	}
	return cfg.Key, nil
}
func (c RuntimeCompatibilityChecker) checkManifest(ctx context.Context, path string, mode Mode, id string, required model.RuntimeCompatibilityFloor) error {
	if !validPatchID(id) || !allowedMode(mode) || mode == ModePlan {
		return ErrInvalidPatch
	}
	floor, err := c.floor(required)
	if err != nil {
		return err
	}
	if !floor.Required() {
		return nil
	}
	fail := func(e error) error { return fmt.Errorf("%w: %v", ErrRuntimeIncompatible, e) }
	key, err := c.trustKey()
	if err != nil {
		return fail(err)
	}
	for _, p := range []string{path, strings.TrimSuffix(path, ".json") + ".sig", key} {
		if e := trustedRegularFile(p, false); e != nil {
			return fail(e)
		}
	}
	command := exec.CommandContext(ctx, "openssl", "dgst", "-sha256", "-verify", key, "-signature", strings.TrimSuffix(path, ".json")+".sig", path)
	if b, e := command.CombinedOutput(); e != nil {
		return fail(fmt.Errorf("manifest signature verification failed (%d bytes)", len(b)))
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 1<<20 {
		return fail(fmt.Errorf("manifest unreadable or too large"))
	}
	if filepath.Base(path) == "PATCH-MANIFEST.json" {
		return c.checkRollingManifest(ctx, path, b, mode, id)
	}
	var m compatibilityManifest
	if err = json.Unmarshal(b, &m); err != nil || m.ID != id || !validPatchID(id) {
		return fail(fmt.Errorf("backup/package identity mismatch"))
	}
	programs := c.programs
	if programs == nil {
		programs = criticalRuntimePrograms
	}
	entries := map[string]struct{ artifact, sha string }{}
	for _, f := range m.Files {
		if _, ok := programs[f.Path]; !ok {
			continue
		}
		if _, found := entries[f.Path]; found {
			return fail(fmt.Errorf("duplicate runtime target"))
		}
		if !strings.HasPrefix(f.Artifact, "payload/") || filepath.Clean(f.Artifact) != f.Artifact || strings.Contains(f.Artifact, "..") || len(f.SHA256) != 64 {
			return fail(fmt.Errorf("invalid signed runtime target"))
		}
		entries[f.Path] = struct{ artifact, sha string }{f.Artifact, f.SHA256}
	}
	backups := map[string]string{}
	if mode == ModeRollback {
		dir := c.BackupDirectory
		if dir == "" {
			dir = defaultBackupDirectory
		}
		backups, err = readRuntimeBackups(dir, id)
		if err != nil {
			return fail(err)
		}
	}
	paths := make([]string, 0, len(programs))
	for dest := range programs {
		paths = append(paths, dest)
	}
	sort.Strings(paths)
	for _, dest := range paths {
		candidate := dest
		if entry, changed := entries[dest]; changed {
			if mode == ModeRollback {
				var ok bool
				candidate, ok = backups[dest]
				if !ok || candidate == "" || (!strings.HasPrefix(candidate, dest+".bak-") || filepath.Dir(candidate) != filepath.Dir(dest) || filepath.Clean(candidate) != candidate) {
					return fail(fmt.Errorf("original backup is absent or outside target identity: %s", dest))
				}
			} else {
				candidate = filepath.Join(filepath.Dir(path), entry.artifact)
				data, e := os.ReadFile(candidate)
				if e != nil {
					return fail(e)
				}
				digest := sha256.Sum256(data)
				if hex.EncodeToString(digest[:]) != entry.sha {
					return fail(fmt.Errorf("runtime payload digest mismatch"))
				}
				// Extraction uses private0600 files. Execute only after signed hash verification.
				if err = os.Chmod(candidate, 0700); err != nil {
					return fail(err)
				}
			}
		}
		inspect := c.inspectProgram
		if inspect == nil {
			inspect = inspectRuntimeProgram
		}
		if err = inspect(ctx, candidate, programs[dest]); err != nil {
			return fail(err)
		}
	}
	return nil
}
func trustedRegularFile(path string, executable bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("runtime evidence must be a non-writable regular file: %s", path)
	}
	if executable && st.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("runtime program is not executable")
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok && s.Uid != 0 && int(s.Uid) != os.Geteuid() {
		return fmt.Errorf("runtime evidence owner is untrusted")
	}
	return nil
}
func inspectRuntimeProgram(ctx context.Context, path, binary string) error {
	if err := trustedRegularFile(path, true); err != nil {
		return err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(before)
	arg := "--version-json"
	if binary == "clusterguard-upgrade" || binary == "clusterguard-update-job" {
		arg = "--runtime-compatibility-protocol"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, arg)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	// Never allow an old or malformed candidate to emit unbounded diagnostics.
	var out boundedCompatibilityOutput
	command.Stdout = &out
	command.Stderr = io.Discard
	if err = command.Run(); err != nil {
		return fmt.Errorf("target program does not report supported runtime capabilities: %s", binary)
	}
	var info struct {
		Binary   string `json:"binary"`
		Protocol int    `json:"runtime_compatibility_protocol"`
		Policy   int    `json:"instance_failover_policy_protocol"`
	}
	if err = json.Unmarshal(out.data, &info); err != nil || info.Binary != binary || info.Protocol != model.RuntimeCompatibilityProtocol || info.Policy != model.InstanceFailoverPolicyProtocol {
		return fmt.Errorf("target program is incompatible or unknown: %s", binary)
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != digest {
		return fmt.Errorf("target changed during capability inspection")
	}
	return nil
}

type boundedCompatibilityOutput struct{ data []byte }

func (b *boundedCompatibilityOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 16384 {
		return 0, fmt.Errorf("capability output exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func readRuntimeBackups(dir, id string) (map[string]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "backup-"+id+"-*.txt"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("package-bound backup manifests unavailable")
	}
	sort.Strings(files)
	result := map[string]string{}
	for _, path := range files {
		if err = trustedRegularFile(path, false); err != nil {
			return nil, err
		}
		info, e := os.Stat(path)
		if e != nil || info.Size() > 8<<20 {
			return nil, fmt.Errorf("backup manifest too large or unreadable")
		}
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		scanner := bufio.NewScanner(io.LimitReader(f, 8<<20))
		if !scanner.Scan() || scanner.Text() != "# package_id="+id {
			f.Close()
			return nil, fmt.Errorf("backup manifest identity mismatch")
		}
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Split(line, "\t")
			if len(parts) != 2 || !filepath.IsAbs(parts[0]) {
				f.Close()
				return nil, fmt.Errorf("malformed backup entry")
			}
			if _, seen := result[parts[0]]; !seen {
				result[parts[0]] = parts[1]
			}
		}
		e = scanner.Err()
		f.Close()
		if e != nil {
			return nil, e
		}
	}
	return result, nil
}
func extractCompatibilityArchive(path, root string) error {
	st, e := os.Lstat(path)
	// Uploaded bytes belong to the console account and remain untrusted until
	// signature/digest verification inside the private extraction directory.
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("archive must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	g, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer g.Close()
	r := tar.NewReader(g)
	var total int64
	count := 0
	layout := ""
	for {
		h, e := r.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		count++
		name := strings.TrimSuffix(h.Name, "/")
		top := strings.SplitN(name, "/", 2)[0]
		if count > 4096 || filepath.Clean(name) != name || (top != "clusterguard-hotfix" && top != "clusterguard-patch") || strings.Contains(name, "..") || (layout != "" && layout != top) {
			return fmt.Errorf("unsupported or unsafe compatibility archive")
		}
		layout = top
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 128<<20 {
			return fmt.Errorf("invalid compatibility archive entry")
		}
		total += h.Size
		if total > 512<<20 {
			return fmt.Errorf("compatibility archive exceeds limit")
		}
		dest := filepath.Join(root, name)
		if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
			return e
		}
		w, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = io.CopyN(w, r, h.Size)
		ce := w.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
}

// RunRuntimeCompatibilityCommand is read-only apart from private temporary extraction.
// No force, clear-floor or fallback-to-unknown option exists.
func RunRuntimeCompatibilityCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("compatibility command required")
	}
	mode := args[0]
	flags := flag.NewFlagSet("runtime-compatibility", flag.ContinueOnError)
	cfg := flags.String("config", defaultMetadataConfig, "trusted runtime config")
	manifest := flags.String("manifest", "", "verified hotfix manifest")
	id := flags.String("patch-id", "", "exact package ID")
	action := flags.String("mode", "rollback", "update mode")
	required := flags.String("required-json", "{}", "additional cluster floor")
	key := flags.String("trust-key", "", "trusted public key")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected compatibility arguments")
	}
	c := RuntimeCompatibilityChecker{MetadataConfigPath: *cfg, TrustKeyPath: *key}
	if mode == "requirements" {
		floor, e := c.floor(model.RuntimeCompatibilityFloor{})
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(floor)
	}
	if mode != "check" || !allowedMode(Mode(*action)) || Mode(*action) == ModePlan {
		return ErrInvalidPatch
	}
	var floor model.RuntimeCompatibilityFloor
	decoder := json.NewDecoder(strings.NewReader(*required))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&floor); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidPatch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return c.checkManifest(ctx, *manifest, Mode(*action), *id, floor)
}
