package service

import (
	"context"
	"maps"
	"sort"
	"time"

	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/runner"
)

// The fleet snapshot is the cockpit's single source of truth. It fuses the three
// data sources the TUI used to read independently into one consistent view, in
// two tiers so the UI never blocks and degrades gracefully off-host:
//
//   - FAST tier (FleetFast): the live GitHub runner list + the host state
//     manifest (version lineage) + the org's currently-published agent version.
//     Always available, including from a remote/non-root admin box.
//   - SLOW/host tier (a Reconcile pass, folded in via MergeHostTier): drift class,
//     cgroup memory (live/peak/max + OOM), and host health (slice, disks, caches).
//     Root-and-on-host only; absent it, runners read as "unaudited" rather than
//     falsely healthy, and the host strips are hidden.
//
// The fusion key is (org, name) for runners and (org, slot) for ephemeral lanes.

// FusedRunner is a persistent runner fused across the live GitHub list
// (RunnerWithOrg), the host manifest (version lineage), and - once the host tier
// is merged - the reconcile verdict (drift + memory). Host-tier fields keep the
// -1 "unknown" sentinel and an empty DriftClass until MergeHostTier populates them.
type FusedRunner struct {
	RunnerWithOrg

	// GroupName is the runner group's display name (resolved via the membership
	// map; the org-runners API omits it). Empty when unresolved / default.
	GroupName string

	// Version lineage from the host state manifest (advisory; empty = unrecorded).
	AgentVersion    string
	PreviousVersion string
	TemplateVersion int
	ConfiguredAt    string
	LastUpgradeAt   string

	// Published is the org's currently-published agent version ("" = unresolved).
	// Behind is set when the recorded AgentVersion is older than Published.
	Published string
	Behind    bool

	// Host/slow tier. DriftClass is "" until the host tier merges (unaudited);
	// the memory fields stay -1 ("unknown") until then.
	DriftClass  string
	DriftDetail string
	MemCur      int64
	MemPeak     int64
	MemMax      int64
	OOMKills    int64
	HostKnown   bool // the reconcile pass classified this runner
}

// FusedSlot is an ephemeral slot lane fused with its recorded agent version and -
// once the host tier merges - its reconcile drift class. The embedded
// EphemeralSlot already carries host health (active/restarts/unit-ok/memory).
type FusedSlot struct {
	EphemeralSlot

	GroupName    string // resolved name of the slot's mint group ("" = unknown/default)
	AgentVersion string
	Published    string
	Behind       bool

	DriftClass  string
	DriftDetail string
}

// HostHealth is the box-wide health block (slice usage, disks, caches). Loaded is
// false off-host / non-root so views can show "unavailable" instead of empty bars.
type HostHealth struct {
	SliceCurrent int64
	SliceMax     int64
	Disks        []runner.DiskStat
	Caches       []CacheSize
	Loaded       bool
}

// HostHealth gathers the box-wide host stats (aggregate slice usage, disk usage,
// cache sizes) directly, without the full reconcile runner-classification pass. It
// is the data behind the Health tab's "Host information" panel - the single place
// host-wide stats live. Off-host the install paths are absent, so it returns an
// unloaded block (Loaded=false) and the panel degrades to "unavailable".
func (m *Manager) HostHealth(ctx context.Context) HostHealth {
	host := m.orchestratorFor("")
	cur, mx := host.SliceUsage(ctx)
	disks := host.HostDisk(ctx, []string{config.DefaultInstallRoot, config.DefaultCacheRoot, config.DefaultToolCacheRoot})
	return HostHealth{
		SliceCurrent: cur,
		SliceMax:     mx,
		Disks:        disks,
		Caches:       m.cacheSizes(ctx, host),
		Loaded:       mx > 0 || len(disks) > 0,
	}
}

// FleetSnapshot is the fused fleet state the cockpit renders from.
type FleetSnapshot struct {
	Runners           []FusedRunner
	Slots             []FusedSlot
	Host              HostHealth
	PartialErrs       map[string]error // per-org list failures (one bad org never sinks the view)
	HostTierAvailable bool             // a reconcile pass succeeded and was merged
	HostTierAt        time.Time        // when that reconcile (drift audit) pass ran (zero until merged)
}

