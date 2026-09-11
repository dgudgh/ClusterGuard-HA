//go:build linux || darwin

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newPeerTestListener(t *testing.T) *net.UnixListener {
	t.Helper()
	root, err := os.MkdirTemp("", "cg-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func assertCredentialRejected(t *testing.T, allowedUID uint32) {
	t.Helper()
	listener := newPeerTestListener(t)
	secured := &credentialListener{UnixListener: listener, allowedUID: allowedUID}
	accepted := make(chan error, 1)
	go func() {
		connection, err := secured.Accept()
		if connection != nil {
			_ = connection.Close()
			accepted <- errors.New("untrusted connection was accepted")
			return
		}
		accepted <- err
	}()
	connection, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	if _, err := connection.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Errorf("untrusted connection was not closed: %v", err)
	}
	_ = listener.Close()
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept did not fail closed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Accept did not exit after listener close")
	}
}
