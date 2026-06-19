// Package log configures the tool's logger. The logger is routed to a file (or
// stderr), never to the TUI's stdout, so log output cannot corrupt rendered
// frames. Token-shaped strings are redacted by callers via secrets.Redact.
package log

import (
	"io"
	"os"

	charmlog "charm.land/log/v2"
)

// New returns a logger writing to the given path, plus a closer. If path is
// empty it logs to stderr and the closer is a no-op.
func New(path string) (*charmlog.Logger, func() error, error) {
	var w io.Writer = os.Stderr
	closer := func() error { return nil }

	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		w = f
		closer = f.Close
	}

	l := charmlog.NewWithOptions(w, charmlog.Options{
		ReportTimestamp: true,
		Prefix:          "srm",
	})
	return l, closer, nil
}
