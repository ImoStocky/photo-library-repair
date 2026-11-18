package kronk

import (
	"strings"
	"testing"

	"github.com/ardanlabs/kronk/sdk/kronk/model"
)

func TestBuildClassifyMessages(t *testing.T) {
	data := []byte{0xff, 0xd8, 0xff} // not a real JPEG; shape-only check
	msgs := buildClassifyMessages("pick a label", data, "jpeg")
	if len(msgs) != 2 {
		t.Fatalf("len(msgs)=%d, want 2 (system + user)", len(msgs))
	}
	if msgs[0]["role"] != model.RoleSystem {
		t.Fatalf("msgs[0].role=%v, want system", msgs[0]["role"])
	}
	if msgs[0]["content"] != "pick a label" {
		t.Fatalf("system content=%v", msgs[0]["content"])
	}
	if msgs[1]["role"] != model.RoleUser {
		t.Fatalf("msgs[1].role=%v, want user", msgs[1]["role"])
	}
	parts, ok := msgs[1]["content"].([]model.D)
	if !ok || len(parts) != 1 {
		t.Fatalf("user content parts=%v, want image-only", msgs[1]["content"])
	}
	if parts[0]["type"] != "image_url" {
		t.Fatalf("part type=%v, want image_url", parts[0]["type"])
	}
	img, ok := parts[0]["image_url"].(model.D)
	if !ok {
		t.Fatalf("image_url type=%T", parts[0]["image_url"])
	}
	url, _ := img["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("url=%q, want jpeg data URL", url)
	}
}

func TestMimeFromMagic(t *testing.T) {
	if got := mimeFromMagic([]byte{0xff, 0xd8, 0xff, 0xe0}); got != "jpeg" {
		t.Fatalf("jpeg magic: %q", got)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 8)...)
	if got := mimeFromMagic(png); got != "png" {
		t.Fatalf("png magic: %q", got)
	}
	webp := []byte("RIFF1234WEBP")
	if got := mimeFromMagic(webp); got != "webp" {
		t.Fatalf("webp magic: %q", got)
	}
}

func TestImageMIMEFromExt(t *testing.T) {
	if got := imageMIME("x.HEIC", nil); got != "heic" {
		t.Fatalf("heic ext: %q", got)
	}
	if got := imageMIME("x.jpg", nil); got != "jpeg" {
		t.Fatalf("jpg ext: %q", got)
	}
}
