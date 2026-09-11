package gtid

import (
	"strings"
	"testing"
)

const (
	sourceUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	otherUUID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func mustParse(t *testing.T, value string) GTIDSet {
	t.Helper()
	set, err := ParseGTIDSet(value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return set
}

func TestParseGTIDSetMergesIntervalsFromOneSource(t *testing.T) {
	// Disjoint, adjacent and overlapping intervals for one UUID must collapse
	// into a single contiguous range before any comparison happens.
	parsed := mustParse(t, sourceUUID+":1-5:7-10,"+sourceUUID+":6-6,"+sourceUUID+":9-12")
	expected := mustParse(t, sourceUUID+":1-12")
	comparison, err := CompareGTIDSets(parsed, expected)
	if err != nil {
		t.Fatalf("compare normalized sets: %v", err)
	}
	if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("interval merge changed transaction identity: %+v", comparison)
	}
}

func TestParseGTIDSetAcceptsTaggedSource(t *testing.T) {
	tagged := mustParse(t, sourceUUID+":dc1:1-5")
	plain := mustParse(t, sourceUUID+":1-5")
	// A tag distinguishes the source, so the untagged history is fully errant.
	comparison, err := CompareGTIDSets(tagged, plain)
	if err != nil {
		t.Fatalf("compare tagged and untagged sources: %v", err)
	}
	if comparison.MissingTransactions != 5 || comparison.ErrantTransactions != 5 {
		t.Fatalf("tagged source was merged with the untagged source: %+v", comparison)
	}
}

func TestParseGTIDSetRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"not a uuid":          "1234:1-5",
		"missing interval":    sourceUUID,
		"zero start":          sourceUUID + ":0-5",
		"zero end":            sourceUUID + ":1-0",
		"end before start":    sourceUUID + ":5-1",
		"empty end":           sourceUUID + ":1-",
		"tag without range":   sourceUUID + ":dc1",
		"garbage interval":    sourceUUID + ":one-two",
		"interval overflow":   sourceUUID + ":18446744073709551616",
		"trailing separator":  sourceUUID + ":1-5,",
		"non hex uuid group":  "zzzzzzzz-aaaa-4aaa-8aaa-aaaaaaaaaaaa:1-5",
		"tag after interval":  sourceUUID + ":1-5:dc1",
		"empty tagged source": sourceUUID + "::1-5",
	}
	for name, value := range cases {
		if _, err := ParseGTIDSet(value); err == nil {
			t.Fatalf("%s: malformed GTID set %q was accepted", name, value)
		}
	}
}

func TestParseGTIDSetEmptyValueIsEmptySet(t *testing.T) {
	for _, value := range []string{"", "   ", "\t"} {
		if _, err := ParseGTIDSet(value); err != nil {
			t.Fatalf("blank GTID set %q was rejected: %v", value, err)
		}
	}
	comparison, err := CompareGTIDSets(mustParse(t, ""), mustParse(t, ""))
	if err != nil || comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("empty GTID sets did not compare as equal: %+v err=%v", comparison, err)
	}
}

func TestCompareGTIDSetsCountsMissingAndErrant(t *testing.T) {
	primary := mustParse(t, sourceUUID+":1-10")
	cases := []struct {
		name      string
		candidate string
		missing   uint64
		errant    uint64
	}{
		{name: "behind", candidate: sourceUUID + ":1-7", missing: 3, errant: 0},
		{name: "ahead", candidate: sourceUUID + ":1-12", missing: 0, errant: 2},
		{name: "diverged", candidate: sourceUUID + ":1-5:8-12", missing: 2, errant: 2},
		{name: "unknown source", candidate: otherUUID + ":1-3", missing: 10, errant: 3},
		{name: "equal", candidate: sourceUUID + ":1-10", missing: 0, errant: 0},
	}
	for _, test := range cases {
		comparison, err := CompareGTIDSets(primary, mustParse(t, test.candidate))
		if err != nil {
			t.Fatalf("%s: compare: %v", test.name, err)
		}
		if comparison.MissingTransactions != test.missing || comparison.ErrantTransactions != test.errant {
			t.Fatalf("%s: got %+v, want missing=%d errant=%d", test.name, comparison, test.missing, test.errant)
		}
	}
}

func TestAssessGTIDRecoveryFastRejoin(t *testing.T) {
	currentExecuted := mustParse(t, sourceUUID+":1-10")
	currentPurged := mustParse(t, sourceUUID+":1-5")

	safe, err := AssessGTIDRecovery(currentExecuted, currentPurged, mustParse(t, sourceUUID+":1-7"))
	if err != nil {
		t.Fatalf("assess safe rejoin: %v", err)
	}
	if !safe.FastRejoinSafe || safe.MissingTransactions != 3 || safe.PurgedMissingTransactions != 0 {
		t.Fatalf("safe rejoin was misclassified: %+v", safe)
	}

	// The former primary executed transactions the current primary never did.
	unsafe, err := AssessGTIDRecovery(currentExecuted, currentPurged, mustParse(t, sourceUUID+":1-12"))
	if err != nil {
		t.Fatalf("assess divergent rejoin: %v", err)
	}
	if unsafe.FastRejoinSafe || unsafe.ErrantTransactions != 2 {
		t.Fatalf("divergent rejoin was misclassified: %+v", unsafe)
	}
}

func TestAssessGTIDRecoveryRejectsPurgedHistoryOutsideExecuted(t *testing.T) {
	currentExecuted := mustParse(t, sourceUUID+":1-10")
	currentPurged := mustParse(t, otherUUID+":1-3")
	if _, err := AssessGTIDRecovery(currentExecuted, currentPurged, mustParse(t, sourceUUID+":1-10")); err == nil {
		t.Fatal("purged history outside the executed set was accepted")
	}
}

func TestHasOnlyAdditionsFromSource(t *testing.T) {
	baseline := mustParse(t, sourceUUID+":1-5")

	extended, err := HasOnlyAdditionsFromSource(baseline, mustParse(t, sourceUUID+":1-8"), sourceUUID)
	if err != nil || !extended {
		t.Fatalf("contiguous extension from the source was rejected: ok=%v err=%v", extended, err)
	}

	foreign, err := HasOnlyAdditionsFromSource(baseline, mustParse(t, otherUUID+":1-3"), sourceUUID)
	if err != nil {
		t.Fatalf("additions from a foreign source: %v", err)
	}
	if foreign {
		t.Fatal("additions from a foreign source were accepted as source-only")
	}

	foreignAlongside, err := HasOnlyAdditionsFromSource(baseline, mustParse(t, sourceUUID+":1-5,"+otherUUID+":1-3"), sourceUUID)
	if err != nil || foreignAlongside {
		t.Fatalf("foreign additions alongside source history were accepted: ok=%v err=%v", foreignAlongside, err)
	}

	if ok, err := HasOnlyAdditionsFromSource(baseline, baseline, "not-a-uuid"); err != nil || ok {
		t.Fatalf("invalid source UUID was accepted: ok=%v err=%v", ok, err)
	}
}

func TestCompareGTIDSetsRejectsTransactionCountOverflow(t *testing.T) {
	// Two sources of 2^63 transactions each overflow uint64 once summed.
	huge := sourceUUID + ":1-9223372036854775808," + otherUUID + ":1-9223372036854775808"
	_, err := CompareGTIDSets(mustParse(t, huge), mustParse(t, ""))
	if err == nil || !strings.Contains(err.Error(), "exceeds uint64") {
		t.Fatalf("uint64 transaction overflow was not detected: %v", err)
	}
}
