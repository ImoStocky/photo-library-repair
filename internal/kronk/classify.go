package kronk

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kronksdk "github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	_ "github.com/gen2brain/heic"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"photo-library-repair/internal/filter"
)

// DefaultVisionModel is used by plr filter classify when --model is omitted.
const DefaultVisionModel = "unsloth/Qwen3-VL-4B-Instruct-BF16"

const classifyTimeout = 5 * time.Minute
const classifyMaxSide = 1024 // JPEG long edge + ImageTokenBudget
const classifyJPEGQuality = 85

// Short EXIF keys for the per-image text sidecar (order preserved).
var classifyExifKeys = []string{
	"DateTimeOriginal",
	"OffsetTimeOriginal",
	"CreateDate",
	"Make",
	"Model",
	"Camera",
	"LensModel",
	"Orientation",
	"GPSLatitude",
	"GPSLongitude",
	"GPSPosition",
	"ImageDescription",
	"Software",
}

// VisionClassifier labels images via Kronk Chat.
// The classify rule text is pinned once as a system prompt; each Classify
// call sends a new image user turn (resized JPEG + short filename/dims/EXIF text).
type VisionClassifier struct {
	krn     *kronksdk.Kronk
	modelID string
	ctx     context.Context

	sysOnce sync.Once
	system  string
}

// NewVisionClassifier loads a vision (projection) model for classify.
// projFile, when non-empty, overrides the downloaded mmproj (e.g. a Q8_0
// projector paired with Q4 text weights).
func NewVisionClassifier(ctx context.Context, modelSource, projFile string) (*VisionClassifier, func(), error) {
	krn, err := loadKronk(ctx, modelSource, projFile)
	if err != nil {
		return nil, nil, err
	}
	if !krn.ModelInfo().HasProjection {
		_ = krn.Unload(ctx)
		return nil, nil, fmt.Errorf("model %q has no vision projection", modelSource)
	}
	return &VisionClassifier{krn: krn, modelID: krn.ModelID(), ctx: ctx}, func() {
		_ = krn.Unload(context.Background())
	}, nil
}

// Name identifies the loaded vision model.
func (c *VisionClassifier) Name() string { return c.modelID }

// Classify implements filter.Classifier.
//
// The first call pins prompt (rule text + labels) as the system setup.
// Later calls keep that system prompt and send a max-side JPEG plus a
// short text sidecar (filename, dimensions, selected EXIF).
func (c *VisionClassifier) Classify(ctx context.Context, info *filter.ImageInfo, prompt string, labels []string) (filter.ClassifyResult, error) {
	_ = labels // already embedded in prompt by filter.BuildClassifyPrompt
	c.sysOnce.Do(func() { c.system = strings.TrimSpace(prompt) })
	if c.system == "" {
		return filter.ClassifyResult{}, fmt.Errorf("empty classify system prompt")
	}
	if info == nil || info.Path == "" {
		return filter.ClassifyResult{}, fmt.Errorf("nil image info")
	}
	jpegBytes, err := imageJPEGBytes(info.Path, classifyMaxSide)
	if err != nil {
		return filter.ClassifyResult{}, err
	}
	if ctx == nil {
		ctx = c.ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, classifyTimeout)
	defer cancel()
	resp, err := c.krn.Chat(callCtx, model.D{
		"messages":        buildClassifyMessages(c.system, jpegBytes, classifySidecarText(info)),
		"max_tokens":      128,
		"enable_thinking": false,
		"temperature":     0.0,
	})
	if err != nil {
		return filter.ClassifyResult{}, err
	}
	raw := ""
	if len(resp.Choices) > 0 && resp.Choices[0].Message != nil {
		raw = resp.Choices[0].Message.Content
	}
	return filter.ClassifyResult{Raw: raw, Label: strings.TrimSpace(raw)}, nil
}

// buildClassifyMessages is system setup + JPEG(+text) user turn.
// Text follows the image (Kronk ImageMessage order) so VL templates keep
// media before the question.
func buildClassifyMessages(systemPrompt string, jpeg []byte, sidecar string) []model.D {
	return model.Messages(
		model.TextMessage(model.RoleSystem, systemPrompt),
		imageWithTextUserMessage(jpeg, sidecar),
	)
}

func imageWithTextUserMessage(jpeg []byte, text string) model.D {
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
	parts := []model.D{
		{
			"type": "image_url",
			"image_url": model.D{
				"url": url,
			},
		},
	}
	if strings.TrimSpace(text) != "" {
		parts = append(parts, model.D{
			"type": "text",
			"text": text,
		})
	}
	return model.D{
		"role":    model.RoleUser,
		"content": parts,
	}
}

// classifySidecarText is a short per-image fact line for the user turn.
// Dimensions/format/bytes describe the original file, not the resized JPEG.
func classifySidecarText(info *filter.ImageInfo) string {
	if info == nil {
		return ""
	}
	name := filepath.Base(info.Path)
	if info.RelPath != "" {
		name = filepath.ToSlash(info.RelPath)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "filename: %s\n", name)
	if info.Width > 0 && info.Height > 0 {
		fmt.Fprintf(&b, "dimensions: %dx%d\n", info.Width, info.Height)
	}
	if info.Format != "" {
		fmt.Fprintf(&b, "format: %s\n", info.Format)
	}
	if info.Size > 0 {
		fmt.Fprintf(&b, "bytes: %d\n", info.Size)
	}
	exif := info.ExifAll()
	var pairs []string
	seen := map[string]bool{}
	for _, key := range classifyExifKeys {
		v := strings.TrimSpace(exif[key])
		if v == "" || seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, key+"="+v)
	}
	if len(pairs) > 0 {
		fmt.Fprintf(&b, "exif: %s\n", strings.Join(pairs, "; "))
	}
	return strings.TrimSpace(b.String())
}

// imageJPEGBytes decodes path, downscales so the long edge is at most maxSide,
// and re-encodes as JPEG for a sharp, budget-sized vision input.
func imageJPEGBytes(path string, maxSide int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		img = downscale(img, maxSide)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: classifyJPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func downscale(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(maxSide) / float64(max(w, h))
	nw := max(1, int(float64(w)*scale))
	nh := max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}
