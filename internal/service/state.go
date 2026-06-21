package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StatePath is the host manifest srm writes to record what it has installed:
// each runner's agent version, the GitHub id it registered under, and the
// template generation of its unit. It lives under /var/lib/srm (root 0600),
// alongside the ephemeral control dir.
//
// The manifest is ADVISORY, never authoritative. The live GitHub runner list and
// the on-disk systemd units are the source of truth; this file only annotates
// them with facts that cannot be read back from a running runner (notably the
// installed agent version, which the actions/runner REST API does not expose). A
// command MUST treat an absent file, a missing entry, or a stale entry as
// "unknown" and fall back to acting on live state - never skip or mutate a real
// runner because the manifest disagrees. An absent file is therefore a complete
// no-op for every command, so hosts created by srm <= v1.3.0 are unaffected.
const StatePath = "/var/lib/srm/state.json"

// CurrentStateVersion is the manifest schema generation. Bump it (and migrate on
// load) if the on-disk shape changes incompatibly. A manifest from a newer srm is
// adopted as-is rather than rejected: the manifest is advisory, so the worst case
// of an unknown field is that a fact is ignored, never that a real runner is lost.
const CurrentStateVersion = 1

// Runner kinds tracked in the manifest. Persistent runners and ephemeral slots
// occupy distinct systemd-unit namespaces but their names/slot-ids could collide
// (a persistent runner "1" and an ephemeral slot "1"), so the kind is part of the
// entry key.
const (
	KindPersistent = "persistent"
	KindEphemeral  = "ephemeral"
)

// RunnerRecord is one manifest entry: what srm installed for a single runner or
// ephemeral slot. All fields beyond the {Kind,Org,Name} key are best-effort
// annotations - any of them may be empty/zero for an entry srm wrote before it
// tracked that fact, which callers must tolerate (treat as "unknown").
type RunnerRecord struct {
	Kind string `json:"kind"` // KindPersistent | KindEphemeral
	Org  string `json:"org"`
	Name string `json:"name"` // runner name, or slot id when Kind is KindEphemeral

	// GitHubRunnerID is the registration id the agent recorded locally at create
	// time (persistent only; ephemeral lanes mint a fresh JIT id per cycle so this
	// stays 0). It is the host-bound identity the upgrade path uses to refuse
	// reclaiming another host's same-named registration. 0 = unknown.
	GitHubRunnerID int64 `json:"githubRunnerId,omitempty"`

	// AgentVersion is the actions/runner agent version on disk (e.g. "2.335.1").
	// Empty = unknown (an older srm installed it, or it was created out-of-band).
	AgentVersion string `json:"agentVersion,omitempty"`
	// PreviousVersion is the agent version this runner ran BEFORE its last upgrade,
	// retained so a failed self-test can roll back to the cached prior tarball.
	PreviousVersion string `json:"previousVersion,omitempty"`
	// TemplateVersion is the systemd-template generation of the installed unit (the
	// Slice A drop-in/ephemeral marker version). 0 = unknown.
	TemplateVersion int `json:"templateVersion,omitempty"`

	// ConfiguredAt / LastUpgradeAt are RFC3339 timestamps stamped by the caller
	// (this package never reads the clock, so the manifest logic stays
	// deterministic). Best-effort; empty when unknown.
	ConfiguredAt  string `json:"configuredAt,omitempty"`
	LastUpgradeAt string `json:"lastUpgradeAt,omitempty"`
}

// StateManifest is the whole on-disk manifest.
type StateManifest struct {
	StateVersion int            `json:"stateVersion"`
	Runners      []RunnerRecord `json:"runners"`
}

// matches reports whether an entry has the given {kind,org,name} key.
func (rs RunnerRecord) matches(kind, org, name string) bool {
	return rs.Kind == kind && rs.Org == org && rs.Name == name
}

// LoadState reads the manifest at path. A missing file is NOT an error: it
// returns an empty (stamped) manifest, so every caller transparently degrades to
// "nothing recorded" on a host that predates the manifest. A present-but-corrupt
// file IS an error - silently discarding it would erase host-bound ids and
// version history that the upgrade path's safety gates rely on.
func LoadState(path string) (*StateManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &StateManifest{StateVersion: CurrentStateVersion}, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var m StateManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse state %s: %w (move it aside to recover; srm will re-record on next create/upgrade)", path, err)
	}
	if m.StateVersion == 0 {
		m.StateVersion = CurrentStateVersion
	}
	return &m, nil
}

