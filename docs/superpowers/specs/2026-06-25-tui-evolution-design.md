# srm TUI Evolution (v2.1.0) - Design & Build Spec

Status: APPROVED for autonomous build. Branch: `feat/v2.1.0` (off `stable` @ v2.0.1).
Mandate (user, 2026-06-25): build all of this fully autonomously, my own decisions, on this branch; give a feature tour when done. Decisions below are LOCKED (I chose the recommended defaults; user delegated). No em/en-dashes anywhere (user is firm).

This spec is self-contained: it is the implementation blueprint to build from cold. It is grounded in an exhaustive capability map of the current TUI (Bubble Tea v2, `internal/tui`) and service layer (`internal/service`).

---

## 0. Goal

Turn the srm TUI from a read-only 5-tab viewer into a live, operable fleet **cockpit + control plane**: surface the data the service already computes but hides, and wire the CLI-only operations into the TUI - safely. Four directions, all in scope, built as phases that share a foundation.

## 1. Locked decisions

1. **Data plane tiering.** Two-tier snapshot. FAST tier = GitHub list + manifest versions (`LocalRunnerVersions`) + `AgentVersionStatus`; always loaded, works on a remote/non-root admin box. SLOW host tier = `Reconcile(fix=false)` + inspection (drift class, live/peak/max mem, OOM, disk, cache, slice); loaded lazily as a follow-up command, cached with a staleness stamp, and **degrades gracefully** to "host data unavailable" off-host/non-root. Never block `Update`; both tiers are `tea.Cmd`.
2. **Auto-refresh.** Default OFF. Opt-in global key `a`. Per-tab intervals (cheap ephemeral/list faster; reconcile-backed slower). A tick NEVER loads while a modal/form/op/filter is active or a load is in flight (single guard predicate; ticker always re-arms, loads only when idle). Not persisted to config initially. Footer shows `auto Ns` + `updated Ns ago` (ambers when stale).
3. **Tabs.** Add **Drift** (6th) and **Lifecycle** (7th) tabs. Plus inline drift badges on Persistent/Ephemeral and a cross-tab "newer binary" banner. (Not crammed into existing screens.)
4. **Detail pane.** Bottom split (table stays visible) toggled with `enter`; an expand key for full height; collapses to the existing one-line detail below a height threshold. Render-only off the loaded snapshot (no service call on open).
5. **Safety ladder.** `confirm` (routine: refresh, group create/assign, retention) -> `dry-run preview then apply` (host-wide: provision, prune, reconcile-fix) -> `typed-confirm` (fleet-wide upgrade-all; uninstall hostname; purge double-token).
6. **Upgrade streaming.** Extend `UpgradeLocalRunners` with an optional `chan<- UpgradeResult` progress channel so the op panel updates per-runner live (mirrors `CreateRunners`' channel). Small service change; the flagship UX.
7. **Net-new group + repo management.** Build the missing GitHub client/service methods (see section 7). The TUI gets full runner-group CRUD + repo assignment with autocomplete multi-select (explicit user requirement).
8. **Host-op gating.** Detect non-root / non-local (`RunnerIsLocal` heuristics + a root/elevation probe) and PRE-DISABLE host-mutating ops with a visible reason, rather than letting them fail deep in the service.
9. **`--purge` in TUI.** Allowed but TRIPLE-gated: typed hostname match + typed `PURGE` token + a writable backup path (the config is backed up first, so keys are recoverable from the tarball). Uninstall is a multi-step wizard with a dry-run blast-radius preview.
10. **Dry-run mechanism.** Thread a per-call `dryRun bool` through the few service signatures used for previews (avoid toggling the global `config.DryRun`, which is a state-leak trap). Where a signature change is too invasive for v1, save/restore the global flag around the preview pass and document it.

## 2. Architecture & shared foundation (PHASE 1 core)

Current model: `internal/tui/root.go` `Model` holds a view struct per tab (each wraps a `bubbles` table + `filterState`). All GitHub/host work is async `tea.Cmd` -> typed `*Msg` (`messages.go`) handled in `Update`. Singleton modal/form (exactly one open). Destructive ops gate through a yes/no confirm (`confirm.go`, defaults No). Forms via `huh`. `CreateRunners` streams `core.ProgressEvent` over a channel the Model polls (`startCreate`/`waitCreateCmd`/`createDoneMsg`) - the template for any long op.

Build these reusable primitives (generalize, do not duplicate, the create flow):

### 2.1 SnapshotStore (single source of truth)
A struct on the Model holding the fused fleet state, replacing each tab's independent load:
- `runners []FusedRunner` where `FusedRunner = { RunnerWithOrg + RunnerRecord(version lineage, ids, timestamps) + RunnerState(drift class/detail, mem, OOM) + behind-current flag }`.
- `ephemeral []EphemeralSlot` (already has MemCur/OOMKills).
- `host { SliceCurrent, SliceMax, Disks[], Caches[] }` from `ReconcileReport`.
- `loadedAt time.Time`, `partialErrs map[string]error`, `hostTierAvailable bool`.
Every view derives its rows from this store. Fast tier populates versions; slow tier fills drift/mem/host. Keyed lookups by `(org,name)` / `(org,slot)`.

### 2.2 Detail pane (drill-down overlay)
A reusable bottom-split panel opened with `enter` on a selected row. Render-only: the active view implements `detailSections(row) []detailSection`; the pane renders them. Sections: identity, version lineage (`AgentVersion`/`PreviousVersion`/`TemplateVersion`/published+behind), drift (class badge + `Detail` text), memory meter (peak/live/cap + OOM), timestamps, and a footer of valid op keys for that subject. Up/down still scroll the table so the pane re-fills as selection moves; `esc`/`enter` closes.

### 2.3 Operation runner (generalized op pipeline)
Generalize `startCreate`/`waitCreateCmd` into `startOp(opSpec)`:
```
opSpec { Title, Noun string; Confirm bool; DryRunFirst bool; TypedConfirm string (""=none);
         Run func(ctx, *Manager, chan<- core.ProgressEvent) (items []opItem, err error) }
opItem { Label string; Outcome (ok|skip|rollback|fail); Detail string }
```
- Ops that already stream (create) emit events directly. Ops that return slices (`UpgradeLocalRunners`, `RefreshLocalUnits`, `Reconcile`) are wrapped by an **event-synthesis adapter** that emits one synthetic `ProgressEvent` per item -> the result panel never knows the difference. (Upgrade gets a real channel per decision #6 for true live streaming.)
- Safety: `Confirm` routes through the existing yes/no modal; `DryRunFirst` runs the op's report-only mode and shows a PREVIEW (the plan) with Apply/Cancel before the real pass; `TypedConfirm` opens a small text-input modal requiring an exact token.
- View: the **Operation result panel** replaces the create-only progress box - live spinner+bar+current-item while running, then a scrollable per-item outcome list (glyph + label + From->To/outcome) + aggregate tally + "enter/esc to dismiss" (dismiss reloads the active tab). Singleton invariant: confirm/preview closes BEFORE the op panel opens, exactly like create today.

### 2.4 Auto-refresh ticker
A `tea.Tick`-driven `autoTickMsg`; on each tick, if the idle-guard passes, fire the snapshot load and re-arm; else re-arm without loading. Toggle key `a`. Footer status segment per decision #2.

### 2.5 Multi-select substrate (BACKLOG #1)
A selection set per list view (Persistent/Ephemeral). `space` toggles the cursor row; `A` selects all visible (post-filter); `esc` (when not filtering, selection non-empty) clears it first. A leftmost `[x]/[ ]` gutter column; header chip "selected: N". Bulk ops act on the set in one streamed op pass. Selection survives filter/scroll; cleared on op completion.

### 2.6 Typed-confirm modal
A new small text-input modal mode (the current confirm is yes/no only) for typed-confirm ops. Gated in the same singleton precedence.

### 2.7 Host/elevation probe
A cached `hostCapable bool` (am I local + root) surfaced to views so host-mutating ops pre-disable with a reason when false.

---

## 3. PHASE 1 - Observability cockpit (mostly Direction A)

Read-only enrichment; low risk; builds the data plane + detail pane.

**Persistent table** gains columns: `VERSION` (from `RunnerRecord.AgentVersion`; `?` only when truly unrecorded; `down-arrow` + warn color when behind published), `DRIFT` (one-glyph class badge), `OS` (`core.Runner.OS`), `GROUP` (`core.Runner.GroupID`, `-` if 0). JOB shows a live in-flight marker when busy. Header tally adds `drift N` + `behind N`.

**Ephemeral table** surfaces the already-collected-but-hidden `MemCur` (LIVE column) and `OOMKills` (OOM column, red when >0); MEM reframed peak/live/cap; crash-loop rows show restart count inline; header adds OOM sum.

**Capacity strip** (pinned banner above runner tables): `slice 12.4G/16.0G [bar] 77%` from `SliceCurrent/SliceMax`, colored by band (green<70 / amber70-90 / red>90); hidden when no auto-cap slice; trailing `sum-peak across N`. **Host strip**: disk bars + cache sizes from `Disks`/`Caches`.

**Detail pane** (2.2) wired for runner + lane.

**Drift-only focus** key `d`: narrow to non-healthy rows (reuses filterState). Filter haystack extended to match version + drift text. Optional `y` copy-diagnostics (paste-ready block) and `g` jump-to-first-drifted.

Data sources: `Manager.Reconcile(fix=false)` (host tier) + `LocalRunnerVersions` + `AgentVersionStatus`. Degrade: off-host -> GitHub-derived columns only, host telemetry marked unavailable, strips hidden. Map `-1` (unknown mem/OOM) to `-` everywhere.

---

## 4. PHASE 2 - Drift & repair command center (Direction C)

**Drift tab (6th).** Class-summary chip strip (count per non-empty class, ordered like `renderReconcile`); DRIFT table (CLASS/ORG/NAME/DETAIL/FIX-PLAN/RESULT) of non-healthy rows; selected-row detail line; HOST HEALTH footer (disks, caches, slice now/max). Provenance line ("audit only" vs "--fix applied") + audit age.

**Newer-binary banner.** Cross-tab persistent alert when any row is `ClassDropInNewer`/`ClassEphemeralNewer`: "NEWER TEMPLATE ON DISK - update the srm binary; N skipped by repair". Purple/amber (not error-red). Dismiss `x` for the session; re-armed on next audit.

**Operations:** `r`/`a` run read-only audit (`Reconcile(false,false,org)` -> `driftMsg`). `f` = repair: dry-run-preview (plan from cached report: refresh stale / restart stuck / remove orphan unit; "N busy SKIPPED"; "GitHub never deleted"; "newer rows skipped") -> confirm -> `Reconcile(true,false,org)` via op-runner. `g` = reap ghosts: a STERNER second confirm (only GitHub-delete path) -> `Reconcile(false,true,org)`. `enter` on a drift row jumps to that runner on Persistent/Ephemeral.

**Inline drift badges** on Persistent/Ephemeral read from the cached `ReconcileReport` (keyed by org+name/slot); degrade to "unaudited" (never assert healthy) when no recent report. A runner flagged "newer" cannot be silently force-refreshed.

Risks: Reconcile is root-only + heavy -> spinner, guard against stacked loads, graceful non-root render. Org-scoped `f` is org-wide (not single-runner) - modal copy must say so.

---

## 5. PHASE 3 - Operational control plane (Direction B + BACKLOG)

All via the op-runner (2.3) + safety ladder (#5). Host-op gating (#8) pre-disables when not local-root.

- **Refresh units** `R` (shift+r): `RefreshLocalUnits(orgScope)` (re-apply drop-in + restart, skip busy). Confirm. Selection-scoped or all.
- **Upgrade agent** `u`: `UpgradeLocalRunners` (serial, self-test, rollback). Options form (optional `--to-version` pin, `--force`). Typed-confirm for host-wide (no selection); confirm for a bounded selection; ALWAYS offer dry-run preview first. Live per-runner reel via the new progress channel (#6); amber "rolled back to PREV (still serving)" on self-test failure.
- **Rollback** `b`: `UpgradeLocalRunners(Rollback:true)`; rows with no recorded `PreviousVersion` shown disabled.
- **Prune dep-cache** `p`: age form (default `CacheRetentionDays|30`) -> dry-run preview ("would prune X files / Y GiB") -> apply (`PruneDepCache`).
- **Provision host** `P` (shift+p): manifest form (apt/node/corepack/seed toggles) -> `HostDrift` preview ("missing, will install: [...]") -> `ProvisionHost`. Indeterminate spinner (one call).
- **Recreate runner** (BACKLOG #2) `c`: destroy + recreate with the SAME config (labels/group; for ephemeral, the slot's jit-params). Confirm; useful for unhealthy/outdated runners. Implement as a compound op (DestroyRunner/Slot then CreateRunners/CreateEphemeralRunners with the captured spec).
- **Bulk ops** (BACKLOG #1): multi-select (2.5) + any op key applies to the set in one streamed pass, grouped by org in the reel. Multi-org selection allowed for refresh/upgrade (iterate per-org), refused for group-assign (group ids are org-local).
- **Retention edit** `e` on Health: `SetRetention(org, days)` form (prefilled, max-bound). Confirm.

---

## 6. PHASE 3b - Runner GROUPS management (EXPLICIT user requirement)

"Create real runner groups and edit/delete them, assigning repos with autocomplete multiple selection, mimicking GitHub web." First-class feature on the **Groups tab**.

**Operations on Groups tab:**
- `n` create group: form (name + visibility all|selected|private). If `selected`, chain into the repo picker.
- `e` edit group: rename + change visibility + edit repo assignment (the picker) for the selected group.
- `d` delete group: confirm (warn that runners in it fall back / become unusable per GitHub semantics). Refuse deleting the `Default` group (id 1) - GitHub forbids it.
- `enter` detail pane: group id, name, visibility, default flag, allows-public, current selected-repo count + list.

**Repo picker (autocomplete multi-select) - the centerpiece, mimics GitHub web "Repository access":**
- A custom Bubble Tea component (huh has no good autocomplete-multiselect): a `textinput` search box + a filtered candidate list (from the org's repos, fetched/paginated, debounced search) + a selected-set chip area. `space`/`enter` toggles a candidate; selected repos render as removable chips; typing filters candidates live.
- Backed by: `ListOrgRepositories(org, query)` for candidates (search/paginate) and the group's current `ListRunnerGroupRepositories(org, groupID)` to pre-populate selection. On save, diff selected vs current -> `SetRunnerGroupRepositories(org, groupID, repoIDs)` (or add/remove deltas).
- Visibility transitions: switching to `selected` enables the picker; to `all`/`private` clears/ignores it (GitHub semantics).

This needs net-new client/service methods (section 7). Honor `config.DryRun` (synthetic results, no mutation).

---

## 7. Net-new service/GitHub-client methods to build

In `internal/ghub` (client) + `internal/service` (Manager wrappers) + `internal/core` (types):
- `UpgradeLocalRunners(... , progress chan<- UpgradeResult)` - add optional channel for live streaming (decision #6).
- `SetRunnerGroupForRunner(org, runnerID, groupID)` - move a runner to a group (`PUT` the runner's group; GitHub: there is no single "move" endpoint - implement via the runner-group `PUT /orgs/{org}/actions/runner-groups/{gid}/runners/{rid}` add + remove-from-old, or recreate JIT with new group for ephemeral; investigate the exact supported endpoint at build time).
- `UpdateRunnerGroup(org, id, name, visibility)` - `PATCH /orgs/{org}/actions/runner-groups/{id}`.
- `DeleteRunnerGroup(org, id)` - `DELETE /orgs/{org}/actions/runner-groups/{id}`.
- `ListRunnerGroupRepositories(org, groupID)` - `GET .../runner-groups/{id}/repositories` (paginated).
- `SetRunnerGroupRepositories(org, groupID, repoIDs[])` - `PUT .../runner-groups/{id}/repositories` (replace set) and/or per-repo `PUT`/`DELETE .../repositories/{repo_id}`.
- `ListOrgRepositories(org, query)` - `GET /orgs/{org}/repos` (paginated) for autocomplete; consider the search API for query.
- Per-call `dryRun bool` threading for preview ops (decision #10) where feasible.
All client methods: respect existing auth (GitHub App per org), pagination, rate limits, and the `config.DryRun` guard.

---

## 8. PHASE 4 - Lifecycle & setup (Direction D)

**Lifecycle tab (7th)** = a card menu (operations, not entities):
- **Onboard** `n`: reuse the canonical `orgForm` from `internal/cli/init.go` (lift to a shared package so TUI and `srm init` validate identically) -> write config -> async `validateOrgAuth` -> green/red result + retry/keep. Optional "encrypt key into secrets.age" confirm when `SRM_SECRETS_PASSPHRASE` set.
- **Backup** `b`: `BackupConfigDir` -> `srm-backup-<ts>.tar.gz` (confirm only if overwriting).
- **Restore** `t`: pick a discoverable archive -> confirm modal listing colliding files -> `RestoreConfigDir` -> reload Manager (rebuild in-process via a `reloadMsg`) so all tabs see new config.
- **Provision** `p`: as Phase 3 provision.
- **Uninstall** `X` (shift): guarded wizard. Step 1 scope+toggles (org / KeepConfig / KeepBinary / KeepGitHub / Purge / Force). Step 2 dry-run `UninstallReport` rendered as a color-coded BLAST RADIUS (removed red; skipped/foreign yellow; "config backed up first"). Step 3 typed-confirm: hostname exact match; Purge requires a second `PURGE` token + writable backup. Execute -> result (removed counts, BackupPath, non-fatal errors) -> offer quit.

**Health graduates to full doctor:** per-org cards add limits-mode line + agent freshness (`AgentVersionStatus`) + slice cap; host-wide section adds toolchain probes (`exec.LookPath` node/pnpm/npm/git/docker/make), manifest drift (`HostDrift`), and rootless-DinD readiness (Linux, when `docker.rootlessDinD`): binaries + newuidmap setuid + /dev/fuse + userns + per-org subuid/subgid count (mirror `printDinDReadiness`). Each red/missing line carries its remediation key (e.g. "press p to provision"). Label the host section "this host" (a remote-mgmt box describes the wrong machine).

---

## 9. Cross-cutting

- **Safety ladder** per #5; **host-op gating** per #8; **degradation off-host** everywhere host data is involved (render GitHub-derived data, mark host telemetry unavailable, pre-disable host ops).
- **Singleton modal/form invariant** preserved throughout; the op-runner, confirm, preview, and typed-confirm are mutually exclusive and never chained concurrently (sequenced, not stacked).
- **Keymap** additions registered in `keys.go` so they appear in help automatically. Keys: `enter` detail, `a` auto-refresh, `d` drift-focus, `space`/`A` select, `R` refresh-unit, `u` upgrade, `b` rollback, `p` prune, `P` provision, `c` recreate, `f` fix, `g` reap/group(context), `e` edit, `x` dismiss-banner, `X` uninstall, `o` org-cycle (existing) / op-launcher menu. Resolve the `g`/`o` overloads per-tab; consider an `o` "actions" menu for discoverability if the keymap gets crowded.
- **No em/en-dashes** in any UI string, code comment, doc, or commit (user firm).

## 10. Build sequence (autonomous order)

1. Foundation: SnapshotStore + two-tier load + detail pane + op-runner (generalize create) + typed-confirm modal + host probe + auto-refresh. Keep `GOOS=linux/windows go build ./...` + existing tests green throughout; the create flow is the conformance baseline.
2. Phase 1 columns/strips/detail wiring (read-only).
3. Phase 2 Drift tab + banner + fix/reap + inline badges.
4. Net-new client/service methods (section 7) + tests.
5. Phase 3 ops (refresh/upgrade/rollback/prune/provision/recreate) + multi-select bulk.
6. Phase 3b Groups CRUD + repo-picker autocomplete component.
7. Phase 4 Lifecycle tab + doctor-Health.
8. Cross-cutting polish, help text, host-op gating, degradation, golden/unit tests for new renderers and the op-runner.

Each phase: build -> `go vet ./...` + `go test ./...` (native) + `GOOS=linux` and `GOOS=windows` `go build ./...` -> commit. Windows: the TUI is cross-platform; gate any Linux-only host data behind the existing OS seams (`doctor_linux.go`/`_windows.go`, `screen_settings_{linux,windows}.go`). Most host telemetry (reconcile/inspect/cgroup) is Linux; the Windows build must compile and degrade.

## 11. Testing

- Reuse the golden-render tests (`render_test.go`, runner dropin/ephemeral golden) - any systemd/template rendering must stay byte-identical unless intentionally changed.
- Add unit tests: SnapshotStore fusion + degradation, op-runner outcome mapping + event synthesis, drift-class -> badge mapping, repo-picker filter/select logic, typed-confirm gating, the new client methods (table-driven against recorded fixtures).
- Manual/live validation deferred; the prod host (ARMORA-DEV-CENTRAL, v2.0.1, DinD on) is available for end-to-end TUI smoke if needed (see srm-live-host-testing memory), but DESTRUCTIVE TUI ops (uninstall/purge) must NEVER be run against prod during testing.

## 12. Known risks / notes

- Reconcile root-only + heavy (folding into snapshot must be the lazy slow tier).
- `UpgradeLocalRunners` per-runner streaming needs the new channel; without it only a spinner is truthful.
- `config.DryRun` global flag is a state-leak trap; prefer per-call dryRun.
- Assign-to-group / group repo endpoints need verification against the live GitHub API at build time (exact endpoints/shapes).
- huh has no autocomplete-multiselect; the repo picker is a custom component.
- Restore must reload the in-memory Manager or every tab shows stale config.
- The lane LABEL gap from prod (lanes are `temporal`-label-only) is orthogonal to this work but worth a Health/doctor note: label-based `runs-on: self-hosted` workflows do not match group-only lanes.

---

When the build is done: give the user a guided TOUR of the implemented features (not a code dump) - what each new tab/op does and how to drive it.
