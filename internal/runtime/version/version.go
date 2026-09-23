// Package version carries build identity for the indexcore binary. Values are
// overridable at build time via -ldflags.
package version

// Default placeholders; set with:
//
//	go build -ldflags "-X .../version.Version=... -X .../version.Commit=... -X .../version.Date=..."
var (
	Version = "0.3.0-alpha"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a human-readable build string.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
