package platformupdate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type CommandInspector struct {
	UpgradeBinaryPath string
}

func (inspector CommandInspector) Inspect(ctx context.Context, patchPath, trustKeyPath string) (Package, error) {
	binary := strings.TrimSpace(inspector.UpgradeBinaryPath)
	if binary == "" {
		binary = DefaultUpgradeBinaryPath
	}
	command := exec.CommandContext(ctx, binary, "--patch", patchPath, "--trust-key", trustKeyPath, "--inspect")
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 2048 {
			message = message[len(message)-2048:]
		}
		return Package{}, fmt.Errorf("signed update package inspection failed: %s", message)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return Package{
		PatchID: values["patch_id"], SourceVersion: values["source"], TargetVersion: values["target"],
		Architecture: values["architecture"], SignatureVerified: values["signature"] == "verified",
		RollbackAvailable: values["rollback"] == "available", Rolling: values["rolling"] == "true",
		DatabaseMutation: values["database_mutation"] != "false",
	}, nil
}
