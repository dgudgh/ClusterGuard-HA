package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"clusterguard.io/ha/internal/platformupdate"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "workspace" {
		if err := platformupdate.RunWorkspaceCommand(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	socketPath := flag.String("socket", platformupdate.DefaultHelperSocketPath, "Unix socket exposed to the ClusterGuard service account")
	root := flag.String("root", platformupdate.DefaultRootDirectory, "software update staging directory")
	runner := flag.String("runner", "/usr/local/libexec/clusterguard-update-job.sh", "root-owned update job runner")
	privateRoot := flag.String("private-root", platformupdate.DefaultPrivateRoot, "root-owned software update execution directory")
	flag.Parse()
	if err := run(*socketPath, *root, *runner, *privateRoot); err != nil {
		log.Fatal(err)
	}
}

func run(socketPath, root, runner, privateRoot string) error {
	info, err := os.Stat(runner)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !ownedByRoot(info) {
		return fmt.Errorf("update runner must be a root-owned, non-writable regular file: %s", runner)
	}
	account, err := user.Lookup("clusterguard")
	if err != nil {
		return fmt.Errorf("resolve clusterguard service account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if info, statErr := os.Lstat(socketPath); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace a non-socket update helper path")
		}
		if err := os.Remove(socketPath); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chown(socketPath, 0, gid); err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	secured := &credentialListener{UnixListener: listener, allowedUID: uint32(uid)}
	handler := platformupdate.NewHelperHandlerWithPrivateRoot(root, privateRoot, platformupdate.CommandLauncher{RunnerPath: runner})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	terminated := make(chan os.Signal, 1)
	signal.Notify(terminated, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-terminated
		_ = server.Close()
	}()
	err = server.Serve(secured)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

type credentialListener struct {
	*net.UnixListener
	allowedUID uint32
}

func (listener *credentialListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, credentialErr := peerUID(connection)
		if credentialErr == nil && (uid == 0 || uid == listener.allowedUID) {
			return connection, nil
		}
		_ = connection.Close()
	}
}
