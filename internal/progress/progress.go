// Package progress prints periodic one-line progress updates for the long
// phases of plr. A Reporter counts work items with atomics while a
// background goroutine rewrites one stderr line per second (\r); Stop
// always prints the final line with a trailing newline.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Reporter tracks the progress of one phase of work.
type Reporter struct {
	label   string
	total   atomic.Int64
	done    atomic.Int64
	start   time.Time
	w       io.Writer
	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once

	mu      sync.Mutex
	lastLen int
}

// New starts a reporter for one phase. The total amount of work is
// unknown until SetTotal is called; lines go to stderr.
func New(label string) *Reporter {
	return newWithWriter(label, os.Stderr)
}

func newWithWriter(label string, w io.Writer) *Reporter {
	r := &Reporter{
		label:   label,
		start:   time.Now(),
		w:       w,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go r.loop()
	return r
}

// SetTotal records the total amount of work. Repeated calls with the same
// value are fine; 0 keeps the total unknown.
func (r *Reporter) SetTotal(n int64) { r.total.Store(n) }

// Add records completed work items. Safe for concurrent use.
func (r *Reporter) Add(n int64) { r.done.Add(n) }

// Stop ends the reporter and prints the final line. Repeated calls are
// ignored.
func (r *Reporter) Stop() {
	r.once.Do(func() {
		close(r.stop)
		<-r.stopped
		r.print(true)
	})
}

func (r *Reporter) loop() {
	defer close(r.stopped)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.print(false)
		}
	}
}

// print writes one progress line, e.g.
//
//	scan: 12345/98765 (12%) 64.5/s eta 21m3s [3m12s]
//
// Live updates rewrite the current terminal line with \r; the final call
// ends with a newline.
func (r *Reporter) print(final bool) {
	done := r.done.Load()
	total := r.total.Load()
	elapsed := time.Since(r.start)

	var b strings.Builder
	if total > 0 {
		fmt.Fprintf(&b, "%s: %d/%d (%d%%)", r.label, done, total, done*100/total)
	} else {
		fmt.Fprintf(&b, "%s: %d", r.label, done)
	}
	if done > 0 && elapsed > 0 {
		rate := float64(done) / elapsed.Seconds()
		fmt.Fprintf(&b, " %.1f/s", rate)
		if total > done {
			eta := time.Duration(float64(total-done) / rate * float64(time.Second))
			fmt.Fprintf(&b, " eta %s", eta.Round(time.Second))
		}
	}
	fmt.Fprintf(&b, " [%s]", elapsed.Round(time.Second))
	line := b.String()

	r.mu.Lock()
	defer r.mu.Unlock()
	if final {
		if r.lastLen > len(line) {
			fmt.Fprintf(r.w, "\r%s\r%s\n", strings.Repeat(" ", r.lastLen), line)
		} else if r.lastLen > 0 {
			fmt.Fprintf(r.w, "\r%s\n", line)
		} else {
			fmt.Fprintf(r.w, "%s\n", line)
		}
		r.lastLen = 0
		return
	}
	pad := ""
	if n := r.lastLen - len(line); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	fmt.Fprintf(r.w, "\r%s%s", line, pad)
	r.lastLen = len(line)
}
