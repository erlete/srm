//go:build windows

package service

import "github.com/erlete/srm/internal/config"

// jobLogsRoot is the durable ephemeral job-log store on Windows, under the machine-wide
// srm data dir (analog of /var/lib/srm/joblogs). See config.JobLogsDir.
var jobLogsRoot = config.JobLogsDir()
