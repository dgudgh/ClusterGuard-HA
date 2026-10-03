package platformupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// A timestamp or matching release line cannot retire a package. Only a
// verified successor's signed declaration and installed deployment can do so.
func (manager *Manager) checkSuperseded(ctx context.Context, patchID string) error {
	entries, err := os.ReadDir(manager.config.RootDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validPatchID(entry.Name()) || entry.Name() == patchID {
			continue
		}
		deployment, installed := manager.Deployment(entry.Name())
		if !installed || deployment.State != "installed" {
			continue
		}
		inspected, err := manager.inspector.Inspect(ctx, filepath.Join(manager.config.RootDirectory, entry.Name(), patchFileName), manager.config.TrustKeyPath)
		if err != nil {
			return fmt.Errorf("cannot revalidate installed successor %s: %w", entry.Name(), err)
		}
		if !inspected.SignatureVerified || inspected.PatchID != entry.Name() {
			return fmt.Errorf("CG_PACKAGE_IDENTITY_MISMATCH: successor %s", entry.Name())
		}
		for _, predecessor := range inspected.Supersedes {
			if predecessor == patchID {
				return fmt.Errorf("CG_PACKAGE_IDENTITY_MISMATCH: %s superseded by installed %s", patchID, entry.Name())
			}
		}
	}
	return nil
}
