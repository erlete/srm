package service

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	src := t.TempDir()
	files := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{"config.yaml", "orgs: []\n", 0o640},
		{"secrets.age", "secret-bytes", 0o600},
		{"acme.pem", "-----KEY-----", 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(src, f.name), []byte(f.data), f.mode); err != nil {
			t.Fatal(err)
		}
	}
	// A subdirectory must be skipped (flat config dir), not cause an error.
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	arc := filepath.Join(t.TempDir(), "b.tar.gz")
	if _, err := BackupConfigDir(src, arc); err != nil {
		t.Fatalf("backup: %v", err)
	}

	dst := t.TempDir()
	if err := RestoreConfigDir(arc, dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(dst, f.name))
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		if string(got) != f.data {
			t.Errorf("%s: data = %q, want %q", f.name, got, f.data)
		}
		// Windows reports synthetic perms (0666/0444); only assert modes on Unix.
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(filepath.Join(dst, f.name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != f.mode {
				t.Errorf("%s: mode = %o, want %o", f.name, fi.Mode().Perm(), f.mode)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "sub")); !os.IsNotExist(err) {
		t.Error("subdirectory should not have been backed up")
	}
}

func TestRestoreRejectsTraversal(t *testing.T) {
	arc := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(arc)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("bad")
	if err := tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	if err := RestoreConfigDir(arc, t.TempDir()); err == nil {
		t.Fatal("expected restore to reject a path-traversal entry, got nil")
	}
}
