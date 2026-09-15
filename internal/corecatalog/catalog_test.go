package corecatalog

import "testing"

func TestCompareVer(t *testing.T) {
	if compareVer("5.0.1", "4.1.1") <= 0 {
		t.Fatal("5.0.1 should be newer than 4.1.1")
	}
	if compareVer("1.14.0", "1.13.21") <= 0 {
		t.Fatal("1.14.0 should be newer")
	}
	if compareVer("1.13.21", "1.13.21") != 0 {
		t.Fatal("equal")
	}
}

func TestEnsureDefault(t *testing.T) {
	got := ensureDefault([]Version{{Version: "1.14.0"}}, "1.12.14")
	if len(got) != 2 || got[1].Version != "1.12.14" {
		t.Fatalf("%+v", got)
	}
	got = ensureDefault([]Version{{Version: "1.12.14"}}, "1.12.14")
	if len(got) != 1 {
		t.Fatalf("dup: %+v", got)
	}
}