// FleetFast builds the always-available tier: GitHub list + manifest versions +
// published comparison. It makes no host (reconcile/inspect) call, so it works
// from a remote admin box; ephemeral lanes are included only when this host owns
// them (off-host the slot enumeration simply returns nothing). orgFilter ""
// spans every configured org. Never returns an error: a per-org list failure is
// recorded in PartialErrs and the rest of the fleet still renders.
func (m *Manager) FleetFast(ctx context.Context, orgFilter string) FleetSnapshot {
	snap := FleetSnapshot{PartialErrs: map[string]error{}}

	var rows []RunnerWithOrg
	if orgFilter != "" {
		if rs, err := m.ListRunners(ctx, orgFilter); err != nil {
			snap.PartialErrs[orgFilter] = err
		} else {
			for _, r := range rs {
				rows = append(rows, RunnerWithOrg{Org: orgFilter, Runner: r, Local: m.RunnerIsLocal(orgFilter, r.Name)})
			}
		}
	} else {
		all, errs := m.ListAllRunners(ctx)
		rows = all
		maps.Copy(snap.PartialErrs, errs)
	}

	// Host manifest (advisory; root-only). A read failure degrades to empty - the
	// version columns simply blank out, never an error.
	recs := map[string]RunnerRecord{}
	if st, err := LoadState(StatePath); err == nil {
		for _, rec := range st.Runners {
			recs[recordKey(rec.Kind, rec.Org, rec.Name)] = rec
		}
	}

	// Published version per org, memoized (one runner-downloads call per org).
	published := map[string]string{}
	pubOf := func(org string) string {
		if v, ok := published[org]; ok {
			return v
		}
		cur, _, _, ok := m.AgentVersionStatus(ctx, org)
		if !ok {
			cur = ""
		}
		published[org] = cur
		return cur
	}

	// Runner -> group membership + group names per org, memoized (the org-runners
	// API omits both).
	type orgGroups struct {
		toGroup map[int64]int64
		names   map[int64]string
	}
	groupMaps := map[string]orgGroups{}
	groupOf := func(org string, runnerID int64) (int64, string) {
		gm, ok := groupMaps[org]
		if !ok {
			t, n := m.RunnerGroups(ctx, org)
			gm = orgGroups{toGroup: t, names: n}
			groupMaps[org] = gm
		}
		gid := gm.toGroup[runnerID]
		return gid, gm.names[gid]
	}

	for _, row := range rows {
		if IsEphemeralRunnerName(row.Runner.Name) {
			continue // churning JIT registration - belongs to the ephemeral panel
		}
		gid, gname := groupOf(row.Org, row.Runner.ID)
		if row.Runner.GroupID == 0 {
			row.Runner.GroupID = gid
		}
		fr := FusedRunner{RunnerWithOrg: row, GroupName: gname, MemCur: -1, MemPeak: -1, MemMax: -1, OOMKills: -1}
		if rec, ok := recs[recordKey(KindPersistent, row.Org, row.Runner.Name)]; ok {
			fr.AgentVersion = rec.AgentVersion
			fr.PreviousVersion = rec.PreviousVersion
			fr.TemplateVersion = rec.TemplateVersion
			fr.ConfiguredAt = rec.ConfiguredAt
			fr.LastUpgradeAt = rec.LastUpgradeAt
		}
		fr.Published = pubOf(row.Org)
		fr.Behind = isBehind(fr.AgentVersion, fr.Published)
		snap.Runners = append(snap.Runners, fr)
	}
	sort.Slice(snap.Runners, func(i, j int) bool {
		if snap.Runners[i].Org != snap.Runners[j].Org {
			return snap.Runners[i].Org < snap.Runners[j].Org
		}
		return snap.Runners[i].Runner.Name < snap.Runners[j].Runner.Name
	})

	// Ephemeral slot lanes (host-local; empty + non-fatal off-host).
	if slots, err := m.ListEphemeralSlots(ctx, orgFilter); err == nil {
		for _, s := range slots {
			fs := FusedSlot{EphemeralSlot: s}
			if rec, ok := recs[recordKey(KindEphemeral, s.Org, s.Slot)]; ok {
				fs.AgentVersion = rec.AgentVersion
			}
			if s.GroupID != 0 {
				groupOf(s.Org, -1) // ensure the org's group-name map is memoized
				if gm, ok := groupMaps[s.Org]; ok {
					fs.GroupName = gm.names[s.GroupID]
				}
			}
			fs.Published = pubOf(s.Org)
			fs.Behind = isBehind(fs.AgentVersion, fs.Published)
			snap.Slots = append(snap.Slots, fs)
		}
	}

	return snap
}

