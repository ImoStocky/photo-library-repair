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
	out := buf.String()
	if !strings.Contains(out, "scan: 10/10 (100%)") {
		t.Fatalf("final line = %q, want a scan: 10/10 (100%%) line", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("final line = %q, want trailing newline", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("final-only output = %q, want no carriage return", out)
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

func TestReporterLiveRewrite(t *testing.T) {
	var buf bytes.Buffer
	r := newWithWriter("scan", &buf)
	r.SetTotal(10)
	r.Add(3)
	r.print(false)
	r.Add(7)
	r.print(false)
	live := buf.String()
	if !strings.Contains(live, "\r") {
		t.Fatalf("live output = %q, want carriage-return rewrite", live)
	}
	if strings.Contains(live, "\n") {
		t.Fatalf("live output = %q, want no newline before Stop", live)
	}
	r.Stop()
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Fatalf("after Stop = %q, want trailing newline", buf.String())
	}
}
