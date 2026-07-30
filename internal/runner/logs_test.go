package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeTail must return exactly the last N lines (in order), all lines when the
// file is shorter than N, and never leak the ring buffer's wrap.
func TestWriteTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	var body strings.Builder
	for i := 1; i <= 50; i++ {
		body.WriteString("line" + strconv.Itoa(i) + "\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writeTail(path, 5, &buf); err != nil {
		t.Fatalf("writeTail: %v", err)
	}
	if got, want := buf.String(), "line46\nline47\nline48\nline49\nline50\n"; got != want {
		t.Fatalf("last 5 = %q, want %q", got, want)
	}

	// N larger than the file returns every line, in order.
	buf.Reset()
	if err := writeTail(path, 1000, &buf); err != nil {
		t.Fatalf("writeTail: %v", err)
	}
	if n := strings.Count(buf.String(), "\n"); n != 50 {
		t.Fatalf("tail(1000) returned %d lines, want 50", n)
	}
	if !strings.HasPrefix(buf.String(), "line1\n") {
		t.Fatalf("tail(1000) should start at line1, got %q", buf.String()[:12])
	}
}

// newestAgentLog prefers the listener log (Runner_*.log) over per-job Worker logs
// even when a Worker log is newer, and falls back to any *.log otherwise.
func TestNewestAgentLog(t *testing.T) {
	diag := t.TempDir()
	write := func(name string, mod time.Time) string {
		p := filepath.Join(diag, name)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	write("Worker_20260730-120500.log", base.Add(5*time.Minute)) // newest by time
	runnerNewer := write("Runner_20260730-120100.log", base.Add(1*time.Minute))
	write("Runner_20260730-120000.log", base)

	got, err := newestAgentLog(diag)
	if err != nil {
		t.Fatalf("newestAgentLog: %v", err)
	}
	if got != runnerNewer {
		t.Fatalf("picked %q, want the newest Runner_ log %q (Runner_ must win over a newer Worker_)", got, runnerNewer)
	}

	// With only a Worker log present, it is used as the fallback.
	only := t.TempDir()
	worker := filepath.Join(only, "Worker_1.log")
	if err := os.WriteFile(worker, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := newestAgentLog(only); err != nil || got != worker {
		t.Fatalf("fallback = %q, %v; want %q", got, err, worker)
	}

	// An empty/absent dir is a clear error, not a panic.
	if _, err := newestAgentLog(filepath.Join(only, "nope")); err == nil {
		t.Fatal("expected an error for a missing _diag dir")
	}
}
