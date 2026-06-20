# Configuration reference

`srm` reads a single YAML file. Resolution precedence:

1. `--config <path>` (explicit override)
2. `/etc/srm/config.yaml` — used automatically when it exists, and the default
   target under sudo (so `sudo srm init` seeds `/etc/srm`)
3. `$HOME/.config/srm/config.yaml` — per-user fallback for non-root invocations

The age secrets file (`secrets.age`) lives next to whichever config is active.
`srm init` writes a config interactively; this page is the hand-edit reference.
Invalid `resourceMode` and isolation user collisions are rejected at load.

## Top-level fields

| Key | Type | Default | Purpose |
| --- | --- | --- | --- |
| `schemaVersion` | int | current | Config-schema generation, stamped by srm. Migrated forward on load; a value newer than this srm understands is refused (so unknown keys aren't silently dropped). Usually omitted in hand-written files. |
| `orgs` | list | — (required) | The organizations srm manages. See [Org](#org-orgs). |
| `concurrency` | int | small | Bulk-op fan-out (kept < 100). |
| `runnerVersion` | string | — | Pinned default actions-runner release (e.g. `"2.335.1"`). |
| `runnerUser` | string | `srm` | Dedicated non-login service user that owns runner trees (single-user mode). |
| `dryRun` | bool | `false` | Preview mutations without calling GitHub (also `--dry-run`). |
| `logFile` | string | — | Optional log file path. |
| `cacheRetentionDays` | int | command default | Default age cutoff for `srm cache prune`. |
| `resources` | map | — (no caps) | Host-wide per-runner cgroup caps. See [Resources](#resources-resources). |
| `resourceMode` | string | `""` | `""` = literal `resources`; `"auto"` = machine-relative %. See [Capacity](#capacity-policy). |
| `sliceMemoryMax` | string | `75%` | Aggregate `srm.slice` ceiling (auto mode only). |
| `isolation` | map | off | Per-org users + caches. See [Isolation](#isolation-isolation). |
| `hardening` | map | off | Optional persistent-unit hardening. See [Hardening](#hardening-hardening). |
| `host` | map | — | Host-once dependency manifest for `srm provision`. See [Host](#host-host). |

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
  at `sliceMemoryMax` (default `75%`) with `MemorySwapMax=0` — the box-wide
  ceiling for **all runners combined**, which is what actually prevents many
  moderate jobs from collectively OOM'ing the host.

### `resources` (and `orgs[].resources`)

All values are **systemd syntax strings** — a percentage (`"25%"`), a size
(`"3G"`), `"0"`, or `"infinity"`. Quote bare numbers/percentages.

| Key | Meaning |
| --- | --- |
| `memoryHigh` | Soft cap — throttle + reclaim above this. |
| `memoryMax` | Hard cap — the cgroup is OOM-killed above this. |
| `memorySwapMax` | Swap cap (`"0"` = no swap; the anti-thrash rule). |
| `cpuWeight` | Relative CPU share (1–10000; 100 = default). |
| `tasksMax` | PID cap (fork-bomb guard). |

Changes apply to **new** runners; run `srm runners refresh` to push them onto
already-installed units (busy runners are skipped). `srm doctor` shows the
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
(`User=`), re-chowns the tree, and restarts — no recreate/re-register.

## Hardening (`hardening`)

| Key | Type | Purpose |
| --- | --- | --- |
| `protectProc` | bool | Adds `ProtectProc=invisible` to **persistent** runner units (ephemeral lanes always set it), hiding other users' `/proc` so a job can't read another org's unit cmdline. Default off → drop-in byte-identical. |

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
