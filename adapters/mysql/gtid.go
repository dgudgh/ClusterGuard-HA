package mysql

import (
	"time"

	"clusterguard.io/ha/pkg/gtid"
)

type GTIDSet = gtid.GTIDSet
type GTIDComparison = gtid.GTIDComparison
type GTIDRecoveryAssessment = gtid.GTIDRecoveryAssessment

func ParseGTIDSet(value string) (GTIDSet, error) { return gtid.ParseGTIDSet(value) }
func CompareGTIDSets(primary, candidate GTIDSet) (GTIDComparison, error) {
	return gtid.CompareGTIDSets(primary, candidate)
}
func AssessGTIDRecovery(executed, purged, former GTIDSet) (GTIDRecoveryAssessment, error) {
	return gtid.AssessGTIDRecovery(executed, purged, former)
}
func likelyTemporalGTIDSamplingSkew(primary, candidate GTIDSet, uuid string, a, b time.Time) (bool, error) {
	return gtid.LikelyTemporalSamplingSkew(primary, candidate, uuid, a, b)
}
func hasOnlyGTIDAdditionsFromSource(primary, candidate GTIDSet, uuid string) (bool, error) {
	return gtid.HasOnlyAdditionsFromSource(primary, candidate, uuid)
}
