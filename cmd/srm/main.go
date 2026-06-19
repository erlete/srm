// Command srm is a TUI + CLI for managing GitHub Actions self-hosted runners
// across one or more organizations on Ubuntu x64 hosts.
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
