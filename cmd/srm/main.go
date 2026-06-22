// Command srm is a TUI + CLI for managing GitHub Actions self-hosted runners across
// one or more organizations. One codebase (internal/*) cross-compiles to Linux x64
// (systemd + apt) and Windows x64 (Service Control Manager + winget) via Go build tags.
package main

import (
	"fmt"
	"os"

	"github.com/erlete/srm/internal/cli"
)

// version is stamped at build time via -ldflags "-X main.version=...".
// Defaults to "dev" for `go run` and un-stamped builds.
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintln(os.Stderr, "srm:", err)
		os.Exit(1)
	}
}
