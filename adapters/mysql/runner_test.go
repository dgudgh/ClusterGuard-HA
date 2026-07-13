package mysql

import (
	"errors"
	"strings"
	"testing"
)

func TestQueryErrorDoesNotExposeBackendOutput(t *testing.T) {
	failure := &QueryError{Code: 1045, Output: "ERROR 1045 password=top-secret SELECT * FROM credentials", Err: errors.New("exit status 1")}
	message := failure.Error()
	if strings.Contains(message, "top-secret") || strings.Contains(message, "SELECT") || !strings.Contains(message, "1045") {
		t.Fatalf("unsafe query error message %q", message)
	}
}
