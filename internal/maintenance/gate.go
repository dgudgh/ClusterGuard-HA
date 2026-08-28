// Package maintenance provides the local, persistent execution gate used by
// the rolling software updater. The marker is intentionally outside Raft
// state so a controller can enforce it before joining the upgraded process.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

const DefaultMarkerPath = "/etc/clusterguard/update-maintenance.json"

var ErrActive = errors.New("software update maintenance is active")

type FileGate struct {
	path string
}

func NewFileGate(path string) FileGate {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultMarkerPath
	}
	return FileGate{path: path}
}

func (gate FileGate) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(gate.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect software update maintenance marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("software update maintenance marker is not a regular file")
	}
	return ErrActive
}
