package mysql

import "testing"

func TestFrozenRecoveryHistoryIsExactNotJustASuperset(t *testing.T) {
	const id = "14158b19-a162-11f1-9ab2-02420a210103"
	if err := verifyFrozenGTID(id+":1-5", id+":1:2-5"); err != nil {
		t.Fatal(err)
	}
	for _, actual := range []string{id + ":1-6", id + ":1-4", "", "invalid"} {
		if err := verifyFrozenGTID(id+":1-5", actual); err == nil {
			t.Fatalf("accepted changed history %q", actual)
		}
	}
}
