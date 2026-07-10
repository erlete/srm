package tui

// hostCapable reports whether host-mutating operations (create, destroy, refresh,
// upgrade, prune, provision, reconcile-fix, uninstall) can run here at all. They
// all need OS privilege - root on Linux, an elevated token on Windows - and they
// act on THIS machine, so a non-elevated or remote admin box must pre-disable them
// with a visible reason rather than let them fail deep in the service. The data
// tiers degrade independently: the fast tier (GitHub list) always works, and the
// host data tier (reconcile) reports its own availability per load.
//
// elevated() is the OS-specific privilege check (probe_other.go / probe_windows.go),
// mirroring the CLI's isElevated so the cockpit and `srm` gate on the same predicate.
func hostCapable() bool { return elevated() }
