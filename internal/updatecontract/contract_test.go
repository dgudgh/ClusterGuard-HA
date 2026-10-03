package updatecontract

import (
	"strings"
	"testing"
)

func TestContractFailClosed(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"missing":       "",
		"unsupported":   strings.Replace(Document, "  version: 2", "  version: 3", 1),
		"unknown":       strings.Replace(Document, "  version: 2", "  version: 2\n  unknown: true", 1),
		"duplicate":     strings.Replace(Document, "  version: 2", "  version: 2\n  version: 2", 1),
		"nesting":       strings.Replace(Document, "  version: 2", "    version: 2", 1),
		"missing field": strings.Replace(Document, "  version: 2\n", "", 1),
		"weakened":      strings.Replace(Document, "  fail_closed: true", "  fail_closed: false", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Parse(source); err == nil || !strings.Contains(err.Error(), "CG_CONTRACT_UNAVAILABLE") {
				t.Fatalf("invalid contract accepted: %v", err)
			}
		})
	}
}
