// Command cgupgrade-pending-order replays the real platformupdate.Snapshot ordering
// against a copy of a site's /var/lib/clusterguard/updates records, then applies the
// console's latest/pending derivation (internal/api/console.html:
// latestSoftwareUpdate / pendingSoftwareUpdate / #execute-software-update gate).
//
// It answers "why is the 滚动升级 button disabled even though the upload verified":
// the button is gated on `pending`, `pending` is derived from `snapshot.packages[0]`,
// and the snapshot is sorted by the recorded `uploaded_at` descending. A record whose
// `uploaded_at` lies in the future (i.e. written while the node clock was ahead)
// therefore shadows every newer upload and pins `pending` to an already-succeeded job.
//
// Usage:
//
//	go run ./tools/diagnostics/cgupgrade-pending-order <updates-root>
//
// Copy the records off the site first, e.g.:
//
//	for p in HF-2026-0928-06 cgupgrade-<from>-to-<to>-x86_64; do
//	  mkdir -p /tmp/snap/$p
//	  scp root@<node>:/var/lib/clusterguard/updates/$p/package.json /tmp/snap/$p/
//	  scp root@<node>:/var/lib/clusterguard/updates/$p/status.json  /tmp/snap/$p/ 2>/dev/null || true
//	done
//
// An absent status.json must stay absent (an empty file is read as a failed job).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"clusterguard.io/ha/internal/platformupdate"
)

const futureTolerance = 5 * time.Minute

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cgupgrade-pending-order <updates-root>")
		os.Exit(2)
	}
	root := os.Args[1]
	now := time.Now().UTC()

	// TrustKeyPath is deliberately non-existent: readiness only sets Snapshot.Reason and
	// does not stop the package scan, so ordering can be inspected off-site.
	manager := platformupdate.NewManager(
		platformupdate.Config{RootDirectory: root, TrustKeyPath: "/nonexistent/trust.pem"},
		platformupdate.WithClock(func() time.Time { return now }),
	)
	snapshot := manager.Snapshot(context.Background())

	fmt.Printf("now(UTC)   %s\n", now.Format(time.RFC3339Nano))
	fmt.Printf("available  %v (reason: %s)\n", snapshot.Available, snapshot.Reason)
	fmt.Printf("packages   %d\n\n", len(snapshot.Packages))

	for index, status := range snapshot.Packages {
		job := "none"
		if status.Job != nil {
			job = string(status.Job.Status)
		}
		skew := status.Package.UploadedAt.Sub(now)
		marker := ""
		if skew > futureTolerance {
			marker = "  <== uploaded_at is in the FUTURE (node clock was ahead)"
		}
		fmt.Printf("[%d] %-44s job=%-10s rolling=%-5v skew=%+s%s\n",
			index, status.Package.PatchID, job, status.Package.Rolling, skew.Round(time.Second), marker)
	}
	fmt.Println()

	if len(snapshot.Packages) == 0 {
		fmt.Println("console: latest=nil -> pending=nil -> 滚动升级 DISABLED")
		return
	}
	latest := snapshot.Packages[0]
	fmt.Printf("console: latest = packages[0] = %s\n", latest.Package.PatchID)
	switch job := latest.Job; {
	case job == nil:
		fmt.Println("console: pending = latest (no job record) -> 滚动升级 ENABLED")
	case job.VerificationRequired,
		job.Status != platformupdate.StatusSucceeded && job.Status != platformupdate.StatusRolledBack:
		fmt.Printf("console: pending = latest (job=%s verification_required=%v) -> 滚动升级 ENABLED\n", job.Status, job.VerificationRequired)
	default:
		fmt.Printf("console: pending = NIL (job=%s verification_required=%v) -> 滚动升级 DISABLED\n", job.Status, job.VerificationRequired)
		fmt.Println("        cause: packages[0] is an already-finished package, not the one just uploaded")
	}
}
