//go:build windows

package runner

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/erlete/srm/internal/config"
)

// TestRunConfinedJobWindows exercises the real Job Object syscalls (CreateJobObject,
// SetInformationJobObject, OpenProcess, AssignProcessToJobObject) end to end on a live
// Windows host: it runs a short child process under a memory+process cap and asserts
// it ran. Job Objects on a child you spawned need no elevation, so this validates the
// P3d mechanism without an ephemeral lane or GitHub. The confinement assertion is soft
// (logged, not failed) because a CI runner may itself sit in a job that forbids
// nesting - in which case runConfinedJob correctly degrades to an uncapped run.
func TestRunConfinedJobWindows(t *testing.T) {
	var out, diag bytes.Buffer
	cmd := exec.Command("cmd", "/c", "echo", "confined-ok")
	cmd.Stdout = &out

	limits := config.ResourceLimits{MemoryMax: "256M", TasksMax: "16"}
	if err := runConfinedJob(cmd, limits, &diag); err != nil {
		t.Fatalf("runConfinedJob: %v (diag: %s)", err, diag.String())
	}
	if !strings.Contains(out.String(), "confined-ok") {
		t.Fatalf("child did not run as expected; stdout=%q", out.String())
	}
	t.Logf("diag: %s", strings.TrimSpace(diag.String()))
	if strings.Contains(diag.String(), "job confined") {
		return // the Job Object caps engaged - the strong outcome
	}
	if strings.Contains(diag.String(), "not applied") || strings.Contains(diag.String(), "setup failed") {
		t.Logf("Job Object did not engage here (likely job-nesting restriction); ran uncapped as designed")
		return
	}
	t.Fatalf("unexpected diag (neither confined nor a known best-effort fallback): %q", diag.String())
}

// TestRunConfinedJobNoLimits asserts the no-caps path is a plain run (the validated
// default ephemeral behavior): zero ResourceLimits => no Job Object, empty diag.
func TestRunConfinedJobNoLimits(t *testing.T) {
	var out, diag bytes.Buffer
	cmd := exec.Command("cmd", "/c", "echo", "plain-ok")
	cmd.Stdout = &out

	if err := runConfinedJob(cmd, config.ResourceLimits{}, &diag); err != nil {
		t.Fatalf("runConfinedJob (no limits): %v", err)
	}
	if !strings.Contains(out.String(), "plain-ok") {
		t.Fatalf("child did not run; stdout=%q", out.String())
	}
	if diag.Len() != 0 {
		t.Fatalf("no-limits path must not touch a Job Object or write diag; got %q", diag.String())
	}
}
