package mysql

import (
	"math"
	"testing"
)

func TestParseGTIDSetNormalizesOverlappingIntervals(t *testing.T) {
	set, err := ParseGTIDSet("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE:5-8:1-3:3-6")
	if err != nil {
		t.Fatalf("parse GTID set: %v", err)
	}
	want, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-8")
	if err != nil {
		t.Fatalf("parse expected GTID set: %v", err)
	}

	comparison, err := CompareGTIDSets(want, set)
	if err != nil {
		t.Fatalf("compare GTID sets: %v", err)
	}
	if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("overlapping intervals were not normalized: %+v", comparison)
	}
}

func TestCompareGTIDSetsRecognizesSubset(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-20")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-10")
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare GTID sets: %v", err)
	}
	if comparison.MissingTransactions != 10 || comparison.ErrantTransactions != 0 {
		t.Fatalf("unexpected subset comparison: %+v", comparison)
	}
}

func TestCompareGTIDSetsFindsMissingAndErrantIntervals(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-20")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-18,ffffffff-1111-2222-3333-444444444444:1")
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare GTID sets: %v", err)
	}
	if comparison.MissingTransactions != 2 || comparison.ErrantTransactions != 1 {
		t.Fatalf("unexpected comparison: %+v", comparison)
	}
}

func TestParseGTIDSetSupportsTaggedSourcesAndMultipleTags(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:Domain_1:1-3:11:Domain_2:8-10")
	if err != nil {
		t.Fatalf("parse tagged primary set: %v", err)
	}
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:Domain_1:1-2:Domain_2:8-9")
	if err != nil {
		t.Fatalf("parse tagged candidate set: %v", err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare tagged sets: %v", err)
	}
	if comparison.MissingTransactions != 3 || comparison.ErrantTransactions != 0 {
		t.Fatalf("unexpected tagged comparison: %+v", comparison)
	}
}

func TestCompareGTIDSetsTreatsDifferentTagsAsDistinctTransactionSources(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:Domain_1:1-3")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:Domain_2:1-3")
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare tagged sets: %v", err)
	}
	if comparison.MissingTransactions != 3 || comparison.ErrantTransactions != 3 {
		t.Fatalf("different tags were merged: %+v", comparison)
	}
}

func TestCompareGTIDSetsNormalizesTagCase(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:Domain_1:1-3")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:domain_1:1-3")
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare tagged sets: %v", err)
	}
	if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("tag case created distinct sources: %+v", comparison)
	}
}

func TestParseGTIDSetAcceptsEmptySet(t *testing.T) {
	set, err := ParseGTIDSet("")
	if err != nil {
		t.Fatalf("empty GTID set: %v", err)
	}
	comparison, err := CompareGTIDSets(set, set)
	if err != nil {
		t.Fatalf("compare empty GTID sets: %v", err)
	}
	if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("unexpected empty comparison: %+v", comparison)
	}
}

func TestCompareGTIDSetsAllowsMaxUint64SingleInterval(t *testing.T) {
	primary, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-18446744073709551615")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ParseGTIDSet("")
	if err != nil {
		t.Fatal(err)
	}

	comparison, err := CompareGTIDSets(primary, candidate)
	if err != nil {
		t.Fatalf("compare maximum GTID set: %v", err)
	}
	if comparison.MissingTransactions != math.MaxUint64 || comparison.ErrantTransactions != 0 {
		t.Fatalf("unexpected maximum comparison: %+v", comparison)
	}
}

func TestCompareGTIDSetsRejectsMultiUUIDOverflow(t *testing.T) {
	overflowing, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-18446744073709551615,ffffffff-1111-2222-3333-444444444444:1")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := ParseGTIDSet("")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		primary   GTIDSet
		candidate GTIDSet
	}{
		{name: "missing", primary: overflowing, candidate: empty},
		{name: "errant", primary: empty, candidate: overflowing},
		{name: "identical overflowing sets", primary: overflowing, candidate: overflowing},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompareGTIDSets(test.primary, test.candidate); err == nil {
				t.Fatal("overflowing GTID comparison succeeded")
			}
		})
	}
}

func TestParseGTIDSetRejectsInvalidIntervals(t *testing.T) {
	for _, value := range []string{
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:0",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:20-10",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:not-a-number",
		":1-10",
		"not-a-uuid:1-10",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-2-3",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseGTIDSet(value); err == nil {
				t.Fatalf("ParseGTIDSet(%q) succeeded", value)
			}
		})
	}
}
