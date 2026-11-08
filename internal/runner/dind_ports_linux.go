//go:build linux

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// installDinDPortWrapperImpl installs the wrapper at path: it creates the directory
// (0755), refuses one that is not owned by srm's own user or is group/world-writable
// (root writes here, so a directory another user could write would let them redirect
// that write), and replaces the file atomically (temp file + rename) only when its
// content or mode differs. Concurrent cycles writing identical bytes are harmless: a
// rename never exposes a partial file, and an exec already in flight keeps its inode.
func installDinDPortWrapperImpl(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Geteuid() || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("refusing %s: it must be a directory owned by uid %d and not group/world-writable", dir, os.Geteuid())
	}
	want := []byte(renderDinDPortWrapper())
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, want) {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() == 0o755 {
			return nil
		}
	}
	tmp, err := os.CreateTemp(dir, ".dockerd-port-wrapper-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(want); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// probeDinDPortWrapperImpl proves the per-org user can execute the wrapper where it
// is installed: the directory must not be on a noexec mount (the v2.6.0 failure: /run
// is noexec on Ubuntu), and a check-mode run as that user (setpriv, like the daemon
// launch) must exit 0 - which also covers path traversal and the interpreter.
func probeDinDPortWrapperImpl(ctx context.Context, usr *user.User, path string) error {
	if p := noexecMount(filepath.Dir(path)); p != "" {
		return errors.New(p)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "setpriv", "--reuid", usr.Uid, "--regid", usr.Gid, "--init-groups", "--",
		path, dinDWrapperCheckArg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("check run failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// noexecMount returns a problem description when dir (or, if dir does not exist
// yet, its nearest existing parent) is on a noexec mount, else "".
func noexecMount(dir string) string {
	for p := dir; ; p = filepath.Dir(p) {
		var st unix.Statfs_t
		if err := unix.Statfs(p, &st); err == nil {
			if st.Flags&unix.ST_NOEXEC != 0 {
				return p + " is on a noexec mount"
			}
			return ""
		}
		if parent := filepath.Dir(p); parent == p {
			return ""
		}
	}
}

// DinDPortWrapperProblem reports why the installed dockerd wrapper could not be
// executed on this host ("" when nothing is wrong, including when it has not been
// installed yet - the first partitioned cycle installs it). For `srm doctor`.
func DinDPortWrapperProblem() string {
	if p := noexecMount(filepath.Dir(DinDPortWrapperPath)); p != "" {
		return p
	}
	fi, err := os.Lstat(DinDPortWrapperPath)
	if err != nil {
		return ""
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o005 != 0o005 {
		return fmt.Sprintf("%s is not a world-executable regular file (mode %v)", DinDPortWrapperPath, fi.Mode())
	}
	return ""
}

// processExited reports whether pid has exited (a zombie nobody has reaped yet, or
// gone). The daemon's pid is the RootlessKit parent (setpriv, env and the rootless
// script all exec), so its exit means the daemon will never come up this time.
func processExited(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// Field 3 (state) follows the parenthesized command name, which may contain spaces.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	state := s[i+2]
	return state == 'Z' || state == 'X'
}

// ReadDinDNetns reads the sysctls a slot's running rootless dockerd actually uses
// (ip_local_port_range and tcp_tw_reuse) inside the slot's detached network
// namespace, entered through the RootlessKit child (see dinDNetnsViaChild). Needs
// root, but not the slot's user namespace. ErrDinDNotRunning means the slot has no
// live daemon right now (between cycles, stopped, or not a DinD lane).
//
// It execs nsenter rather than calling setns in-process, so no Go thread is ever left
// in a foreign namespace.
func ReadDinDNetns(ctx context.Context, org, slot string) (DinDNetns, error) {
	ns, err := dinDNetnsViaChild(dinDStateDir(org, slot))
	if err != nil {
		return DinDNetns{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nsenter", append([]string{"--net=" + ns}, dinDNetnsReadArgs...)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// A child_pid left by a stopped daemon points at a gone (or reused) process: the
		// path is missing, or is not a namespace and nsenter refuses it. Not running.
		if strings.Contains(msg, "Invalid argument") || strings.Contains(msg, "No such file") {
			return DinDNetns{}, ErrDinDNotRunning
		}
		if strings.Contains(msg, "Operation not permitted") || strings.Contains(msg, "Permission denied") {
			return DinDNetns{}, errors.New("needs root to enter the slot's network namespace")
		}
		return DinDNetns{}, fmt.Errorf("read slot network namespace: %v: %s", err, msg)
	}
	return parseDinDNetns(string(out))
}
