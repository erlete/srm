//go:build windows

package runner

import (
	"fmt"
	"io"
	"os/exec"
	"unsafe"

	// Aliased: the package name "windows" collides with this package's `windows` struct.
	win "golang.org/x/sys/windows"

	"github.com/erlete/srm/internal/config"
)

// procGlobalMemoryStatusEx resolves total physical RAM so systemd percentage caps
// (e.g. MemoryMax "25%") can be turned into absolute bytes. x/sys/windows does not
// wrap GlobalMemoryStatusEx, so it is called through a lazy kernel32 proc.
var procGlobalMemoryStatusEx = win.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX struct.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// totalPhysicalMemory returns total installed RAM in bytes, or 0 if it can't be read
// (which makes percentage caps resolve to "no limit" rather than a wrong value).
func totalPhysicalMemory() uint64 {
	var m memoryStatusEx
	m.dwLength = uint32(unsafe.Sizeof(m))
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r1 == 0 {
		return 0
	}
	return m.ullTotalPhys
}

// EphemeralCapsLine summarizes the Job Object caps this host will apply for the given
// limits, resolving percentages against real RAM exactly as runConfinedJob does, for a
// one-time line at supervisor startup. Returns "UNCAPPED ..." iff no cap will engage, so
// the lane's own log makes the silent-uncapped case obvious. See formatJobLimits.
func EphemeralCapsLine(r config.ResourceLimits) string {
	return formatJobLimits(deriveJobLimits(r, totalPhysicalMemory()))
}

// runConfinedJob runs cmd, confining it and its whole process tree to a Windows Job
// Object that carries the ephemeral job's resource caps for the job's duration. THIS
// process (the long-lived supervisor) holds the job handle and closes it when the job
// exits; JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE then reaps anything still in the job, so a
// wedged tree cannot outlive its cycle (the analog of the Linux unit's KillMode=mixed).
//
// When no caps are configured (the default) it is byte-identical to a bare cmd.Run(),
// so the validated default ephemeral path is unchanged. Cap setup is best-effort: if
// the Job Object can't be created or the process can't be assigned, the job still runs
// uncapped and a line is written to diag - never a silent drop, never a failed job.
func runConfinedJob(cmd *exec.Cmd, r config.ResourceLimits, diag io.Writer) error {
	limits := deriveJobLimits(r, totalPhysicalMemory())
	if limits.isZero() {
		return cmd.Run()
	}

	job, err := createJobWithLimits(limits)
	if err != nil {
		fmt.Fprintf(diag, "srm: resource cap disabled (job object setup failed: %v)\n", err)
		return cmd.Run()
	}
	defer win.CloseHandle(job)

	// Start, then assign before the agent spawns its heavy children (Runner.Listener/
	// Worker). cmd.exe and run.cmd are tiny and created sub-millisecond apart, so the
	// memory-heavy descendants are created AFTER assignment and inherit the job. This
	// is resource capping, not a security boundary, so the negligible start->assign
	// window is acceptable (the Linux cgroup is likewise not atomic at fork).
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := assignPidToJob(job, cmd.Process.Pid); err != nil {
		fmt.Fprintf(diag, "srm: resource cap not applied (%v)\n", err)
	} else {
		fmt.Fprintf(diag, "srm: job confined (memBytes=%d activeProcesses=%d)\n", limits.memBytes, limits.activeProcesses)
	}
	return cmd.Wait()
}

// createJobWithLimits creates a Job Object and applies the derived caps. The caller
// owns the returned handle and must CloseHandle it.
func createJobWithLimits(l jobLimits) (win.Handle, error) {
	job, err := win.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	var info win.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = win.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if l.memBytes > 0 {
		info.BasicLimitInformation.LimitFlags |= win.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(l.memBytes)
	}
	if l.activeProcesses > 0 {
		info.BasicLimitInformation.LimitFlags |= win.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = l.activeProcesses
	}
	if _, err := win.SetInformationJobObject(job, win.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		win.CloseHandle(job)
		return 0, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return job, nil
}

// assignPidToJob opens the process by pid (with just the rights AssignProcessToJobObject
// needs) and assigns it to the job. On Windows 8+/Server 2012+ this works even when the
// caller is itself in a job (automatic job nesting).
func assignPidToJob(job win.Handle, pid int) error {
	ph, err := win.OpenProcess(win.PROCESS_SET_QUOTA|win.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer win.CloseHandle(ph)
	if err := win.AssignProcessToJobObject(job, ph); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}
