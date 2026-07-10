package mysql

import "testing"

func TestParseGTIDSetNormalizesOverlappingIntervals(t *testing.T) {
	set, err := ParseGTIDSet("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE:5-8:1-3:3-6")
	if err != nil {
		t.Fatalf("parse GTID set: %v", err)
	}
	want, err := ParseGTIDSet("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-8")
	if err != nil {
		t.Fatalf("parse expected GTID set: %v", err)
	}

	comparison := CompareGTIDSets(want, set)
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

	comparison := CompareGTIDSets(primary, candidate)
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

	comparison := CompareGTIDSets(primary, candidate)
	if comparison.MissingTransactions != 2 || comparison.ErrantTransactions != 1 {
		t.Fatalf("unexpected comparison: %+v", comparison)
	}
}

func TestParseGTIDSetAcceptsEmptySet(t *testing.T) {
	set, err := ParseGTIDSet("")
	if err != nil {
		t.Fatalf("empty GTID set: %v", err)
	}
	comparison := CompareGTIDSets(set, set)
	if comparison.MissingTransactions != 0 || comparison.ErrantTransactions != 0 {
		t.Fatalf("unexpected empty comparison: %+v", comparison)
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