// Save atomically writes the manifest to path (root-only 0600). It self-bootstraps
// the parent dir and writes via a temp file + fsync + rename, mirroring RecordJIT,
// so a crash mid-write never leaves a torn manifest that a later load would reject.
func (m *StateManifest) Save(path string) error {
	if m.StateVersion == 0 {
		m.StateVersion = CurrentStateVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns the entry for a {kind,org,name} key, or ok=false if unrecorded.
func (m *StateManifest) Get(kind, org, name string) (RunnerRecord, bool) {
	for _, rs := range m.Runners {
		if rs.matches(kind, org, name) {
			return rs, true
		}
	}
	return RunnerRecord{}, false
}

// Put upserts an entry by its {Kind,Org,Name} key (replacing any prior entry with
// the same key). The manifest order is otherwise preserved.
func (m *StateManifest) Put(rs RunnerRecord) {
	for i := range m.Runners {
		if m.Runners[i].matches(rs.Kind, rs.Org, rs.Name) {
			m.Runners[i] = rs
			return
		}
	}
	m.Runners = append(m.Runners, rs)
}

// Remove deletes the entry for a key (no-op if absent).
func (m *StateManifest) Remove(kind, org, name string) {
	out := m.Runners[:0]
	for _, rs := range m.Runners {
		if !rs.matches(kind, org, name) {
			out = append(out, rs)
		}
	}
	m.Runners = out
}

// nowStamp is the RFC3339 (UTC) timestamp written into manifest entries. It is the
// one place this package touches the clock, kept out of the pure load/save/mutate
// logic so that logic stays deterministic and testable.
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// mutateState loads the host manifest, applies fn, and atomically saves it. It is
// the single read-modify-write seam for state.json; create/upgrade/destroy call it
// serially (operator-invoked, never concurrent with each other), and the atomic
// rename in Save guards against a torn file regardless.
//
// Recording is ADVISORY: the manifest annotates live state, it never gates an
// operation. So every call site treats a returned error as a non-fatal warning -
// a failure to persist a version note must not fail (or roll back) the create or
// upgrade it merely describes. The version simply reads as "unknown" until the
// next successful write, which is exactly the absent-entry degradation callers
// already tolerate.
func (m *Manager) mutateState(fn func(*StateManifest)) error {
	st, err := LoadState(StatePath)
	if err != nil {
		return err
	}
	fn(st)
	return st.Save(StatePath)
}

// recordCreated stamps a freshly created runner or slot into the manifest
// (best-effort; see mutateState). It preserves an existing ConfiguredAt so a
// recreate of the same key keeps its original creation time.
func (m *Manager) recordCreated(rec RunnerRecord) error {
	return m.mutateState(func(st *StateManifest) {
		if prev, ok := st.Get(rec.Kind, rec.Org, rec.Name); ok && prev.ConfiguredAt != "" {
			rec.ConfiguredAt = prev.ConfiguredAt
		} else if rec.ConfiguredAt == "" {
			rec.ConfiguredAt = nowStamp()
		}
		st.Put(rec)
	})
}

// forget drops a runner/slot from the manifest after it is removed from the host
// (best-effort; see mutateState).
func (m *Manager) forget(kind, org, name string) error {
	return m.mutateState(func(st *StateManifest) { st.Remove(kind, org, name) })
}

// LocalRunnerVersions returns the recorded agent version of each PERSISTENT runner
// this host installed, keyed by RunnerVersionKey(org, name). It is best-effort and
// purely observational: a read failure (no manifest, or run without root) yields an
// empty map, never an error, so `runners list` simply shows blank versions instead
// of failing. Ephemeral lanes are keyed by slot, not by the churning JIT runner
// names that appear in the GitHub list, so they are intentionally not included here.
func (m *Manager) LocalRunnerVersions() map[string]string {
	out := map[string]string{}
	st, err := LoadState(StatePath)
	if err != nil {
		return out
	}
	for _, rec := range st.Runners {
		if rec.Kind == KindPersistent && rec.AgentVersion != "" {
			out[RunnerVersionKey(rec.Org, rec.Name)] = rec.AgentVersion
		}
	}
	return out
}

// RunnerVersionKey is the lookup key for LocalRunnerVersions: org and name joined
// by a NUL, which cannot occur in either, so distinct (org, name) pairs never
// collide regardless of dashes or dots in the values.
func RunnerVersionKey(org, name string) string { return org + "\x00" + name }
