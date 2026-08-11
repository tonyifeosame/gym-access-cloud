package handlers

// Build metadata, set once at startup from the link-time variables in main.
//
// Kept in the handlers package rather than read from main directly because
// main imports handlers, not the other way round. main owns the linker
// symbols (they must be package-level vars in `main` for -X to bind to them)
// and hands the values over here.

var buildInfo = BuildInfo{Version: "dev", Commit: "unknown"}

// BuildInfo identifies the binary that is running.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// SetBuildInfo records the link-time build metadata. Empty values are ignored
// so an unstamped `go build` keeps the readable defaults rather than reporting
// a blank version, which reads as a broken deployment rather than a local one.
func SetBuildInfo(version, commit string) {
	if version != "" {
		buildInfo.Version = version
	}
	if commit != "" {
		buildInfo.Commit = commit
	}
}

// Build returns the current build metadata.
func Build() BuildInfo { return buildInfo }
