package ntui

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"runtime/pprof"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	stallAfter  = 8 * time.Second // the loop is "frozen" if it hasn't beaten for this long
	maxLogBytes = 1 << 20         // start the log over when it grows past this
	maxStalls   = 5               // stall dumps per run; a flapping loop must not fill the disk
)

// diag writes ~/.barq/barq.log: goroutine dumps when the Bubble Tea loop
// stops responding (a freeze), on SIGQUIT, and on fatal runtime errors, so a
// freeze can be diagnosed afterwards without having run barq with stderr
// redirected.
type diag struct {
	f    *os.File
	beat atomic.Int64 // unix nanos of the loop's last Update
}

// startDiag opens the log and starts the watchdog. It returns nil, and barq
// runs without diagnostics, when the log can't be opened.
func startDiag() *diag {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".barq")
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	path := filepath.Join(dir, "barq.log")
	flags := os.O_WRONLY | os.O_CREATE | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil
	}
	d := &diag{f: f}
	d.beat.Store(time.Now().UnixNano())
	d.logf("start pid %d", os.Getpid())
	_ = debug.SetCrashOutput(f, debug.CrashOptions{}) // fatal runtime errors
	go d.watch()
	return d
}

func (d *diag) logf(format string, args ...any) {
	if d == nil {
		return
	}
	fmt.Fprintf(d.f, "%s [%d] %s\n", time.Now().Format(time.RFC3339), os.Getpid(), fmt.Sprintf(format, args...))
}

// dump writes every goroutine's stack.
func (d *diag) dump(why string) {
	if d == nil {
		return
	}
	d.logf("%s: goroutine dump follows", why)
	_ = pprof.Lookup("goroutine").WriteTo(d.f, 2)
	fmt.Fprintln(d.f)
}

// heartbeat is called by the loop on every Update.
func (d *diag) heartbeat() {
	if d != nil {
		d.beat.Store(time.Now().UnixNano())
	}
}

// watch dumps the stacks once per stall. The loop beats at least once a
// second (the disk-sync tick), so a long gap means it is blocked.
func (d *diag) watch() {
	const every = 2 * time.Second
	t := time.NewTicker(every)
	defer t.Stop()
	last := time.Now()
	stalled := false
	dumps := 0
	for now := range t.C {
		late := now.Sub(last) > 2*every // the machine slept; the beat is stale for that reason
		last = now
		if late {
			d.heartbeat()
			continue
		}
		gap := now.Sub(time.Unix(0, d.beat.Load()))
		switch {
		case gap > stallAfter && !stalled:
			stalled = true
			if dumps++; dumps > maxStalls {
				continue
			}
			d.dump(fmt.Sprintf("UI loop unresponsive for %s", gap.Round(time.Second)))
		case gap <= stallAfter && stalled:
			stalled = false
			d.logf("UI loop responsive again")
		}
	}
}

// onSignals handles termination signals: quit() asks the loop to stop
// cleanly, but a frozen loop never will, so a second look 3s later dumps the
// stacks and exits. SIGQUIT dumps and exits at once.
func (d *diag) onSignals(quit func()) func() {
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for s := range sig {
			if s == syscall.SIGQUIT {
				d.dump("SIGQUIT")
				os.Exit(2)
			}
			quit()
			go func() {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					d.dump(fmt.Sprintf("%v did not stop the UI loop", s))
					os.Exit(1)
				}
			}()
		}
	}()
	return func() { signal.Stop(sig); close(done) }
}
