// Package service is the orchestration layer and the only thing the TUI and CLI
// talk to. It composes the GitHub adapter, secrets, and config into use-case
// methods, owning cross-cutting policy (bulk concurrency, dry-run, guardrails).
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/erlete/srm/internal/auth"
	"github.com/erlete/srm/internal/config"
	"github.com/erlete/srm/internal/core"
	ghub "github.com/erlete/srm/internal/github"
	"github.com/erlete/srm/internal/secrets"
)

// Manager is the application service. It lazily builds and caches one
// authenticated GitHub client per org.
type Manager struct {
	cfg     *config.Config
	cfgPath string // file the config was loaded from; where SaveConfig writes
	sec     secrets.Store

	mu      sync.Mutex
	clients map[string]ghub.Client

	// dryMu serializes withDryRun's save/restore of the shared cfg.DryRun flag so
	// two overlapping preview/apply goroutines (prune, uninstall) can't interleave
	// and leave dry-run stuck on. Kept separate from mu to avoid a deadlock when the
	// wrapped op acquires mu via client(). Reconcile threads dry-run explicitly
	// instead and never touches this.
	dryMu sync.Mutex
}

// New creates a Manager over the given config and secret store. cfgPath is the
// file the config was loaded from (empty disables SaveConfig - e.g. in tests).
func New(cfg *config.Config, sec secrets.Store, cfgPath string) *Manager {
	return &Manager{cfg: cfg, cfgPath: cfgPath, sec: sec, clients: make(map[string]ghub.Client)}
}

// Config exposes the underlying configuration (read-only intent). Snapshotted under
// the mutex so a concurrent Reload swap can't be observed torn.
func (m *Manager) Config() *config.Config { return m.currentConfig() }

// DryRun reports whether the Manager is globally in dry-run mode (the --dry-run
// flag). Callers that thread the flag explicitly (e.g. Reconcile) read it here.
func (m *Manager) DryRun() bool { return m.cfg.DryRun }

// withDryRun temporarily overrides the global dry-run flag and returns a restore
// func (use with defer). It holds dryMu for the whole scope so concurrent callers
// serialize: the save always captures the true original value and the flag can
// never be left stuck on by an interleaving. Do NOT call a helper that also takes
// dryMu from within the wrapped op (there are none today).
func (m *Manager) withDryRun(dry bool) func() {
	m.dryMu.Lock()
	prev := m.cfg.DryRun
	m.cfg.DryRun = dry
	return func() {
		m.cfg.DryRun = prev
		m.dryMu.Unlock()
	}
}

// ConfigPath returns the file the config was loaded from (where SaveConfig writes).
func (m *Manager) ConfigPath() string { return m.cfgPath }

