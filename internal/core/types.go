// Package core holds the tool's domain types. It is a leaf package: it imports
// nothing from the rest of the codebase, so every other layer (github adapter,
// service, provision, runner, tui) can depend on it without import cycles.
package core

import "strings"

// Runner is a self-hosted runner as the tool reasons about it, decoupled from
// the go-github wire types.
type Runner struct {
	ID        int64
	Name      string
	OS        string
	Status    string // "online" | "offline"
	Busy      bool
	Ephemeral bool
	GroupID   int64
	Version   string
	Labels    []Label
}

// Online reports whether the runner is currently connected to GitHub.
func (r Runner) Online() bool { return r.Status == StatusOnline }

// Idle reports whether the runner is online but not currently running a job.
func (r Runner) Idle() bool { return r.Online() && !r.Busy }

// Runner status values returned by the GitHub API.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// Label is a runner label. Read-only labels (self-hosted, the OS, the
// architecture) are assigned by GitHub and cannot be removed; only custom
// labels are mutable.
type Label struct {
	Name     string
	ReadOnly bool
}

// Group is an organization runner group.
type Group struct {
	ID           int64
	Name         string
	Visibility   string // "all" | "selected" | "private"
	Default      bool
	AllowsPublic bool
}

// Repo is an organization repository, decoupled from the go-github wire type. It
// backs the runner-group "Repository access" picker (assign repos to a group with
// visibility "selected").
type Repo struct {
	ID       int64
	Name     string // short name (e.g. "api")
	FullName string // "org/api"
	Private  bool
	PushedAt string // RFC3339 last-push time; orders the current-job scan (recent first)
}

// RunnerJob is the workflow job a runner is currently executing, resolved by
// scanning in-progress runs (there is no direct runner->job endpoint).
type RunnerJob struct {
	Repo     string // "org/repo"
	Workflow string // workflow name
	JobName  string // job name
	URL      string // html_url of the job
}

// DependencyManifest describes the host-once dependency layer for a profile.
// It is applied a single time during host provisioning, never per job.
type DependencyManifest struct {
	AptPackages          []string `koanf:"aptPackages" yaml:"aptPackages,omitempty"`                   // installed once on the host
	SetupScripts         []string `koanf:"setupScripts" yaml:"setupScripts,omitempty"`                 // escape hatch for complex setups
	ToolCacheSeeds       []string `koanf:"toolCacheSeeds" yaml:"toolCacheSeeds,omitempty"`             // tarballs to seed RUNNER_TOOL_CACHE
	PersistentCachePaths []string `koanf:"persistentCachePaths" yaml:"persistentCachePaths,omitempty"` // host paths kept across jobs
}

// Empty reports whether the manifest declares nothing (bring-your-own-host).
func (m DependencyManifest) Empty() bool {
	return len(m.AptPackages) == 0 && len(m.SetupScripts) == 0 &&
		len(m.ToolCacheSeeds) == 0 && len(m.PersistentCachePaths) == 0
}

// IsInlineScript reports whether a DependencyManifest.SetupScripts entry is an
// inline script body (authored in the TUI/CLI manifest editor) rather than a path
// to a pre-placed file on the host: a shebang or an embedded newline is the signal.
// Callers use it to materialize inline bodies at provision time and to avoid dumping
// a whole script where a short label belongs.
func IsInlineScript(entry string) bool {
	return strings.HasPrefix(strings.TrimSpace(entry), "#!") || strings.Contains(entry, "\n")
}

// RunnerProfile is the unit the TUI lists/edits/applies. It bundles the GitHub
// registration parameters with the host dependency layer the runners consume.
type RunnerProfile struct {
	Name                  string             `koanf:"name" yaml:"name"`
	Labels                []string           `koanf:"labels" yaml:"labels,omitempty"`
	GroupID               int64              `koanf:"groupID" yaml:"groupID"`
	Ephemeral             bool               `koanf:"ephemeral" yaml:"ephemeral"` // JIT/one-job by default
	Manifest              DependencyManifest `koanf:"manifest" yaml:"manifest,omitempty"`
	DefaultContainerImage string             `koanf:"defaultContainerImage" yaml:"defaultContainerImage,omitempty"`
	RequireJobContainer   bool               `koanf:"requireJobContainer" yaml:"requireJobContainer"`
}

// CreateSpec parameterizes a bulk runner-creation request.
type CreateSpec struct {
	Org        string
	Profile    string
	Count      int
	NamePrefix string
	Labels     []string
	GroupID    int64
	Ephemeral  bool
}

// DeleteResult reports the outcome of deleting a single runner.
type DeleteResult struct {
	ID   int64
	Name string
	Err  error
}

// ProgressEvent is emitted by bulk operations so the TUI can render live
// progress without blocking.
type ProgressEvent struct {
	Index   int
	Total   int
	Message string
	Err     error
}
