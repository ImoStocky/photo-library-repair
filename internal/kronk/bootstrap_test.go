package kronk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProjFileKeepsDownloaded(t *testing.T) {
	got, err := resolveProjFile("/models/mmproj-BF16.gguf", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/models/mmproj-BF16.gguf" {
		t.Fatalf("got %q, want downloaded path", got)
	}
}

func TestResolveProjFileOverride(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "mmproj-Q8_0.gguf")
	if err := os.WriteFile(override, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProjFile("/models/mmproj-BF16.gguf", override)
	if err != nil {
		t.Fatal(err)
	}
	if got != override {
		t.Fatalf("got %q, want override %q", got, override)
	}
}

func TestResolveProjFileMissingOverride(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.gguf")
	_, err := resolveProjFile("/models/mmproj-BF16.gguf", missing)
	if err == nil {
		t.Fatal("missing mmproj override must error")
	}
	if !strings.Contains(err.Error(), "mmproj") {
		t.Fatalf("error = %v, want mmproj mention", err)
	}
}
