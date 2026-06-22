# srm Windows port (cross-OS support)

> Status: **shipped, then unified (v2.0.0)**. Goal achieved: identical CLI + TUI +
> functionality on Windows x64 and Linux (Ubuntu) x64.
>
> **The mirror described below was a staging strategy, now SUPERSEDED.** Once the
> Windows port was feature-complete and live-validated, the `internal-win/*` mirror and
> `cmd/srm-win` were folded back into a single `internal/*` + `cmd/srm` tree that
> cross-compiles to both OSes via Go build tags (`*_linux.go` / `*_windows.go` /
> `*_other.go` behind shared interfaces). This eliminated ~53 duplicated files and the
> drift the mirror invited. The sections below are kept as the historical record of how
> the port was staged; for the current layout, read the build-tag splits in `internal/*`.

## Strategy: a mirror, not a refactor (historical - superseded by the v2.0.0 union)

The Linux build was feature-complete and validated in production. To add Windows
support without risking a single working Linux path, the Windows port FIRST lived in a
**parallel mirror tree** rather than behind build tags woven through the live code:

- `internal/*` + `cmd/srm` -> the **Linux** build. Never touched by the port.
- `internal-win/*` + `cmd/srm-win` -> the **Windows** build. All Windows work happens here.

`internal-win/` began as a verbatim copy of `internal/` with import paths rewritten
(`.../internal/...` -> `.../internal-win/...`). It is its own self-contained tree, so
the Windows port can diverge freely. The cost (duplication, no automatic propagation
of Linux fixes) is deliberate: the user chose isolation over DRY to keep the two OS
paths from entangling.

The mirror must stay **cross-compilable** (the Linux CI runs `go build ./...` /
`go test ./...` over the whole module, including `internal-win`). So genuinely
Windows-only syscalls go behind `_windows.go` + `_other.go` stub pairs (the same
pattern as `prune_linux.go` / `prune_other.go`); everything else is plain Go that
compiles everywhere and only *runs* on Windows.

## "linux x64" vs "ubuntu x64"

The actions/runner AGENT srm downloads is `linux-x64` (distro-agnostic). srm's HOST
management is `ubuntu-x64` (systemd + apt + cgroup v2 + POSIX users). Cross-OS support
is almost entirely the second layer; the `runner.Orchestrator` interface is the seam.

## Phasing

- **P0 - runnable Windows binary (DONE).** Mirror tree + `cmd/srm-win`; build green on
  Windows and Linux; Windows data-path layer (`internal-win/config/paths.go`, rooted at
  `%ProgramData%\srm`); `filepath.Join` for per-org cache dirs; Administrator detection
  (`isElevated`, x/sys/windows). **Validated on a real Windows host:** GitHub App auth
  (RSA-JWT), `runners list` against a live org, and TUI rendering all work natively.
  The portable layer (CLI, TUI, config, secrets, auth, GitHub) is cross-platform as-is.
- **P1 - Windows persistent runners.** Generalize the download layer (os/arch + `.zip`
  extractor; fix the `VersionFromURL` lockstep so `AgentVersion` isn't silently nulled);
  implement a `windows` Orchestrator behind the interface: install/start/stop/remove via
  the runner's `config.cmd --runasservice` + the Windows Service Control Manager; NTFS
  ACLs instead of chown; reconcile/inspection via SCM + Job Objects.
- **P2 - Windows ephemeral lanes.** systemd `Restart=always` (respawn-on-clean-exit) has
  no SCM analog, so a single long-lived service runs an in-process `for { RunCycle() }`
  loop with an srm-tracked restart counter.
- **P3 - resource caps + provisioner.** Per-runner Job Objects (no host-wide slice
  analog -> drop auto-capacity on Windows, or gate via concurrency); a Windows
  provisioner (winget/choco instead of apt).

## Path mapping (Linux -> Windows)

| Linux | Windows |
|---|---|
| `/etc/srm/config.yaml` | `%ProgramData%\srm\config.yaml` |
| `/var/lib/srm/state.json` | `%ProgramData%\srm\state.json` |
| `/var/lib/srm/agent-cache` | `%ProgramData%\srm\agent-cache` |
| `/var/lib/srm/ephemeral` | `%ProgramData%\srm\ephemeral` |
| `/opt/actions-runners` | `C:\actions-runners` |
| `/opt/hostedtoolcache` | `C:\hostedtoolcache` |
| `/opt/srm-cache` | `%ProgramData%\srm\cache` |
| `/usr/local/bin/srm` | `%ProgramFiles%\srm\srm.exe` |

See `docs/design/v1.1-lifecycle.md` for the broader roadmap and the portability
assessment that grounded this plan.
