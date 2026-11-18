# Configuration reference

`srm` reads a single YAML file. Resolution precedence:

1. `--config <path>` (explicit override)
2. `/etc/srm/config.yaml` - used automatically when it exists, and the default
   target under sudo (so `sudo srm init` seeds `/etc/srm`)
3. `$HOME/.config/srm/config.yaml` - per-user fallback for non-root invocations

The age secrets file (`secrets.age`) lives next to whichever config is active.
`srm init` writes a config interactively; this page is the hand-edit reference.
Invalid `resourceMode` and isolation user collisions are rejected at load.

## Top-level fields

| Key | Type | Default | Purpose |
| --- | --- | --- | --- |
| `schemaVersion` | int | current | Config-schema generation, stamped by srm. Migrated forward on load; a value newer than this srm understands is refused (so unknown keys aren't silently dropped). Usually omitted in hand-written files. |
| `orgs` | list | - (required) | The organizations srm manages. See [Org](#org-orgs). |
| `concurrency` | int | small | Bulk-op fan-out (kept < 100). |
| `runnerVersion` | string | - | Pinned default actions-runner release (e.g. `"2.335.1"`). |
| `runnerUser` | string | `srm` | Dedicated non-login service user that owns runner trees (single-user mode). |
| `dryRun` | bool | `false` | Preview mutations without calling GitHub (also `--dry-run`). |
| `logFile` | string | - | Optional log file path. |
| `cacheRetentionDays` | int | command default | Default age cutoff for `srm cache prune`. |
| `resources` | map | - (no caps) | Host-wide per-runner cgroup caps. See [Resources](#resources-resources). |
| `resourceMode` | string | `""` | `""` = literal `resources`; `"auto"` = machine-relative %. See [Capacity](#capacity-policy). |
| `sliceMemoryMax` | string | `75%` | Aggregate `srm.slice` ceiling (auto mode only). |
| `isolation` | map | off | Per-org users + caches. See [Isolation](#isolation-isolation). |
| `hardening` | map | off | Optional persistent-unit hardening. See [Hardening](#hardening-hardening). |
| `host` | map | - | Host-once dependency manifest for `srm provision`. See [Host](#host-host). |

## Org (`orgs[]`)

| Key | Type | Purpose |
| --- | --- | --- |
| `name` | string | GitHub org login (e.g. `Acme`). |
| `appID` | int | GitHub App ID. |
| `installationID` | int | The App's installation ID for this org. |
| `privateKeyPath` | string | Path to the App private key `.pem` (root-only, e.g. `/etc/srm/Acme.pem`). Omit if using `secrets.age`. |
| `defaultGroupID` | int | Runner group used when a create omits `--group` (also the ephemeral mint default). |
| `defaultLabels` | list | Default labels shown/seeded (e.g. `[self-hosted, linux, x64]`). |
| `installRoot` | string | Per-runner tree root (default `/opt/actions-runners`). |
| `runnerUser` | string | Per-org override of the service user (isolation mode). |
| `resources` | map | Per-org override of the cgroup caps (field-level merge over the host default). |
| `profiles` | list | Optional named runner profiles. |
| `dind.portRange` | string | Rootless-DinD only: this org's pool of dynamically published host ports, `START-END` (e.g. `10000-29999`). Each ephemeral slot gets its own block of it. Empty = off. See [Published ports](#published-ports-orgsdind). |
| `dind.portsPerSlot` | int | Block size per slot inside `dind.portRange` (default `1000`). |
| `cache.env` | map | Add, move or remove build-tool cache variables for this org (`NAME: subdirectory`, `""` removes a default). See [Build-tool caches](#build-tool-caches-orgscache). |

See [GITHUB_APP_SETUP.md](GITHUB_APP_SETUP.md) for creating the App, the exact
permissions (**Organization → Self-hosted runners: Read & write**, plus optional
**Administration** for the retention panel), and finding the IDs.

## Capacity policy

`srm` emits cgroup v2 directives into each runner's systemd unit. Two modes:

- **Manual / off** (`resourceMode` unset): the literal `resources` fields are
  written verbatim. An empty `resources` writes no caps (the historical default).
- **Auto** (`resourceMode: auto`): starts from machine-relative percentage caps
  that **systemd evaluates against live RAM**, so the host can be resized with no
  reconfiguration. Defaults: `MemoryHigh=20%`, `MemoryMax=25%`, `MemorySwapMax=0`.
  Any explicit `resources` (host) or `orgs[].resources` (org) field overrides a
  single auto value. In auto mode srm also writes a parent **`srm.slice`** capped
  at `sliceMemoryMax` (default `75%`) with `MemorySwapMax=0` - the box-wide
  ceiling for **all runners combined**, which is what actually prevents many
  moderate jobs from collectively OOM'ing the host.

### `resources` (and `orgs[].resources`)

All values are **systemd syntax strings** - a percentage (`"25%"`), a size
(`"3G"`), `"0"`, or `"infinity"`. Quote bare numbers/percentages.

| Key | Meaning |
| --- | --- |
| `memoryHigh` | Soft cap - throttle + reclaim above this. |
| `memoryMax` | Hard cap - the cgroup is OOM-killed above this. |
| `memorySwapMax` | Swap cap (`"0"` = no swap; the anti-thrash rule). |
| `cpuWeight` | Relative CPU share (1-10000; 100 = default). |
| `tasksMax` | PID cap (fork-bomb guard). |

Changes apply to **new** runners; run `srm runners refresh` to push them onto
already-installed persistent runners (busy runners are skipped). Ephemeral slots pick
them up when recreated (TUI Ephemeral tab, `c`, idle slots only). `srm doctor` shows the
resolved caps; `srm reconcile` reports live `slice mem`, per-runner peak/now, and
any OOM events.

## Isolation (`isolation`)

| Key | Type | Purpose |
| --- | --- | --- |
| `perOrgUsers` | bool | When true, each org runs as `srm-<slug(org)>` owning a private HOME (`{installRoot}/{org}`, 0700), dep cache (`/opt/srm-cache/<org>`, 0700), and tool cache (`/opt/hostedtoolcache/<org>`). |

There is **no shared unix group** (a shared group is itself a cross-org read
channel). Cross-org reads of creds/caches are denied. Two orgs that slug to the
same user are rejected at load (set an explicit `orgs[].runnerUser` to resolve).
Migration is in place: `srm runners refresh [--org]` rewrites the drop-in
(`User=`), re-chowns the tree, and restarts - no recreate/re-register.

## Hardening (`hardening`)

| Key | Type | Purpose |
| --- | --- | --- |
| `protectProc` | bool | Adds `ProtectProc=invisible` to **persistent** runner units (ephemeral lanes always set it), hiding other users' `/proc` so a job can't read another org's unit cmdline. Default off → drop-in byte-identical. |

## Docker (`docker`)

Gives **ephemeral** jobs a rootless Docker daemon to build images against, with no
host root and no shared `docker` group. Default off (no daemon, byte-identical to
before). See `docs/EPHEMERAL.md` for the full model and the hardening trade-off.

| Key | Type | Purpose |
| --- | --- | --- |
| `rootlessDinD` | bool | Per-job rootless `dockerd` on ephemeral slots; `DOCKER_HOST`/`BUILDX_BUILDER` injected so a stock `setup-buildx-action` build works unchanged. Daemon + data-root wiped each cycle. |
| `buildkitImage` | string | Override the pre-seeded buildx BuildKit image. Default `moby/buildkit:buildx-stable-1` (the **standard** image - the `-rootless` tag double-nests and fails inside a rootless daemon). |

Requires host prerequisites srm does not install (add via the `host` manifest below
or a `setupScript`): `docker-ce` + `docker-ce-rootless-extras`, `uidmap`,
`slirp4netns`, `fuse-overlayfs`, and `kernel.unprivileged_userns_clone=1` /
`user.max_user_namespaces>0`. `srm doctor` probes these and the per-user subid range
(allocated automatically by `EnsureBase`). DinD slots run a less-hardened unit than
the strict ephemeral default - see `docs/EPHEMERAL.md`.

### Published ports (`orgs[].dind`)

Every slot's rootless daemon starts with the same kernel default range for dynamic
host ports (`32768-60999`), so parallel jobs that publish ports without naming one
(`docker run -p 0:80`, `-P`, testcontainers) ask for the same host port and the
second one fails with `error while calling RootlessKit PortManager.AddPort(): ...
bind: address already in use`. Give each org a pool and srm splits it into one block
per slot:

```yaml
orgs:
  - name: acme
    dind:
      portRange: 10000-29999   # this org's pool; must not overlap another org's
      portsPerSlot: 500        # optional, default 1000
```

Slot `N` gets `[start + (N-1)*portsPerSlot, start + N*portsPerSlot - 1]`, so here
slot 1 publishes from `10000-10499`, slot 2 from `10500-10999`, and the pool has room
for 40 slots. srm pins the block inside the slot's own network namespace right before
the daemon starts, through a small root-owned wrapper it installs at
`/usr/local/lib/srm/dockerd-port-wrapper`; the host's `net.ipv4.ip_local_port_range`
is never changed.

- **Applies per cycle.** Each cycle re-reads `config.yaml`, so a slot picks up a new
  or removed pool when its current job ends. No unit change, recreate, or restart.
- **Fail-open.** A malformed pool, a slot past the end of its pool, or a wrapper the
  runner user cannot execute (for example `/usr/local/lib` on a `noexec` mount) leaves
  that slot on the default range (the previous behavior) and logs the reason to the
  slot's journal. If the daemon still fails to start with the wrapper, srm relaunches
  it without the wrapper in the same cycle. `srm doctor` lists all of these.
- **Pick pools outside the host's ephemeral range** (`sysctl
  net.ipv4.ip_local_port_range`, usually `32768-60999`). Inside it, the host's own
  outbound connections, and the daemons of orgs with no pool, draw from the same
  ports. Keep clear of ports that services on the host listen on.
- **Size the block for connections, too.** The block is also the source-port range
  of the two proxy hops inside the slot's namespace (RootlessKit's forwarder and
  `docker-proxy`), one pair per inbound connection to a published port. srm sets
  `net.ipv4.tcp_tw_reuse=1` there, so a published port sustains roughly
  `portsPerSlot` new connections per second (without it, closed connections would
  hold their source port for 60 seconds). A job that opens more short-lived
  connections than that to one published port needs a larger `portsPerSlot`.
- **Explicit host ports** (`-p 8080:80`) are not partitioned: two jobs that publish
  the same fixed port still collide, and a fixed port inside another slot's block can
  take that port from it.

`srm doctor` prints each pool and, for every local slot, the block it is assigned next
to the range and `tcp_tw_reuse` its running daemon actually uses, read from the slot's
network namespace (`pinned`, `pending` until its next cycle, `not applied`, `default`,
or `not running`), plus warnings for overlapping pools. Run it as root. The Health tab
shows a summary.

## Build-tool caches (`orgs[].cache`)

Every runner's environment points each package manager's cache at a host-persistent
directory under the org's cache root: `/opt/srm-cache`, or `/opt/srm-cache/<org>` with
`isolation.perOrgUsers` (`%ProgramData%\srm\cache` on Windows). Jobs reuse downloads
across runs, including on ephemeral lanes whose `HOME` is wiped after every job.

| Variable | Tool | Subdirectory |
| --- | --- | --- |
| `npm_config_cache` | npm | `npm` |
| `npm_config_store_dir` | pnpm 10 and older | `pnpm` |
| `pnpm_config_store_dir` | pnpm 11 and newer | `pnpm` |
| `YARN_CACHE_FOLDER` | Yarn classic | `yarn` |
| `COREPACK_HOME` | Corepack (downloaded pnpm/Yarn) | `corepack` |
| `PIP_CACHE_DIR` | pip | `pip` |
| `UV_CACHE_DIR` | uv | `uv` |
| `GOMODCACHE` | Go modules | `go/mod` |
| `GOCACHE` | Go build cache | `go/build` |
| `CARGO_HOME` | Cargo registry and git caches | `cargo` |
| `GRADLE_USER_HOME` | Gradle | `gradle` |
| `PLAYWRIGHT_BROWSERS_PATH` | Playwright browsers | `playwright` |
| `MISE_CACHE_DIR` | mise downloads | `mise/cache` |

Both pnpm variables point at the same store: pnpm keeps one subdirectory per store
version (`v3`, `v10`, `v11`), so versions do not clash.

These directories are shared by all of the org's runners, and parallel ephemeral slots
use them at the same time. The defaults are therefore only caches that are safe to read
and write concurrently.

`cache.env` changes the list for one org, as `NAME: subdirectory`:

```yaml
orgs:
  - name: Acme
    cache:
      env:
        MISE_DATA_DIR: mise/data   # add: share the tools mise installs
        TURBO_CACHE_DIR: turbo     # add: Turborepo's local cache
        UV_CACHE_DIR: python/uv    # move a default
        GOCACHE: ""                # remove a default
```

- The subdirectory is relative to the cache root and may contain letters, digits, `.`,
  `_`, `-` and `/`, with no `.` or `..` segments.
- An empty value removes a default.
- srm refuses names it or the runner sets (`HOME`, `PATH`, `TMPDIR`, `USERPROFILE`, ...),
  names that load code (`LD_*`, `DYLD_*`, `PYTHONPATH`, `NODE_PATH`, `NODE_OPTIONS`, ...)
  and the `RUNNER_`, `GITHUB_`, `ACTIONS_`, `SRM_` and `DOCKER_` prefixes, since a shared
  directory there would let one job affect the next.
- An invalid entry is left out and the rest still applies. `srm doctor` shows each org's
  cache variables and lists every ignored entry; a bad entry never stops a runner.

Two additions need care, which is why they are not defaults:

- `MISE_DATA_DIR` shares installed tools, so a job that runs `mise prune` or uninstalls
  a version changes them for every other job of the org.
- `TURBO_CACHE_DIR` shares build outputs that any job of the org can write, including a
  pull request's. Keep release builds off it (`turbo run build --force`).

`srm cache prune` deletes cache files not accessed within `cacheRetentionDays`, but it
skips `COREPACK_HOME`, `UV_CACHE_DIR`, `PLAYWRIGHT_BROWSERS_PATH` and `MISE_DATA_DIR`
wherever `cache.env` points them. They hold unpacked installs, where deleting single
files leaves an install that looks complete but is broken. Trim them with the tool
itself (`uv cache prune`, `mise prune`).

The variables live in each runner's unit (the service environment on Windows). After
changing `cache.env`, or upgrading to an srm whose defaults changed, `srm reconcile`
reports the runners as drift until their units are re-applied:

- Persistent runners: `sudo srm runners refresh` (busy runners are skipped; `--org`
  stages one org at a time).
- Ephemeral slots: `runners refresh` does not cover them. Recreate each slot (TUI
  Ephemeral tab, `c`) while it is idle: recreating stops the slot, including a job
  running on it. Until then the slot keeps working without the change.

In workflows on these runners, leave the cache options of setup actions off
(`actions/setup-node` `cache:`, `astral-sh/setup-uv` `enable-cache`): they would upload
the org's whole shared cache to GitHub's cache, and `setup-uv` also prunes it after the
job.

## Host (`host`)

Host-once dependency manifest applied by `srm provision`; `srm doctor` reports
drift. Self-hosted runners ship bare, so the job toolchain lives here.

| Key | Type | Purpose |
| --- | --- | --- |
| `aptPackages` | list | apt packages installed once on the host. |
| `setupScripts` | list | Escape hatch for complex setups. |
| `toolCacheSeeds` | list | Tarballs to pre-seed the tool cache (e.g. `node@22.11.0`, `go@1.26.4`, `python@3.12.13`). |
| `persistentCachePaths` | list | Host paths kept across jobs. |

## Worked example

```yaml
concurrency: 8
runnerVersion: "2.335.1"
runnerUser: srm
cacheRetentionDays: 30

host:
  aptPackages: [libatomic1, libstdc++6, ca-certificates, curl, build-essential, git, jq, unzip]
  toolCacheSeeds: [node@22.11.0, go@1.26.4, python@3.12.13]

orgs:
  - name: Acme
    appID: 123456
    installationID: 7654321
    privateKeyPath: /etc/srm/Acme.pem
    defaultGroupID: 7
    defaultLabels: [self-hosted, linux, x64]
    installRoot: /opt/actions-runners

# Machine-relative caps that scale with host RAM (no reconfig on resize).
resourceMode: auto
sliceMemoryMax: "75%"          # aggregate ceiling for ALL runners combined
resources:
  tasksMax: "4096"             # per-runner PID guard (override; memory stays auto %)

isolation:
  perOrgUsers: true            # each org as srm-<slug(org)>, private HOME + caches

hardening:
  protectProc: true            # hide other users' /proc from persistent units
```
