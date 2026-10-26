// Package version exposes build metadata injected with -ldflags.
package version

// Set at build time: -ldflags "-X github.com/classy-cash/app/internal/version.Version=1.2.3 ...".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// ConfigVersion is the version of the environment-variable contract (docs/INTEGRATION.md).
// It is bumped whenever a variable is removed or changes meaning.
const ConfigVersion = 1
