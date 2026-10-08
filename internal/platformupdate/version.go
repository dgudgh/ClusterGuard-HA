package platformupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"clusterguard.io/ha/internal/buildinfo"
)

// Called under manager.mu by snapshot/start. Legacy records predate the version
// field; their own signed archive can supply it without changing operation history.
func (manager *Manager) reconcileHotfixVersion(ctx context.Context, softwarePackage Package) (Package, error) {
	if softwarePackage.ArtifactsPruned {
		return softwarePackage, nil
	}
	archive := filepath.Join(manager.config.RootDirectory, softwarePackage.PatchID, patchFileName)
	info, err := os.Lstat(archive)
	if err != nil || !info.Mode().IsRegular() {
		return softwarePackage, ErrInvalidPatch
	}
	trust, err := os.ReadFile(manager.config.TrustKeyPath)
	if err != nil {
		return softwarePackage, err
	}
	trustDigest := sha256.Sum256(trust)
	runner, _ := os.Stat(manager.config.UpgradeBinaryPath)
	key := fmt.Sprintf("%s:%s:%d:%d:%x", softwarePackage.PatchID, softwarePackage.SHA256, info.Size(), info.ModTime().UnixNano(), trustDigest)
	if runner != nil {
		key += fmt.Sprintf(":%d", runner.ModTime().UnixNano())
	}
	file, err := os.Open(archive)
	if err != nil {
		return softwarePackage, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != softwarePackage.SHA256 {
		return softwarePackage, ErrInvalidPatch
	}
	if verified, ok := manager.verifiedPatchVersions[key]; ok {
		softwarePackage.PatchVersion = verified.PatchVersion
		softwarePackage.Supersedes = append([]string(nil), verified.Supersedes...)
		return softwarePackage, nil
	}
	inspected, err := manager.inspector.Inspect(ctx, archive, manager.config.TrustKeyPath)
	if err != nil || !inspected.SignatureVerified || inspected.Kind != PackageKindHotfix ||
		inspected.PatchID != softwarePackage.PatchID || inspected.SourceVersion != softwarePackage.SourceVersion ||
		inspected.TargetVersion != softwarePackage.TargetVersion || inspected.Architecture != softwarePackage.Architecture ||
		(inspected.PatchVersion != "" && !buildinfo.ValidProductVersion(inspected.PatchVersion)) {
		return softwarePackage, ErrInvalidPatch
	}
	softwarePackage.PatchVersion = inspected.PatchVersion
	softwarePackage.Supersedes = append([]string(nil), inspected.Supersedes...)
	manager.verifiedPatchVersions[key] = inspected
	return softwarePackage, nil
}
