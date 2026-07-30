package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewestJobLog: the per-job capture prefers the newest Worker_*.log (the job's own
// log), even when a Runner_*.log (the listener) is newer; empty diag returns "".
func TestNewestJobLog(t *testing.T) {
	diag := t.TempDir()
	if got := NewestJobLog(diag); got != "" {
		t.Fatalf("empty diag should be \"\", got %q", got)
	}

	write := func(name string, age time.Duration) string {
		p := filepath.Join(diag, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		_ = os.Chtimes(p, mt, mt)
		return p
	}

	write("Runner_2020.log", 2*time.Hour)
	worker := write("Worker_2021.log", 1*time.Hour)
	if got := NewestJobLog(diag); got != worker {
		t.Errorf("want Worker log %q, got %q", worker, got)
	}
	// A newer Runner_ log must NOT win over the Worker_ log.
	write("Runner_2022.log", 1*time.Minute)
	if got := NewestJobLog(diag); got != worker {
		t.Errorf("Worker must be preferred over a newer Runner log, got %q", got)
	}
}
