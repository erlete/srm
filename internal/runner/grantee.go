package runner

// IcaclsGrantee exposes icaclsGrantee (the well-known-SID mapping used to grant a
// built-in service account access via icacls) to sibling packages. The host
// dependency layer (internal/provision) grants its cache paths to the runner
// service account using the SAME mapping the orchestrator uses on runner trees, so
// the localized-name lookup that fails with error 1332 is avoided in one place. The
// canonical implementation and its rationale live with the orchestrator; this is a
// thin, additive export so provision need not duplicate the SID table.
func IcaclsGrantee(user string) string { return icaclsGrantee(user) }
