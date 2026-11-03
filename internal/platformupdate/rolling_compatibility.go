package platformupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The signed RPM bytes, not its NEVRA or a manifest capability assertion, select
// the programs to inspect. rpm2cpio only decodes; no RPM scriptlets run here.
func (c RuntimeCompatibilityChecker) checkRollingManifest(ctx context.Context, manifest string, body []byte, mode Mode, id string) error {
	fail := func(e error) error { return fmt.Errorf("%w: %v", ErrRuntimeIncompatible, e) }
	type candidate struct {
		RPM string `json:"rpm"`
		SHA string `json:"sha256"`
	}
	var m struct {
		ID     string    `json:"patch_id"`
		Source candidate `json:"source"`
		Target candidate `json:"target"`
	}
	if json.Unmarshal(body, &m) != nil || m.ID != id {
		return fail(fmt.Errorf("RPM package identity mismatch"))
	}
	target := m.Target
	if mode == ModeRollback {
		target = m.Source
	}
	if filepath.Base(target.RPM) != target.RPM || !strings.HasPrefix(target.RPM, "clusterguard-ha-") || !strings.HasSuffix(target.RPM, ".rpm") || len(target.SHA) != 64 {
		return fail(fmt.Errorf("invalid signed RPM target"))
	}
	rpm := filepath.Join(filepath.Dir(manifest), "payload", target.RPM)
	if e := trustedRegularFile(rpm, false); e != nil {
		return fail(e)
	}
	data, e := os.ReadFile(rpm)
	if e != nil || len(data) > 128<<20 {
		return fail(fmt.Errorf("RPM unreadable or oversized"))
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != target.SHA {
		return fail(fmt.Errorf("RPM digest mismatch"))
	}
	temp, e := os.MkdirTemp("", "cg-runtime-rpm-")
	if e != nil {
		return fail(e)
	}
	defer os.RemoveAll(temp)
	// Freeze the verified bytes before invoking the decoder.
	frozen := filepath.Join(temp, "candidate.rpm")
	if e = os.WriteFile(frozen, data, 0600); e != nil {
		return fail(e)
	}
	programs := c.programs
	if programs == nil {
		programs = criticalRuntimePrograms
	}
	candidates, e := extractRuntimeRPM(ctx, frozen, temp, programs)
	if e != nil {
		return fail(e)
	}
	for dest, binary := range programs {
		path, ok := candidates[dest]
		if !ok {
			return fail(fmt.Errorf("RPM omits required runtime program: %s", binary))
		}
		inspect := c.inspectProgram
		if inspect == nil {
			inspect = inspectRuntimeProgram
		}
		if e = inspect(ctx, path, binary); e != nil {
			return fail(e)
		}
	}
	return nil
}

func extractRuntimeRPM(ctx context.Context, rpm, root string, programs map[string]string) (map[string]string, error) {
	command := exec.CommandContext(ctx, "rpm2cpio", rpm)
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	command.Stderr = io.Discard
	pipe, e := command.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = command.Start(); e != nil {
		return nil, fmt.Errorf("RPM capability decoder unavailable: %w", e)
	}
	paths, e := readRuntimeCPIO(pipe, root, programs)
	if e != nil {
		_ = command.Process.Kill()
	}
	// Consume trailing archive padding, with a bound, before Wait.
	if e == nil {
		var n int64
		n, e = io.Copy(io.Discard, io.LimitReader(pipe, 1<<20))
		if n == 1<<20 {
			e = fmt.Errorf("RPM trailing output exceeds limit")
			_ = command.Process.Kill()
		}
	}
	wait := command.Wait()
	if e != nil {
		return nil, e
	}
	if wait != nil {
		return nil, fmt.Errorf("RPM payload decode failed: %w", wait)
	}
	return paths, nil
}

// Read newc without extracting arbitrary paths, symlinks, devices or hardlinks.
// RPMs legitimately contain links elsewhere; only required regular executables
// are materialized in a private directory under generated names.
func readRuntimeCPIO(input io.Reader, root string, programs map[string]string) (map[string]string, error) {
	reader := bufio.NewReader(io.LimitReader(input, 512<<20))
	paths := map[string]string{}
	seen := map[string]bool{}
	align := func(n int64) error { _, e := io.CopyN(io.Discard, reader, (4-n%4)%4); return e }
	for count := 0; count < 65536; count++ {
		var header [110]byte
		if _, e := io.ReadFull(reader, header[:]); e != nil {
			return nil, e
		}
		if string(header[:6]) != "070701" {
			return nil, fmt.Errorf("unsupported RPM cpio format")
		}
		number := func(index int) (int64, error) { return strconv.ParseInt(string(header[6+index*8:14+index*8]), 16, 64) }
		mode, e := number(1)
		if e != nil {
			return nil, e
		}
		links, e := number(4)
		if e != nil {
			return nil, e
		}
		size, e := number(6)
		if e != nil {
			return nil, e
		}
		namesize, e := number(11)
		if e != nil || namesize < 1 || namesize > 4096 || size < 0 || size > 128<<20 {
			return nil, fmt.Errorf("invalid RPM cpio entry")
		}
		name := make([]byte, int(namesize))
		if _, e = io.ReadFull(reader, name); e != nil {
			return nil, e
		}
		if name[len(name)-1] != 0 {
			return nil, fmt.Errorf("unterminated RPM path")
		}
		if e = align(110 + namesize); e != nil {
			return nil, e
		}
		raw := string(name[:len(name)-1])
		if raw == "TRAILER!!!" {
			if size != 0 {
				return nil, fmt.Errorf("invalid RPM trailer")
			}
			return paths, nil
		}
		raw = strings.TrimPrefix(raw, "./")
		dest := "/" + raw
		if filepath.Clean(dest) != dest || strings.ContainsRune(raw, 0) || strings.HasPrefix(raw, "/") || strings.Contains(raw, "..") || seen[dest] {
			return nil, fmt.Errorf("unsafe or duplicate RPM path")
		}
		seen[dest] = true
		if _, needed := programs[dest]; needed {
			if mode&0170000 != 0100000 || mode&0022 != 0 || mode&0111 == 0 || links != 1 {
				return nil, fmt.Errorf("required RPM program is not a trusted regular executable")
			}
			path := filepath.Join(root, fmt.Sprintf("program-%d", len(paths)))
			f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if e != nil {
				return nil, e
			}
			_, e = io.CopyN(f, reader, size)
			closed := f.Close()
			if e != nil {
				return nil, e
			}
			if closed != nil {
				return nil, closed
			}
			paths[dest] = path
		} else {
			if _, e = io.CopyN(io.Discard, reader, size); e != nil {
				return nil, e
			}
		}
		if e = align(size); e != nil {
			return nil, e
		}
	}
	return nil, fmt.Errorf("RPM cpio entry limit exceeded")
}
