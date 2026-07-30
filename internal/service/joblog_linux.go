//go:build !windows

package service

// jobLogsRoot is the durable ephemeral job-log store on Linux (FHS: /var/lib/srm).
// Root-owned 0700 (joblog.Store creates it) so an unprivileged job can't read another
// org's logs or tamper with its own.
const jobLogsRoot = "/var/lib/srm/joblogs"