// SaveConfig writes the in-memory config back to its file (0600), creating the
// parent dir if needed. It mirrors `srm init`'s full-document marshal, so the file
// is tool-owned (comments in a hand-edited file are not preserved). No secrets are
// written - the App key lives in the secrets store or is referenced by path.
func (m *Manager) SaveConfig() error {
	if m.cfgPath == "" {
		return fmt.Errorf("no config path is set - cannot persist changes")
	}
	if dir := filepath.Dir(m.cfgPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := yaml.Marshal(m.currentConfig())
	if err != nil {
		return err
	}
	return os.WriteFile(m.cfgPath, data, 0o600)
}

// currentConfig returns the live config pointer under the mutex. The config is
// SWAPPED wholesale by Reload/UpsertOrg (never mutated in place, other than the
// dryMu-guarded DryRun flag), so a caller may use the returned pointer without further
// locking - it observes a consistent config even if a concurrent reload swaps in a new
// one. Readers that iterate cfg.Orgs from a command goroutine (which can run
// concurrently with a wizard's Reload) MUST snapshot through this.
func (m *Manager) currentConfig() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// Reload re-reads the config from its file, swapping in the fresh config and
// dropping every cached GitHub client (credentials may have changed - a new org, or
// a restored config dir). The *Manager pointer is preserved, so callers holding it
// (the TUI) see the new config without re-wiring. The secrets store is unchanged. A
// no-op-safe error is returned when no config path is set or the file can't be read.
// The swap is a whole-pointer replacement under m.mu (see currentConfig).
func (m *Manager) Reload() error {
	if m.cfgPath == "" {
		return fmt.Errorf("no config path is set - cannot reload")
	}
	cfg, err := config.Load(m.cfgPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg = cfg
	m.clients = make(map[string]ghub.Client)
	m.mu.Unlock()
	return nil
}

// UpsertOrg adds or replaces an org in the in-memory config (matched by name).
// Persist it with SaveConfig; drop stale clients with Reload. Used by the TUI onboard
// wizard, mirroring `srm init`'s upsert. It builds a fresh config with a fresh Orgs
// slice and swaps the whole pointer under m.mu - never an in-place append - so a
// concurrent cfg.Orgs reader can never observe a torn slice header.
func (m *Manager) UpsertOrg(oc config.OrgConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *m.cfg // shallow copy; the Orgs slice below is fresh, so no aliasing
	orgs := make([]config.OrgConfig, len(next.Orgs), len(next.Orgs)+1)
	copy(orgs, next.Orgs)
	replaced := false
	for i := range orgs {
		if orgs[i].Name == oc.Name {
			orgs[i] = oc
			replaced = true
			break
		}
	}
	if !replaced {
		orgs = append(orgs, oc)
	}
	next.Orgs = orgs
	m.cfg = &next
}

// HasSecretsPassphrase reports whether a secrets passphrase is already available
// (SRM_SECRETS_PASSPHRASE or the persisted passphrase file). The onboard form uses
// this to decide whether it must collect a new passphrase to encrypt a pasted key.
func (m *Manager) HasSecretsPassphrase() bool {
	_, ok := secrets.ResolvePassphrase(config.PassphraseFilePath(m.cfgPath))
	return ok
}

// StoreOrgKey encrypts org's App private key (PEM) into srm's age secrets file and
// switches the Manager to that store, so the key is usable immediately without a
// restart. The encryption passphrase is resolved from SRM_SECRETS_PASSPHRASE, then the
// persisted passphrase file, then newPassphrase - which, when used, is persisted
// root-only (0600) so the detached ephemeral units (`srm _runner-cycle`, run as root)
// and later invocations can decrypt without an env var. The PEM is never logged.
func (m *Manager) StoreOrgKey(org, pemContents, newPassphrase string) error {
	if m.cfgPath == "" {
		return fmt.Errorf("no config path is set - cannot store the App key")
	}
	st, err := secrets.EncryptAppKey(config.SecretsFilePath(m.cfgPath), config.PassphraseFilePath(m.cfgPath), org, pemContents, newPassphrase)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sec = st
	m.clients = make(map[string]ghub.Client)
	m.mu.Unlock()
	return nil
}

// OrgNames lists configured orgs (snapshotting the config for the iteration).
func (m *Manager) OrgNames() []string { return m.currentConfig().OrgNames() }

// requireOrg validates that an explicit, configured org was supplied. Operations
// do NOT fall back to a hidden active org - callers must name the org so a
// destructive command can never silently hit the wrong one.
func (m *Manager) requireOrg(org string) (string, error) {
	if org == "" {
		return "", fmt.Errorf("no org specified")
	}
	if _, ok := m.currentConfig().Org(org); !ok {
		return "", fmt.Errorf("org %q not configured", org)
	}
	return org, nil
}

// client returns an authenticated client for org, building it on first use.
func (m *Manager) client(ctx context.Context, org string) (ghub.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[org]; ok {
		return c, nil
	}
	oc, ok := m.cfg.Org(org)
	if !ok {
		return nil, fmt.Errorf("org %q not configured", org)
	}
	hc, err := auth.HTTPClient(ctx, oc, m.sec)
	if err != nil {
		return nil, err
	}
	c, err := ghub.New(hc)
	if err != nil {
		return nil, err
	}
	m.clients[org] = c
	return c, nil
}

// AppInstallationAccess reports the org App installation's repo-visibility posture
// (repository selection + whether private repos are visible), read via an APP-JWT
// client. It powers the doctor/Health surfacing of the silent "private repos
// invisible" misconfiguration (an App granted only organization permissions).
// Best-effort and purely observational.
func (m *Manager) AppInstallationAccess(ctx context.Context, org string) (ghub.InstallationAccess, error) {
	oc, ok := m.currentConfig().Org(org)
	if !ok {
		return ghub.InstallationAccess{}, fmt.Errorf("org %q not configured", org)
	}
	hc, err := auth.AppHTTPClient(ctx, oc, m.sec)
	if err != nil {
		return ghub.InstallationAccess{}, err
	}
	return ghub.AppInstallationAccess(ctx, hc, org)
}

// ListRunners returns all runners for the named org.
func (m *Manager) ListRunners(ctx context.Context, org string) ([]core.Runner, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return nil, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, err
	}
	return c.ListRunners(ctx, org)
}

// RunnerWithOrg pairs a runner with the org it belongs to and whether this host
// has an srm-managed install tree for it, for cross-org / cross-host views.
type RunnerWithOrg struct {
	Org    string
	Runner core.Runner
	Local  bool // an srm-managed install tree for this runner exists on THIS host
}

// ListAllRunners lists runners across every configured org, tagging each with
// its org and host-locality. A per-org error map is returned so one unreachable
// org doesn't sink the whole view; orgs are visited in configured order.
func (m *Manager) ListAllRunners(ctx context.Context) ([]RunnerWithOrg, map[string]error) {
	var out []RunnerWithOrg
	errs := make(map[string]error)
	for _, name := range m.currentConfig().OrgNames() {
		rs, err := m.ListRunners(ctx, name)
		if err != nil {
			errs[name] = err
			continue
		}
		for _, r := range rs {
			out = append(out, RunnerWithOrg{Org: name, Runner: r, Local: m.RunnerIsLocal(name, r.Name)})
		}
	}
	return out, errs
}

// RunnerIsLocal reports whether this host has an srm-managed install tree for
// the given runner at the org-namespaced path {installRoot}/{org}/{name}. It is
// the signal for "runs on this machine" and is only meaningful when run on the
// host itself; from a remote admin box every runner reads as non-local.
func (m *Manager) RunnerIsLocal(org, name string) bool {
	root := config.DefaultInstallRoot
	if oc, ok := m.cfg.Org(org); ok && oc.InstallRoot != "" {
		root = oc.InstallRoot
	}
	if fi, err := os.Stat(filepath.Join(root, org, name)); err == nil && fi.IsDir() {
		return true
	}
	return false
}

// ResolveRunnerOrgs maps each given runner id to the org that owns it by
// scanning every configured org. Ids found in no org are returned in 'unknown';
// 'errs' flags orgs that couldn't be listed (so a missing id isn't confused
// with an unreachable org).
func (m *Manager) ResolveRunnerOrgs(ctx context.Context, ids []int64) (owned map[int64]string, unknown []int64, errs map[string]error) {
	all, errs := m.ListAllRunners(ctx)
	idToOrg := make(map[int64]string, len(all))
	for _, row := range all {
		idToOrg[row.Runner.ID] = row.Org
	}
	owned = make(map[int64]string, len(ids))
	for _, id := range ids {
		if org, ok := idToOrg[id]; ok {
			owned[id] = org
		} else {
			unknown = append(unknown, id)
		}
	}
	return owned, unknown, errs
}

// GroupWithOrg pairs a runner group with the org it belongs to.
type GroupWithOrg struct {
	Org   string
	Group core.Group
}

// ListGroups returns the named org's runner groups.
func (m *Manager) ListGroups(ctx context.Context, org string) ([]core.Group, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return nil, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return nil, err
	}
	return c.ListGroups(ctx, org)
}

