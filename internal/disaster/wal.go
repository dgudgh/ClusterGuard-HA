package disaster

import (
	"context"
	"fmt"
	"strings"

	"clusterguard.io/ha/pkg/model"
)

type WALReader interface {
	VerifyWAL(context.Context, model.RecoveryEvidence, model.RecoveryWALRequest) (model.RecoveryWALResult, error)
}

func formatLSN(value uint64) string { return fmt.Sprintf("%X/%X", value>>32, value&0xffffffff) }

// CompareWAL collects byte-level and transaction-level proof from fenced
// Agents. A missing range is never replaced with LSN or role assumptions.
func CompareWAL(ctx context.Context, evidence []model.RecoveryEvidence, reader WALReader) ([]model.RecoveryProof, error) {
	if reader == nil {
		return nil, fmt.Errorf("WAL proof reader is unavailable")
	}
	var proofs []model.RecoveryProof
	for _, candidate := range evidence {
		for _, other := range evidence {
			if candidate.InstanceID == other.InstanceID {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			timeline, end, err := CommonTimeline(candidate, other)
			if err != nil {
				return nil, err
			}
			cstart, err := LSN(candidate.Redo)
			if err != nil {
				return nil, err
			}
			ostart, err := LSN(other.Redo)
			if err != nil {
				return nil, err
			}
			start := min(cstart, ostart)
			if start >= end {
				return nil, fmt.Errorf("checkpoint history has no verifiable common WAL range")
			}
			path, err := timelinePath(candidate)
			if err != nil {
				return nil, err
			}
			position := start
			for _, branch := range path {
				branchEnd, _ := LSN(branch.SwitchLSN)
				branchEnd = min(branchEnd, end)
				if branchEnd <= position {
					continue
				}
				left, err := reader.VerifyWAL(ctx, candidate, model.RecoveryWALRequest{Fingerprint: candidate.Fingerprint, Timeline: branch.Timeline, Start: formatLSN(position), End: formatLSN(branchEnd)})
				if err != nil {
					return nil, fmt.Errorf("verify candidate common WAL: %w", err)
				}
				right, err := reader.VerifyWAL(ctx, other, model.RecoveryWALRequest{Fingerprint: other.Fingerprint, Timeline: branch.Timeline, Start: formatLSN(position), End: formatLSN(branchEnd)})
				if err != nil {
					return nil, fmt.Errorf("verify peer common WAL: %w", err)
				}
				if !strings.HasPrefix(left.Digest, "sha256:") || len(left.Digest) != 71 || left.Digest != right.Digest {
					return nil, fmt.Errorf("members have conflicting or incomplete shared WAL bytes")
				}
				position = branchEnd
				if branch.Timeline == timeline {
					break
				}
			}
			if position != end {
				return nil, fmt.Errorf("incomplete shared PostgreSQL ancestry proof")
			}
			proof := model.RecoveryProof{CandidateID: candidate.InstanceID, OtherID: other.InstanceID, CandidateFingerprint: candidate.Fingerprint, OtherFingerprint: other.Fingerprint, CommonWALVerified: true}
			otherEnd, _ := LSN(other.Position)
			if otherEnd > end && other.Timeline != candidate.Timeline {
				path, err := timelinePath(other)
				if err != nil {
					return nil, err
				}
				position := end
				for _, branch := range path {
					branchEnd, _ := LSN(branch.SwitchLSN)
					if branchEnd <= position {
						continue
					}
					result, err := reader.VerifyWAL(ctx, other, model.RecoveryWALRequest{Fingerprint: other.Fingerprint, Timeline: branch.Timeline, Start: formatLSN(position), End: formatLSN(branchEnd)})
					if err != nil {
						return nil, fmt.Errorf("cannot prove absence of independent branch commits: %w", err)
					}
					proof.DiscardedTransactions = proof.DiscardedTransactions || result.Transactions
					position = branchEnd
				}
				proof.DiscardedWALVerified = position == otherEnd
			}
			proofs = append(proofs, proof)
		}
	}
	return proofs, nil
}
