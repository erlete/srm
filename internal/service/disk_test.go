package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/erlete/srm/internal/config"
)

// fakeFree swaps diskFreeFn for a sequence of free-space readings (the last one
// repeats) on a 100 GiB filesystem, and makes the poll instant.
func fakeFree(t *testing.T, avail ...uint64) *int {
	t.Helper()
	oldF, oldP := diskFreeFn, diskPollEvery
	t.Cleanup(func() { diskFreeFn, diskPollEvery = oldF, oldP })
	diskPollEvery = time.Millisecond
	calls := 0
	diskFreeFn = func(string) (uint64, uint64, error) {
		i := min(calls, len(avail)-1)
		calls++
		return avail[i], 100 << 30, nil
	}
	return &calls
}

func diskManager(minFree string) *Manager {
	return &Manager{cfg: &config.Config{MinFreeDisk: minFree}}
}

func TestWaitForDiskWaitsThenResumes(t *testing.T) {
	calls := fakeFree(t, 5<<30, 5<<30, 50<<30) // below 10G twice, then room
	var log bytes.Buffer
	if err := diskManager("10G").waitForDisk(context.Background(), "acme", "3", &log); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Errorf("polled %d times, want 3", *calls)
	}
	out := log.String()
	if !strings.Contains(out, "only 5.0 GiB free") || !strings.Contains(out, "resuming") {
		t.Errorf("log: %q", out)
	}
	if strings.Count(out, "waiting before registering") != 1 {
		t.Errorf("the wait must be reported once per report interval: %q", out)
	}
}

func TestWaitForDiskPercent(t *testing.T) {
	fakeFree(t, 20<<30) // 20% of 100G free, minimum 10%
	if err := diskManager("10%").waitForDisk(context.Background(), "acme", "1", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForDiskStopsWithTheCycle(t *testing.T) {
	fakeFree(t, 1<<30)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := diskManager("10G").waitForDisk(ctx, "acme", "1", &bytes.Buffer{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's", err)
	}
}

// Off, invalid or unreadable never blocks a slot.
func TestWaitForDiskNeverBlocksOnBadInput(t *testing.T) {
	calls := fakeFree(t, 1<<30)
	if err := diskManager("").waitForDisk(context.Background(), "acme", "1", &bytes.Buffer{}); err != nil || *calls != 0 {
		t.Errorf("unset: err %v, %d reads", err, *calls)
	}
	if err := diskManager("lots").waitForDisk(context.Background(), "acme", "1", &bytes.Buffer{}); err != nil {
		t.Errorf("invalid: %v", err)
	}
	diskFreeFn = func(string) (uint64, uint64, error) { return 0, 0, errors.New("statfs failed") }
	if err := diskManager("10G").waitForDisk(context.Background(), "acme", "1", &bytes.Buffer{}); err != nil {
		t.Errorf("unreadable: %v", err)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[uint64]string{512: "512 B", 1536: "1.5 KiB", 27 << 30: "27.0 GiB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
