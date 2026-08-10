package main

// version is stamped at build time with -ldflags "-X main.version=X.Y.Z".
//
// It tracks the `mac/vX.Y.Z` tag stream, NOT `server/v*` or `cli/v*`: the .app
// is released independently of the two binaries it bundles, and the self-updater
// (Phase 4) compares this value against the `mac/v*` releases on GitHub.
//
// The literal "dev" is meaningful, not just a placeholder — an unstamped build
// must never offer itself an "update" to a published release that is probably
// older than the working tree it was built from.
var version = "dev"

// isDevBuild reports whether this binary was built without a release stamp.
func isDevBuild() bool { return version == "dev" || version == "" }

// displayVersion renders the version for human-facing text.
func displayVersion() string {
	if isDevBuild() {
		return "development build"
	}
	return "v" + version
}
