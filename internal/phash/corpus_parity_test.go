package phash

import (
	"bufio"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gen2brain/heic"
)

func TestCorpusBlockhashParity(t *testing.T) {
	paths := corpusPaths(t)
	if len(paths) == 0 {
		t.Skip("no corpus paths")
	}
	for _, path := range paths {
		t.Run(baseName(path), func(t *testing.T) {
			img := decodeCorpus(t, path)
			b := img.Bounds()
			t.Logf("%T bounds=%v", img, b)

			fastSum := bindPixelSum(img)
			slowSum := bindPixelSum(atOnly{img})
			// Spot-check a grid of pixels (full image would be huge).
			stepX := max(1, b.Dx()/64)
			stepY := max(1, b.Dy()/64)
			mismatches := 0
			checked := 0
			for y := b.Min.Y; y < b.Max.Y; y += stepY {
				for x := b.Min.X; x < b.Max.X; x += stepX {
					checked++
					if fastSum(x, y) != slowSum(x, y) {
						mismatches++
						if mismatches <= 5 {
							t.Errorf("pixelSum (%d,%d): fast=%d slow=%d", x, y, fastSum(x, y), slowSum(x, y))
						}
					}
				}
			}
			if mismatches > 0 {
				t.Fatalf("%d/%d sampled pixels differ", mismatches, checked)
			}

			start := time.Now()
			fast, err := blockhash(img, b, 16, 16)
			fastDur := time.Since(start)
			if err != nil {
				t.Fatal("fast:", err)
			}
			start = time.Now()
			slow, err := blockhash(atOnly{img}, b, 16, 16)
			slowDur := time.Since(start)
			if err != nil {
				t.Fatal("slow:", err)
			}
			if fast.Hex() != slow.Hex() {
				t.Fatalf("blockhash mismatch\n fast %s\n slow %s", fast.Hex(), slow.Hex())
			}
			t.Logf("blockhash16 ok  fast=%s slow=%s  hash=%s", fastDur, slowDur, fast.Hex()[:16])
		})
	}
}

func corpusPaths(t *testing.T) []string {
	t.Helper()
	if env := os.Getenv("PLR_CORPUS_LIST"); env != "" {
		return readPathList(t, env)
	}
	// Default list written by the corpus sampling helper.
	const def = "/tmp/corpus-sample.txt"
	if _, err := os.Stat(def); err == nil {
		return readPathList(t, def)
	}
	return nil
}

func readPathList(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			t.Logf("skip missing %s", p)
			continue
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeCorpus(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err == nil {
		return img
	}
	// HEIC fallback via the same decoder fingerprint uses.
	if f2, err2 := os.Open(path); err2 == nil {
		defer f2.Close()
		if h, err3 := heic.Decode(f2); err3 == nil {
			return h
		}
	}
	t.Fatalf("decode %s: %v", path, err)
	return nil
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
