package configuration

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"clusterguard.io/ha/internal/config"
)

// publishValidatedOverrides restores the previous overlay when a newly written
// candidate fails validation. It never restarts or releases the task's gate.
// A concurrent/foreign edit is evidence of lost ownership, never permission to
// overwrite it. Retain the rejected candidate before restoring the startup file.
func publishValidatedOverrides(path string, previous, next config.ConfigurationOverrides, validate func() error) error {
	current, err := config.ReadConfigurationOverrides(path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, previous) {
		return fmt.Errorf("configuration changed before publishing")
	}
	writeErr := config.WriteConfigurationOverrides(path, next)
	if writeErr == nil {
		writeErr = validate()
	}
	if writeErr == nil {
		return nil
	}
	current, err = config.ReadConfigurationOverrides(path)
	if err == nil && reflect.DeepEqual(current, previous) {
		return fmt.Errorf("configuration publication failed; previous overlay unchanged: %w", writeErr)
	}
	if err != nil || !reflect.DeepEqual(current, next) {
		return fmt.Errorf("configuration publication failed; ownership changed, restoration refused: %w", writeErr)
	}
	// Task IDs are UUIDs (optionally .rollback); the digest is canonical hex.
	evidence := filepath.Join(filepath.Dir(path), "configuration-"+next.TaskID+".rejected-"+next.Digest+".json")
	if err = saveBackup(evidence, next); err != nil {
		return fmt.Errorf("configuration rejected; evidence save failed, restoration withheld: %w", err)
	}
	current, err = config.ReadConfigurationOverrides(path)
	if err != nil || !reflect.DeepEqual(current, next) {
		return fmt.Errorf("configuration ownership changed before restoration")
	}
	if previous.TaskID != "" {
		err = config.WriteConfigurationOverrides(path, previous)
	} else {
		err = os.Remove(path)
		if err == nil {
			var dir *os.File
			dir, err = os.Open(filepath.Dir(path))
			if err == nil {
				err = dir.Sync()
				dir.Close()
			}
		}
	}
	if err != nil {
		return fmt.Errorf("configuration rejected; restoration failed: %w", err)
	}
	return fmt.Errorf("configuration rejected; previous overlay restored, maintenance retained: %w", writeErr)
}
