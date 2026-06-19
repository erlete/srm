# Ephemeral (JIT) runners

`srm` runs runners in two distinct shapes. They coexist freely in the same org and
group; a job is routed by **labels**, not by runner type.

| | Persistent | Ephemeral (JIT) |
|---|---|---|
| Registration | once (`config.sh --token`), reused forever | **fresh per job**, single-use, auto-deregisters |
| Unit | `actions.runner.<org>.<name>.service` | `actions.ephemeral.<org>.<slot>.service` |
| Addressed by | runner **name** | **slot** id (a numbered lane) |
| State between jobs | accumulates | **clean slate** (`_work`/`_diag`/tmp wiped) |
| Creds on disk | persist | only during a job, wiped between cycles |

## The allocation model (three layers)

1. **Slot (lane)** — created once per slot: a systemd unit + a warm extracted agent
   tree under `{installRoot}/<org>/.ephemeral/<slot>`. `srm runners create
   --ephemeral --count N` makes N numbered lanes (1..N).
2. **JIT registration** — minted **fresh for every job** (`GenerateOrgJITConfig`),
   used once, then the runner auto-deregisters. A slot that runs 100 jobs mints 100
   throwaway registrations named `srm-eph-<org>-<slot>-<nonce>`.
3. **Org** — shares the GitHub App key used to mint.

So the lane is stable; the identity flowing through it is disposable.

## The cycle

Each slot's unit runs `srm _runner-cycle --org X --slot N` as **root** on a loop
(`Restart=always`). One cycle:

1. Reap a ghost from a prior crashed cycle (`.jit-id`), if any.
2. Mint a JIT config (reads the App key in `/etc/srm` — root only).
3. Record the runner id (`fsync`'d) **before** the job, so a crash leaves a
   reapable ghost.
4. Reset the workspace (`_work`/`_diag`/`.runner`/`.credentials*`), keep the warm
   binaries.
5. **Drop root → `srm-<org>` via `setpriv`** (PAM-free), assert `euid != 0` first,
   write a `.ran-as` attestation, then run `run.sh --jitconfig <blob>` for exactly
   one job.
6. After the job, if the runner is still registered (it should auto-deregister),
   reap it — reaping is by **registration state**, not run.sh's exit code (which is
   unreliable).

The unit has **no `User=`** — it must start as root to mint, then drops privileges
inside the cycle. Hardening is stricter than the persistent drop-in:
`NoNewPrivileges=true`, `ProtectProc=invisible`, `LimitCORE=0`.

> The JIT blob is passed as the `--jitconfig` argument: runner 2.335.1's `run.sh`
> does not read it from stdin. It is therefore visible in the cycle's
> `/proc/<pid>/cmdline`; this is acceptable because it is single-use and
> short-lived, `ProtectProc=invisible` hides it from other per-org users, and
> per-org isolation separates uids. (A global `hidepid` / `ProtectProc` on the
> persistent units would close the residual same-host cross-unit window.)

## Commands

```sh
# Create N ephemeral slot lanes (run as root on the host).
srm runners create --ephemeral --count 6 --org Acme --labels temporal --group temporal

# Tear one slot down (drains the in-flight job, deregisters, removes the lane).
srm runners destroy --ephemeral --slot 3 --org Acme

# Audit. Ephemeral lanes are a separate family judged by HOST HEALTH; reconcile
# never joins them to the GitHub list (the registration name changes every job).
srm reconcile --org Acme

# Deregister offline ephemeral ghosts (the one gated exception to "reconcile never
# deletes on GitHub"). Honors --dry-run.
srm reconcile --reap-ephemeral --org Acme
```

## TUI

Bare `srm` opens the cockpit. Because the two natures must never be confused, they
live in **separate tabs**, each with its own columns and actions:

- **Persistent** — the cross-org/cross-host runner inventory. `n` provisions named
  runners, `d` destroys (local) or deregisters (remote) by name, `/` filters.
  Ephemeral JIT registrations are excluded here — they belong to their own tab.
- **Ephemeral** — the host-local slot lanes (ORG/SLOT/STATE/RESTARTS/CONFORM/MEM),
  judged by host health alone. `n` adds slots (scale up), `d` drains + destroys a
  slot by id. Never addressed by runner name.
- **Settings** — the capacity policy (Rule 2: tune caps from the TUI, not env).
  `e` opens the editor: mode (auto/manual), per-runner caps, and the aggregate
  `srm.slice` ceiling. Saving writes `config.yaml` (validated, so a saved config is
  never one srm would refuse to load). Changes apply to **new** runners; run
  `srm runners refresh` to push them onto already-installed units.

## Capacity (auto, machine-relative)

Ephemeral slots inherit the same cgroup caps as persistent runners. With
`resourceMode: auto`, each runner is capped by machine-relative percentages and all
runners share an `srm.slice` aggregate ceiling that **auto-scales with the host** —
see `config.example.yaml`. `MemorySwapMax=0` keeps a hungry job from swap-thrashing
the host. The slice ceiling defaults to 75% of RAM and is tunable via
`sliceMemoryMax` (or the TUI Settings panel).

## Honest effects

**Buys:** clean slate per job (fresh process + `PrivateTmp` + wiped `_work`, without
re-downloading the agent or cold caches), zero credentials at rest between jobs,
drift-free churn (no offline-but-registered zombies — they auto-deregister),
per-job + aggregate memory caps.

**Does NOT:** lower idle RAM (an idle JIT slot holds the same listener as a
persistent runner — the OOM lever is fewer concurrent jobs + the caps), deliver
scale-to-zero (`Restart=always` keeps the lane warm; demand-based autoscale needs a
webhook and is out of scope), or remove per-job latency (a mint + reset + cold
`run.sh` start adds a few seconds vs a parked persistent runner). The clean-slate
guarantee is scoped to **cross-org** + fresh `_work`/tmp/registration; the agent
tree, per-org `$HOME`, and caches stay warm.
