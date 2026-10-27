package version

import (
	"testing"
	"time"
)

func TestLegacyTimestampLabel(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	loc, _ := time.LoadLocation("Europe/Warsaw")
	Version = "202610271430" // 14:30 UTC = 15:30 in Warsaw (CET, UTC+1 after the 25 Oct change)
	if got := Label(loc); got != "Wersja z 15:30, 27 paź" {
		t.Fatalf("label %q", got)
	}
	if ts, ok := Stamp(); !ok || ts.Year() != 2026 || ts.Minute() != 30 {
		t.Fatal("stamp")
	}
	Version = "dev"
	if _, ok := Stamp(); ok || Label(loc) != "wersja dev" {
		t.Fatal("dev builds must not pretend to be dated")
	}
	Version = "2026-10-27"
	if _, ok := Stamp(); ok {
		t.Fatal("only YmdHi is a release stamp")
	}
}
