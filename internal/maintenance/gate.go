// Package maintenance combines the local bootstrap marker with the
// Raft-replicated software update gate. The local marker fences a controller
// before it joins; the replicated gate survives Leader changes and coordinates
// the final cluster-wide release.
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

type ReplicatedGateReader interface {
	SoftwareUpdateMaintenanceActive() bool
}

type Gate struct {
	local      FileGate
	replicated ReplicatedGateReader
}

func NewFileGate(path string) FileGate {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultMarkerPath
	}
	return FileGate{path: path}
}

func NewGate(path string, replicated ReplicatedGateReader) Gate {
	return Gate{local: NewFileGate(path), replicated: replicated}
}

func (gate Gate) Check(ctx context.Context) error {
	if err := gate.local.Check(ctx); err != nil {
		return err
	}
	if gate.replicated != nil && gate.replicated.SoftwareUpdateMaintenanceActive() {
		return ErrActive
	}
	return nil
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
