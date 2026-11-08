# Changelog

All notable changes to `srm` are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.5.1] - 2026-09-24

### Fixed
- **Headless key import left an undecryptable store** when the secrets passphrase
  came only from `SRM_SECRETS_PASSPHRASE`. The passphrase is now persisted
  root-only whenever it is absent from disk (env-supplied included) and the key is
  encrypted with that on-disk passphrase, so the detached ephemeral units, which
  do not inherit the operator's environment, can decrypt it.

## [2.5.0] - 2026-09-24

### Added
- **`srm config import-key`** - a non-interactive way to move an org's GitHub App
  key into the encrypted store: it reads the `.pem` (`--key-file`, default the
  org's configured `privateKeyPath`), validates it, encrypts it, and clears
  `privateKeyPath` from the config. `--passphrase` (or `SRM_SECRETS_PASSPHRASE`)
  supplies the secrets passphrase; `--remove-source` deletes the plaintext `.pem`
  after a verified import. On Windows the runner service account is granted read
  access to the encrypted store.

### Fixed
- **Ephemeral lanes could hot-loop on a setup failure.** When a cycle failed
  before the job started (org lookup, GitHub client, or JIT mint), it returned
  without any pacing; on Windows, whose in-process supervisor has no restart
  floor, that spun thousands of cycles. Those early failures now back off like a
  failed job.

### Security
- Go toolchain directive bumped to 1.26.6, clearing the stdlib advisories
  GO-2026-6218, GO-2026-6090, GO-2026-5972 and GO-2026-5026.

## [2.4.0] - 2026-07-30

### Added
- **Per-org profiles** - named create-presets that bundle labels, a runner group
  and the nature (persistent or ephemeral). `srm runners create --profile <name>`
  and the create wizard's Profile select seed a create from one; `srm config
  profiles list/set/remove` and a Settings > Lifecycle "Manage profiles" editor
  manage them. The container fields are carried as metadata only (not enforced
  by srm).
- **Runs "active now"** - `srm runs active` and a live block above the Runs tab
  history relate the fleet's busy runners to the repository, workflow and job
  they are running right now, joined live from GitHub.

### Changed
- One canonical vocabulary for runner classes and ephemeral slot states
  (active / down / crash-loop / unit-drift), shared by the Ephemeral and Drift
  tabs, the CLI and `srm reconcile`.

### Fixed
- **False `crash-loop` / `ephemeral-stuck` on busy ephemeral lanes.** A lane's
  restart counter climbs once per job (`Restart=always`), so a healthy, busy lane
  was flagged as crash-looping. An active, conformant lane is now healthy
  regardless of restart churn, and a crash loop is diagnosed from systemd's
  result for the last cycle.
- Consistency fixes for reap labels, booleans, the version sentinel and footer
  verbs across the TUI.

## [2.3.0] - 2026-07-30

Configuration moves into the TUI (no more hand-editing host files) and ephemeral
job logs survive the per-cycle wipe.

### Added
- **Paste the App private key** during onboarding (`srm init`, first run and the
  TUI Onboard wizard): the key is validated and stored encrypted, with no `.pem`
  left on disk. An interactive secrets passphrase is asked once and persisted
  root-only next to the config, so ephemeral units decrypt it without changes.
- **`srm provision --rootless-dind`** installs the full rootless-Docker
  prerequisite set (docker-ce + rootless extras, uidmap, fuse-overlayfs,
  slirp4netns, user-namespace sysctls) idempotently; the TUI `P` provision folds
  it in when `docker.rootlessDinD` is on.
- **Host policy editor** - `docker.rootlessDinD`, `docker.buildkitImage`,
  `isolation.perOrgUsers` and `hardening.protectProc` are editable from the
  Settings form and from `srm config show` / `srm config set key=value`.
- **Re-edit an org** - the previously frozen org fields can be changed from
  Settings > Lifecycle "Edit an org" or `srm config edit-org` (the slug stays
  locked; the key defaults to keep).
- **Host dependency manifest editor** - Settings > Lifecycle "Edit host manifest"
  and `srm config manifest show/add/remove` manage apt packages, tool-cache seeds,
  cache paths and setup scripts. A setup script can be stored inline
  (`--script-file`) and is materialized at provision time.
- **Runner logs** - `srm runners logs <name>` (or `--ephemeral --slot N`) with
  `--follow`, `--lines` and `--source journal|agent`, and an `L` log panel in the
  TUI (Persistent, Ephemeral and Drift tabs; `f` toggles follow).
- **Durable ephemeral job logs** - each finished job's log is captured root-owned
  under `/var/lib/srm/joblogs` before the next cycle wipes the lane. `srm runs
  list` / `srm runs logs <runner-id>` read them, a new **Runs** tab browses them
  (`/` filter, `i` detail, `L` view), and `srm cache prune` applies
  `jobLogRetentionDays`.
- **App permission surfacing** - `srm doctor` reports which repositories the App
  installation can see, and the Health card warns when private repositories are
  invisible to it.
- An Ephemeral tab `LABELS` column (also matched by the filter).

### Changed
- `srm runners create --ephemeral --count N` is **additive**: new slots take the
  next free ids (reusing gaps) instead of re-creating slots 1 to N.
- An org's default labels and default runner group are now applied at create
  time and shown as form placeholders.
- `srm doctor` exits non-zero with a BLOCKER line and the fix when a hard
  rootless-DinD prerequisite is missing; the Health tab shows the same blocker.
- Tabs and table columns span the full terminal width, with slack distributed
  proportionally across columns.

### Fixed
- The ephemeral per-job reset unmounts stale rootless-DinD mounts left by a
  killed cycle before wiping the work tree, so the wipe no longer fails busy.
- The agent version check no longer reports "current" for an org with zero
  runners.

### Security
- Go toolchain directive bumped to 1.26.5, fixing the stdlib TLS advisory
  GO-2026-5856.

## [2.2.0] - 2026-07-12

Follow-up TUI release that builds out the deferred items from the v2.1.0 release
audit: richer control-plane forms, deeper Health/Drift observability, in-TUI
lifecycle wizards, and per-row mouse selection.

### Added
- **Upgrade options form** - pressing `u` now opens a small form to pin a target
  agent version (blank = the configured pin, else GitHub-current) and toggle
  `force` (reinstall / allow a downgrade) before the dry-run preview; both thread
  into the preview and the applied upgrade. Scope and the host-wide typed-confirm
  are unchanged.
- **Lifecycle Onboard + Restore wizards** - the Settings > Lifecycle panel gains
  a working **Onboard** card (the same org-credential form as `srm init`, then a
  save + in-place reload + GitHub auth check) and a **Restore** card (pick a
  config-dir backup, confirm, restore, and reload). The manager reloads in place,
  so new/restored config shows without restarting the TUI.
- **Uninstall options form + safer purge** - the Uninstall card opens a Step-1
  form exposing the scope (full host / one org) and the Keep\*/Force/Purge toggles
  the CLI has. The fail-safe default still leaves `/etc/srm` in place; selecting a
  purge that would remove it escalates to a second `PURGE` typed-confirm and an
  up-front writable-backup pre-check.
- **Memory sparkline** - the runner / slot Information panel shows a compact
  block-rune `trend` sparkline of recent live cgroup memory, scaled to the cgroup
  cap and captioned with the sample count (one sample per refresh, capped at 30).
- **Per-row mouse click** - a left click on a fleet table row selects it; a click
  in the multi-select gutter also toggles its checkbox (persistent, ephemeral,
  groups, and drift tables).

### Changed
- **Drift tab** - a `FIX-PLAN` column states the remediation for each row (refresh
  drop-in, restart, recreate lane, update the srm binary, ...), and a provenance
  line records when the drift was last audited (a read-only pass).
- **Health host panel** - adds a dependency-manifest drift summary (satisfied /
  N missing / unknown) and, when `docker.rootlessDinD` is on, a rootless-Docker
  readiness block (a pass/fail line per prerequisite). The rootless-DinD probe now
  lives in the service layer and is shared by `srm doctor` and the TUI. The panel
  is now **scrollable** (mouse wheel or `PgUp`/`PgDn`) with a `▲▼ N%` hint, so the
  full read-out is reachable when it is taller than the screen.

## [2.1.0] - 2026-07-10

Major TUI release: the terminal UI becomes a full fleet **cockpit and control
plane**, and the whole run is grounded in a multi-agent release audit.

### Added
- **Six-tab cockpit** (Health / Persistent / Ephemeral / Groups / Drift /
  Settings) fed by a two-tier fused snapshot (fast GitHub + manifest tier,
  slower host-reconcile tier) that degrades gracefully off-host.
- **Information panel** (`i`) - a full-screen, scrollable read-out for the
  selected runner, ephemeral slot, group, **org (Health)**, or **drift row**,
  with a copy-ready `runs-on` snippet and, for a busy runner, an async
  **current-job** lookup (scans the org's accessible repos, most-recent-push
  first).
- **Drift tab** - every non-healthy runner/slot the reconcile tier surfaces,
  with an incremental `/` filter, `enter` to jump to the row's home tab, and
  `f` fix / `g` reap ops (dry-run preview first).
- **Control plane** - `u` upgrade, `b` rollback, `R` refresh-units, `p` prune
  dep-cache, `P` provision host, `c` recreate, each behind the appropriate
  safety rung (dry-run preview and/or confirm / typed-confirm). Multi-select
  (`space` / `A`) scopes destroy / recreate / **upgrade / rollback / refresh**
  to a chosen subset; a host-wide upgrade-all is typed-hostname-confirmed.
- **Runner groups** - create / edit / delete, an autocomplete repo-access
  picker for "selected" visibility, and `e` to move a persistent runner into an
  existing group (a Select of the org's groups).
- **Org filter** (`o`) - a centered multi-select that hides unselected orgs
  across every view.
- **Mouse support** - the wheel scrolls the active table / Information panel /
  picker; a left click on the tab bar switches tabs.
- **Health host panel + Lifecycle panel** - host capacity mode, aggregate
  slice memory, disks, dep-caches, and toolchain doctor live on Health;
  Settings hosts the Lifecycle operations (backup / uninstall) panel.
- Docs: `GITHUB_APP_SETUP.md` documents the optional Repository permissions
  (Actions: Read + Metadata: Read) that light up the repo picker, group
  assigned-repos, and current-job lookup.

### Changed
- The reconcile dry-run decision is now threaded explicitly through `Reconcile`
  instead of mutating the shared config flag; `withDryRun` (prune / uninstall
  previews) serializes on a dedicated mutex.

### Fixed
- **Dry-run could get stuck on**: overlapping Drift fix/reap plan goroutines
  raced on the shared `cfg.DryRun` flag; a bad interleaving left it on, after
  which real destroy/upgrade/create ops silently no-op'd while reporting
  success. The flag is no longer shared-mutated.
- Recreate/destroy op progress reached only `(n-1)/n`; it now reaches 100%.
- The repo picker filter swallowed the `space` key instead of toggling a repo;
  the runner table `space`-select jumped instead of selecting.

## [2.0.1] - 2026-06-23

### Fixed
- **Docker container actions broke on rootless-DinD lanes** (`docker: pull access denied
  for <hash>, repository does not exist`). The per-job rootless buildx builder
  (`srm-rootless`, docker-container driver) is pointed at by `BUILDX_BUILDER`, so EVERY
  `docker build` on the lane - including the runner's own build of a Docker container
  action (e.g. `appleboy/ssh-action`) - ran on it. The docker-container driver leaves a
  no-output build only in BuildKit's cache, never in the daemon image store, so the
  runner's follow-up `docker run <hash>:<tag>` could not find the image locally and fell
  back to pulling `<hash>` from Docker Hub, failing with "pull access denied". The builder
  is now created with `--driver-opt default-load=true`, which implicitly `--load`s every
  no-output build into the daemon store (matching the classic docker driver). Builds that
  name an output (`--push`/`--load`/`--output`) are unaffected. Needs buildx >= 0.14.

## [2.0.0] - 2026-06-22

Major release: srm gains a second OS target (Windows x64) and its two parallel
internal trees are unified into one cross-compiling codebase.

### Added
- **Windows x64 support** - srm manages self-hosted runners on Windows x64 with the
  same CLI + TUI as Linux. Persistent runners install as Windows Services (`config.cmd
  --runasservice` under `NT AUTHORITY\NETWORK SERVICE`); ephemeral lanes run a long-lived
  supervisor service that mints a single-use JIT registration per job (the Service Control
  Manager stand-in for systemd `Restart=always`); the tool-cache + build-cache env is
  injected via the per-service registry `Environment` (`AGENT_TOOLSDIRECTORY` ->
  `C:\hostedtoolcache`); ephemeral jobs are resource-capped via a Job Object (`MemoryMax`
  + `TasksMax`) and get a fresh wiped profile each cycle (the Windows analog of the per-job
  `_home`); the host dependency layer provisions via winget/choco. Install with
  `install.ps1`. Live-validated end-to-end.
- **Rootless Docker for ephemeral jobs** (`docker.rootlessDinD`, opt-in) - ephemeral
  lanes can build images with **no host root and no shared `docker` group**. Each job
  cycle starts a per-job rootless `dockerd` as the per-org user (its own user namespace,
  socket under `/run/srm/dind/<org>/<slot>`), injects `DOCKER_HOST` at it, pre-seeds a
  rootless-capable `buildx` builder and points `BUILDX_BUILDER` at it (so the default
  `docker/setup-buildx-action` + `docker-container` driver, including `cache type=gha`,
  works with **no workflow change**), then tears the daemon down and wipes its data-root
  on cycle reset - so no image layers, build cache, or registry creds leak between jobs.
  `EnsureBase` allocates a non-overlapping `/etc/subuid`+`/etc/subgid` range per org user;
  `doctor` probes the host prerequisites (rootless docker binaries, `uidmap`, `slirp4netns`,
  `fuse-overlayfs`, unprivileged userns, the subid range). The daemon is pinned to the
  cgroupfs cgroup driver and the builder to the standard BuildKit image +
  `--oci-worker-no-process-sandbox`; the DinD slot unit is deliberately less hardened than
  the strict ephemeral unit (nested rootless build containers must mount `/proc`,
  `sethostname`, write cgroupfs, and call the setuid id-map helpers) - see
  `docs/CONFIGURATION.md` and `docs/EPHEMERAL.md`. Live-validated on Ubuntu 24.04 / cgroup v2.

### Changed
- **One cross-compiling codebase.** The Windows port was staged in a parallel
  `internal-win/*` mirror; it is now folded back into a single `internal/*` + `cmd/srm`
  tree that compiles to Linux x64 (systemd + apt) and Windows x64 (Service Control Manager
  + winget) via Go build tags. One `srm` binary per OS from one source - eliminating ~53
  duplicated files and the cross-tree drift the mirror invited.

### Fixed
- **Ephemeral jobs now run with `HOME`/`USER`/`LOGNAME` set.** The ephemeral unit
  starts as root and `setpriv`-drops per job; `setpriv` is not a login, so jobs ran
  with `HOME` unset and `USER=root` - unlike every normal runner and unlike srm's own
  persistent lanes (which get `HOME` from systemd `User=`). Any action that reads
  `$HOME` broke: npm/`.netrc` auth, `git config --global`, `changesets/action`
  computing `undefined/.netrc`. srm now sets them explicitly, with `HOME` a **fresh
  per-job dir** (`{slot}/_home`, wiped each cycle) so the clean-slate guarantee holds
  and it never collides with the org's persistent runners that share the passwd HOME.
- **Rootless-DinD review fixes** (all fail-open, falling back to the prior behavior).
  The ephemeral slot's `ExecStart` now bakes the active `--config` path, so a DinD lane
  created against a non-default config no longer silently runs with the daemon off; CDI
  is disabled in the pre-seeded buildkitd config (no GPU-discovery noise on a GPU-less
  host); and the BuildKit image is cached across cycles on the build-tool cache root, so
  the per-cycle data-root wipe no longer forces a registry re-pull.

## [1.4.0] - 2026-06-21

### Added
- **`srm runners upgrade`** - in-place upgrade of the actions/runner AGENT on this
  host's PERSISTENT runners, the data-plane half of progressive updates. A runner
  keeps its registration: srm swaps the agent's version-specific payload (bin/,
  externals/) the way the agent's own auto-update does, needing no token, labels, or
  group. Upgrades run one runner at a time, each self-tested (a bounded poll that
  fails on a crash-loop, not a single sample) back to active before the next is
  touched, so at most one runner is ever offline. The prior payload is snapshotted
  aside by rename first, so a failed upgrade is rolled back to the working agent by a
  local rename - independent of the download cache or any recorded version. Busy
  runners are skipped, and a runner that becomes busy mid-pass is re-checked and
  skipped before its swap. `--to-version` / `runnerVersionPin` pin a target (refused
  unless its checksum is verifiable), `--rollback` restores the recorded prior
  version, `--force` allows a re-install or downgrade, `--dry-run` previews (and
  writes nothing). Ephemeral-lane agent upgrade is intentionally deferred (recreate a
  lane to refresh its agent) pending a safe job-drain protocol.
- **Host state manifest** (`/var/lib/srm/state.json`, root 0600) - records each
  runner's installed agent version, its host-bound GitHub id, and unit template
  generation. It is advisory: the live GitHub list and on-disk units remain the
  source of truth, so an absent or stale manifest never skips a real runner (hosts
  created by srm <= v1.3.0 are unaffected). It drives the upgrade safety gates
  (skip-if-at-target, anti-downgrade, host-bound id) and the new version readouts.
- **Agent version visibility** - `runners list` gains a `VERSION` column for
  runners this host installed, and `doctor` reports when a newer agent is published.
- **`runnerVersionPin`** config field - the explicit upgrade target, kept separate
  from `runnerVersion` (which Load coerces to a default and so can never signal
  "unpinned"). Empty means upgrades track the published version.

### Changed
- New persistent runners are registered with `--disableupdate` so the agent's own
  auto-updater can't move the version out from under srm's manifest; srm owns runner
  versions via `runners upgrade`.
- The downloaded agent tarball cache moved from `{installRoot}/.cache` (which doubled
  as the job user's writable `$HOME/.cache` in single-user mode) to a root-only
  `/var/lib/srm/agent-cache`, so untrusted job code can no longer plant a tarball that
  a later root-side extract would trust. The host-bound id gate now also falls back to
  the authoritative id in the agent's `.runner` file when the manifest has none.

## [1.3.0] - 2026-06-21

### Added
- **Template versioning** - persistent drop-ins and ephemeral units now carry a
  `# srm-dropin-vN` / `# srm-ephemeral-vN` generation marker. `reconcile` compares
  it before byte-equality: an older or absent marker is a forward bump this srm
  owns (`refresh` re-renders and stamps it), an equal marker falls through to the
  existing conformance check, and a *newer* marker is authoritative-skipped (new
  `dropin-newer` / `ephemeral-newer` classes) so a mixed-version fleet converges
  upward instead of two hosts rewriting each other's units. The marker is compared
  ignoring its own comment line, so existing markerless units upgrade seamlessly:
  they read as conformant (no drift churn, no restarts) and pick up the marker on
  their next real template change or recreate. The control-plane groundwork for
  staged progressive updates.

## [1.2.1] - 2026-06-21

### Fixed
- Installer (`install.sh`) now stages the binary and swaps it by **atomic rename**,
  keeping the prior binary as `srm.prev`. An in-place overwrite could hit "text
  file busy" or be read half-written when upgrading a host where srm is actively
  exec'd (every ephemeral lane re-execs it each job cycle).

## [1.2.0] - 2026-06-21

### Added
- **One-line installer** ([install.sh](install.sh)): `curl -fsSL .../install.sh | sudo bash`
  downloads the latest release, verifies its sha256, and installs to
  `/usr/local/bin/srm` (Ubuntu x64; `SRM_VERSION` / `PREFIX` overrides).
- Config **`schemaVersion`** - the config file now carries a schema generation that
  srm stamps and migrates forward on load. A config written by a *newer* srm is
  refused rather than silently lossy-loaded (koanf drops unknown keys). The
  control-plane foundation for progressive in-place updates; existing unversioned
  configs are adopted automatically.

### Changed
- Removed all em-dashes and en-dashes from source, docs, and output (plain hyphens).

## [1.1.0] - 2026-06-20

Lifecycle, onboarding, and UX additions (backward-compatible - existing configs
and hosts keep working untouched).

### Added
- **`srm uninstall`** - a safe, scoped purge of srm's host footprint (runners,
  ephemeral lanes, per-org users, caches, slice). Scope flags
  `--force` / `--keep-config` / `--keep-binary` / `--keep-github` / `--purge`, a
  drain gate that refuses to tear down a busy runner or in-flight lane (override
  with `--force`), a `--dry-run` plan preview, and a typed-hostname confirmation
  (`--yes` for automation). Shared infra (`/opt/hostedtoolcache`) and config are
  preserved unless `--purge`.
- **`srm backup` / `srm restore`** - archive the config directory (`config.yaml`,
  App keys, `secrets.age`) to a `.tar.gz` and restore it (path-traversal-safe,
  re-asserting file modes). `uninstall --purge` takes an automatic backup first.
- **Guided first-run setup** - bare `srm` on an unconfigured host now runs an
  interactive wizard (reusing `srm init`'s form) that writes the config and
  verifies GitHub auth before launching the TUI, so a new user never hand-edits
  `config.yaml`. `srm init` is kept for CI, re-init, and adding another org.
- **Filtering on every list view** - the `/` incremental filter now works on the
  **Ephemeral**, **Groups**, and **Health** tabs (was Persistent-only).
- `CONTRIBUTING.md` documenting the branch policy (squash-merge to `stable`),
  SemVer, the release flow, and the changelog requirement.

### Changed
- **`srm init`** verifies the App credentials against GitHub after writing the
  config (the common wrong-installation-id / unreadable-key trap now surfaces
  immediately instead of at first TUI load).
- TUI: the selected table row highlight spans the **full row width** (previously
  only the first column was highlighted); the Groups tab gained a selection
  detail line.

### Fixed
- Strip a UTF-8 **BOM** from the agent's `.runner` file before parsing its id -
  some agents write one, which previously broke id-based deregistration.
- `srm runners create` **self-bootstraps its base directories** (the shared
  `installRoot/.cache`, plus the download and JIT control-file parents), so a
  create succeeds on a clean host or immediately after a full uninstall.

### Security
- **Host-bound deregistration** - destroying/uninstalling a persistent runner now
  deregisters it on GitHub by the agent's **host-local id** (from `.runner`), never
  by name. A name match could deregister a same-named runner owned by **another
  host** in a shared org; id matching makes the GitHub delete strictly host-owned.
- **CI supply-chain hardening** - GitHub Actions are pinned to commit SHAs (not
  mutable tags), `checkout` runs with `persist-credentials: false`, a
  `govulncheck` gate fails the build on any reachable known vulnerability, and
  weekly Dependabot keeps pins/modules current. The `actions/setup-go` v6 bump
  also pulls in a `form-data` CVE fix.

## [1.0.0] - 2026-06-19

First stable release. `srm` is an interactive **fleet administrator** (TUI + CLI)
for GitHub Actions self-hosted runners across one or more organizations on
dedicated **Ubuntu x64** hosts. It manages both **persistent** and **ephemeral
(JIT)** runners, provisions the host dependency layer, enforces machine-relative
cgroup memory caps, isolates orgs from each other, and audits host-vs-GitHub
drift. It is **not an autoscaler** (defer to ARC / Runner Scale Sets for elastic
scaling).

Validated on a live two-org, single-host deployment (8 cores, 15 GB):
4 persistent + 20 ephemeral slots running under one aggregate memory ceiling.

### Authentication & configuration
- **GitHub App installation auth only** - per-org `*http.Client` minted from the
  App installation token (`internal/auth`); no PATs. See
  [docs/GITHUB_APP_SETUP.md](docs/GITHUB_APP_SETUP.md).
- **Multi-org** config (koanf/YAML); every operation names its org (`--org`, or
  implicitly when one org is configured). No hidden "active org".
- **Secrets**: App private key referenced by `privateKeyPath`, or encrypted into
  an age file (`secrets.age`) via `SRM_SECRETS_PASSPHRASE`. Short-lived
  registration/remove tokens are minted on demand, never stored or logged.
- **Config resolution**: `--config` > `/etc/srm/config.yaml` (auto when present,
  and the default under sudo) > `$HOME/.config/srm/config.yaml`. Full field
  reference in [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
- `srm init` (interactive setup), `srm doctor` (auth/connectivity/retention +
  host toolchain probe), `srm version` / `--version`.

### Persistent runners
- `srm runners create` - download + **sha256-verify** + extract the actions
  agent, configure as a dedicated non-root user, install + start a hardened
  systemd unit (`actions.runner.<org>.<name>.service`), ensuring the runner group.
- `srm runners destroy <name>` - host teardown (stop/uninstall/remove tree) +
  GitHub deregister. Org-aware: resolves a name to its owning org and refuses
  ambiguous cross-org names.
- `srm runners list` (cross-org, ORG/MACHINE columns), `srm runners delete <id…>`
  (bounded-concurrency, org-aware), `srm groups list/create`.
- `srm runners refresh [--org]` - re-applies the systemd drop-in (hardening +
  tool-cache env + caps + isolation `User=`) and restarts idle runners in place
  (busy runners skipped). Also the in-place per-org isolation migration path.
- Per-runner trees are **org-namespaced** (`{installRoot}/{org}/{name}`).

### Ephemeral (JIT) runners
- A fixed **slot lane** model: `srm runners create --ephemeral --count N` stands
  up N numbered systemd lanes (`actions.ephemeral.<org>.<slot>.service`,
  `Restart=always`). Each lane mints a **fresh single-use JIT registration per
  job** (`GenerateOrgJITConfig`), runs exactly one job, then auto-deregisters -
  zero credentials at rest between jobs, drift-free churn, clean `_work` per job.
- **Privilege split**: the lane starts as **root** to mint from the App key
  (`/etc/srm/<org>.pem`, root-only), records the runner id (fsync) for crash
  reaping, then drops to the per-org user via `setpriv` (PAM-free) and execs
  `run.sh --jitconfig <blob>` for one job. Stricter hardening than persistent
  units (`NoNewPrivileges`, `ProtectProc=invisible`, `LimitCORE=0`).
- Root-trusted control files (`jit-id`, `jit-params`) live OUTSIDE the
  job-writable slot tree at `/var/lib/srm/ephemeral/<org>/<slot>` (root 0700), so
  untrusted job code cannot forge a deregister id or poison mint params.
- `srm runners destroy --ephemeral --slot N` (drains + deregisters + removes).
- `srm reconcile --reap-ephemeral` deregisters offline, non-busy JIT ghosts left
  by a crashed cycle - the one gated exception to "reconcile never deletes on
  GitHub", guarded so it can never kill a live or another host's runner.
- See [docs/EPHEMERAL.md](docs/EPHEMERAL.md). Note: JIT registrations carry
  **exactly** the labels you pass (`--labels`); GitHub does not auto-add
  `self-hosted`/`Linux`/`X64` as it does for `config.sh` runners - pass the full
  set to match `runs-on: [self-hosted, …]`.

### Dynamic capacity policy (OOM protection)
- `resourceMode: auto` applies **machine-relative percentage** cgroup caps that
  systemd evaluates against live RAM, so they auto-scale on host resize with no
  reconfiguration: per-runner `MemoryHigh=20%`, `MemoryMax=25%`, `MemorySwapMax=0`.
- An aggregate **`srm.slice`** holds every runner under one ceiling
  (`sliceMemoryMax`, default `75%`) - the lever that stops many moderate jobs from
  collectively OOM'ing the host (a per-runner cap can't). `MemorySwapMax=0`
  prevents swap-thrash that once wedged sshd.
- Manual mode (`resources:` with literal values, no `resourceMode`) and the
  default (no caps) remain byte-identical to prior behavior.

### Per-org isolation
- `isolation.perOrgUsers: true` runs each org as `srm-<slug(org)>` owning a
  private HOME (`{installRoot}/{org}`, 0700), a private dep cache
  (`/opt/srm-cache/<org>`, 0700), and a private tool cache
  (`/opt/hostedtoolcache/<org>`). **No shared unix group** (which would itself be
  a cross-org read channel). Cross-org reads of creds/caches are denied.
- Load-time guard rejects two orgs that slug to the same service user.

### Host provisioning
- `srm provision` applies a **host-once** dependency layer (self-hosted runners
  ship bare): `--apt`, `--node` (NodeSource + the libs `actions/setup-node`'s
  prebuilt Node needs), `--corepack`, `--seed`/`--seed-node` (pre-bake tool-cache
  versions), `setupScripts`, plus the config `host:` manifest. Runners point at a
  shared/per-org tool cache via `AGENT_TOOLSDIRECTORY`.
- `srm cache prune` - atime-based eviction of host build-tool dep caches
  (per-org under isolation).

### Reconcile & observability
- `srm reconcile [--fix] [--org]` - audits host vs GitHub and classifies each
  runner (healthy / stale-dropin / stuck / orphan-unit / legacy-flat /
  orphan-github / unknown); ephemeral lanes are a separate family judged by host
  health alone. `--fix` repairs **host-side** drift only (refresh stale, restart
  stuck, remove orphan units; busy skipped); GitHub entries are never deleted
  (except `--reap-ephemeral`). An `unknown` guard prevents mass-misclassification
  when an org's GitHub list fails.
- **Per-runner OOM + memory observability**: reads each unit's cgroup v2
  `memory.events` **oom_kill** counter (direct OOM attribution) and live
  `MemoryCurrent`; reports a HOST HEALTH **`slice mem`** aggregate line
  (`now used / cap`), per-runner `peak / cap (now)`, and a dedicated **OOM
  EVENTS** section. Disk + per-org cache sizes too.

### TUI
- Bubble Tea v2 cockpit with five clearly-separated tabs: **Persistent**,
  **Ephemeral**, **Groups**, **Health**, **Settings**. Persistent and Ephemeral
  are deliberately distinct (different columns, create/destroy flows, and
  addressing - runner name vs slot id); cross-nature actions are impossible.
- `n` create wizard (huh) with a live progress bar; `d` destroy/deregister behind
  a confirm modal; `/` incremental filter (Persistent); `o` org filter; `r`
  refresh; `e` edit the capacity policy (Settings → writes `config.yaml`); `?`
  help; full-width tables, footer pinned bottom-left, centered modals.

### Security & hardening
- Agents never run as root; per-unit systemd hardening drop-in (`ProtectHome`,
  `PrivateTmp`, `ProtectKernel*`, `ProtectControlGroups`, `LockPersonality`, …),
  safe for general CI. App keys are root-only under `/etc/srm`.
- Opt-in `hardening.protectProc: true` adds `ProtectProc=invisible` to persistent
  units (ephemeral lanes always set it), closing the residual cross-org
  `/proc/<pid>/cmdline` window. Default off → drop-in byte-identical.
- The whole feature set passed a multi-agent adversarial review before release.

### Known limitations
- **Ubuntu x64 only** (systemd + apt + cgroup v2 assumed).
- Not an autoscaler; `Restart=always` keeps ephemeral lanes warm (no
  scale-to-zero - that needs a webhook, out of scope).
- Ephemeral does not lower idle RAM (an idle JIT lane ≈ a persistent listener);
  the OOM levers are the caps + slice + trimming concurrency.
- Ephemeral lanes wipe `_diag`/`_work` each cycle, so on-disk job history is a
  persistent-runner concept.

[Unreleased]: https://github.com/erlete/srm/compare/v2.5.1...HEAD
[2.5.1]: https://github.com/erlete/srm/compare/v2.5.0...v2.5.1
[2.5.0]: https://github.com/erlete/srm/compare/v2.4.0...v2.5.0
[2.4.0]: https://github.com/erlete/srm/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/erlete/srm/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/erlete/srm/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/erlete/srm/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/erlete/srm/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/erlete/srm/compare/v1.4.0...v2.0.0
[1.4.0]: https://github.com/erlete/srm/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/erlete/srm/compare/v1.2.1...v1.3.0
[1.2.1]: https://github.com/erlete/srm/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/erlete/srm/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/erlete/srm/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/erlete/srm/releases/tag/v1.0.0
