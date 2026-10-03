package main

var Version = "v2.20.0-control"

// SourceSHA is injected into signed platform binaries with -ldflags. A
// version label can name several commits, while a protected legacy-baseline
// finalization must bind the launchd process to one exact source commit.
var SourceSHA = ""
