# Changelog

All notable changes to `srm` are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/erlete/srm/compare/v1.2.1...HEAD
[1.2.1]: https://github.com/erlete/srm/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/erlete/srm/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/erlete/srm/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/erlete/srm/releases/tag/v1.0.0
