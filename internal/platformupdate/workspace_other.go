//go:build !linux && !darwin

package platformupdate

import (
	"errors"
	"os"
)

func RunWorkspaceCommand([]string) error {
	return errors.New("secure update workspaces are unsupported on this platform")
}

func openTrustedWorkspaceDirectory(string, bool) (*os.File, error) {
	return nil, errors.New("secure update workspaces are unsupported on this platform")
}
