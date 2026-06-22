//go:build windows

package cli

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows/svc"

	"github.com/erlete/srm/internal/runner"
	"github.com/erlete/srm/internal/service"
)

// runSupervisor runs the ephemeral slot supervisor. Started by the SCM it dispatches
// the Windows service control handler (so SCM sees a real, controllable service); run
// by hand it loops in the foreground until interrupted (useful for diagnosis).
func runSupervisor(mgr *service.Manager, org, slot string) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isSvc {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		superviseLoop(ctx, mgr, org, slot)
		return nil
	}
	return svc.Run("actions.ephemeral."+org+"."+slot, &ephemeralService{mgr: mgr, org: org, slot: slot})
}

// superviseLoop runs job cycles back to back until ctx is cancelled. Manager.RunCycle
// mints a single-use JIT config, runs EXACTLY one job, and reaps; it self-paces on
// failure (backoff) so a broken lane cannot hot-loop the GitHub API. Each iteration
// bumps the slot's cycle counter - the Windows stand-in for systemd NRestarts, since
// the supervisor loops in process rather than being re-exec'd per job.
func superviseLoop(ctx context.Context, mgr *service.Manager, org, slot string) {
	runner.LogEphemeralSupervisor(org, slot, "supervisor started")
	// Echo the caps this supervisor actually loaded, ONCE, so "is this lane capped?"
	// is answerable from the lane's own log. A config `resources:` change only takes
	// effect when the lane is recreated/restarted (config is read at process start), so
	// a stale or unparsed cap silently runs UNCAPPED - this line makes that visible.
	runner.LogEphemeralSupervisor(org, slot, "ephemeral lane caps: "+runner.EphemeralCapsLine(mgr.Config().ResourcesFor(org)))
	for ctx.Err() == nil {
		if err := mgr.RunCycle(ctx, org, slot); err != nil {
			runner.LogEphemeralSupervisor(org, slot, "cycle error: "+err.Error())
		} else {
			runner.LogEphemeralSupervisor(org, slot, "cycle completed (job ran)")
		}
		runner.IncrementEphemeralCycle(org, slot)
	}
	runner.LogEphemeralSupervisor(org, slot, "supervisor stopping (context cancelled)")
}

// ephemeralService adapts superviseLoop to the Windows service control protocol: it
// reports Running as soon as the loop is launched (the agent then blocks waiting for
// a job, which is healthy), and on Stop/Shutdown cancels the loop's context - which
// kills the in-flight run.cmd (CommandContext) and ends the loop. A job cancelled
// this way leaves a recorded JIT id that the next start (or a destroy) reaps.
type ephemeralService struct {
	mgr       *service.Manager
	org, slot string
}

func (s *ephemeralService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // also covers the <-done path (loop ended on its own)
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseLoop(ctx, s.mgr, s.org, s.slot)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case <-done:
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}
