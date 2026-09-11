//go:build linux || darwin

package platformupdate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Pin every directory component before accessing the service-writable staging
// area. A lexical child check alone cannot prevent ancestor symlink traversal.
func openHelperDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidPatch
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		next, err := unix.Openat(fd, component, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, &os.PathError{Op: "open helper directory", Path: path, Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openHelperFile(directory *os.File, name string, flags int, mode os.FileMode) (*os.File, error) {
	if name == "." || name == ".." || name == "" || filepath.Base(name) != name || flags&os.O_TRUNC != 0 {
		return nil, ErrInvalidPatch
	}
	// NONBLOCK prevents a FIFO from hanging the privileged helper before fstat
	// rejects it. Never truncate or chmod until the opened inode is validated.
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "open helper file", Path: name, Err: err}
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, ErrInvalidPatch
	}
	return os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name)), nil
}

func writeHelperJob(directory *os.File, job Job) error {
	contents, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".status-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := openHelperFile(directory, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	defer unix.Unlinkat(int(directory.Fd()), name, 0)
	if _, err := file.Write(append(contents, '\n')); err != nil {
		return err
	}
	if err := file.Chmod(updateFileMode); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return unix.Renameat(int(directory.Fd()), name, int(directory.Fd()), jobFileName)
}
