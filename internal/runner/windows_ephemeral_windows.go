//go:build windows

package runner

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// installSupervisorService creates the slot's supervisor Windows service through the
// SCM. It uses golang.org/x/sys/windows/svc/mgr rather than a hand-built
// `sc create binPath=` so the exe path and arguments are quoted correctly (the
// embedded-quote binPath form is a notorious source of "service won't start"). The
// service is a long-lived `srm-win _runner-supervisor` that loops RunCycle in process
// under the low-privilege service account, set to relaunch if it crashes.
func (w *windows) installSupervisorService(org, slot, svc string) error {
	self := w.opts.SelfExe
	if self == "" {
		self = defaultSelfExeWin
	}
	args := []string{"_runner-supervisor", "--org", org, "--slot", slot}
	if w.opts.ConfigPath != "" {
		args = append(args, "--config", w.opts.ConfigPath)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	cfg := mgr.Config{
		StartType:        mgr.StartAutomatic,
		ServiceStartName: scAccount(w.opts.User), // built-in passwordless account
		DisplayName:      svc,
		Description:      fmt.Sprintf("srm ephemeral runner lane %s/%s", org, slot),
	}
	s, err := m.CreateService(svc, self, cfg, args...)
	if err != nil {
		return fmt.Errorf("create supervisor service: %w", err)
	}
	defer s.Close()

	// Relaunch the supervisor if it crashes; reset the failure counter daily. A clean
	// SCM stop reports SERVICE_STOPPED and is NOT a failure, so an intentional destroy
	// never triggers a restart - only an unexpected exit does.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}, 86400)
	return nil
}
