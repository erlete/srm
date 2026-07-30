package joblog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStoreWriteListReadPrune is the round-trip for the durable store: a captured log
// and a log-less run both persist, List is newest-first, ReadLog returns the body, and
// Prune (dry-run then real) drops only entries past the cutoff.
func TestStoreWriteListReadPrune(t *testing.T) {
	s := Store{Root: t.TempDir()}

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "Worker_1.log")
	if err := os.WriteFile(src, []byte("job output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	m1 := Meta{Org: "acme", Slot: "1", RunnerName: "srm-eph-acme-1-ab", RunnerID: 101,
		StartedUnix: now.Add(-2 * time.Hour).Unix(), EndedUnix: now.Add(-2*time.Hour + 30*time.Second).Unix(),
		OK: true, CapturedUnix: now.Unix()}
	if err := s.Write(m1, src); err != nil {
		t.Fatalf("write m1: %v", err)
	}
	m2 := Meta{Org: "acme", Slot: "1", RunnerName: "srm-eph-acme-1-cd", RunnerID: 102,
		StartedUnix: now.Add(-1 * time.Hour).Unix(), EndedUnix: now.Add(-1 * time.Hour).Unix(),
		OK: false, Error: "boom", CapturedUnix: now.Unix()}
	if err := s.Write(m2, ""); err != nil { // no source log -> sidecar only
		t.Fatalf("write m2: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 runs, got %d", len(list))
	}
	if list[0].RunnerID != 102 { // newest-first: m2 started after m1
		t.Errorf("newest-first broken: %+v", list)
	}
	if list[0].HasLog {
		t.Errorf("m2 should have NO captured log")
	}
	if !list[1].HasLog {
		t.Errorf("m1 should have a captured log")
	}

	body, err := s.ReadLog(list[1])
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(body) != "job output\n" {
		t.Errorf("log body = %q", body)
	}

	// Dry-run: counts m1 (>90 min old), deletes nothing.
	n, _, err := s.Prune(now.Add(-90*time.Minute), true)
	if err != nil {
		t.Fatalf("dry prune: %v", err)
	}
	if n != 1 {
		t.Errorf("dry-run should count 1 (m1), got %d", n)
	}
	if l, _ := s.List(); len(l) != 2 {
		t.Errorf("dry-run must not delete, still want 2, got %d", len(l))
	}

	// Real prune: removes m1, keeps m2.
	if n, _, err = s.Prune(now.Add(-90*time.Minute), false); err != nil {
		t.Fatalf("prune: %v", err)
	} else if n != 1 {
		t.Errorf("prune should remove 1, got %d", n)
	}
	l, _ := s.List()
	if len(l) != 1 || l[0].RunnerID != 102 {
		t.Errorf("after prune want only m2, got %+v", l)
	}
}

func TestListMissingRootEmpty(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "never-created")}
	l, err := s.List()
	if err != nil || len(l) != 0 {
		t.Fatalf("missing root should be empty, got %d %v", len(l), err)
	}
}
