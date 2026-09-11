//go:build linux

package main

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestCredentialListenerAcceptsKernelReportedUID(t *testing.T) {
	listener := newPeerTestListener(t)
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	secured := &credentialListener{UnixListener: listener, allowedUID: uint32(os.Getuid())}
	connection, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	accepted, err := secured.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	uid, err := peerUID(accepted.(*net.UnixConn))
	if err != nil || uid != uint32(os.Getuid()) {
		t.Fatalf("kernel uid=%d expected=%d err=%v", uid, os.Getuid(), err)
	}
}

func TestCredentialListenerRejectsOtherUID(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root is explicitly authorized; run as non-root to test rejection")
	}
	assertCredentialRejected(t, uint32(os.Getuid()+1))
}

func TestCredentialListenerAcceptsRootIndependentlyOfServiceUID(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires an actual root peer; credentials are not mocked")
	}
	listener := newPeerTestListener(t)
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	secured := &credentialListener{UnixListener: listener, allowedUID: 65534}
	connection, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	accepted, err := secured.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Close()
}

func TestPeerUIDRejectsClosedSocket(t *testing.T) {
	listener := newPeerTestListener(t)
	connection, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if _, err := peerUID(connection); err == nil {
		t.Fatal("closed socket credentials were accepted")
	}
}