// ListAllGroups lists runner groups across every configured org. A per-org error
// map is returned so one unreachable org doesn't sink the whole view.
func (m *Manager) ListAllGroups(ctx context.Context) ([]GroupWithOrg, map[string]error) {
	var out []GroupWithOrg
	errs := make(map[string]error)
	for _, name := range m.currentConfig().OrgNames() {
		gs, err := m.ListGroups(ctx, name)
		if err != nil {
			errs[name] = err
			continue
		}
		for _, g := range gs {
			out = append(out, GroupWithOrg{Org: name, Group: g})
		}
	}
	return out, errs
}

// FindRunnerOrgsByName returns the orgs that have a runner with the given name.
// Names are unique within an org but can collide across orgs, so this may return
// more than one. 'errs' flags orgs that couldn't be listed.
func (m *Manager) FindRunnerOrgsByName(ctx context.Context, name string) (orgs []string, errs map[string]error) {
	all, errs := m.ListAllRunners(ctx)
	for _, row := range all {
		if row.Runner.Name == name {
			orgs = append(orgs, row.Org)
		}
	}
	return orgs, errs
}

// DeleteRunner removes a single runner (honoring dry-run).
func (m *Manager) DeleteRunner(ctx context.Context, org string, id int64) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if m.cfg.DryRun {
		return nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	return c.DeleteRunner(ctx, org, id)
}

// Retention reads the org artifact-and-log retention policy. Callers should
// treat an error (commonly 403 from an under-privileged App) as "unavailable".
func (m *Manager) Retention(ctx context.Context, org string) (ghub.Retention, error) {
	org, err := m.requireOrg(org)
	if err != nil {
		return ghub.Retention{}, err
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return ghub.Retention{}, err
	}
	return c.GetArtifactRetention(ctx, org)
}

// SetRetention updates the org retention period in days (honoring dry-run).
func (m *Manager) SetRetention(ctx context.Context, org string, days int) error {
	org, err := m.requireOrg(org)
	if err != nil {
		return err
	}
	if m.cfg.DryRun {
		return nil
	}
	c, err := m.client(ctx, org)
	if err != nil {
		return err
	}
	return c.SetArtifactRetention(ctx, org, days)
}
