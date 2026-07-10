package api

import (
	"strings"
	"testing"
)

func TestConsoleReadsTheCapabilitiesResponseShape(t *testing.T) {
	if !strings.Contains(string(consoleHTML), "Object.entries(item.features)") {
		t.Fatal("console must render the API feature map instead of an undefined capabilities array")
	}
}

func TestConsoleMarksAnEmptyRefreshAsComplete(t *testing.T) {
	if !strings.Contains(string(consoleHTML), "已刷新控制面状态。") {
		t.Fatal("console must not leave the operator on a connecting status after an empty refresh")
	}
}
