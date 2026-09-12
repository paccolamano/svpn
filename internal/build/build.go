// Package build carries what the linker stamps into a binary.
package build

// Version is the build's version, set at link time by scripts/git-version.sh
// through the Makefile, or by goreleaser from the tag. The "unknown" default
// only survives a plain `go build`.
var Version = "unknown"
