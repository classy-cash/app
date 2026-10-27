// Package version exposes build metadata injected with -ldflags.
package version

import "time"

// Set at build time: -ldflags "-X github.com/classy-cash/app/internal/version.Version=202610271430 ...".
//
// Releases keep the legacy PHP scheme: Version is the UTC build time as YYYYMMDDHHMM ("YmdHi"),
// e.g. 202610271430. The UI shows it as "Wersja z 15:30, 27 paź" in the installation's time zone.
// Local builds report "dev".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// ConfigVersion is the version of the environment-variable contract (docs/INTEGRATION.md).
// It is bumped whenever a variable is removed or changes meaning.
const ConfigVersion = 1

const layout = "200601021504"

// Stamp returns the current release time parsed from Version (UTC), or false for "dev"/unknown formats.
func Stamp() (time.Time, bool) {
	t, err := time.ParseInLocation(layout, Version, time.UTC)
	return t, err == nil
}

var months = []string{"sty", "lut", "mar", "kwi", "maj", "cze", "lip", "sie", "wrz", "paź", "lis", "gru"}

// Label renders the legacy "Wersja z HH:mm, dd MMM" text in loc, or "wersja dev".
func Label(loc *time.Location) string {
	t, ok := Stamp()
	if !ok {
		return "wersja " + Version
	}
	t = t.In(loc)
	return "Wersja z " + t.Format("15:04") + ", " + t.Format("02") + " " + months[t.Month()-1]
}
