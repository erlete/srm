# srm - self-hosted runner manager

A Bubble Tea TUI + CLI for managing **GitHub Actions self-hosted runners** across
one or more organizations, on dedicated **Ubuntu x64** hosts.

> **Scope:** `srm` is an *interactive fleet administrator* for a single host or a
> small fleet - listing/creating/deleting runners, managing groups and labels,
> minting registration/JIT tokens, and provisioning the host dependency layer.
> It is **not an autoscaler.** For elastic scaling defer to
> [Actions Runner Controller (ARC) / Runner Scale Sets](https://docs.github.com/en/actions/how-tos/manage-runners/use-actions-runner-controller),
> `philips-labs/terraform-aws-github-runner`, or the Actions Runner Scale Set Client.

## Locked design

| Decision         | Choice                                                                                                                      |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Hosts            | Dedicated **Ubuntu 24.04-26.x, x64 only** (systemd + apt)                                                                  |
| Auth             | **GitHub App installation token only** - see [docs/GITHUB_APP_SETUP.md](docs/GITHUB_APP_SETUP.md)                          |
| Runner lifecycle | **Ephemeral / JIT** by default, systemd-supervised warm loop                                                               |
| Dependency model | **Host-baked** - provision the host once; ephemeral runners consume it. `setupScripts` + bring-your-own-host escape hatches |
| Containers       | Per-job container is an **opt-in per profile** (default: jobs use the host toolchain)                                      |
| Multi-org        | First-class - many orgs in one config; every operation names its org (`--org`, or implicitly when one is configured); TUI cycles the view with `o` |

## Architecture

Strictly one-directional layering:

```
TUI / CLI  →  service.Manager  →  { github adapter · runner orchestrator · provision · secrets · config }
                                          ↑ all depend on the leaf `core` types package
```

Every GitHub call and shell-out runs inside a Bubble Tea `tea.Cmd` and returns a
typed `tea.Msg`; the UI never blocks. Bulk operations use bounded concurrency
with rate-limit-aware backoff (there is no bulk-delete API).

| Package              | Responsibility                                                                 |
| -------------------- | ------------------------------------------------------------------------------ |
| `internal/core`      | Pure domain types (leaf, no internal imports)                                  |
| `internal/config`    | Multi-org YAML config (koanf)                                                  |
| `internal/secrets`   | Pluggable secret store - age-encrypted file (headless default) / env           |
| `internal/auth`      | Per-org `*http.Client` from the GitHub App installation token                  |
| `internal/github`    | go-github v88 adapter (runners, groups, tokens, JIT, downloads, retention)     |
| `internal/service`   | Orchestration: list/delete, bulk engine, policy guardrails, lifecycle          |
| `internal/provision` | Host-once dependency manifest apply + drift (apt · setup scripts · cache paths) |
| `internal/runner`    | On-machine agent: download/verify/extract, systemd units + drop-ins, cgroup caps/`srm.slice`, per-org isolation, ephemeral JIT supervise + cycle |
| `internal/tui`       | Bubble Tea v2 UI - five-tab cockpit (Persistent/Ephemeral/Groups/Health/Settings), spinner, help, confirm modal, huh create wizard |
| `internal/cli`       | cobra commands (bare = TUI; `init`, `runners`, `groups`, `provision`, `doctor`, `cache`, `reconcile`, `version`) |

## Status - v1.0.0 (stable)

Everything below is implemented and validated on a live two-org,
single-host deployment:

- **Auth & config** - GitHub App installation auth, age-encrypted secrets,
  multi-org config, `srm init` / `srm doctor` / `srm version`.
- **Persistent runners** - `runners create` (download + sha256-verify + extract,
  configure as a dedicated user, hardened systemd unit, ensure group),
  `runners destroy` (host teardown + deregister), org-aware `list`/`delete`,
  `runners refresh` (in-place drop-in / isolation migration).
- **Ephemeral (JIT) runners** - `runners create --ephemeral --count N` slot lanes
  that mint a fresh single-use registration per job (root mints → setpriv drop →
  one job → auto-deregister), `runners destroy --ephemeral --slot N`,
  `reconcile --reap-ephemeral`. See [docs/EPHEMERAL.md](docs/EPHEMERAL.md).
- **Dynamic capacity** - `resourceMode: auto` machine-relative cgroup caps plus an
  aggregate `srm.slice` ceiling, auto-scaling with host RAM (no reconfig on resize).
- **Per-org isolation** - `isolation.perOrgUsers`: each org as its own service
  user with a private HOME + caches; no shared unix group.
- **Provisioning** - `srm provision` (apt / Node / corepack / seed) + `cache prune`.
- **Reconcile & observability** - host-vs-GitHub drift audit + `--fix`, plus
  per-runner cgroup OOM-kill attribution, live + aggregate slice memory, OOM events.
- **TUI** - five-tab cockpit (Persistent / Ephemeral / Groups / Health / Settings)
  with a create wizard, filter, confirm modal, and a Settings capacity editor.
- **Hardening** - non-root agents, per-unit systemd sandbox, opt-in `ProtectProc`
  on persistent units.

See [CHANGELOG.md](CHANGELOG.md) for the full inventory and
[docs/CONFIGURATION.md](docs/CONFIGURATION.md) for every config field.

## Install & run on the server

`srm` is a single static binary (all dependencies are pure Go, so it builds with
`CGO_ENABLED=0` and has no runtime dependencies).

```bash
# On the Ubuntu x64 host (or cross-compiled and copied over):
make install            # builds dist/srm and installs to /usr/local/bin/srm
# or just:
make build-linux        # produces ./dist/srm to scp to the host
# or install straight from source (note: reports version "dev" - not stamped):
go install github.com/erlete/srm/cmd/srm@latest
```

There are two ways `srm` runs on a server:

1. **Interactive administration** - SSH in and run `srm` (the TUI) or the
   headless subcommands (`srm runners list`, `srm runners delete …`,
   `srm doctor`). This is the management plane; `srm` is **not** a daemon.
2. **Supervised runners** - `srm runners create` installs one **systemd** unit
   per runner, running the agent as a non-root user with an srm hardening drop-in.
   Two shapes: **persistent** (`actions.runner.<org>.<name>.service`, registered
   once) and **ephemeral** (`actions.ephemeral.<org>.<slot>.service`, a warm lane
   that mints a fresh JIT registration per job, runs it, and re-mints - see
   [docs/EPHEMERAL.md](docs/EPHEMERAL.md)). systemd keeps them alive; `srm` is the
   controller/installer.

For distribution across many servers, grab the prebuilt static binary from the
[latest GitHub Release](https://github.com/erlete/srm/releases) - it is built and
attached automatically on every `v*` tag by
[.github/workflows/release.yml](.github/workflows/release.yml)
(`srm-<version>-ubuntu-x64` + `.sha256`) - and `curl` it onto each host, or
`go install github.com/erlete/srm/cmd/srm@latest`.

### Host layout (mainstream & secure)

srm-managed runners use FHS-standard locations and a locked-down service identity:

| Path / identity | Purpose | Perms |
| --- | --- | --- |
| `/usr/local/bin/srm` | the binary | 0755 |
| `/etc/srm/` | config + secrets (off-bounds) | 0700 root |
| `/etc/srm/config.yaml` | configuration | 0640 root |
| `/etc/srm/<org>.pem` or `secrets.age` | App key | 0600 root |
| `/opt/actions-runners/<org>/<name>/` | per-runner tree (org-namespaced) | 0750 `srm` |
| user `srm` (system, nologin) | runs the agents - **never root** | - |
| `actions.runner.<org>.<name>.service` | systemd unit + srm hardening drop-in (`ProtectHome`, `PrivateTmp`, `ProtectKernel*`, `ProtectControlGroups`, `LockPersonality`, …) | - |

Per-runner trees are **org-namespaced** (`{installRoot}/{org}/{name}`) so several
orgs can share one host without colliding. Each unit gets a hardening drop-in at
`/etc/systemd/system/<unit>.d/10-hardening.conf` with defense-in-depth that is
safe for general CI. The stricter `NoNewPrivileges` / `ProtectSystem=strict` /
`RestrictSUIDSGID` are **opt-in** (commented in the drop-in) because they break
workflows that use `sudo`/`apt`. Only root (and the control plane) can read the
App key; agents run as the unprivileged `srm` user inside a systemd sandbox.

## Configure

First-run interactive setup (recommended), once per org:

```bash
srm init        # prompts for org, App ID, installation ID, key path, defaults
srm doctor      # verify auth + connectivity for all configured orgs (or --org)
```

**Config location.** srm is a root/sudo-operated tool, so the canonical config
is **`/etc/srm/config.yaml`** (0640 root). Resolution precedence:

1. `--config <path>` (explicit override)
2. `/etc/srm/config.yaml` - used automatically if it exists, and the default
   target when running as root (so `sudo srm init` seeds `/etc/srm`)
3. `~/.config/srm/config.yaml` - per-user fallback for non-root invocations

The age secrets file (`secrets.age`) lives next to whichever config is active.
To edit by hand instead of `srm init`, see
[config.example.yaml](config.example.yaml) and the full field reference in
[docs/GITHUB_APP_SETUP.md](docs/GITHUB_APP_SETUP.md).

### Auth (GitHub App only)

`srm` authenticates as a GitHub App installation. The full walkthrough - creating
the App, the exact permissions (**Organization → Self-hosted runners: Read &
write**, plus optional **Administration** for the retention panel), installing it
per org, and finding the App ID / installation ID - is in
[docs/GITHUB_APP_SETUP.md](docs/GITHUB_APP_SETUP.md).

### Secrets

The App private key is never committed. Either reference a `.pem` via
`privateKeyPath`, or set `SRM_SECRETS_PASSPHRASE` and let `srm init` encrypt it
into `secrets.age` next to the config (e.g. `/etc/srm/secrets.age`, age, 0600).
Short-lived registration/remove tokens (~1 h) are minted on demand and never
stored.

## Usage

```bash
srm                       # launch the TUI (bare invocation)
srm init                  # interactively configure an org
srm --org acme            # TUI scoped to a specific org

# Management plane (run anywhere with the App key):
srm runners list              # list runners across ALL configured orgs (ORG column)
srm runners list --org acme   # filter to one org
srm runners delete 42 43      # deregister runners by id (add --dry-run to preview)
srm groups list               # list runner groups
srm groups create srm-ci      # create a runner group
srm doctor                    # check auth, connectivity, and retention-policy access

# On-host orchestration (run as root ON the target Ubuntu host):
sudo srm provision --node --corepack       # bake the job toolchain (apt/Node/pnpm)
sudo srm runners create --org acme \
     --count 2 --name-prefix srm-ci --labels srm-ci --group srm-ci   # persistent
sudo srm runners destroy srm-ci-2 --org acme

# Ephemeral (JIT) slot lanes - mint a fresh single-use registration per job.
# JIT runners carry EXACTLY --labels (GitHub does NOT auto-add self-hosted/Linux/X64),
# so pass the full set to match `runs-on: [self-hosted, temporal]`:
sudo srm runners create --ephemeral --count 10 --org acme \
     --labels self-hosted,Linux,X64,temporal --group temporal
sudo srm runners destroy --ephemeral --slot 3 --org acme            # drain + remove a slot

sudo srm runners refresh [--org acme]      # re-apply drop-ins (cache/caps/isolation); migrate in place
sudo srm cache prune                       # evict stale build-tool cache entries
sudo srm reconcile [--fix] [--org acme]    # audit host vs GitHub drift + health + OOM; --fix repairs host-side
sudo srm reconcile --reap-ephemeral --org acme   # deregister offline JIT ghosts (gated)
```

`srm reconcile` is the deep host/fleet check: it classifies each runner
(healthy / stale drop-in / stuck / orphan unit / legacy layout / GitHub-only),
reports host disk + cache sizes and per-runner memory vs cap, and with `--fix`
repairs **host-side** drift (refresh stale, restart stuck, remove orphan units) -
busy runners are skipped and GitHub-side entries are never deleted.

### Host provisioning (the job toolchain)

Self-hosted runners ship **bare** - no Node, Python, build tools - so a job that
"just works" on `ubuntu-latest` fails with exit 127 until the host is
provisioned. `srm provision` applies a host-once dependency layer:

```bash
sudo srm provision --apt build-essential,jq,unzip   # apt packages
sudo srm provision --node --corepack                # Node (NodeSource) + pnpm/yarn shims
sudo srm provision --seed-node 22.11.0,20.18.1       # pre-bake versions into the shared tool cache
```

It also applies the config `host:` manifest (`aptPackages`, `setupScripts`,
`persistentCachePaths`, `toolCacheSeeds`). Provision **before** creating runners
so the toolchain is on PATH when they register. `srm doctor` reports the host
toolchain and any manifest drift.

**Per-project language versions.** The system Node is the default/bootstrap;
each workflow still picks its version with `actions/setup-node` (etc.). srm
points every runner at a **shared host tool cache** (`AGENT_TOOLSDIRECTORY` →
`/opt/hostedtoolcache`), so a version one runner downloads is reused by all -
and `srm provision --seed-node <versions>` (or `host.toolCacheSeeds`) pre-bakes
exact versions so the first use is instant and offline.

### The TUI

A tabbed, multi-org cockpit. Persistent and ephemeral runners have fully
different natures, so they live in **separate, never-mistakable tabs** (distinct
columns, create/destroy flows, and addressing - runner **name** vs **slot id**):

- **Tabs** (`tab`/`shift+tab`): **Persistent** (cross-org runner table with
  ORG/MACHINE columns + a color-coded detail line), **Ephemeral** (host-local
  slot lanes judged by host health - state / restarts / conformance / memory +
  an OOM badge), **Groups**, **Health** (per-org auth + retention cards), and
  **Settings** (the capacity policy).
- `o` cycles the org filter; `r` refresh; `/` incremental **filter** (Persistent);
  `n` opens the **create wizard** (persistent runners *or* ephemeral slots) with a
  live progress bar; `d` destroys the selection (a persistent runner - host
  teardown if local, else deregister - or an ephemeral slot by id) behind a
  confirm modal; on **Settings**, `e` opens the **capacity editor** (mode,
  per-runner caps, slice ceiling → writes `config.yaml`, validated).
- `?` toggles full help; `↑/↓` move; `q` quits.

## License

Licensed under the **Apache License, Version 2.0** - see [LICENSE](LICENSE) and
[NOTICE](NOTICE). You may use, modify, and redistribute this software, including
commercially, provided you retain the copyright/license notices and the contents
of `NOTICE`.

Copyright © 2026 Paulo Sánchez ([@erlete](https://github.com/erlete)).
