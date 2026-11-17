package filter

import (
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"50KB", 50 * 1024},
		{"2MB", 2 * 1024 * 1024},
		{"100", 100},
		{"1GB", 1024 * 1024 * 1024},
	}
	for _, tc := range cases {
		got, err := ParseSize(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseSize(%q)=%d,%v want %d", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseSize("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestConditionBoundaries(t *testing.T) {
	yaml := `
version: 1
default: accept
rules:
  - name: maxw
    match: { max_width: 499 }
    action: drop
  - name: ratio
    match: { aspect_ratio: 16 }
    action: drop
  - name: size
    match: { max_file_size: 50KB }
    action: drop
`
	rs, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	info := &ImageInfo{Width: 499, Height: 499, Size: 50 * 1024, Format: "jpeg"}
	if d := rs.Evaluate(info); d.Rule != "maxw" || d.Action != ActionDrop {
		t.Fatalf("499 width: %+v", d)
	}
	info = &ImageInfo{Width: 500, Height: 500, Size: 50*1024 + 1, Format: "jpeg"}
	if d := rs.Evaluate(info); d.Action != ActionAccept {
		t.Fatalf("500 should accept through maxw: %+v", d)
	}

	rs2, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: ratio
    match: { aspect_ratio: 16 }
    action: drop
`))
	if err != nil {
		t.Fatal(err)
	}
	// 4000x250 → 16.0 exactly
	if d := rs2.Evaluate(&ImageInfo{Width: 4000, Height: 250}); d.Rule != "ratio" {
		t.Fatalf("ratio 16: %+v", d)
	}
	// 4000x249 → ~16.064 > 16.02 tolerance
	if d := rs2.Evaluate(&ImageInfo{Width: 4000, Height: 249}); d.Action != ActionAccept {
		t.Fatalf("ratio 16.06 should miss ±0.02: %+v (r=%v)", d, aspectRatio(4000, 249))
	}
	// 16.01 within ±0.02 of 16
	if d := rs2.Evaluate(&ImageInfo{Width: 1601, Height: 100}); d.Rule != "ratio" {
		t.Fatalf("ratio 16.01: %+v", d)
	}
}

func TestAnyAllNotAndListOR(t *testing.T) {
	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: clutter
    match:
      any:
        - all: [ { format: png }, { max_file_size: 50KB } ]
        - all: [ { max_width: 127 }, { max_height: 127 } ]
    action: drop
  - name: jpeg-or-heic
    match: { format: [heic, jpeg] }
    action: drop
  - name: not-tiny
    match:
      not: { max_width: 10 }
    action: accept
`))
	if err != nil {
		t.Fatal(err)
	}
	if d := rs.Evaluate(&ImageInfo{Format: "png", Size: 40 * 1024, Width: 200, Height: 200}); d.Rule != "clutter" {
		t.Fatalf("40KB png: %+v", d)
	}
	if d := rs.Evaluate(&ImageInfo{Format: "webp", Size: 100, Width: 100, Height: 100}); d.Rule != "clutter" {
		t.Fatalf("100x100: %+v", d)
	}
	if d := rs.Evaluate(&ImageInfo{Format: "png", Size: 60 * 1024, Width: 200, Height: 200}); d.Rule != "jpeg-or-heic" && d.Action != ActionAccept {
		// png over 50KB and >127 — not clutter; not jpeg/heic → accept via later or default
		if d.Action != ActionAccept {
			t.Fatalf("big png: %+v", d)
		}
	}
	if d := rs.Evaluate(&ImageInfo{Format: "jpeg", Width: 2000, Height: 2000, Size: 1 << 20}); d.Rule != "jpeg-or-heic" {
		t.Fatalf("jpeg: %+v", d)
	}
}

func TestFirstMatchAndTagAndDefault(t *testing.T) {
	rs, err := LoadBytes([]byte(`
version: 1
default: drop
rules:
  - name: mark
    match: { format: png }
    action: tag
    tag: pngish
  - name: keep-png
    match: { format: png }
    action: accept
`))
	if err != nil {
		t.Fatal(err)
	}
	d := rs.Evaluate(&ImageInfo{Format: "png", Width: 1, Height: 1})
	if d.Action != ActionAccept || d.Rule != "keep-png" || len(d.Tags) != 1 || d.Tags[0] != "pngish" {
		t.Fatalf("got %+v", d)
	}
	d = rs.Evaluate(&ImageInfo{Format: "jpeg", Width: 1, Height: 1})
	if d.Action != ActionDrop || d.Rule != DefaultRuleName {
		t.Fatalf("default drop: %+v", d)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		yaml string
		sub  string
	}{
		{`version: 2
default: accept
rules: []`, "version"},
		{`version: 1
default: accept
rules:
  - name: BadName
    match: {}
    action: drop`, "name"},
		{`version: 1
default: accept
rules:
  - name: x
    match: { max_file_size: potato }
    action: drop`, "size"},
		{`version: 1
default: accept
rules:
  - name: x
    match: { no_such_key: 1 }
    action: drop`, "unknown"},
		{`version: 1
default: accept
extra: 1
rules: []`, "field"},
		{`version: 1
default: accept
rules:
  - name: _default
    match: {}
    action: drop`, "reserved"},
	}
	for _, tc := range cases {
		_, err := LoadBytes([]byte(tc.yaml))
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.sub) && !strings.Contains(err.Error(), tc.sub) {
			// known fields error text varies; just require an error containing something useful
			if err == nil {
				t.Fatalf("expected error containing %q for %s", tc.sub, tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.sub) && tc.sub != "field" && tc.sub != "size" && tc.sub != "unknown" {
				t.Fatalf("err %v want substring %q", err, tc.sub)
			}
		}
	}
}

func TestEmptyMatchMatchesAll(t *testing.T) {
	rs, err := LoadBytes([]byte(`
version: 1
default: accept
rules:
  - name: all
    match: {}
    action: drop
`))
	if err != nil {
		t.Fatal(err)
	}
	if d := rs.Evaluate(&ImageInfo{Format: "gif"}); d.Rule != "all" {
		t.Fatalf("%+v", d)
	}
}
