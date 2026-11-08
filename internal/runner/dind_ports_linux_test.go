//go:build linux

package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallDinDPortWrapper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lib", "srm")
	path := filepath.Join(dir, "dockerd-port-wrapper")
	if err := installDinDPortWrapperImpl(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, []byte(renderDinDPortWrapper())) {
		t.Fatalf("wrapper content wrong (err %v)", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("wrapper mode %v, want 0755", fi.Mode().Perm())
	}
	// Idempotent: an up-to-date wrapper is left alone (no rewrite, same inode).
	before, _ := os.Stat(path)
	if err := installDinDPortWrapperImpl(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("an unchanged wrapper was rewritten")
	}
	// A stale wrapper is replaced.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installDinDPortWrapperImpl(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, []byte(renderDinDPortWrapper())) {
		t.Fatal("a stale wrapper was not replaced")
	}
	// No temp files are left behind.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files in %s: %v", dir, ents)
	}
}

// root writes into the wrapper directory, so one that others can write is refused.
func TestInstallDinDPortWrapperRefusesWritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "srm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := installDinDPortWrapperImpl(filepath.Join(dir, "dockerd-port-wrapper")); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("world-writable dir accepted: %v", err)
	}
}

func TestNoexecMountOnRegularDir(t *testing.T) {
	// The test temp dir is executable (go test runs binaries from there), and a path
	// that does not exist yet resolves to its nearest existing parent.
	if p := noexecMount(filepath.Join(t.TempDir(), "not", "yet")); p != "" {
		t.Fatalf("exec-capable dir reported as noexec: %q", p)
	}
}

func TestProcessExited(t *testing.T) {
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Skip("no sleep binary")
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	if processExited(live.Process.Pid) {
		t.Fatal("a running process reported as exited")
	}
	dead := exec.Command("sh", "-c", "exit 126")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !processExited(dead.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("an exited (zombie) process was never reported as exited")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = dead.Wait()
}

// A daemon that dies at startup (the v2.6.0 failure: DOCKERD could not be exec'd)
// must end the wait at once instead of after the full timeout.
func TestWaitDockerSocketOrExitDetectsEarlyExit(t *testing.T) {
	d := exec.Command("sh", "-c", "exit 126")
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Wait() }()
	start := time.Now()
	err := waitDockerSocketOrExit(context.Background(), filepath.Join(t.TempDir(), "docker.sock"), 60*time.Second, d)
	if err == nil || !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("err = %v, want an early-exit error", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("early exit took %v to detect", time.Since(start))
	}
}
