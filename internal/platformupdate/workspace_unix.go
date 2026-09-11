//go:build linux || darwin

package platformupdate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// RunWorkspaceCommand is a root-only CLI, not a socket API. No path supplied
// by the service account is ever interpreted as a privileged output path.
func RunWorkspaceCommand(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("workspace commands require root")
	}
	if len(args) == 2 && (args[0] == "check-directory" || args[0] == "create-directory") {
		dir, err := trustedWorkspaceDirectory(args[1], args[0] == "create-directory")
		if err == nil {
			return dir.Close()
		}
		return err
	}
	if len(args) == 2 && args[0] == "check-file" {
		dir, err := trustedWorkspaceDirectory(filepath.Dir(args[1]), false)
		if err != nil {
			return err
		}
		defer dir.Close()
		file, err := openHelperFile(dir, filepath.Base(args[1]), os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer file.Close()
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			return err
		}
		if stat.Uid != 0 || stat.Mode&0o022 != 0 {
			return errors.New("privileged input must be root-owned and not group/world writable")
		}
		return nil
	}
	if len(args) == 3 && args[0] == "snapshot" {
		return snapshotWorkspaceFile(args[1], args[2])
	}
	return errors.New("invalid workspace command")
}

func trustedWorkspaceDirectory(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalidPatch
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		if create {
			if err := unix.Mkdirat(fd, part, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return nil, err
			}
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			unix.Close(fd)
			return nil, err
		}
		if stat.Uid != 0 || stat.Mode&0o022 != 0 {
			unix.Close(fd)
			return nil, fmt.Errorf("untrusted workspace ancestor: %s", path)
		}
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openTrustedWorkspaceDirectory(path string, create bool) (*os.File, error) {
	return trustedWorkspaceDirectory(path, create)
}

func snapshotWorkspaceFile(source, destination string) error {
	dir, err := openHelperDirectory(filepath.Dir(source))
	if err != nil {
		return err
	}
	defer dir.Close()
	input, err := openHelperFile(dir, filepath.Base(source), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer input.Close()
	outputDir, err := trustedWorkspaceDirectory(filepath.Dir(destination), false)
	if err != nil {
		return err
	}
	defer outputDir.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".snapshot-" + hex.EncodeToString(nonce[:])
	output, err := openHelperFile(outputDir, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer output.Close()
	defer unix.Unlinkat(int(outputDir.Fd()), name, 0)
	// A concurrently modified upload may produce invalid bytes, never trusted
	// code: signature and payload verification run on this immutable snapshot.
	const limit = 2 << 30
	n, err := io.Copy(output, io.LimitReader(input, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("update input exceeds snapshot limit")
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(int(outputDir.Fd()), name, int(outputDir.Fd()), filepath.Base(destination)); err != nil {
		return err
	}
	return outputDir.Sync()
}
