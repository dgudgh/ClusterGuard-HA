package platformupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// A timestamp or matching release line cannot retire a package. Only a
// verified successor's signed declaration and installed deployment can do so.
//
// "Installed" has two sources on purpose. The deployment record is the direct
// one. It is not the only one, and on 2026-10-08 it was missing exactly where
// this guard mattered: HF-2026-1008-01 was applied at 16:57 by an update runner
// that predated deployment.json, so no site has that record - not the public
// tree, not the private one. A guard that reads only the deployment record is
// not a conservative guard, it is a guard that never runs, and the first thing
// it fails to stop is a rollback of an already-superseded patch that silently
// reverts files a newer, verified patch had replaced.
func (manager *Manager) checkSuperseded(ctx context.Context, patchID string) error {
	entries, err := os.ReadDir(manager.config.RootDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validPatchID(entry.Name()) || entry.Name() == patchID {
			continue
		}
		if !manager.successorInstalled(entry.Name()) {
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

// successorInstalled answers "is this package's payload on disk" from the two
// records that can answer it: the deployment record, or the operation that the
// runner only writes as succeeded after every declared file matched its signed
// digest on every node.
//
// The second source is restricted to hotfixes on purpose. A declaration of
// supersession lives in a hotfix manifest, so a rolling package can never be a
// blocking successor; and treating every succeeded package as one would make
// this guard re-inspect the whole update history - including RPM packages from
// release lines this script no longer accepts - before every action, turning a
// guard against one silent rollback into a way to block all of them.
func (manager *Manager) successorInstalled(patchID string) bool {
	if deployment, ok := manager.Deployment(patchID); ok {
		return deployment.State == "installed"
	}
	softwarePackage, found := manager.Package(patchID)
	if !found || softwarePackage.Kind != PackageKindHotfix {
		return false
	}
	job, found := manager.Job(patchID)
	return found && payloadInstalled(job)
}
