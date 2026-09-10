// Package disaster coordinates explicit, fail-closed whole-cluster recovery.
package disaster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	mysql "clusterguard.io/ha/pkg/gtid"
	"clusterguard.io/ha/pkg/model"
)

func Digest(value interface{}) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func EvidenceFingerprint(e model.RecoveryEvidence) string {
	e.Fingerprint = ""
	e.ObservedAt = time.Time{}
	return Digest(e)
}

func LSN(value string) (uint64, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[0]) > 8 || len(parts[1]) > 8 {
		return 0, fmt.Errorf("invalid WAL position")
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid WAL position")
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid WAL position")
	}
	return hi<<32 | lo, nil
}

func ValidateEvidence(members []model.DatabaseInstance, evidence []model.RecoveryEvidence) error {
	if len(members) < 2 || len(evidence) != len(members) {
		return fmt.Errorf("every registered member must provide fresh fenced evidence")
	}
	seen := map[model.ResourceID]bool{}
	native := map[string]bool{}
	for _, member := range members {
		if member.Engine != members[0].Engine || member.ClusterID != members[0].ClusterID || !model.ValidResourceID(member.ResourceID) {
			return fmt.Errorf("recovery members must belong to one registered engine and cluster")
		}
	}
	for _, e := range evidence {
		if seen[e.InstanceID] || !e.Fenced || !e.Complete || e.PreparedTransactions || e.ObservedAt.IsZero() || e.Fingerprint != EvidenceFingerprint(e) {
			return fmt.Errorf("member evidence is duplicate, incomplete, unfenced or contains prepared transactions")
		}
		seen[e.InstanceID] = true
		found := false
		for _, member := range members {
			if member.ResourceID != e.InstanceID {
				continue
			}
			key := "resource_id"
			if member.Engine == model.EngineMySQL {
				key = "server_uuid"
			}
			if e.Engine != member.Engine || e.NativeID == "" || !strings.EqualFold(e.NativeID, member.EngineIdentity[key]) {
				return fmt.Errorf("native member identity changed")
			}
			if e.Engine == model.EnginePostgreSQL && (e.SystemIdentifier == "" || e.SystemIdentifier != member.EngineIdentity["system_identifier"]) {
				return fmt.Errorf("PostgreSQL system identifier changed")
			}
			found = true
		}
		if !found || native[strings.ToLower(e.NativeID)] {
			return fmt.Errorf("unregistered or duplicated native identity")
		}
		native[strings.ToLower(e.NativeID)] = true
	}
	return nil
}

