package mysql

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clusterguard.io/ha/pkg/adapter"
)

func TestQueryErrorDoesNotExposeBackendOutput(t *testing.T) {
	failure := &QueryError{Code: 1045, Output: "ERROR 1045 password=top-secret SELECT * FROM credentials", Err: errors.New("exit status 1")}
	message := failure.Error()
	if strings.Contains(message, "top-secret") || strings.Contains(message, "SELECT") || !strings.Contains(message, "1045") {
		t.Fatalf("unsafe query error message %q", message)
	}
}

func TestCLIQueryRunnerKeepsConnectTimeoutInsideCallerDeadline(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	binaryPath := filepath.Join(directory, "mysql")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CG_MYSQL_ARGUMENTS\"\nprintf 'value\\n1\\n'\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake mysql client: %v", err)
	}
	t.Setenv("CG_MYSQL_ARGUMENTS", argumentsPath)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	rows, err := (CLIQueryRunner{Binary: binaryPath}).Query(ctx, adapter.Endpoint{Hostname: "mysql-a", Port: 3306}, adapter.Credentials{Username: "discover"}, "SELECT 1 AS value")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 || rows[0]["value"] != "1" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatalf("read fake mysql arguments: %v", err)
	}
	connectTimeout := 0
	for _, argument := range strings.Split(string(arguments), "\n") {
		if !strings.HasPrefix(argument, "--connect-timeout=") {
			continue
		}
		connectTimeout, err = strconv.Atoi(strings.TrimPrefix(argument, "--connect-timeout="))
		if err != nil {
			t.Fatalf("parse connect timeout %q: %v", argument, err)
		}
	}
	if connectTimeout < 1 || connectTimeout >= 4 {
		t.Fatalf("connect timeout = %d, want a positive timeout below the 4s caller deadline", connectTimeout)
	}
}
