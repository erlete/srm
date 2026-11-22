package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/erlete/srm/internal/diskfree"
)

// Seams for tests.
var (
	diskFreeFn      = diskfree.Free
	diskPollEvery   = 30 * time.Second
	diskReportEvery = 10 * time.Minute
)

// waitForDisk blocks while the filesystem holding org's install root (where the
// slots and their Docker data live) has less free space than minFreeDisk. It
// returns nil once there is room, at once when no minimum is set, when the value
// is invalid (`srm doctor` reports it) or when free space cannot be read, and
// ctx's error if the cycle is stopped while waiting. It reports to log when it
// starts waiting, every diskReportEvery while it waits, and when it resumes.
func (m *Manager) waitForDisk(ctx context.Context, org, slot string, log io.Writer) error {
	if strings.TrimSpace(m.cfg.MinFreeDisk) == "" {
		return nil
	}
	root := m.installRootFor(org)
	var waitingSince, lastReport time.Time
	for {
		avail, total, err := diskFreeFn(root)
		if err != nil {
			return nil // unreadable: never block a slot on the check itself
		}
		min, err := m.cfg.MinFreeBytes(total)
		if err != nil || min == 0 {
			return nil
		}
		if avail >= min {
			if !waitingSince.IsZero() {
				fmt.Fprintf(log, "srm: slot %s/%s: %s free on %s, resuming after waiting %s\n",
					org, slot, HumanBytes(avail), root, time.Since(waitingSince).Round(time.Second))
			}
			return nil
		}
		now := time.Now()
		if waitingSince.IsZero() {
			waitingSince = now
		}
		if lastReport.IsZero() || now.Sub(lastReport) >= diskReportEvery {
			fmt.Fprintf(log, "srm: slot %s/%s: only %s free on %s (minFreeDisk %s); waiting before registering for a job\n",
				org, slot, HumanBytes(avail), root, m.cfg.MinFreeDisk)
			lastReport = now
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(diskPollEvery):
		}
	}
}

// DiskSlot is one local ephemeral slot tree's disk use.
type DiskSlot struct {
	Org, Slot string
	Bytes     uint64
	Partial   bool // the walk hit its time budget: Bytes is a lower bound (0 = not measured)
	Orphan    bool // a slot tree with no unit: left by an interrupted create
}

// DiskReport is the runners' filesystem as `srm doctor` shows it.
type DiskReport struct {
	Root        string
	Avail       uint64
	Total       uint64
	Err         string // free space unreadable
	MinFree     uint64 // resolved minFreeDisk, 0 = off
	MinFreeSpec string
	MinFreeErr  string // invalid minFreeDisk (ignored by the slots)
	Slots       []DiskSlot
}

// Below reports whether the filesystem is under minFreeDisk (slots are waiting).
func (r DiskReport) Below() bool { return r.Err == "" && r.MinFree > 0 && r.Avail < r.MinFree }

// slotSizeBudget bounds the time doctor spends measuring each slot tree, and
// diskSizeBudget all of them: slots past it are reported unmeasured.
const (
	slotSizeBudget = 5 * time.Second
	diskSizeBudget = 30 * time.Second
)

// DiskUsage measures the filesystem holding the install root of the first of
// orgs and the disk used by every local ephemeral slot tree of orgs, including
// trees left without a unit by an interrupted create.
func (m *Manager) DiskUsage(ctx context.Context, orgs []string) DiskReport {
	org := ""
	if len(orgs) > 0 {
		org = orgs[0]
	}
	rep := DiskReport{Root: m.installRootFor(org), MinFreeSpec: m.cfg.MinFreeDisk}
	if avail, total, err := diskFreeFn(rep.Root); err != nil {
		rep.Err = err.Error()
	} else {
		rep.Avail, rep.Total = avail, total
		if min, err := m.cfg.MinFreeBytes(total); err != nil {
			rep.MinFreeErr = err.Error()
		} else {
			rep.MinFree = min
		}
	}
	units := map[string]bool{}
	if slots, err := m.ListEphemeralSlots(ctx, ""); err == nil {
		for _, s := range slots {
			units[s.Org+"/"+s.Slot] = true
		}
	}
	allDone := time.Now().Add(diskSizeBudget)
	for _, o := range orgs {
		base := filepath.Join(m.installRootFor(o), o, ".ephemeral")
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !validSlot(e.Name()) {
				continue
			}
			deadline := time.Now().Add(slotSizeBudget)
			if deadline.After(allDone) {
				deadline = allDone
			}
			b, partial := diskfree.TreeSize(filepath.Join(base, e.Name()), deadline)
			rep.Slots = append(rep.Slots, DiskSlot{
				Org: o, Slot: e.Name(), Bytes: b, Partial: partial, Orphan: !units[o+"/"+e.Name()],
			})
		}
	}
	sort.Slice(rep.Slots, func(i, j int) bool {
		if rep.Slots[i].Org != rep.Slots[j].Org {
			return rep.Slots[i].Org < rep.Slots[j].Org
		}
		return slotLess(rep.Slots[i].Slot, rep.Slots[j].Slot)
	})
	return rep
}

// HumanBytes renders n in binary units ("12.3 GiB").
func HumanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