// Select requires a candidate that contains every peer's committed history.
// Equivalent copies are tied by immutable UUID, never by wall clock or host name.
func Select(members []model.DatabaseInstance, evidence []model.RecoveryEvidence, proofs []model.RecoveryProof) (model.ResourceID, error) {
	if err := ValidateEvidence(members, evidence); err != nil {
		return "", err
	}
	var candidates []model.ResourceID
	for _, candidate := range evidence {
		containsAll := true
		for _, other := range evidence {
			if candidate.InstanceID == other.InstanceID {
				continue
			}
			var contains bool
			var err error
			switch candidate.Engine {
			case model.EngineMySQL:
				contains, err = mysqlContains(candidate, other)
			case model.EnginePostgreSQL:
				contains, err = PostgreSQLContains(candidate, other, proofs)
			default:
				return "", fmt.Errorf("disaster recovery is not qualified for this engine")
			}
			if err != nil {
				return "", err
			}
			if !contains {
				containsAll = false
				break
			}
		}
		if containsAll {
			candidates = append(candidates, candidate.InstanceID)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no member provably contains all committed history; divergent commits or missing WAL require manual review")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates[0], nil
}

func mysqlContains(candidate, other model.RecoveryEvidence) (bool, error) {
	c, err := mysql.ParseGTIDSet(candidate.GTIDExecuted)
	if err != nil {
		return false, fmt.Errorf("invalid executed GTID evidence")
	}
	p, err := mysql.ParseGTIDSet(candidate.GTIDPurged)
	if err != nil {
		return false, fmt.Errorf("invalid purged GTID evidence")
	}
	o, err := mysql.ParseGTIDSet(other.GTIDExecuted)
	if err != nil {
		return false, fmt.Errorf("invalid peer GTID evidence")
	}
	assessment, err := mysql.AssessGTIDRecovery(c, p, o)
	if err != nil {
		return false, fmt.Errorf("inconsistent GTID evidence")
	}
	return assessment.ErrantTransactions == 0, nil
}

// CommonTimeline returns the deepest shared branch and its shared upper bound.
// Full WAL byte comparison is still required, including on the same timeline:
// two cloned primaries can write different WAL under one timeline identifier.
func CommonTimeline(a, b model.RecoveryEvidence) (uint32, uint64, error) {
	ap, err := timelinePath(a)
	if err != nil {
		return 0, 0, err
	}
	bp, err := timelinePath(b)
	if err != nil {
		return 0, 0, err
	}
	if a.Timeline == b.Timeline {
		if len(a.History) != len(b.History) {
			return 0, 0, fmt.Errorf("conflicting history for one PostgreSQL timeline")
		}
		for i := range a.History {
			x, _ := LSN(a.History[i].SwitchLSN)
			y, _ := LSN(b.History[i].SwitchLSN)
			if a.History[i].Timeline != b.History[i].Timeline || x != y {
				return 0, 0, fmt.Errorf("conflicting history for one PostgreSQL timeline")
			}
		}
	}
	var timeline uint32
	var end uint64
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i].Timeline != bp[i].Timeline {
			break
		}
		timeline = ap[i].Timeline
		aend, _ := LSN(ap[i].SwitchLSN)
		bend, _ := LSN(bp[i].SwitchLSN)
		end = min(aend, bend)
		if aend != bend || i+1 == len(ap) || i+1 == len(bp) {
			break
		}
	}
	if timeline == 0 || end == 0 {
		return 0, 0, fmt.Errorf("no verified common PostgreSQL timeline")
	}
	return timeline, end, nil
}

func timelinePath(e model.RecoveryEvidence) ([]model.RecoveryTimeline, error) {
	if e.Timeline == 0 || e.SystemIdentifier == "" {
		return nil, fmt.Errorf("PostgreSQL control identity is incomplete")
	}
	path := append([]model.RecoveryTimeline{}, e.History...)
	path = append(path, model.RecoveryTimeline{Timeline: e.Timeline, SwitchLSN: e.Position})
	var lastTimeline uint32
	var lastLSN uint64
	for _, entry := range path {
		position, err := LSN(entry.SwitchLSN)
		if err != nil || entry.Timeline <= lastTimeline || position == 0 || position < lastLSN {
			return nil, fmt.Errorf("PostgreSQL timeline history is invalid")
		}
		lastTimeline, lastLSN = entry.Timeline, position
	}
	if path[0].Timeline != 1 {
		return nil, fmt.Errorf("PostgreSQL timeline ancestry is incomplete")
	}
	return path, nil
}

func PostgreSQLContains(candidate, other model.RecoveryEvidence, proofs []model.RecoveryProof) (bool, error) {
	if candidate.SystemIdentifier != other.SystemIdentifier {
		return false, fmt.Errorf("PostgreSQL members belong to different database systems")
	}
	_, commonEnd, err := CommonTimeline(candidate, other)
	if err != nil {
		return false, err
	}
	cend, err := LSN(candidate.Position)
	if err != nil {
		return false, err
	}
	oend, err := LSN(other.Position)
	if err != nil {
		return false, err
	}
	if candidate.Timeline == other.Timeline && cend < oend {
		return false, nil
	}
	for _, p := range proofs {
		if p.CandidateID != candidate.InstanceID || p.OtherID != other.InstanceID {
			continue
		}
		if p.CandidateFingerprint != candidate.Fingerprint || p.OtherFingerprint != other.Fingerprint {
			return false, fmt.Errorf("WAL comparison belongs to stale evidence")
		}
		if !p.CommonWALVerified {
			return false, nil
		}
		if other.Timeline == candidate.Timeline || (other.Timeline < candidate.Timeline && oend <= commonEnd) {
			return true, nil
		}
		return p.DiscardedWALVerified && !p.DiscardedTransactions, nil
	}
	return false, nil
}
