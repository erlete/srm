package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Runner log sources. A runner's host log can be read either from the systemd
// journal for its unit (the durable, always-on source on Linux) or from the
// actions/runner agent's OWN diagnostic logs under _diag. The journal is the
// default; _diag is the fallback (and the only source on Windows, which has no
// journald).
const (
	LogSourceJournal = "journal" // the unit's system log (journalctl -u on Linux)
	LogSourceAgent   = "agent"   // the runner agent's _diag logs
)

// defaultLogLines is how many recent lines a log read returns when the caller
// does not ask for a specific count.
const defaultLogLines = 200

// LogOptions parameterizes a runner/slot log read. The zero value reads the last
// defaultLogLines lines of the journal, no follow.
type LogOptions struct {
	Lines  int    // tail the last N lines (<=0 = defaultLogLines)
	Follow bool   // stream new lines until ctx is cancelled (journal source only)
	Source string // LogSourceJournal (default / "") or LogSourceAgent
}

// lines resolves the effective tail length (a sane default for <=0).
func (o LogOptions) lines() int {
	if o.Lines <= 0 {
		return defaultLogLines
	}
	return o.Lines
}

// RunnerLog writes a persistent runner's host log to w. On Linux the default
// source is the systemd journal for the runner's unit (journalctl -u); Source
// LogSourceAgent tails the agent's newest _diag log instead. With Follow set
// (journal source) it streams until ctx is cancelled - a cancelled follow is a
// clean stop, not an error.
func (u *ubuntu) RunnerLog(ctx context.Context, org, name string, opts LogOptions, w io.Writer) error {
	if opts.Source == LogSourceAgent {
		return tailAgentLog(u.runnerDir(org, name), opts, w)
	}
	return journalctl(ctx, u.svcName(org, name), opts, w)
}

// EphemeralLog writes an ephemeral slot lane's host log to w. The slot unit's
// journal holds both srm's per-cycle output and the agent's stdout/stderr (the
// unit has no logging directives), so it is the durable source; LogSourceAgent
// tails the slot tree's _diag (the current cycle only - it is wiped each job).
func (u *ubuntu) EphemeralLog(ctx context.Context, org, slot string, opts LogOptions, w io.Writer) error {
	if opts.Source == LogSourceAgent {
		return tailAgentLog(u.ephemeralSlotDir(org, slot), opts, w)
	}
	return journalctl(ctx, EphemeralSvcName(org, slot), opts, w)
}

// RunnerLog on Windows tails the agent's _diag logs: there is no journald, so the
// agent's own diagnostics are the durable per-runner source. Source and Follow
// are ignored (no journal to follow).
func (w *windows) RunnerLog(_ context.Context, org, name string, opts LogOptions, out io.Writer) error {
	return tailAgentLog(w.runnerDir(org, name), opts, out)
}

// EphemeralLog on Windows prefers the srm supervisor log (the mint/run/reset cycle
// narration) and falls back to the agent _diag. Source LogSourceAgent forces the
// _diag tail. Follow is not supported for file sources.
func (w *windows) EphemeralLog(_ context.Context, org, slot string, opts LogOptions, out io.Writer) error {
	if opts.Source != LogSourceAgent {
		sup := filepath.Join(w.ephemeralControlDir(org, slot), "supervisor.log")
		if _, err := os.Stat(sup); err == nil {
			return writeTail(sup, opts.lines(), out)
		}
	}
	return tailAgentLog(w.ephemeralSlotDir(org, slot), opts, out)
}

// journalctl streams a systemd unit's journal to w: the last opts.lines() lines,
// then (when Follow) new lines until ctx is cancelled. journalctl for a system
// unit needs root (or membership in systemd-journal); its permission error is
// surfaced verbatim. A cancelled follow returns nil (the user stopped it).
func journalctl(ctx context.Context, unit string, opts LogOptions, w io.Writer) error {
	args := []string{"-u", unit, "--no-pager", "-n", strconv.Itoa(opts.lines())}
	if opts.Follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if opts.Follow && ctx.Err() != nil {
		return nil // Ctrl-C / cancelled follow is a clean stop
	}
	return err
}

// tailAgentLog writes the last opts.lines() lines of the newest agent _diag log
// under runnerTreeDir to w. The listener log (Runner_*.log) is preferred over the
// per-job Worker_*.log; Follow is not supported for file logs.
func tailAgentLog(runnerTreeDir string, opts LogOptions, w io.Writer) error {
	path, err := newestAgentLog(filepath.Join(runnerTreeDir, "_diag"))
	if err != nil {
		return err
	}
	return writeTail(path, opts.lines(), w)
}

// newestAgentLog returns the most-recently-modified log under diag, preferring a
// Runner_*.log (the continuous listener log) over Worker_*.log (per-job). Returns
// a clear error when the dir is absent or holds no logs.
func newestAgentLog(diag string) (string, error) {
	entries, err := os.ReadDir(diag)
	if err != nil {
		return "", fmt.Errorf("no agent logs at %s (the runner may not have run yet): %w", diag, err)
	}
	pick := func(prefix string) string {
		var best string
		var bestMod int64
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			if prefix != "" && !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if best == "" || info.ModTime().Unix() > bestMod {
				best, bestMod = filepath.Join(diag, e.Name()), info.ModTime().Unix()
			}
		}
		return best
	}
	if p := pick("Runner_"); p != "" {
		return p, nil
	}
	if p := pick(""); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no agent logs found under %s", diag)
}

// NewestJobLog returns the newest per-JOB agent log under diag: the Worker_*.log the
// runner writes for a job execution (an ephemeral lane runs one job per cycle, so the
// newest Worker IS that job's log), falling back to the newest Runner_*.log, then any
// .log. Returns "" (no error) when diag has no logs, so the durable job-log capture
// (internal/joblog) can still record the run with an empty body.
func NewestJobLog(diag string) string {
	entries, err := os.ReadDir(diag)
	if err != nil {
		return ""
	}
	pick := func(prefix string) string {
		var best string
		var bestMod int64
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			if prefix != "" && !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if best == "" || info.ModTime().Unix() > bestMod {
				best, bestMod = filepath.Join(diag, e.Name()), info.ModTime().Unix()
			}
		}
		return best
	}
	if p := pick("Worker_"); p != "" {
		return p
	}
	if p := pick("Runner_"); p != "" {
		return p
	}
	return pick("")
}

// writeTail writes the last n lines of the file at path to w. It scans once with a
// fixed-size ring buffer, so memory is bounded by n regardless of file size.
func writeTail(path string, n int, w io.Writer) error {
	if n <= 0 {
		n = defaultLogLines
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	ring := make([]string, n)
	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // tolerate long agent log lines
	for sc.Scan() {
		ring[count%n] = sc.Text()
		count++
	}
	if err := sc.Err(); err != nil {
		return err
	}

	start, size := 0, count
	if count > n {
		start, size = count%n, n
	}
	for i := 0; i < size; i++ {
		if _, err := io.WriteString(w, ring[(start+i)%n]+"\n"); err != nil {
			return err
		}
	}
	return nil
}
