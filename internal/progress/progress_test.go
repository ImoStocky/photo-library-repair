package progress

import (
	"bytes"
	"strings"
	"testing"
)

func TestReporterFinalLine(t *testing.T) {
	var buf bytes.Buffer
	r := newWithWriter("scan", &buf)
	r.SetTotal(10)
	r.Add(10)
	r.Stop()
	if !strings.Contains(buf.String(), "scan: 10/10 (100%)") {
		t.Fatalf("final line = %q, want a scan: 10/10 (100%%) line", buf.String())
	}
}

func TestReporterStopIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	r := newWithWriter("plan", &buf)
	r.Stop()
	r.Stop()
	if n := strings.Count(buf.String(), "plan:"); n != 1 {
		t.Fatalf("final lines = %d, want 1 (%q)", n, buf.String())
	}
}

func TestReporterUnknownTotal(t *testing.T) {
	var buf bytes.Buffer
	r := newWithWriter("apply", &buf)
	r.Add(3)
	r.Stop()
	if !strings.HasPrefix(buf.String(), "apply: 3 ") {
		t.Fatalf("final line = %q, want it to start with an apply: 3 line without a total", buf.String())
	}
}
