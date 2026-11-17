package filter

import "testing"

func TestExplain(t *testing.T) {
	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: a
    match: { max_width: 10 }
    action: drop
  - name: b
    match: { format: png }
    action: tag
    tag: p
  - name: c
    match: { format: png }
    action: accept
`))
	if err != nil {
		t.Fatal(err)
	}
	dec, steps := rs.Explain(&ImageInfo{Format: "png", Width: 100, Height: 100})
	if dec.Action != ActionAccept || dec.Rule != "c" {
		t.Fatalf("%+v", dec)
	}
	if len(steps) < 3 || steps[0].Matched || !steps[1].Matched || !steps[2].Matched {
		t.Fatalf("%+v", steps)
	}
}
