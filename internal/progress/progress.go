// Package progress prints periodic one-line progress updates for the long
// phases of plr. A Reporter counts work items with atomics while a
// background goroutine prints one line per second to a writer (os.Stderr
// by default); Stop always prints the final line.
package progress

import (
	"fmt"
	"io"
	"os"
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
		r.print(time.Now())
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
			r.print(time.Now())
		}
	}
}

// print writes one line, e.g.
//
//	scan: 12345/98765 (12%) 64.5/s eta 21m03s [3m12s]
func (r *Reporter) print(now time.Time) {
	done := r.done.Load()
	total := r.total.Load()
	elapsed := now.Sub(r.start)
	if total > 0 {
		fmt.Fprintf(r.w, "%s: %d/%d (%d%%)", r.label, done, total, done*100/total)
	} else {
		fmt.Fprintf(r.w, "%s: %d", r.label, done)
	}
	if done > 0 && elapsed > 0 {
		rate := float64(done) / elapsed.Seconds()
		fmt.Fprintf(r.w, " %.1f/s", rate)
		if total > done {
			fmt.Fprintf(r.w, " eta %s", time.Duration(float64(total-done)/rate*float64(time.Second)))
		}
	}
	fmt.Fprintf(r.w, " [%s]\n", elapsed.Round(time.Second))
}