// MergeHostTier folds a reconcile pass into a fast snapshot: per-runner drift
// class + cgroup memory (matched by org+name / org+slot) and box-wide host health.
// Runners absent from the report keep their unaudited state. Pure (no I/O) so the
// fusion is unit-testable; the caller runs Reconcile off the event loop and merges
// here only on success.
func MergeHostTier(snap FleetSnapshot, rep ReconcileReport, at time.Time) FleetSnapshot {
	byKey := make(map[string]RunnerState, len(rep.Runners))
	for _, st := range rep.Runners {
		byKey[st.Org+"\x00"+st.Name] = st
	}
	for i := range snap.Runners {
		r := &snap.Runners[i]
		if st, ok := byKey[r.Org+"\x00"+r.Runner.Name]; ok {
			r.DriftClass, r.DriftDetail = st.Class, st.Detail
			r.MemCur, r.MemPeak, r.MemMax, r.OOMKills = st.MemCur, st.MemPeak, st.MemMax, st.OOMKills
			r.HostKnown = true
		}
	}
	for i := range snap.Slots {
		s := &snap.Slots[i]
		if st, ok := byKey[s.Org+"\x00"+s.Slot]; ok {
			s.DriftClass, s.DriftDetail = st.Class, st.Detail
		}
	}
	snap.Host = HostHealth{
		SliceCurrent: rep.SliceCurrent, SliceMax: rep.SliceMax,
		Disks: rep.Disks, Caches: rep.Caches, Loaded: true,
	}
	snap.HostTierAvailable = true
	snap.HostTierAt = at
	return snap
}

// NewerOnDisk counts fleet rows whose on-disk template is a NEWER generation than
// this srm binary (the authoritative-skip classes). Non-zero means the binary is
// behind and repair is skipping rows - the cross-tab "newer template" banner.
func (s FleetSnapshot) NewerOnDisk() int {
	n := 0
	for _, r := range s.Runners {
		if r.DriftClass == ClassDropInNewer {
			n++
		}
	}
	for _, sl := range s.Slots {
		if sl.DriftClass == ClassEphemeralNewer {
			n++
		}
	}
	return n
}

// DriftCount counts persistent runners + ephemeral slots in a non-healthy class.
// Returns 0 when the host tier has not loaded (everything reads as unaudited, not
// drifted).
func (s FleetSnapshot) DriftCount() int {
	n := 0
	for _, r := range s.Runners {
		if r.HostKnown && r.DriftClass != "" && r.DriftClass != ClassHealthy {
			n++
		}
	}
	for _, sl := range s.Slots {
		if sl.DriftClass != "" && sl.DriftClass != ClassEphemeralSlot {
			n++
		}
	}
	return n
}

// BehindCount counts fleet rows running an agent older than their org publishes.
func (s FleetSnapshot) BehindCount() int {
	n := 0
	for _, r := range s.Runners {
		if r.Behind {
			n++
		}
	}
	for _, sl := range s.Slots {
		if sl.Behind {
			n++
		}
	}
	return n
}

// recordKey is the manifest lookup key: kind, org, and name joined by NUL (which
// none of them can contain), so distinct entries never collide.
func recordKey(kind, org, name string) string { return kind + "\x00" + org + "\x00" + name }

// isBehind reports whether installed is an older agent version than published.
// Either side empty (unrecorded / unresolved) is "not behind" - we never assert a
// runner is stale without both facts.
func isBehind(installed, published string) bool {
	return installed != "" && published != "" && runner.CompareVersions(installed, published) < 0
}
