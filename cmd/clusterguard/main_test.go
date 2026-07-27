package main

import (
	"bytes"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformauth "clusterguard.io/ha/internal/auth"
)

func TestHTTPServerHasBoundedRequestTimeouts(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.WriteTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("HTTP server has unbounded timeouts: %+v", server)
	}
	if server.MaxHeaderBytes <= 0 || server.MaxHeaderBytes > 128<<10 {
		t.Fatalf("HTTP server MaxHeaderBytes=%d", server.MaxHeaderBytes)
	}
}

func TestShutdownHTTPServerDrainsActiveRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	}))
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if response != nil {
			response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- shutdownHTTPServer(server, time.Second) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before active request drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("active request failed: %v", err)
	}
	if err := <-serveDone; err != nil && err != http.ErrServerClosed {
		t.Fatalf("serve result: %v", err)
	}
}

func TestDefaultConfigurationUsesOfficialClusterGuardPath(t *testing.T) {
	if defaultConfigPath != "/etc/clusterguard/clusterguard.json" {
		t.Fatalf("default config path = %q", defaultConfigPath)
	}
}

func TestParseServerFlagsSupportsConfigurationValidation(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseServerFlags([]string{"--config", "/tmp/clusterguard.json", "--check-config"}, &stderr)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("parse server flags options=%+v err=%v stderr=%q", options, err, stderr.String())
	}
	if options.configPath != "/tmp/clusterguard.json" || !options.checkConfig {
		t.Fatalf("parse server flags options=%+v", options)
	}
}

func TestParseServerFlagsRejectsUnexpectedArguments(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseServerFlags([]string{"serve"}, &stderr); err == nil {
		t.Fatal("expected unexpected positional argument to be rejected")
	}
}

func TestLogConfigurationWarningsPrintsDeprecations(t *testing.T) {
	var output bytes.Buffer
	logConfigurationWarnings(log.New(&output, "", 0), []string{
		"CG_APPROVAL_TOKEN is deprecated for database operations",
	})
	if !strings.Contains(output.String(), "configuration warning: CG_APPROVAL_TOKEN is deprecated") {
		t.Fatalf("warning output=%q", output.String())
	}
}

func TestPrepareAdminRecoveryWritesPrivateHashArtifactAndPrintsPasswordOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	var stdout, stderr bytes.Buffer
	exitCode := runAdmin(
		[]string{"prepare-recovery", "--output", path}, &stdout, &stderr,
		bytes.NewReader(bytes.Repeat([]byte{0x61}, 4096)),
		func() time.Time { return time.Date(2026, time.July, 17, 3, 30, 0, 0, time.UTC) },
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("prepare recovery exit=%d stderr=%q", exitCode, stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("recovery artifact mode=%v err=%v", info.Mode().Perm(), err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recovery artifact: %v", err)
	}
	marker := "Temporary password (shown once): "
	index := strings.Index(stdout.String(), marker)
	if index < 0 {
		t.Fatalf("recovery output=%q", stdout.String())
	}
	password := strings.TrimSpace(stdout.String()[index+len(marker):])
	if len(password) < 12 || bytes.Contains(contents, []byte(password)) {
		t.Fatalf("unsafe generated recovery password or artifact: password-length=%d artifact=%s", len(password), contents)
	}
	artifact, err := platformauth.ReadAdminRecoveryArtifact(path, time.Date(2026, time.July, 17, 3, 31, 0, 0, time.UTC))
	if err != nil || artifact.Username != platformauth.DefaultAdminUsername {
		t.Fatalf("read generated recovery artifact=%+v err=%v", artifact, err)
	}
}

func TestPrepareAdminRecoveryRefusesToOverwriteExistingArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-recovery.json")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed recovery artifact: %v", err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := runAdmin(
		[]string{"prepare-recovery", "--output", path}, &stdout, &stderr,
		bytes.NewReader(bytes.Repeat([]byte{0x62}, 4096)), time.Now,
	)
	if exitCode == 0 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("overwrite recovery exit=%d stderr=%q", exitCode, stderr.String())
	}
}
